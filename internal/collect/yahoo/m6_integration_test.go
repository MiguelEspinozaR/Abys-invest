//go:build integration

package yahoo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/testsupport"
)

// M6a integration coverage: price consistency (§22) y persistencia de la beta
// observada de Yahoo (D17). Sin red externa: todo con httptest + fixtures.

// TestIngestQuoteNoSobrescribeBarraHistorica es la regresión de §22: el quote
// actualiza SOLO `close` de la barra del exchange y deja intactos
// adjusted_close, OHLC y volume de la serie histórica.
func TestIngestQuoteNoSobrescribeBarraHistorica(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: "M6YAHOO", CIK: "9900000006", Name: "M6 Yahoo Quote",
		Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM daily_prices WHERE security_id=$1`, sec.ID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	client := testClient(t)
	if _, err := client.IngestPrices(ctx, pool, sec.ID, "AAPL"); err != nil {
		t.Fatalf("IngestPrices: %v", err)
	}
	before, err := storage.GetLatestPrice(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("GetLatestPrice: %v", err)
	}

	// El invariante real es que `close` quede igual al cierre regular del quote
	// (no "que cambie": con el fixture real el cierre de la serie y el del quote
	// pueden coincidir y ese es justamente el caso idempotente).
	quoteBar, err := client.GetQuoteBar(ctx, "AAPL")
	if err != nil {
		t.Fatalf("GetQuoteBar: %v", err)
	}
	after, err := client.IngestQuote(ctx, pool, sec.ID, "AAPL")
	if err != nil {
		t.Fatalf("IngestQuote: %v", err)
	}

	if after.Date.Format("2006-01-02") != before.Date.Format("2006-01-02") {
		t.Fatalf("el quote debe escribir en la fecha del exchange: antes %s, después %s",
			before.Date.Format("2006-01-02"), after.Date.Format("2006-01-02"))
	}
	if after.AdjustedClose != before.AdjustedClose {
		t.Fatalf("adjusted_close NO debe cambiar: %v -> %v", before.AdjustedClose, after.AdjustedClose)
	}
	if after.Open == nil || before.Open == nil || *after.Open != *before.Open {
		t.Fatalf("open no debe cambiar: %v -> %v", before.Open, after.Open)
	}
	if after.High == nil || before.High == nil || *after.High != *before.High {
		t.Fatalf("high no debe cambiar: %v -> %v", before.High, after.High)
	}
	if after.Low == nil || before.Low == nil || *after.Low != *before.Low {
		t.Fatalf("low no debe cambiar: %v -> %v", before.Low, after.Low)
	}
	if after.Volume == nil || before.Volume == nil || *after.Volume != *before.Volume {
		t.Fatalf("volume no debe cambiar: %v -> %v", before.Volume, after.Volume)
	}
	if after.Close != quoteBar.Price {
		t.Fatalf("close debe quedar en el cierre regular del quote (%v), quedó en %v", quoteBar.Price, after.Close)
	}
	// Idempotencia: reingerir el mismo quote no cambia nada.
	again, err := client.IngestQuote(ctx, pool, sec.ID, "AAPL")
	if err != nil {
		t.Fatalf("IngestQuote (2ª vez): %v", err)
	}
	if again.Close != after.Close || again.AdjustedClose != after.AdjustedClose || again.Volume == nil && after.Volume != nil {
		t.Fatalf("la reingesta del quote no debe ser idempotente: %+v vs %+v", again, after)
	}
	if after.Source != "yahoo" {
		t.Fatalf("una barra histórica no se degrada a 'yahoo_quote': %q", after.Source)
	}

	// Ninguna barra del universo puede quedarse sin OHLC por efecto del quote.
	var sinOHLC int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM daily_prices WHERE security_id=$1 AND open IS NULL`, sec.ID).Scan(&sinOHLC); err != nil {
		t.Fatalf("count sin OHLC: %v", err)
	}
	if sinOHLC != 0 {
		t.Fatalf("hay %d barras sin OHLC tras el quote (§22)", sinOHLC)
	}
}

