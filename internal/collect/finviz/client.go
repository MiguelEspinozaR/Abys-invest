// Package finviz implements a minimal sector/industry adapter for
// finviz.com/quote.ashx (HTML table scrape). It is the documented fallback of
// plan D1 when Yahoo quoteSummary is unavailable (crumb/cookies blocked): the
// plan names Finviz as the alternative source (ADR-0003) and the sector is
// stored in securities.sector/industry regardless of origin.
package finviz

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// DefaultQuoteURL is the Finviz quote page with industry/sector links.
	DefaultQuoteURL = "https://finviz.com/quote.ashx"
	// DefaultResponseTimeout bounds single quote fetches.
	DefaultResponseTimeout = 30 * time.Second
	// DefaultUserAgent identifies the scraper (JS-rendered pages need a real UA).
	DefaultUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0 Safari/537.36"
)

// ErrNoSectorInfo is returned when the page has no sector/industry links.
var ErrNoSectorInfo = errors.New("finviz: sin sector/industry en la página")

// SectorInfo is the parsed sector/industry of a ticker.
type SectorInfo struct {
	Sector   string `json:"sector"`
	Industry string `json:"industry"`
}

// Client is the Finviz quote scraper.
type Client struct {
	httpClient *http.Client
	quoteURL   string
	userAgent  string
}

// Option customizes a Client (tests use these to avoid real network).
type Option func(*Client)

// WithHTTPClient overrides the http.Client (timeouts, httptest).
func WithHTTPClient(c *http.Client) Option { return func(cl *Client) { cl.httpClient = c } }

// WithQuoteURL overrides the quote page URL (httptest).
func WithQuoteURL(u string) Option {
	return func(cl *Client) { cl.quoteURL = strings.TrimRight(u, "/") }
}

// WithUserAgent overrides the default User-Agent.
func WithUserAgent(ua string) Option { return func(cl *Client) { cl.userAgent = ua } }

// NewClient builds a Finviz quote scraper.
func NewClient(opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: DefaultResponseTimeout},
		quoteURL:   DefaultQuoteURL,
		userAgent:  DefaultUserAgent,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// sectorLinkRe matches the sector link in the categories block:
//
//	<a href="screener?v=111&f=sec_technology" class="quote-header_category">Technology</a>
var sectorLinkRe = regexp.MustCompile(`(?i)<a href="[^"]*f=sec_[^"]*"[^>]*>\s*([^<]+?)\s*<`)

// industryLinkRe matches the industry link (title attr holds the full name):
//
//	<a href="screener?v=111&f=ind_consumerelectronics" class="quote-header_category" title="Consumer Electronics">
var industryLinkRe = regexp.MustCompile(`(?i)<a href="[^"]*f=ind_[^"]*"[^>]*title="([^"]+)"`)

var categoriesRe = regexp.MustCompile(`(?i)class="quote-header_categories"`)

// GetSectorInfo fetches the quote page of ticker and extracts sector/industry
// from the category links (plan D1 fallback).
func (c *Client) GetSectorInfo(ctx context.Context, ticker string) (*SectorInfo, error) {
	q := url.Values{}
	q.Set("t", strings.ToUpper(strings.TrimSpace(ticker)))
	u := c.quoteURL + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("finviz: build request %s: %w", ticker, err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("finviz: request %s: %w", ticker, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) //nolint:errcheck
		return nil, fmt.Errorf("finviz: status %d para %s", resp.StatusCode, ticker)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("finviz: leer página %s: %w", ticker, err)
	}
	return parseQuotePage(body)
}

// parseQuotePage extracts sector/industry from a rendered quote page. It
// looks only inside the quote-header_categories block.
func parseQuotePage(body []byte) (*SectorInfo, error) {
	loc := categoriesRe.FindIndex(body)
	if loc == nil {
		return nil, ErrNoSectorInfo
	}
	block := body[loc[0]:]
	if len(block) > 8192 {
		block = block[:8192]
	}

	var info SectorInfo
	if m := sectorLinkRe.FindSubmatch(block); m != nil {
		info.Sector = strings.TrimSpace(string(m[1]))
	}
	if m := industryLinkRe.FindSubmatch(block); m != nil {
		info.Industry = strings.TrimSpace(string(m[1]))
	}
	if info.Sector == "" && info.Industry == "" {
		return nil, ErrNoSectorInfo
	}
	return &info, nil
}
