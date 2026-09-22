package yahoo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/miky/abys-invest/internal/collect/finviz"
)

// quoteSummaryServer emulates the Yahoo crumb session (fc.yahoo.com cookies,
// getcrumb, and /v10/finance/quoteSummary).
func quoteSummaryServer(t *testing.T, sector, industry string, failSummary bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sm", Value: "test-session"})
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(CrumbPath, func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("sm"); err != nil {
			http.Error(w, "sin cookies", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "test-crumb-abc")
	})
	mux.HandleFunc("/v10/finance/quoteSummary/", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("crumb"); got != "test-crumb-abc" {
			http.Error(w, "crumb inválido", http.StatusUnauthorized)
			return
		}
		if failSummary {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		symbol := strings.TrimPrefix(r.URL.Path, "/v10/finance/quoteSummary/")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"quoteSummary":{"result":[{"assetProfile":{"sector":%q,"industry":%q},"symbol":%q}]}}`,
			sector, industry, symbol)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGetQuoteSummary(t *testing.T) {
	srv := quoteSummaryServer(t, "Technology", "Consumer Electronics", false)
	c := NewClient(WithBaseURL(srv.URL), WithCookieBase(srv.URL), WithCrumbBase(srv.URL))

	resp, err := c.GetQuoteSummary(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("GetQuoteSummary: %v", err)
	}
	if len(resp.QuoteSummary.Result) == 0 {
		t.Fatal("sin resultado")
	}
	ap := resp.QuoteSummary.Result[0].AssetProfile
	if ap.Sector != "Technology" || ap.Industry != "Consumer Electronics" {
		t.Fatalf("sector/industry incorrectos: %+v", ap)
	}
}

func TestGetQuoteSummaryRateLimit(t *testing.T) {
	srv := quoteSummaryServer(t, "Technology", "Consumer Electronics", true)
	c := NewClient(WithBaseURL(srv.URL), WithCookieBase(srv.URL), WithCrumbBase(srv.URL))

	ctx := context.Background()
	_, err := c.GetQuoteSummary(ctx, "AAPL")
	if err == nil {
		t.Fatal("se esperaba error por rate limit")
	}
	if !strings.Contains(err.Error(), "crumb") && !strings.Contains(err.Error(), "429") {
		// El 429 puede provenir del crumb o del summary; ambos son degradables.
		t.Logf("error obtenido: %v", err)
	}
}

// TestResolveSectorInfo cubre el plan D1 completo: Yahoo OK → sin fallback;
// Yahoo bloqueado (429) → Finviz fallback; ambos fallan → error.
func TestResolveSectorInfo(t *testing.T) {
	ctx := context.Background()

	t.Run("yahoo-ok", func(t *testing.T) {
		srv := quoteSummaryServer(t, "Technology", "Consumer Electronics", false)
		yc := NewClient(WithBaseURL(srv.URL), WithCookieBase(srv.URL), WithCrumbBase(srv.URL))
		fz := &fakeFinviz{err: fmt.Errorf("no debe usarse")}
		info, err := resolveSectorInfo(ctx, "AAPL", yc, fz)
		if err != nil {
			t.Fatalf("resolveSectorInfo: %v", err)
		}
		if deref(info.Sector) != "Technology" || deref(info.Industry) != "Consumer Electronics" {
			t.Fatalf("sector/industry incorrectos: %+v", info)
		}
		if fz.calls != 0 {
			t.Fatal("finviz no debe consultarse cuando yahoo funciona")
		}
	})

	t.Run("yahoo-429-finviz-fallback", func(t *testing.T) {
		srv := quoteSummaryServer(t, "", "", true)
		yc := NewClient(WithBaseURL(srv.URL), WithCookieBase(srv.URL), WithCrumbBase(srv.URL))
		fz := &fakeFinviz{
			info: &finviz.SectorInfo{Sector: "Tecnología", Industry: "Electrónica"},
		}
		info, err := resolveSectorInfo(ctx, "MSFT", yc, fz)
		if err != nil {
			t.Fatalf("fallback: %v", err)
		}
		if deref(info.Sector) != "Tecnología" {
			t.Fatalf("esperado sector de finviz, got %v", deref(info.Sector))
		}
		if fz.calls != 1 {
			t.Fatalf("finviz consultado %d veces, esperado 1", fz.calls)
		}
	})

	t.Run("ambos-fallan", func(t *testing.T) {
		srv := quoteSummaryServer(t, "", "", true)
		yc := NewClient(WithBaseURL(srv.URL), WithCookieBase(srv.URL), WithCrumbBase(srv.URL))
		fz := &fakeFinviz{err: errors.New("finviz caído")}
		if _, err := resolveSectorInfo(ctx, "TSLA", yc, fz); err == nil {
			t.Fatal("se esperaba error cuando ambas fuentes fallan")
		}
	})
}

type fakeFinviz struct {
	info  *finviz.SectorInfo
	err   error
	calls int
}

func (f *fakeFinviz) GetSectorInfo(_ context.Context, _ string) (*finviz.SectorInfo, error) {
	f.calls++
	return f.info, f.err
}