// TestIngestQuoteInsertaBarraSintetica: sin fila para la fecha del exchange se
// inserta una barra con adjusted_close = close y source = 'yahoo_quote' (p. ej.
// un fin de semana), sin inventar OHLC.
func TestIngestQuoteInsertaBarraSintetica(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: "M6WEEK", CIK: "9900000007", Name: "M6 Yahoo Weekend",
		Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM daily_prices WHERE security_id=$1`, sec.ID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	client := testClient(t)
	bar, err := client.IngestQuote(ctx, pool, sec.ID, "AAPL")
	if err != nil {
		t.Fatalf("IngestQuote: %v", err)
	}
	if bar.Source != "yahoo_quote" {
		t.Fatalf("source de la barra nueva: %q", bar.Source)
	}
	if bar.AdjustedClose != bar.Close {
		t.Fatalf("adjusted_close = close en la barra de quote: %v vs %v", bar.AdjustedClose, bar.Close)
	}
	if bar.Open != nil {
		t.Fatal("una barra de quote no trae OHLC")
	}
	if bar.Date.After(time.Now().UTC().Add(48 * time.Hour)) {
		t.Fatalf("fecha imposible: %s", bar.Date.Format("2006-01-02"))
	}
}

// TestEnrichReferenceDataPersisteBeta (D17): la beta del defaultKeyStatistics de
// Yahoo se guarda en securities.beta con beta_updated_at; un "beta": null deja
// la columna en NULL (degradación explícita, no un 1.0 inventado).
func TestEnrichReferenceDataPersisteBeta(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: "M6BETA", CIK: "9900000008", Name: "M6 Beta",
		Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	ctxSec, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	updated, err := enrichReferenceData(ctxSec, pool, []string{"M6BETA"},
		betaServer(t, `{"beta":1.15}`), &fakeFinviz{err: fmt.Errorf("no debe usarse")})
	if err != nil {
		t.Fatalf("enrichReferenceData: %v", err)
	}
	if updated != 1 {
		t.Fatalf("se esperaba 1 security enriquecida, hay %d", updated)
	}
	got, err := storage.GetSecurityByTicker(ctx, pool, "M6BETA")
	if err != nil {
		t.Fatalf("GetSecurityByTicker: %v", err)
	}
	if got.Beta == nil || *got.Beta != 1.15 {
		t.Fatalf("beta no persistida: %+v", got.Beta)
	}
	if got.BetaUpdatedAt == nil || got.BetaUpdatedAt.IsZero() {
		t.Fatal("beta_updated_at debe fijarse")
	}
	if deref(got.Sector) != "Technology" {
		t.Fatalf("sector: %q", deref(got.Sector))
	}
	// ADR D29: el job sector escribe TAMBIÉN la fila de beta_history, no solo la
	// caché. Y el invariante "caché == última fila de la historia" debe cumplirse.
	testsupport.AssertBetaCacheMatchesHistory(t, pool, got.ID)
	obs, err := storage.GetLatestBetaObservation(ctx, pool, got.ID)
	if err != nil {
		t.Fatalf("GetLatestBetaObservation: %v", err)
	}
	if obs.Source != storage.BetaSourceYahoo {
		t.Fatalf("source de la observación = %q, esperado %q", obs.Source, storage.BetaSourceYahoo)
	}

	// Security sin beta previa + "beta": null → NULL (no 1.0), y la fila de WACC
	// que se escriba después tendrá que degradar a configured_fallback.
	if _, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: "M6NOBETA", CIK: "9900000009", Name: "M6 Sin Beta",
		Type: "stock", Currency: "USD", Status: "active",
	}); err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	if _, err := enrichReferenceData(ctxSec, pool, []string{"M6NOBETA"},
		betaServer(t, `{"beta":null}`), &fakeFinviz{err: fmt.Errorf("no debe usarse")}); err != nil {
		t.Fatalf("enrichReferenceData (sin beta): %v", err)
	}
	got, err = storage.GetSecurityByTicker(ctx, pool, "M6NOBETA")
	if err != nil {
		t.Fatalf("GetSecurityByTicker: %v", err)
	}
	if got.Beta != nil {
		t.Fatalf("beta null debe quedar NULL, got %v", *got.Beta)
	}
	// Una pasada sin beta no inventa una observación.
	if _, err := storage.GetLatestBetaObservation(ctx, pool, got.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("beta null no debe crear fila en beta_history: %v", err)
	}

	// Y la beta conocida NO se borra con una segunda pasada sin beta.
	if _, err := enrichReferenceData(ctxSec, pool, []string{"M6BETA"},
		betaServer(t, `{"beta":null}`), &fakeFinviz{err: fmt.Errorf("no debe usarse")}); err != nil {
		t.Fatalf("enrichReferenceData (2): %v", err)
	}
	got, _ = storage.GetSecurityByTicker(ctx, pool, "M6BETA")
	if got.Beta == nil || *got.Beta != 1.15 {
		t.Fatalf("la beta previa debe sobrevivir a un hueco de Yahoo: %+v", got.Beta)
	}
	_ = sec
}

// betaServer sirve un quoteSummary con el keyStats crudo indicado y EXIGE que
// la petición incluya defaultKeyStatistics (si no, responde 400).
func betaServer(t *testing.T, keyStats string) *Client {
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
		if !strings.Contains(r.URL.Query().Get("modules"), "defaultKeyStatistics") {
			http.Error(w, "falta defaultKeyStatistics", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"quoteSummary":{"result":[{"assetProfile":{"sector":"Technology","industry":"Consumer Electronics"},"defaultKeyStatistics":%s}]}}`, keyStats)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewClient(WithBaseURL(srv.URL), WithCookieBase(srv.URL), WithCrumbBase(srv.URL), WithRequestsPerSecond(1000), WithRetryBase(time.Millisecond))
}
