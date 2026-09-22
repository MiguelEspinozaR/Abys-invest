package yahoo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

const (
	// QuoteSummaryPath is the v10 quoteSummary endpoint; {symbol} is
	// URL-substituted. Requires a crumb session (plan D1).
	QuoteSummaryPath = "/v10/finance/quoteSummary/%s"
	// CookieBootstrapURL is the host that seeds the session cookies Yahoo
	// requires for the crumb workflow.
	CookieBootstrapURL = "https://fc.yahoo.com"
	// CrumbPath returns the crumb token for the current session.
	CrumbPath = "/v1/test/getcrumb"
	// CrumbTTL refreshes the crumb after this duration.
	CrumbTTL = 10 * time.Minute
	// quoteModules is the module set requested for sector/industry.
	quoteModules = "assetProfile"
)

// QuoteSummaryResponse mirrors the v10 quoteSummary JSON envelope.
type QuoteSummaryResponse struct {
	QuoteSummary struct {
		Result []struct {
			AssetProfile struct {
				Sector   string `json:"sector"`
				Industry string `json:"industry"`
			} `json:"assetProfile"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"quoteSummary"`
}

// ErrEmptyQuoteSummary is returned when the envelope has no result rows
// (unknown symbol or assetProfile not available).
var ErrEmptyQuoteSummary = fmt.Errorf("yahoo: quoteSummary sin resultado")

// cookieBootstrap performs a best-effort GET to seed the HTTP cookie jar
// (fc.yahoo.com). Failures are tolerated: some deployments validate the crumb
// without the bootstrap cookies.
func (c *Client) cookieBootstrap(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cookieBase, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) //nolint:errcheck
	resp.Body.Close()
}

// getCrumb fetches (or reuses) the session crumb token. Rate-limited and
// retried like any other request; an error is returned when Yahoo rejects the
// request (e.g. HTTP 429/401) so callers can degrade gracefully.
func (c *Client) getCrumb(ctx context.Context) (string, error) {
	if c.crumb != "" && time.Now().Before(c.crumbExpiry) {
		return c.crumb, nil
	}
	cookieBootstrapOnce(c, ctx)
	crumbURL := strings.TrimRight(c.crumbBase, "/") + CrumbPath
	body, err := c.get(ctx, crumbURL) // usa el jar del client (cookies)
	if err != nil {
		return "", fmt.Errorf("yahoo: obtener crumb: %w", err)
	}
	crumb := strings.TrimSpace(string(body))
	if crumb == "" || crumb == "Too Many Requests" || strings.Contains(crumb, "<") {
		return "", fmt.Errorf("yahoo: crumb no válido %q", truncate(crumb, 24))
	}
	c.crumb = crumb
	c.crumbExpiry = time.Now().Add(CrumbTTL)
	return crumb, nil
}

// GetQuoteSummary fetches sector and industry for a symbol via the v10
// quoteSummary assetProfile module (plan D1).
func (c *Client) GetQuoteSummary(ctx context.Context, symbol string) (*QuoteSummaryResponse, error) {
	crumb, err := c.getCrumb(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("modules", quoteModules)
	q.Set("crumb", crumb)
	u := c.baseURL + fmt.Sprintf(QuoteSummaryPath, strings.ToUpper(strings.TrimSpace(symbol))) + "?" + q.Encode()

	body, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	var resp QuoteSummaryResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("yahoo: parse quoteSummary %s: %w", symbol, err)
	}
	if resp.QuoteSummary.Error != nil {
		return nil, fmt.Errorf("yahoo: quoteSummary %s: %s (%s)",
			symbol, resp.QuoteSummary.Error.Description, resp.QuoteSummary.Error.Code)
	}
	if len(resp.QuoteSummary.Result) == 0 {
		return nil, ErrEmptyQuoteSummary
	}
	return &resp, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ensureJar installs a default cookie jar when none was configured.
func ensureJar(j http.CookieJar) http.CookieJar {
	if j != nil {
		return j
	}
	jar, _ := cookiejar.New(nil)
	return jar
}

// cookieBootstrapOnce ensures the cookie jar is seeded exactly once per
// client (best-effort, never blocks the quote summary path for long).
func cookieBootstrapOnce(c *Client, ctx context.Context) {
	if c.cookieBootstrapped {
		return
	}
	c.cookieBootstrapped = true
	c.cookieBootstrap(ctx)
}
