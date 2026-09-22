package edgar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testUA = "AbysInvest/1.0 (contact@abys-invest.dev)"

func newTestClient(t *testing.T, opts ...Option) *Client {
	t.Helper()
	all := append([]Option{WithRetryBase(5 * time.Millisecond), WithRequestsPerSecond(10000)}, opts...)
	c, err := NewClient(testUA, all...)
	if err != nil {
		t.Fatalf("NewClient falló: %v", err)
	}
	return c
}

func TestClientCompanyFactsFallbackAltPath(t *testing.T) {
	// Some CDN edges 404 the primary path; the client must fall back to /api/xbrl/.
	primaryPath := "/companyfacts/CIK0000320193.json"
	altPath := "/api/xbrl/companyfacts/CIK0000320193.json"
	paths := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths[r.URL.Path] = true
		if r.URL.Path == altPath {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"alt":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(t, WithBaseURL(srv.URL))
	body, err := c.GetCompanyFacts(context.Background(), "320193")
	if err != nil {
		t.Fatalf("GetCompanyFacts falló con fallback: %v", err)
	}
	if !paths[primaryPath] {
		t.Fatal("no se intentó el path primario")
	}
	if !paths[altPath] {
		t.Fatal("no se usó el path alternativo")
	}
	if string(body) != `{"alt":true}` {
		t.Fatalf("body inesperado: %s", body)
	}
}

func TestNewClientRequiresUserAgent(t *testing.T) {
	if _, err := NewClient("   "); err == nil {
		t.Fatal("NewClient debería fallar con User-Agent vacío")
	}
}

func TestClientUserAgentAndPath(t *testing.T) {
	var gotUA, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t, WithBaseURL(srv.URL))
	body, err := c.GetCompanyFacts(context.Background(), "320193")
	if err != nil {
		t.Fatalf("GetCompanyFacts falló: %v", err)
	}
	if gotUA != testUA {
		t.Fatalf("User-Agent incorrecto: %q", gotUA)
	}
	if gotPath != "/companyfacts/CIK0000320193.json" {
		t.Fatalf("path incorrecto: %q", gotPath)
	}
	var decoded map[string]bool
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body no decodificable: %v", err)
	}
	if !decoded["ok"] {
		t.Fatal("body inesperado")
	}
}

func TestClientRetryOn429ThenSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"done":true}`))
	}))
	defer srv.Close()

	c := newTestClient(t, WithBaseURL(srv.URL))
	body, err := c.GetCompanyFacts(context.Background(), "320193")
	if err != nil {
		t.Fatalf("GetCompanyFacts falló tras retry: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("se esperaban 3 intentos, hubo %d", calls.Load())
	}
	if !strings.Contains(string(body), "done") {
		t.Fatalf("body inesperado: %s", body)
	}
}

func TestClientRetryOnServerError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, WithBaseURL(srv.URL))
	if _, err := c.GetCompanyTickers(context.Background()); err != nil {
		t.Fatalf("GetCompanyTickers falló tras retry: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("se esperaban 2 intentos, hubo %d", calls.Load())
	}
}

func TestClientNoRetryOnClientError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := newTestClient(t, WithBaseURL(srv.URL))
	_, err := c.GetCompanyFacts(context.Background(), "320193")
	if err == nil {
		t.Fatal("se esperaba error 404")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusNotFound {
		t.Fatalf("error no es StatusError 404: %v", err)
	}
	// El fallback de companyfacts prueba también la ruta alternativa (404) y
	// termina ahí: un 4xx nunca se reintenta con backoff. Total = 2 intentos
	// (ruta primaria + alternativa), ambos 404 sin reintentos adicionales.
	if calls.Load() != 2 {
		t.Fatalf("se esperaban exactamente 2 intentos (primaria+fallback), hubo %d", calls.Load())
	}
}

func TestClientRetriesNetworkErrors(t *testing.T) {
	// Start and immediately stop a server: connections are refused.
	closed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := closed.URL
	closed.Close()

	c := newTestClient(t, WithBaseURL(url), WithRetryBase(2*time.Millisecond))
	_, err := c.GetCompanyFacts(context.Background(), "320193")
	if err == nil {
		t.Fatal("se esperaba error de red")
	}
	if !strings.Contains(err.Error(), "edgar: request") {
		t.Fatalf("error sin contexto 'edgar: request': %v", err)
	}
}

func TestClientGetXBRLFiling(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html/>`))
	}))
	defer srv.Close()

	c := newTestClient(t)
	body, err := c.GetXBRLFiling(context.Background(), srv.URL+"/Archives/edgar/data/320193/R1.htm")
	if err != nil {
		t.Fatalf("GetXBRLFiling falló: %v", err)
	}
	if gotUA != testUA {
		t.Fatalf("User-Agent incorrecto en filing: %q", gotUA)
	}
	if string(body) != "<html/>" {
		t.Fatalf("body inesperado: %s", body)
	}
}

func TestRateLimiterEnforcesRate(t *testing.T) {
	// 5 req/s => interval 200ms; two waits must take >= ~190ms.
	l := newRateLimiter(5)
	ctx := context.Background()

	start := time.Now()
	if err := l.wait(ctx); err != nil {
		t.Fatalf("primer wait falló: %v", err)
	}
	// Consume the pending token immediately, then the next wait must block.
	if err := l.wait(ctx); err != nil {
		t.Fatalf("segundo wait falló: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 190*time.Millisecond {
		t.Fatalf("el rate limiter no bloqueó: elapsed=%v (esperado >= ~190ms)", elapsed)
	}
}

func TestRateLimiterContextCancel(t *testing.T) {
	l := newRateLimiter(1) // 1 req/s => el segundo wait se bloquea 1s
	ctx := context.Background()
	if err := l.wait(ctx); err != nil {
		t.Fatalf("primer wait falló: %v", err)
	}
	cctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := l.wait(cctx); err == nil {
		t.Fatal("se esperaba error por cancelación de contexto")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error inesperado: %v", err)
	}
}

func TestParseCompanyTickers(t *testing.T) {
	data := []byte(`{"0":{"cik_str":320193,"ticker":"AAPL","title":"Apple Inc."},
		"1":{"cik_str":789019,"ticker":"MSFT","title":"MICROSOFT CORP"}}`)
	got, err := ParseCompanyTickers(data)
	if err != nil {
		t.Fatalf("ParseCompanyTickers falló: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("se esperaban 2 tickers, hay %d", len(got))
	}
	byTicker := map[string]CompanyTicker{}
	for _, e := range got {
		byTicker[e.Ticker] = e
	}
	aapl, ok := byTicker["AAPL"]
	if !ok {
		t.Fatal("faltó AAPL")
	}
	if aapl.CIKString != "0000320193" || aapl.Title != "Apple Inc." {
		t.Fatalf("AAPL incorrecto: %+v", aapl)
	}
}

func TestParseCompanyTickersInvalid(t *testing.T) {
	if _, err := ParseCompanyTickers([]byte(`not json`)); err == nil {
		t.Fatal("se esperaba error de formato")
	}
}

func TestNormalizeCIK(t *testing.T) {
	cases := []struct{ in, want string }{
		{"320193", "0000320193"},
		{"0000320193", "0000320193"},
		{"  789019  ", "0000789019"},
	}
	for _, c := range cases {
		got, err := NormalizeCIK(c.in)
		if err != nil || got != c.want {
			t.Errorf("NormalizeCIK(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "abc", "12345678901"} {
		if _, err := NormalizeCIK(bad); err == nil {
			t.Errorf("NormalizeCIK(%q) debería fallar", bad)
		}
	}
}
