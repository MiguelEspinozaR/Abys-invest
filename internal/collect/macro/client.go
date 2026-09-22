// Package macro implements the US macro data HTTP adapters (ADR-0003).
// The initial provider is the BLS Public API v2 (Bureau of Labor Statistics),
// used to ingest the CPI-U series (CUSR0000SA0).
//
// The BLS API allows 25 queries/day without an API key, which is sufficient
// for the daily batch (1 query covers 7 years of monthly data).
package macro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the BLS Public API v2 base.
	DefaultBaseURL = "https://api.bls.gov"
	// TimeseriesPath is the POST endpoint for series data.
	TimeseriesPath = "/publicAPI/v2/timeseries/data/"
	// MaxRequestsPerMinute mirrors the no-key daily budget prudently.
	MaxRequestsPerMinute = 5
	// maxRetries is the number of attempts (initial + retries).
	maxRetries = 3
	// maxBodyBytes caps a BLS response.
	maxBodyBytes = 16 << 20

	defaultRetryBase  = 500 * time.Millisecond
	defaultHTTPTimout = 60 * time.Second
)

// StatusError is returned when BLS answers with a non-2xx status.
type StatusError struct {
	StatusCode int
	URL        string
	BodyPrefix string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("macro: unexpected status %d for %s (body: %.200s)", e.StatusCode, e.URL, e.BodyPrefix)
}

// ErrRequestFailed is returned when BLS reports status REQUEST_FAILED
// (e.g. yearly quota exceeded, invalid series).
var ErrRequestFailed = errors.New("macro: BLS request failed")

// ErrorResponse is the BLS error envelope.
type ErrorResponse struct {
	Status string `json:"status"`
	Msg    string `json:"message"`
}

// Client is the BLS HTTP client.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiKey     string
	limiter    *rateLimiter
	retryBase  time.Duration
}

// Option customizes a Client (tests use these to avoid real network).
type Option func(*Client)

// WithHTTPClient overrides the http.Client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.httpClient = c }
}

// WithBaseURL overrides the API base URL (e.g. httptest server).
func WithBaseURL(u string) Option {
	return func(cl *Client) { cl.baseURL = strings.TrimRight(u, "/") }
}

// WithRequestsPerMinute overrides the rate budget (default MaxRequestsPerMinute).
func WithRequestsPerMinute(rpm int) Option {
	return func(cl *Client) { cl.limiter = newRateLimiter(rpm) }
}

// WithRetryBase sets the base backoff duration (tests use small values).
func WithRetryBase(d time.Duration) Option {
	return func(cl *Client) { cl.retryBase = d }
}

// NewClient builds a BLS client. apiKey is optional (25 daily queries without
// it); it must come from the environment (BLS_API_KEY), never from code.
func NewClient(apiKey string, opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: defaultHTTPTimout},
		baseURL:    DefaultBaseURL,
		apiKey:     strings.TrimSpace(apiKey),
		limiter:    newRateLimiter(MaxRequestsPerMinute),
		retryBase:  defaultRetryBase,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// post performs a POST with rate limiting and retries on transient failures.
func (c *Client) post(ctx context.Context, url string, body []byte) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := c.limiter.wait(ctx); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("macro: build request for %s: %w", url, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("macro: request %s failed (attempt %d/%d): %w", url, attempt, maxRetries, err)
			if attempt < maxRetries {
				if waitErr := c.backoff(ctx, attempt, 0); waitErr != nil {
					return nil, waitErr
				}
			}
			continue
		}

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
			resp.Body.Close()
			if readErr != nil {
				return nil, fmt.Errorf("macro: read body of %s: %w", url, readErr)
			}
			return data, nil

		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = &StatusError{StatusCode: resp.StatusCode, URL: url, BodyPrefix: string(data)}
			if attempt < maxRetries {
				retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
				if waitErr := c.backoff(ctx, attempt, retryAfter); waitErr != nil {
					return nil, waitErr
				}
			}
			continue

		default:
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, &StatusError{StatusCode: resp.StatusCode, URL: url, BodyPrefix: string(data)}
		}
	}
	return nil, lastErr
}

// backoff waits for the given attempt number: explicit duration (Retry-After)
// or exponential backoff base * 2^(attempt-1). Context-aware.
func (c *Client) backoff(ctx context.Context, attempt int, explicit time.Duration) error {
	d := explicit
	if d <= 0 {
		d = c.retryBase * time.Duration(1<<(attempt-1))
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("macro: retry aborted: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// parseRetryAfter converts a Retry-After header to a duration.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := time.ParseDuration(v); err == nil {
		return secs
	}
	var s int
	if _, err := fmt.Sscanf(v, "%d", &s); err == nil {
		return time.Duration(s) * time.Second
	}
	return 0
}

// rateLimiter enforces a fixed requests-per-interval budget with a ticker.
type rateLimiter struct {
	rpm    int
	ticker *time.Ticker
}

func newRateLimiter(rpm int) *rateLimiter {
	if rpm <= 0 {
		rpm = MaxRequestsPerMinute
	}
	return &rateLimiter{rpm: rpm, ticker: time.NewTicker(time.Minute / time.Duration(rpm))}
}

// wait blocks until the next request slot is available. A pending token is
// consumed immediately (first request), then one token is produced per
// interval, capping the throughput.
func (l *rateLimiter) wait(ctx context.Context) error {
	select {
	case <-l.ticker.C:
		return nil
	default:
	}
	select {
	case <-l.ticker.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("macro: rate limiter aborted: %w", ctx.Err())
	}
}

// decodeJSON parses a BLS payload and surfaces the common error envelope.
func decodeJSON(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("macro: parse response: %w", err)
	}
	return nil
}
