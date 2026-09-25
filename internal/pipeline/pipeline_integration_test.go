//go:build integration

package pipeline

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
)

// Integración del pipeline M4c (B4): RefreshMetricsAndScores recalcula y
// persiste derived_metrics + scores para las securities activas con precio, y
// es idempotente (upsert por (security, as_of, modelo)).
//
// Ejecución: DATABASE_URL=... go test ./internal/pipeline -p 1 -tags=integration -count=1
// (mismo patrón que los tests de integración existentes: DATABASE_URL via
// env, setup de security + fundamentales + serie de precios sintética).

const pipelineTestTicker = "AAPL"

var integPool *pgxpool.Pool

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		os.Exit(0) // sin BD: suite de integración se omite (sin error)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var err error
	integPool, err = storage.Connect(ctx, dsn)
	if err != nil {
		panic("pipeline integration: Connect falló: " + err.Error())
	}
	defer integPool.Close()
	if err := storage.RunMigrations(ctx, integPool, "../../migrations"); err != nil {
		panic("pipeline integration: migraciones fallaron: " + err.Error())
	}
	os.Exit(m.Run())
}

// seedPipelineAAPL crea un security AAPL con fundamentales FY, serie de
// precios sintética (300 barras -> trend inputs disponibles) y sin métricas:
// el refresh debe producirlas.
func seedPipelineAAPL(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	if _, err := integPool.Exec(ctx, `TRUNCATE derived_metrics, daily_prices, macro_series, fundamentals, edgar_staging, securities RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("TRUNCATE falló: %v", err)
	}

	sec, err := storage.UpsertSecurity(ctx, integPool, &storage.Security{
		Ticker: pipelineTestTicker, CIK: "0000320193", Name: "Apple Inc",
		Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}

	// Fundamentales FY2025 (misma forma que el fixture M3 de la API).
	periodEnd := time.Date(2025, 9, 28, 0, 0, 0, 0, time.UTC)
	fy := "FY"
	src := "test"
	u := "USD"
	val := func(concept string, v float64) storage.Fundamental {
		vv := v
		return storage.Fundamental{
			SecurityID: sec.ID, Concept: concept, Value: &vv, Unit: &u,
			PeriodType: "P", PeriodStart: &periodEnd, PeriodEnd: periodEnd,
			FiscalYear: int16p(2025), FiscalPeriod: &fy, FilingDate: &periodEnd,
			Source: src,
		}
	}
	funds := []storage.Fundamental{
		val("net_earnings", 112000), val("shares_outstanding", 15400),
		val("shareholders_equity", 62000), val("total_liabilities", 302000),
		val("free_cash_flow", 98500), val("long_term_debt", 98959),
		val("short_term_debt", 19987), val("cash_and_equivalents", 29943),
		val("operating_cash_flow", 118254), val("capex", 19749),
	}
	tx, err := integPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := storage.UpsertFundamentals(ctx, tx, funds); err != nil {
		t.Fatalf("upsert fundamentals: %v", err)
	}

	// Serie de precios: 300 días sintéticos alcistas (Close == AdjustedClose).
	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	prices := make([]storage.DailyPrice, 0, 300)
	for i := 0; i < 300; i++ {
		close := 100 + float64(i)*0.2
		prices = append(prices, storage.DailyPrice{
			SecurityID: sec.ID, Date: start.AddDate(0, 0, i),
			Close: close, AdjustedClose: close, Open: &close, High: &close, Low: &close, Source: "test",
		})
	}
	if err := storage.UpsertDailyPrices(ctx, tx, prices); err != nil {
		t.Fatalf("upsert prices: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func int16p(v int16) *int16 { return &v }

// TestRefreshMetricsAndScoresPersists verifica (CA2) que el refresh recalcula
// y persiste las 8 métricas derivadas + el score de AAPL, y que una segunda
// pasada es idempotente (mismas filas, mismas claves).
func TestRefreshMetricsAndScoresPersists(t *testing.T) {
	if integPool == nil {
		t.Skip("sin DATABASE_URL")
	}
	seedPipelineAAPL(t)
	ctx := context.Background()

	first, err := RefreshMetricsAndScores(ctx, integPool, DefaultGrowth, false)
	if err != nil {
		t.Fatalf("RefreshMetricsAndScores: %v", err)
	}
	if first.Tickers != 1 || first.Metrics != 1 || first.Scores != 1 {
		t.Fatalf("conteos esperados 1/1/1, got %+v", first)
	}

	sec, err := storage.GetSecurityByTicker(ctx, integPool, pipelineTestTicker)
	if err != nil {
		t.Fatalf("GetSecurityByTicker: %v", err)
	}

	mts, err := storage.GetLatestMetrics(ctx, integPool, sec.ID)
	if err != nil {
		t.Fatalf("GetLatestMetrics: %v", err)
	}
	if len(mts) != 8 {
		t.Fatalf("se esperaban 8 métricas derivadas persistidas, hay %d: %+v", len(mts), mts)
	}
	seen := map[string]bool{}
	for _, m := range mts {
		seen[m.Metric] = true
	}
	for _, name := range []string{"eps", "pe_ratio", "pb_ratio", "pcf_ratio", "peg_ratio", "roe", "de_ratio", "fcf_yield"} {
		if !seen[name] {
			t.Errorf("métrica %q ausente en derived_metrics", name)
		}
	}

	score, err := storage.GetLatestScore(ctx, integPool, pipelineTestTicker)
	if err != nil {
		t.Fatalf("GetLatestScore: %v", err)
	}
	if score.Score < 0 || score.Score > 100 || score.ModelVersion == "" {
		t.Fatalf("score persistido inválido: %+v", score)
	}

	// Idempotencia (upsert): segunda pasada no duplica filas.
	second, err := RefreshMetricsAndScores(ctx, integPool, DefaultGrowth, false)
	if err != nil {
		t.Fatalf("segundo RefreshMetricsAndScores: %v", err)
	}
	if second.Tickers != 1 || second.Metrics != 1 || second.Scores != 1 {
		t.Fatalf("segunda pasada inesperada: %+v", second)
	}
	var nMetrics, nScores int
	if err := integPool.QueryRow(ctx, `SELECT count(*) FROM derived_metrics WHERE security_id=$1`, sec.ID).Scan(&nMetrics); err != nil {
		t.Fatalf("count metrics: %v", err)
	}
	if err := integPool.QueryRow(ctx, `SELECT count(*) FROM scores WHERE security_id=$1`, sec.ID).Scan(&nScores); err != nil {
		t.Fatalf("count scores: %v", err)
	}
	if nMetrics != 8 || nScores != 1 {
		t.Fatalf("idempotencia rota: metrics=%d (esperado 8), scores=%d (esperado 1)", nMetrics, nScores)
	}
}

// TestRefreshMetricsAndScoresDryRun verifica que dry-run no persiste.
func TestRefreshMetricsAndScoresDryRun(t *testing.T) {
	if integPool == nil {
		t.Skip("sin DATABASE_URL")
	}
	seedPipelineAAPL(t)
	ctx := context.Background()

	res, err := RefreshMetricsAndScores(ctx, integPool, DefaultGrowth, true)
	if err != nil {
		t.Fatalf("RefreshMetricsAndScores dry-run: %v", err)
	}
	if res.Tickers != 1 {
		t.Fatalf("dry-run debería seguir resolviendo el universo: %+v", res)
	}
	sec, _ := storage.GetSecurityByTicker(ctx, integPool, pipelineTestTicker)
	var nMetrics, nScores int
	_ = integPool.QueryRow(ctx, `SELECT count(*) FROM derived_metrics WHERE security_id=$1`, sec.ID).Scan(&nMetrics)
	_ = integPool.QueryRow(ctx, `SELECT count(*) FROM scores WHERE security_id=$1`, sec.ID).Scan(&nScores)
	if nMetrics != 0 || nScores != 0 {
		t.Fatalf("dry-run no debe persistir: metrics=%d scores=%d", nMetrics, nScores)
	}
}
