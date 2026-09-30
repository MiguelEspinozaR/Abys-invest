package yahoo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
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
	// quoteModules is the requested module set: sector/industry (M2) and beta
	// (M6a, plan D17). Extending the list does NOT multiply requests:
	// quoteSummary v10 returns several modules in a single response.
	quoteModules = "assetProfile,defaultKeyStatistics"
)

// QuoteSummaryResponse mirrors the v10 quoteSummary JSON envelope.
type QuoteSummaryResponse struct {
	QuoteSummary struct {
		Result []struct {
			AssetProfile struct {
				Sector   string `json:"sector"`
				Industry string `json:"industry"`
			} `json:"assetProfile"`
			DefaultKeyStatistics struct {
				// beta comes as a plain number, but it can be null or absent for
				// foreign/ADR issuers: BetaFloat + pointer so "absent" is
				// distinguishable from "beta 0" without inventing a 1.0.
				Beta *BetaFloat `json:"beta"`
				// beta3Year is parsed for completeness of the module but NOT used
				// in M6a: the CAPM uses the standard (5y) beta.
				Beta3Year *BetaFloat `json:"beta3Year"`
			} `json:"defaultKeyStatistics"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"quoteSummary"`
}

// BetaFloat decodes the beta field of defaultKeyStatistics without letting an
// unexpected value bring down the unmarshal of the whole envelope (and with it
// the sector of M2, plan B3). It accepts a number, a numeric string, null and
// the {raw, fmt} wrapper; anything else ("NaN", "", a nested object) is treated
// as absent. It is NEVER a naked float64 for the same reason.
type BetaFloat float64

// UnmarshalJSON implements json.Unmarshaler with a tolerant, never-failing
// contract: an unparseable value decodes to 0, and Valid() then reports it as
// absent. The decode of the envelope must never fail because of a beta.
func (b *BetaFloat) UnmarshalJSON(data []byte) error {
	var out float64
	trimmed := strings.TrimSpace(string(data))
	switch {
	case trimmed == "", trimmed == "null":
		// ausente
	case strings.HasPrefix(trimmed, "{"): // {raw, fmt}
		var wrapper struct {
			Raw *float64 `json:"raw"`
		}
		if err := json.Unmarshal([]byte(trimmed), &wrapper); err != nil || wrapper.Raw == nil {
			out = 0
		} else {
			out = *wrapper.Raw
		}
	case strings.HasPrefix(trimmed, `"`): // string numérico
		var s string
		if err := json.Unmarshal([]byte(trimmed), &s); err != nil {
			out = 0
			break
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			out = f
		}
	default: // número plano
		if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
			out = 0
		}
	}
	*b = BetaFloat(out)
	return nil
}

// Valid reports whether the decoded beta is credible: finite, positive and
// within (0, 10] (a beta above 10 is a Yahoo data error, not a risk profile).
// 0, NaN, ±Inf and negatives are rejected → the security degrades to
// Config.BetaAssumed with wacc_source = configured_fallback, which is honest.
func (b BetaFloat) Valid() bool {
	v := float64(b)
	return v > 0 && v <= 10 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

// KeyStatsFor extracts the usable beta from the first result row. It returns
// (nil, nil) when there is no valid beta: the absence of a beta is legitimate
// data (foreign/ADR, degraded module), not an error. The error result is kept
// for API symmetry with the sector accessors and is always nil today.
func KeyStatsFor(resp *QuoteSummaryResponse) (beta *float64, err error) {
	if resp == nil || len(resp.QuoteSummary.Result) == 0 {
		return nil, nil
	}
	ks := resp.QuoteSummary.Result[0].DefaultKeyStatistics
	if ks.Beta == nil || !ks.Beta.Valid() {
		return nil, nil
	}
	v := float64(*ks.Beta)
	return &v, nil
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
	if beta, _ := KeyStatsFor(&resp); beta == nil {
		// Traza a nivel debug (nunca Warn): la ausencia de beta es el caso
		// esperado en issuers extranjeros/ADR, no un fallo del fetch (plan D17).
		slog.Debug("quoteSummary sin beta utilizable", "symbol", symbol)
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
