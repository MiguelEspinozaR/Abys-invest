// Package yahoo implements the Yahoo Finance v8 chart HTTP adapter (ADR-0003).
//
// A single endpoint serves both the historical OHLCV series and the current
// quote: GET /v8/finance/chart/{symbol} with range+interval. No API key is
// required; a User-Agent header identifies the caller and a built-in rate
// limiter (default 20 req/s) prevents abuse.
package yahoo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the Yahoo chart API base.
	DefaultBaseURL = "https://query1.finance.yahoo.com"
	// ChartPath is the v8 chart endpoint; {symbol} is URL-substituted.
	ChartPath = "/v8/finance/chart/%s"
	// MaxRequestsPerSec is the default rate budget.
	MaxRequestsPerSec = 20
	// maxRetries is the number of attempts (initial + retries).
	maxRetries = 3
	// maxBodyBytes caps a chart payload (~200KB for 5y daily).
	maxBodyBytes = 32 << 20

	defaultRetryBase  = 400 * time.Millisecond
	defaultHTTPTimout = 60 * time.Second
	defaultUserAgent  = "AbysInvest/1.0 (dev)"
)

// StatusError is returned when Yahoo answers with a non-2xx status.
type StatusError struct {
	StatusCode int
	URL        string
	BodyPrefix string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("yahoo: unexpected status %d for %s (body: %.200s)", e.StatusCode, e.URL, e.BodyPrefix)
}

// ErrEmptyResult is returned when the chart API returns no result rows
// (unknown symbol or no data for the requested range/interval).
var ErrEmptyResult = errors.New("yahoo: chart result vacío")

// Client is the Yahoo Finance HTTP client.
type Client struct {
	httpClient *http.Client
	baseURL    string
	userAgent  string
	limiter    *rateLimiter
	retryBase  time.Duration
}

// Option customizes a Client (tests use these to avoid real network).
type Option func(*Client)

// WithHTTPClient overrides the http.Client (e.g. timeouts, httptest).
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) { cl.httpClient = c }
}

// WithBaseURL overrides the API base URL (e.g. httptest server).
func WithBaseURL(u string) Option {
	return func(cl *Client) { cl.baseURL = strings.TrimRight(u, "/") }
}

// WithRequestsPerSecond overrides the rate limit (default MaxRequestsPerSec).
func WithRequestsPerSecond(rps int) Option {
	return func(cl *Client) { cl.limiter = newRateLimiter(rps) }
}

// WithUserAgent overrides the default User-Agent header.
func WithUserAgent(ua string) Option {
	return func(cl *Client) { cl.userAgent = ua }
}

// WithRetryBase sets the base backoff duration (tests use small values).
func WithRetryBase(d time.Duration) Option {
	return func(cl *Client) { cl.retryBase = d }
}

// NewClient builds a Yahoo Finance client. The User-Agent may be set via the
// SEC_EDGAR-style environment variable; a dev fallback is used when empty
// (Yahoo does not enforce a mandatory identification policy).
func NewClient(opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: defaultHTTPTimout},
		baseURL:    DefaultBaseURL,
		userAgent:  defaultUserAgent,
		limiter:    newRateLimiter(MaxRequestsPerSec),
		retryBase:  defaultRetryBase,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// get performs a GET with rate limiting and retries on transient failures
// (429, 5xx, network errors).
func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := c.limiter.wait(ctx); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("yahoo: build request for %s: %w", url, err)
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("yahoo: request %s failed (attempt %d/%d): %w", url, attempt, maxRetries, err)
			if attempt < maxRetries {
				if waitErr := c.backoff(ctx, attempt, 0); waitErr != nil {
					return nil, waitErr
				}
			}
			continue
		}

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
			resp.Body.Close()
			if readErr != nil {
				return nil, fmt.Errorf("yahoo: read body of %s: %w", url, readErr)
			}
			return body, nil

		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = &StatusError{StatusCode: resp.StatusCode, URL: url, BodyPrefix: string(body)}
			if attempt < maxRetries {
				retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
				if waitErr := c.backoff(ctx, attempt, retryAfter); waitErr != nil {
					return nil, waitErr
				}
			}
			continue

		default:
			// 4xx (other than 429): not retried.
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, &StatusError{StatusCode: resp.StatusCode, URL: url, BodyPrefix: string(body)}
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
		return fmt.Errorf("yahoo: retry aborted: %w", ctx.Err())
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

// rateLimiter enforces a fixed requests-per-second budget with a ticker.
type rateLimiter struct {
	rps    int
	ticker *time.Ticker
}

func newRateLimiter(rps int) *rateLimiter {
	if rps <= 0 {
		rps = MaxRequestsPerSec
	}
	return &rateLimiter{rps: rps, ticker: time.NewTicker(time.Second / time.Duration(rps))}
}

// wait blocks until the next request slot is available. A pending token is
// consumed immediately (first request), then one token is produced per
// interval, capping the throughput at rps requests/second.
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
		return fmt.Errorf("yahoo: rate limiter aborted: %w", ctx.Err())
	}
}

// chartURL builds the v8 chart endpoint URL for symbol/range/interval.
func (c *Client) chartURL(symbol, dateRange, interval string) string {
	q := url.Values{}
	q.Set("range", dateRange)
	q.Set("interval", interval)
	q.Set("includePrePost", "false")
	return c.baseURL + fmt.Sprintf(ChartPath, url.PathEscape(symbol)) + "?" + q.Encode()
}

// jsonDecode is a small helper kept for readability of parsers.
func jsonDecode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
