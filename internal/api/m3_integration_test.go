//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/api"
	"github.com/miky/abys-invest/internal/storage"
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var err error
	pool, err = storage.Connect(ctx, dsn)
	if err != nil {
		panic("M3 integration: Connect falló: " + err.Error())
	}
	defer pool.Close()
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
	periodEnd := time.Date(2025, 9, 28, 0, 0, 0, 0, time.UTC)
	fundVal := func(concept string, v float64) storage.Fundamental {
		return storage.Fundamental{
			SecurityID: ids[m3Tick], Concept: concept, Value: &v, Unit: d(3),
			PeriodType: "P", PeriodStart: &periodEnd, PeriodEnd: periodEnd,
			FiscalYear: int16Ptr(2025), FiscalPeriod: &fy, FilingDate: &periodEnd,
			Source: src,
		}
	}
	funds := []storage.Fundamental{
		fundVal("net_earnings", 112000), fundVal("shares_outstanding", 15400),
		fundVal("shareholders_equity", 62000), fundVal("total_liabilities", 302000),
		fundVal("free_cash_flow", 98500), fundVal("long_term_debt", 98959),
		fundVal("short_term_debt", 19987), fundVal("cash_and_equivalents", 29943),
		fundVal("operating_cash_flow", 118254), fundVal("capex", -9445),
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
		g, _ := val["graham"].(float64)
		if g <= 0 {
			t.Fatalf("graham esperado >0 con EPS=7.27, got %v", val["graham"])
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
}
