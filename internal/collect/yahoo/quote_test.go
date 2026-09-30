package yahoo

import (
	"context"
	"encoding/json"
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
//
// keyStats es el JSON CRUDO del módulo defaultKeyStatistics (o "" para omitirlo
// del envelope): pasarlo tal cual es lo que permite probar que una beta con
// formato inesperado (string, {raw,fmt}, "NaN") NO rompe el unmarshal del
// envelope completo, incluido el sector de M2.
func quoteSummaryServer(t *testing.T, sector, industry string, failSummary bool, keyStats ...string) *httptest.Server {
	t.Helper()
	ks := ""
	if len(keyStats) > 0 {
		ks = keyStats[0]
	}
	var wantModules string // si != "", el servidor exige que modules la contenga
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
		if wantModules != "" && !strings.Contains(r.URL.Query().Get("modules"), wantModules) {
			http.Error(w, "falta el módulo "+wantModules, http.StatusBadRequest)
			return
		}
		if failSummary {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		symbol := strings.TrimPrefix(r.URL.Path, "/v10/finance/quoteSummary/")
		w.Header().Set("Content-Type", "application/json")
		ksField := ""
		if ks != "" {
			ksField = fmt.Sprintf(`,"defaultKeyStatistics":%s`, ks)
		}
		fmt.Fprintf(w, `{"quoteSummary":{"result":[{"assetProfile":{"sector":%q,"industry":%q},"symbol":%q%s}]}}`,
			sector, industry, symbol, ksField)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGetQuoteSummary(t *testing.T) {
	srv := quoteSummaryServer(t, "Technology", "Consumer Electronics", false, `{"beta":1.15}`)
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
	beta, err := KeyStatsFor(resp)
	if err != nil || beta == nil {
		t.Fatalf("beta no extraída: %v %v", beta, err)
	}
	if *beta != 1.15 {
		t.Fatalf("beta: esperado 1.15, got %v", *beta)
	}
}

func TestGetQuoteSummaryRateLimit(t *testing.T) {
	srv := quoteSummaryServer(t, "Technology", "Consumer Electronics", true, `{"beta":1.15}`)
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

// TestKeyStatsForBeta: la extracción tolera null, módulo ausente, result vacío y
// betas no creíbles (0, -1, 99). La ausencia de beta es DATO, no error.
func TestKeyStatsForBeta(t *testing.T) {
	build := func(t *testing.T, keyStats string) *QuoteSummaryResponse {
		t.Helper()
		body := `{"quoteSummary":{"result":[{"assetProfile":{"sector":"Technology"},"defaultKeyStatistics":` + keyStats + `}]}}`
		var resp QuoteSummaryResponse
		if err := json.Unmarshal([]byte(body), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return &resp
	}

	t.Run("número válido", func(t *testing.T) {
		beta, err := KeyStatsFor(build(t, `{"beta":1.23,"beta3Year":0.9}`))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if beta == nil || *beta != 1.23 {
			t.Fatalf("beta: %v", beta)
		}
	})
	for name, body := range map[string]string{
		"null":            `{"beta":null}`,
		"módulo sin beta": `{"beta3Year":0.9}`,
		"beta 0":          `{"beta":0}`,
		"beta -1":         `{"beta":-1}`,
		"beta 99":         `{"beta":99}`,
		"objeto vacío":    `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			beta, err := KeyStatsFor(build(t, body))
			if err != nil {
				t.Fatalf("la ausencia de beta no es error: %v", err)
			}
			if beta != nil {
				t.Fatalf("se esperaba nil, got %v", *beta)
			}
		})
	}
	t.Run("result vacío", func(t *testing.T) {
		var resp QuoteSummaryResponse
		if err := json.Unmarshal([]byte(`{"quoteSummary":{"result":[]}}`), &resp); err != nil {
			t.Fatal(err)
		}
		if beta, _ := KeyStatsFor(&resp); beta != nil {
			t.Fatalf("se esperaba nil, got %v", *beta)
		}
	})
	t.Run("resp nil", func(t *testing.T) {
		if beta, _ := KeyStatsFor(nil); beta != nil {
			t.Fatal("se esperaba nil")
		}
	})
}

// TestBetaFloatDecodificacionTolerante es la prueba de que una beta con formato
// inesperado NO rompe el envelope ni el sector de M2: el unmarshal del
// quoteSummary es único, así que un fallo aquí perdería también el sector.
func TestBetaFloatDecodificacionTolerante(t *testing.T) {
	tests := []struct {
		name    string
		keyStat string
		want    *float64
	}{
		{"número", `{"beta":1.15}`, f64(1.15)},
		{"string numérico", `{"beta":"1.15"}`, f64(1.15)},
		{"wrapper raw/fmt", `{"beta":{"raw":1.15,"fmt":"1.15"}}`, f64(1.15)},
		{"wrapper sin raw", `{"beta":{"fmt":"1.15"}}`, nil},
		{"NaN como string", `{"beta":"NaN"}`, nil},
		{"string vacío", `{"beta":""}`, nil},
		{"null", `{"beta":null}`, nil},
		{"ausente", `{}`, nil},
		{"objeto inesperado", `{"beta":{"raw":"nope"}}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"quoteSummary":{"result":[{"assetProfile":{"sector":"Technology","industry":"Consumer Electronics"},"defaultKeyStatistics":` + tt.keyStat + `}]}}`
			var resp QuoteSummaryResponse
			if err := json.Unmarshal([]byte(body), &resp); err != nil {
				t.Fatalf("el envelope no debe fallar al decodificar: %v", err)
			}
			// El sector de M2 sobrevive SIEMPRE.
			ap := resp.QuoteSummary.Result[0].AssetProfile
			if ap.Sector != "Technology" || ap.Industry != "Consumer Electronics" {
				t.Fatalf("el sector se perdió: %+v", ap)
			}
			beta, _ := KeyStatsFor(&resp)
			switch {
			case tt.want == nil && beta != nil:
				t.Fatalf("se esperaba beta ausente, got %v", *beta)
			case tt.want != nil && beta == nil:
				t.Fatalf("se esperaba %v, got ausente", *tt.want)
			case tt.want != nil && *beta != *tt.want:
				t.Fatalf("esperado %v, got %v", *tt.want, *beta)
			}
		})
	}
}

// TestGetQuoteSummaryPideDefaultKeyStatistics prueba de que la beta NO cuesta un
// request extra: el mismo quoteSummary trae sector y key statistics.
func TestGetQuoteSummaryPideDefaultKeyStatistics(t *testing.T) {
	srv := quoteSummaryServer(t, "Technology", "Consumer Electronics", false, `{"beta":1.15}`)
	c := NewClient(WithBaseURL(srv.URL), WithCookieBase(srv.URL), WithCrumbBase(srv.URL))

	resp, err := c.GetQuoteSummary(context.Background(), "AAPL")
	if err != nil {
		t.Fatalf("GetQuoteSummary: %v", err)
	}
	beta, _ := KeyStatsFor(resp)
	if beta == nil {
		t.Fatal("la beta debe venir en la misma llamada")
	}
	if !strings.Contains(quoteModules, "defaultKeyStatistics") {
		t.Fatalf("quoteModules debe incluir defaultKeyStatistics: %q", quoteModules)
	}
	if !strings.Contains(quoteModules, "assetProfile") {
		t.Fatalf("quoteModules debe incluir assetProfile: %q", quoteModules)
	}
}

func f64(v float64) *float64 { return &v }

// TestResolveReferenceData sustituye a TestResolveSectorInfo (M6a D17): la beta
// es independiente del sector y ambos degradan por separado.
func TestResolveReferenceData(t *testing.T) {
	ctx := context.Background()

	t.Run("yahoo-sector-y-beta", func(t *testing.T) {
		srv := quoteSummaryServer(t, "Technology", "Consumer Electronics", false, `{"beta":1.15}`)
		yc := NewClient(WithBaseURL(srv.URL), WithCookieBase(srv.URL), WithCrumbBase(srv.URL))
		fz := &fakeFinviz{err: fmt.Errorf("no debe usarse")}
		ref, err := resolveReferenceData(ctx, "AAPL", yc, fz)
		if err != nil {
			t.Fatalf("resolveReferenceData: %v", err)
		}
		if deref(ref.Sector) != "Technology" || deref(ref.Industry) != "Consumer Electronics" {
			t.Fatalf("sector/industry incorrectos: %+v", ref)
		}
		if ref.Beta == nil || *ref.Beta != 1.15 {
			t.Fatalf("beta: %v", ref.Beta)
		}
		if fz.calls != 0 {
			t.Fatal("finviz no debe consultarse cuando yahoo trae sector")
		}
	})

	t.Run("yahoo-beta-sin-assetProfile-finviz-solo-sector", func(t *testing.T) {
		// Envelope con defaultKeyStatistics pero SIN assetProfile: la beta se
		// conserva y el sector sale de Finviz.
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.SetCookie(w, &http.Cookie{Name: "sm", Value: "test-session"})
			w.WriteHeader(http.StatusOK)
		})
		mux.HandleFunc(CrumbPath, func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "test-crumb-abc")
		})
		mux.HandleFunc("/v10/finance/quoteSummary/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"quoteSummary":{"result":[{"symbol":"GEV","defaultKeyStatistics":{"beta":0.62}}]}}`)
		})
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		yc := NewClient(WithBaseURL(srv.URL), WithCookieBase(srv.URL), WithCrumbBase(srv.URL))
		fz := &fakeFinviz{info: &finviz.SectorInfo{Sector: "Industriales", Industry: "Electricidad"}}
		ref, err := resolveReferenceData(ctx, "GEV", yc, fz)
		if err != nil {
			t.Fatalf("resolveReferenceData: %v", err)
		}
		if ref.Beta == nil || *ref.Beta != 0.62 {
			t.Fatalf("la beta debe persistirse aunque falte el sector: %v", ref.Beta)
		}
		if deref(ref.Sector) != "Industriales" {
			t.Fatalf("sector esperado de finviz, got %q", deref(ref.Sector))
		}
		if fz.calls != 1 {
			t.Fatalf("finviz consultado %d veces, esperado 1", fz.calls)
		}
	})

	t.Run("yahoo-429-fallback-sin-beta", func(t *testing.T) {
		srv := quoteSummaryServer(t, "", "", true, `{"beta":1.15}`)
		yc := NewClient(WithBaseURL(srv.URL), WithCookieBase(srv.URL), WithCrumbBase(srv.URL))
		fz := &fakeFinviz{info: &finviz.SectorInfo{Sector: "Tecnología", Industry: "Electrónica"}}
		ref, err := resolveReferenceData(ctx, "MSFT", yc, fz)
		if err != nil {
			t.Fatalf("fallback: %v", err)
		}
		if deref(ref.Sector) != "Tecnología" {
			t.Fatalf("esperado sector de finviz, got %q", deref(ref.Sector))
		}
		if ref.Beta != nil {
			t.Fatalf("Finviz no aporta beta: %v", *ref.Beta)
		}
		if fz.calls != 1 {
			t.Fatalf("finviz consultado %d veces, esperado 1", fz.calls)
		}
	})

	t.Run("ambos-fallan", func(t *testing.T) {
		srv := quoteSummaryServer(t, "", "", true, `{"beta":1.15}`)
		yc := NewClient(WithBaseURL(srv.URL), WithCookieBase(srv.URL), WithCrumbBase(srv.URL))
		fz := &fakeFinviz{err: errors.New("finviz caído")}
		if _, err := resolveReferenceData(ctx, "TSLA", yc, fz); err == nil {
			t.Fatal("se esperaba error cuando ambas fuentes fallan")
		}
	})

	t.Run("beta-inutilizable-con-sector", func(t *testing.T) {
		// beta null + sector presente: se persiste el sector y la beta queda
		// ausente (no un 1.0 inventado).
		srv := quoteSummaryServer(t, "Technology", "Consumer Electronics", false, `{"beta":null}`)
		yc := NewClient(WithBaseURL(srv.URL), WithCookieBase(srv.URL), WithCrumbBase(srv.URL))
		fz := &fakeFinviz{err: fmt.Errorf("no debe usarse")}
		ref, err := resolveReferenceData(ctx, "AAPL", yc, fz)
		if err != nil {
			t.Fatalf("resolveReferenceData: %v", err)
		}
		if deref(ref.Sector) != "Technology" {
			t.Fatalf("sector: %q", deref(ref.Sector))
		}
		if ref.Beta != nil {
			t.Fatalf("beta null debe quedar ausente, got %v", *ref.Beta)
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
