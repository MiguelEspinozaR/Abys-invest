// Package edgar implements the SEC EDGAR HTTP adapter (ADR-0003).
//
// SEC EDGAR fair-access policy requires an identifying User-Agent on every
// request and at most 10 requests/second. The User-Agent must come from the
// environment (SEC_EDGAR_USER_AGENT) and is never hardcoded by the caller side;
// NewClient rejects an empty User-Agent.
package edgar

import (
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
	// DefaultBaseURL is the SEC data API base.
	DefaultBaseURL = "https://data.sec.gov"
	// CompanyFactsPath is the company facts endpoint; CIK must be 10 digits.
	CompanyFactsPath = "/companyfacts/CIK%s.json"
	// CompanyFactsAltPath is the legacy efts API variant. Some SEC CDN edges
	// only serve companyfacts through this path, so GetCompanyFacts falls back
	// to it when the primary path returns 404.
	CompanyFactsAltPath = "/api/xbrl/companyfacts/CIK%s.json"
	// CompanyTickersPath is the full SEC company tickers catalog.
	CompanyTickersPath = "/company_tickers.json"
	// CompanyTickersAltPath is the canonical catalog location. The legacy CDN
	// (data.sec.gov/company_tickers.json) can return NoSuchKey on some edges,
	// while www.sec.gov/files/company_tickers.json is always served.
	CompanyTickersAltPath = "https://www.sec.gov/files/company_tickers.json"
	// MaxRequestsPerSec is the SEC fair-access limit.
	MaxRequestsPerSec = 10
	// maxRetries is the number of attempts (initial + retries).
	maxRetries = 3
	// maxBodyBytes caps a downloaded payload (companyfacts can be tens of MB).
	maxBodyBytes = 512 << 20

	defaultRetryBase  = 500 * time.Millisecond
	defaultHTTPTimout = 120 * time.Second
)

// StatusError is returned when SEC EDGAR answers with a non-2xx status.
type StatusError struct {
	StatusCode int
	URL        string
	BodyPrefix string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("edgar: unexpected status %d for %s (body: %.200s)", e.StatusCode, e.URL, e.BodyPrefix)
}

// Client is the SEC EDGAR HTTP client.
type Client struct {
	httpClient *http.Client
	baseURL    string
	userAgent  string
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

// WithRequestsPerSecond overrides the rate limit (default MaxRequestsPerSec).
func WithRequestsPerSecond(rps int) Option {
	return func(cl *Client) { cl.limiter = newRateLimiter(rps) }
}

// WithRetryBase sets the base backoff duration (tests use small values).
func WithRetryBase(d time.Duration) Option {
	return func(cl *Client) { cl.retryBase = d }
}

// NewClient builds an EDGAR client. userAgent is mandatory (SEC policy).
func NewClient(userAgent string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(userAgent) == "" {
		return nil, errors.New("edgar: NewClient requires a non-empty user agent (SEC_EDGAR_USER_AGENT)")
	}
	c := &Client{
		httpClient: &http.Client{Timeout: defaultHTTPTimout},
		baseURL:    DefaultBaseURL,
		userAgent:  userAgent,
		limiter:    newRateLimiter(MaxRequestsPerSec),
		retryBase:  defaultRetryBase,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// GetCompanyFacts returns the raw companyfacts JSON for the given CIK
// (any format: "320193" or "0000320193" are both accepted).
func (c *Client) GetCompanyFacts(ctx context.Context, cik string) ([]byte, error) {
	padded, err := NormalizeCIK(cik)
	if err != nil {
		return nil, err
	}
	primary := c.baseURL + fmt.Sprintf(CompanyFactsPath, padded)
	body, err := c.get(ctx, primary)
	if err != nil {
		var statusErr *StatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
			// Some SEC CDN edges only serve companyfacts under the /api/xbrl/ path.
			alt := c.baseURL + fmt.Sprintf(CompanyFactsAltPath, padded)
			if alt != primary {
				return c.get(ctx, alt)
			}
		}
		return nil, err
	}
	return body, nil
}

// GetCompanyTickers returns the raw SEC company tickers catalog JSON.
func (c *Client) GetCompanyTickers(ctx context.Context) ([]byte, error) {
	primary := c.baseURL + CompanyTickersPath
	body, err := c.get(ctx, primary)
	if err != nil {
		var statusErr *StatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
			// Some CDN edges do not serve /company_tickers.json; the canonical
			// location on www.sec.gov always works.
			if CompanyTickersAltPath != primary {
				return c.get(ctx, CompanyTickersAltPath)
			}
		}
		return nil, err
	}
	return body, nil
}

// GetXBRLFiling fetches an arbitrary SEC EDGAR filing URL (10-K/10-Q XBRL docs).
func (c *Client) GetXBRLFiling(ctx context.Context, url string) ([]byte, error) {
	return c.get(ctx, url)
}

// get performs a GET honoring the rate limit and retries transient failures.
func (c *Client) get(ctx context.Context, url string) ([]byte, error) {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := c.limiter.wait(ctx); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("edgar: build request for %s: %w", url, err)
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("edgar: request %s failed (attempt %d/%d): %w", url, attempt, maxRetries, err)
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
				return nil, fmt.Errorf("edgar: read body of %s: %w", url, readErr)
			}
			return body, nil

		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			// Transient: retry honoring Retry-After when present.
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
		return fmt.Errorf("edgar: retry aborted: %w", ctx.Err())
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
		return fmt.Errorf("edgar: rate limiter aborted: %w", ctx.Err())
	}
}

// NormalizeCIK zero-pads a CIK to 10 digits ("320193" -> "0000320193").
func NormalizeCIK(cik string) (string, error) {
	cik = strings.TrimSpace(cik)
	if cik == "" {
		return "", errors.New("edgar: empty cik")
	}
	for _, r := range cik {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("edgar: invalid cik %q (must be digits)", cik)
		}
	}
	if len(cik) > 10 {
		return "", fmt.Errorf("edgar: invalid cik %q (too long)", cik)
	}
	return fmt.Sprintf("%010s", cik), nil
}

// CompanyTicker is one entry of the SEC company_tickers catalog.
type CompanyTicker struct {
	CIK       int64  `json:"cik_str"`
	Ticker    string `json:"ticker"`
	Title     string `json:"title"`
	CIKString string `json:"-"`
}

// ParseCompanyTickers decodes the SEC company_tickers.json payload.
func ParseCompanyTickers(data []byte) ([]CompanyTicker, error) {
	var raw map[string]CompanyTicker
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("edgar: parse company tickers: %w", err)
	}
	out := make([]CompanyTicker, 0, len(raw))
	for k, e := range raw {
		if k == "" || e.Ticker == "" {
			continue
		}
		e.CIKString = fmt.Sprintf("%010d", e.CIK)
		out = append(out, e)
	}
	return out, nil
}
