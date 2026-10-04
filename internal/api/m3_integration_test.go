//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/api"
	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/testsupport"
	"github.com/miky/abys-invest/internal/valuation"
)

// E2E del plan M3 (T10/T13): el router sirve los endpoints de valoración,
// score, comparables y backtest sobre datos de fixture insertados en la BD.
// Ejecución: DATABASE_URL=... go test ./internal/api -p 1 -tags=integration -count=1
//
// Los datos usan el prefijo M3TST para no colisionar con securities reales;
// el suite storage trunca las tablas de datos después de correr.

const (
	m3Tick  = "M3TST"
	m3PeerA = "M3TSA"
	m3PeerB = "M3TSB"

	m3Sector = "Technology"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		os.Exit(0) // sin BD: suite de integración se omite (sin error)
	}
	// Validate and redact DSN before connecting (ADR D30)
	// Validate the DSN targets a test DB; only its redacted form is ever logged (ADR D30).
	testsupport.EnsureTestDSN(dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var err error
	pool, err = storage.Connect(ctx, dsn)
	if err != nil {
		panic("M3 integration: Connect falló: " + err.Error())
	}
	defer pool.Close()
	if err := storage.EnsureTestDatabase(ctx, pool); err != nil {
		panic("M3 integration: guard de BD de test falló (no se debe tocar producción): " + err.Error())
	}
	// Serialise the database phase of the integration suites (shared TRUNCATEs),
	// which is what lets them run WITHOUT `-p 1`.
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	releaseLock, err := testsupport.LockIntegrationDB(lockCtx, pool)
	lockCancel()
	if err != nil {
		panic("M3 integration: no se pudo tomar el advisory lock: " + err.Error())
	}
	defer releaseLock()
	if err := storage.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		panic("M3 integration: migraciones fallaron: " + err.Error())
	}
	os.Exit(m.Run())
}

// seedFixture crea un security con fundamentals FY, serie de precios
// (300 barras sintéticas), métricas, pares de sector y un score.
func seedFixture(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	d := func(n int) *string { s := string(rune('A' + n%26)); return &s }
	sector := m3Sector
	fy := "FY"
	src := "test"
	secs := []*storage.Security{
		{Ticker: m3Tick, CIK: "900000001", Name: "M3 Test Co", Type: "stock", Currency: "USD", Status: "active", Sector: &sector, Exchange: d(0)},
		{Ticker: m3PeerA, CIK: "900000002", Name: "M3 Peer A", Type: "stock", Currency: "USD", Status: "active", Sector: &sector, Exchange: d(0)},
		{Ticker: m3PeerB, CIK: "900000003", Name: "M3 Peer B", Type: "stock", Currency: "USD", Status: "active", Sector: &sector, Exchange: d(0)},
	}
	var ids = map[string]int64{}
	for _, s := range secs {
		out, err := storage.UpsertSecurity(ctx, pool, s)
		if err != nil {
			t.Fatalf("upsert security %s: %v", s.Ticker, err)
		}
		ids[s.Ticker] = out.ID
	}

	// Fundamentals FY del ticker principal (EPS = 112000/15400 = 7.27; FCF 98B).
	// Usar FY2024 con filing_date <= asOf (precio ~2024-10-28) para evitar look-ahead.
	// Duración ~365 días para facts de duración; period_start IS NULL para instants.
	periodEnd := time.Date(2024, 9, 30, 0, 0, 0, 0, time.UTC)
	periodStart := periodEnd.AddDate(0, 0, -365)
	filingDate := time.Date(2024, 10, 15, 0, 0, 0, 0, time.UTC) // <= asOf (~2024-10-28)
	fundVal := func(concept string, v float64, instant bool) storage.Fundamental {
		f := storage.Fundamental{
			SecurityID: ids[m3Tick], Concept: concept, Value: &v, Unit: d(3),
			PeriodEnd:  periodEnd,
			FiscalYear: int16Ptr(2024), FiscalPeriod: &fy, FilingDate: &filingDate,
			Source: src,
		}
		if instant {
			f.PeriodType = "instant"
			f.PeriodStart = nil // instant facts: period_start IS NULL (EDGAR real)
		} else {
			f.PeriodType = "duration"
			f.PeriodStart = &periodStart // duration facts: ~1 año (330-400 días)
		}
		return f
	}
	funds := []storage.Fundamental{
		fundVal("net_earnings", 112000, false), fundVal("shares_outstanding", 15400, true),
		fundVal("free_cash_flow", 98500, false), fundVal("cash_and_equivalents", 29943, true),
		fundVal("operating_cash_flow", 118254, false), fundVal("capex", -9445, false),
		// total_debt es derivado (long_term_debt + short_term_debt) en EDGAR real;
		// aquí lo sembramos directo para que el loader lo encuentre.
		fundVal("total_debt", 118946, true),
		fundVal("depreciation_amortization", 11000, false),
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := storage.UpsertFundamentals(ctx, tx, funds); err != nil {
		t.Fatalf("upsert fundamentals: %v", err)
	}
	// Serie de precios: 300 días sintéticos (100 + i*0.2 → alcista prolija).
	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	prices := make([]storage.DailyPrice, 0, 300)
	for i := 0; i < 300; i++ {
		close := 100 + float64(i)*0.2
		prices = append(prices, storage.DailyPrice{
			SecurityID: ids[m3Tick], Date: start.AddDate(0, 0, i),
			Close: close, AdjustedClose: close, Open: &close, High: &close, Low: &close, Source: "test",
		})
	}
	if err := storage.UpsertDailyPrices(ctx, tx, prices); err != nil {
		t.Fatalf("upsert prices: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Métricas y score del ticker principal.
	asOf := start.AddDate(0, 0, 299)
	metricVals := []struct {
		name  string
		value *float64
	}{
		{"pe_ratio", fp(25.9)}, {"pb_ratio", fp(42.0)}, {"fcf_yield", fp(3.0)},
		{"roe", fp(1.78)}, {"de_ratio", fp(4.9)},
	}
	metricsRows := make([]storage.DerivedMetric, 0, len(metricVals))
	for _, mv := range metricVals {
		metricsRows = append(metricsRows, storage.DerivedMetric{
			SecurityID: ids[m3Tick], AsOf: asOf, Metric: mv.name, Value: mv.value,
			InputsSnapshot: []byte(`{"source":"test"}`), ModelVersion: "1.0.0",
		})
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin metrics: %v", err)
	}
	if err := storage.UpsertDerivedMetrics(ctx, tx, metricsRows); err != nil {
		t.Fatalf("upsert metrics: %v", err)
	}
	scoreRow := &storage.Score{
		SecurityID: ids[m3Tick], AsOf: asOf, Score: 61, Signal: "mantener",
		Justification: "M3 fixture: score de integración", ModelVersion: "1.0.0",
	}
	if err := storage.UpsertScore(ctx, tx, scoreRow); err != nil {
		t.Fatalf("upsert score: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit score: %v", err)
	}
}

func int16Ptr(v int16) *int16 { return &v }
func fp(v float64) *float64   { return &v }

func do(t *testing.T, router http.Handler, method, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	router.ServeHTTP(rec, req)
	var body map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec, body
}

func errCode(t *testing.T, body map[string]any) string {
	t.Helper()
	e, _ := body["error"].(map[string]any)
	if e == nil {
		return ""
	}
	c, _ := e["code"].(string)
	return c
}

func TestM3Endpoints(t *testing.T) {
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	seedFixture(t)
	router := api.NewRouter(pool)

	t.Run("health-ok", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/health")
		if rec.Code != http.StatusOK {
			t.Fatalf("/health esperado 200, got %d", rec.Code)
		}
		if body["status"] != "ok" {
			t.Fatalf("status inesperado: %v", body)
		}
	})

	t.Run("security-ok", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/securities/"+m3Tick)
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		if body["ticker"] != m3Tick {
			t.Fatalf("ticker inesperado: %v", body)
		}
	})

	t.Run("security-not-found", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/securities/NOPE")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("esperado 404, got %d", rec.Code)
		}
		if errCode(t, body) != "not_found" {
			t.Fatalf("error code inesperado: %v", body)
		}
	})

	t.Run("security-invalid-ticker", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/securities/AAAA%20BBBB%20CCCC%20DDDD%20EEEE%20FFFF")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400, got %d", rec.Code)
		}
		if errCode(t, body) != "validation_error" {
			t.Fatalf("error code inesperado: %v", body)
		}
	})

	t.Run("prices-ok", func(t *testing.T) {
		rec, _ := do(t, router, http.MethodGet, "/prices/"+m3Tick)
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("metrics-ok", func(t *testing.T) {
		rec, _ := do(t, router, http.MethodGet, "/metrics/"+m3Tick)
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("valuation-graham", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/valuation/"+m3Tick)
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		val, ok := (body["value"]).(map[string]any)
		if !ok {
			t.Fatalf("sin bloque value: %v", body)
		}
		// Contrato 2.0.0: `graham` es un bloque con status y los TRES
		// escenarios; el valor base es `graham.base` (ya no un número plano).
		g, ok := val["graham"].(map[string]any)
		if !ok {
			t.Fatalf("value.graham debe ser objeto 2.0.0, got %T: %v", val["graham"], val)
		}
		base, _ := g["base"].(float64)
		if base <= 0 {
			t.Fatalf("graham.base esperado >0 con EPS=7.27, got %v", g)
		}
		if g["status"] != "available" {
			t.Fatalf("graham.status esperado available con EPS positivo, got %v", g["status"])
		}
		if val["model_version"] != valuation.ModelVersion {
			t.Fatalf("model_version esperado %s, got %v", valuation.ModelVersion, val["model_version"])
		}
		// Sin fila persistida el endpoint recalcula: debe declararlo.
		if src := body["valuation_source"]; src != "computed" && src != "persisted" {
			t.Fatalf("valuation_source ausente o inválido: %v", body["valuation_source"])
		}
		// El 1.x `upside_pct` "vs consenso" desaparece en 2.0.0: no hay consenso.
		if _, ok := body["upside_pct"]; ok {
			t.Fatalf("upside_pct ya no existe en el contrato 2.0.0: %v", body)
		}
		if body["price"] == nil {
			t.Fatalf("falta price en /valuation: %v", body)
		}
	})

	t.Run("score-ok", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/score/"+m3Tick)
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		if body["score"] == nil || body["signal"] == nil {
			t.Fatalf("score/signal ausentes: %v", body)
		}
	})

	t.Run("scores-list", func(t *testing.T) {
		rec, _ := do(t, router, http.MethodGet, "/scores?ticker="+m3Tick)
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("comparables-ok", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/compare/comparables?ticker="+m3Tick)
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		peers, _ := body["peer_count"].(float64)
		if peers < 0 {
			t.Fatalf("peer_count inesperado: %v", body)
		}
	})

	t.Run("backtest-sma-ok", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/backtest/sma?ticker="+m3Tick)
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		if body["strategy"] != "sma" || body["params"] == nil || body["results"] == nil {
			t.Fatalf("respuesta backtest incompleta (D8: strategy/params/results): %v", body)
		}
		results, _ := body["results"].(map[string]any)
		row, ok := results[m3Tick].(map[string]any)
		if !ok {
			t.Fatalf("resultados por ticker esperados, got %v", results)
		}
		for _, metric := range []string{"total_return", "cagr", "sharpe", "max_drawdown", "total_trades"} {
			if row[metric] == nil {
				t.Fatalf("métrica D10 %q ausente en resultados: %v", metric, row)
			}
		}
	})

	t.Run("compare-multi-ticker-ok", func(t *testing.T) {
		// D8: comparativa de activos con rendimiento normalizado y riesgo.
		rec, body := do(t, router, http.MethodGet, "/compare?tickers="+m3Tick+","+m3PeerA)
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		np, _ := body["normalized_performance"].(map[string]any)
		rm, _ := body["risk_metrics"].(map[string]any)
		if np[m3Tick] == nil || rm[m3Tick] == nil {
			t.Fatalf("comparativa incompleta (D8): %v", body)
		}
		points, _ := np[m3Tick].([]any)
		if len(points) < 2 {
			t.Fatalf("series normalizadas esperadas (≥2 puntos), got %d", len(points))
		}
		first, _ := points[0].(map[string]any)
		if first["index"] != 100.0 {
			t.Fatalf("primer índice debe ser 100 (base normalizada), got %v", first["index"])
		}
	})

	t.Run("backtest-strategy-invalida", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/backtest/rsi?ticker="+m3Tick)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400, got %d", rec.Code)
		}
		if errCode(t, body) != "unsupported" {
			t.Fatalf("error code inesperado: %v", body)
		}
	})

	// Validación de NaN/Inf en parámetros float (fix reviewer finding)
	t.Run("compare-rf-NaN-rechazado", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/compare?tickers="+m3Tick+","+m3PeerA+"&rf=NaN")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400 para rf=NaN, got %d: %s", rec.Code, rec.Body.String())
		}
		if errCode(t, body) != "validation_error" {
			t.Fatalf("error code esperado validation_error, got: %v", body)
		}
	})

	t.Run("compare-rf-Infinity-rechazado", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/compare?tickers="+m3Tick+","+m3PeerA+"&rf=Infinity")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400 para rf=Infinity, got %d: %s", rec.Code, rec.Body.String())
		}
		if errCode(t, body) != "validation_error" {
			t.Fatalf("error code esperado validation_error, got: %v", body)
		}
	})

	t.Run("compare-rf-neg-Infinity-rechazado", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/compare?tickers="+m3Tick+","+m3PeerA+"&rf=-Infinity")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400 para rf=-Infinity, got %d: %s", rec.Code, rec.Body.String())
		}
		if errCode(t, body) != "validation_error" {
			t.Fatalf("error code esperado validation_error, got: %v", body)
		}
	})

	t.Run("compare-rf-valido", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/compare?tickers="+m3Tick+","+m3PeerA+"&rf=0.02")
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200 para rf=0.02, got %d: %s", rec.Code, rec.Body.String())
		}
		if body["risk_metrics"] == nil {
			t.Fatalf("risk_metrics ausente en respuesta válida: %v", body)
		}
	})

	t.Run("backtest-initial_capital-NaN-rechazado", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/backtest/sma?ticker="+m3Tick+"&initial_capital=NaN")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400 para initial_capital=NaN, got %d: %s", rec.Code, rec.Body.String())
		}
		if errCode(t, body) != "validation_error" {
			t.Fatalf("error code esperado validation_error, got: %v", body)
		}
	})

	t.Run("backtest-initial_capital-Infinity-rechazado", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/backtest/sma?ticker="+m3Tick+"&initial_capital=Infinity")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400 para initial_capital=Infinity, got %d: %s", rec.Code, rec.Body.String())
		}
		if errCode(t, body) != "validation_error" {
			t.Fatalf("error code esperado validation_error, got: %v", body)
		}
	})

	t.Run("backtest-initial_capital-valido", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/backtest/sma?ticker="+m3Tick+"&initial_capital=10000")
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200 para initial_capital=10000, got %d: %s", rec.Code, rec.Body.String())
		}
		if body["results"] == nil {
			t.Fatalf("results ausente en respuesta válida: %v", body)
		}
	})

	// --- NaN serialization guards (reviewer round 8, items 2a/2b) ---

	// ZZNAN: fixture with a NaN adjusted_close to trigger marshal failure in /compare/history
	const zznanTicker = "ZZNAN"

	t.Run("compare-history-NaN-adjusted_close-500", func(t *testing.T) {
		// Seed a security with one NaN adjusted_close price
		ctx := context.Background()
		sector := "Test"
		sec := &storage.Security{Ticker: zznanTicker, CIK: "999999999", Name: "NaN Test", Type: "stock", Currency: "USD", Status: "active", Sector: &sector}
		if _, err := storage.UpsertSecurity(ctx, pool, sec); err != nil {
			t.Fatalf("upsert ZZNAN: %v", err)
		}
		// Get the security ID
		sec, err := storage.GetSecurityByTicker(ctx, pool, zznanTicker)
		if err != nil {
			t.Fatalf("get security ZZNAN: %v", err)
		}
		id := sec.ID

		// Insert prices: one normal, one with NaN adjusted_close
		prices := []storage.DailyPrice{
			{SecurityID: id, Date: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), Close: 100, AdjustedClose: 100, Source: "test"},
			{SecurityID: id, Date: time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC), Close: 101, AdjustedClose: math.NaN(), Source: "test"},
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := storage.UpsertDailyPrices(ctx, tx, prices); err != nil {
			t.Fatalf("upsert prices: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}

		// Request history - should get 500 with JSON error envelope
		rec, body := do(t, router, http.MethodGet, "/compare/history?ticker="+zznanTicker)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("esperado 500 para NaN en adjusted_close, got %d: %s", rec.Code, rec.Body.String())
		}
		if errCode(t, body) != "internal_error" {
			t.Fatalf("error code esperado internal_error, got: %v", body)
		}
		// Body must be valid JSON with error envelope
		if body["error"] == nil {
			t.Fatalf("cuerpo debe tener envelope error: %v", body)
		}

		// Cleanup: remove the test security and its prices
		tx, err = pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin cleanup: %v", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM daily_prices WHERE security_id = $1`, id); err != nil {
			t.Fatalf("cleanup prices: %v", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM securities WHERE id = $1`, id); err != nil {
			t.Fatalf("cleanup security: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit cleanup: %v", err)
		}
	})

	t.Run("compare-comparables-sin-datos-200", func(t *testing.T) {
		// Security without sector, no peers → should return 200 with empty peers
		ctx := context.Background()
		sec := &storage.Security{Ticker: "ZZEMPTY", CIK: "999999998", Name: "Empty Test", Type: "stock", Currency: "USD", Status: "active", Sector: nil}
		if _, err := storage.UpsertSecurity(ctx, pool, sec); err != nil {
			t.Fatalf("upsert ZZEMPTY: %v", err)
		}
		sec, err := storage.GetSecurityByTicker(ctx, pool, "ZZEMPTY")
		if err != nil {
			t.Fatalf("get security ZZEMPTY: %v", err)
		}
		id := sec.ID

		rec, body := do(t, router, http.MethodGet, "/compare/comparables?ticker=ZZEMPTY")
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200 para security sin sector, got %d: %s", rec.Code, rec.Body.String())
		}
		peers, _ := body["peers"].([]any)
		if peers != nil && len(peers) != 0 {
			t.Fatalf("peers esperado vacío, got: %v", body)
		}

		// Cleanup
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin cleanup: %v", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM securities WHERE id = $1`, id); err != nil {
			t.Fatalf("cleanup security: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit cleanup: %v", err)
		}
	})
}
