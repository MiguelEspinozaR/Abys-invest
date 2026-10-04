//go:build integration

package pipeline

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/testsupport"
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
	// Validate and redact DSN before connecting (ADR D30)
	redacted := testsupport.EnsureTestDSN(dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var err error
	integPool, err = storage.Connect(ctx, dsn)
	if err != nil {
		panic("pipeline integration: Connect falló: " + redacted)
	}
	defer integPool.Close()
	if err := storage.EnsureTestDatabase(ctx, integPool); err != nil {
		panic("pipeline integration: guard de BD de test falló (no se debe tocar producción): " + redacted)
	}
	// Serialise the database phase of the integration suites (shared TRUNCATEs),
	// which is what lets them run WITHOUT `-p 1`.
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	releaseLock, err := testsupport.LockIntegrationDB(lockCtx, integPool)
	lockCancel()
	if err != nil {
		panic("pipeline integration: no se pudo tomar el advisory lock: " + err.Error())
	}
	defer releaseLock()
	if err := storage.RunMigrations(ctx, integPool, "../../migrations"); err != nil {
		panic("pipeline integration: migraciones fallaron: " + redacted)
	}
	os.Exit(m.Run())
}

// seedPipelineAAPL crea un security AAPL con fundamentales FY, serie de
// precios sintética (300 barras -> trend inputs disponibles) y sin métricas:
// el refresh debe producirlas.
func seedPipelineAAPL(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	// growth_metrics y wacc_metrics explícitos: el CASCADE de securities ya los
	// cubre, pero el orden explícito deja el test determinista si mañana una de
	// las dos deja de depender de securities.
	if _, err := integPool.Exec(ctx, `TRUNCATE growth_metrics, wacc_metrics, derived_metrics, daily_prices, macro_series, fundamentals, edgar_staging, securities RESTART IDENTITY CASCADE`); err != nil {
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
	filingDate := periodEnd
	fy := "FY"
	src := "test"
	u := "USD"
	val := func(concept string, v float64, instant bool) storage.Fundamental {
		vv := v
		f := storage.Fundamental{
			SecurityID: sec.ID, Concept: concept, Value: &vv, Unit: &u,
			PeriodType: "P", PeriodEnd: periodEnd,
			FiscalYear: int16p(2025), FiscalPeriod: &fy, FilingDate: &filingDate,
			Source: src,
		}
		if instant {
			f.PeriodType = "instant"
			f.PeriodStart = nil // instant facts: period_start IS NULL (EDGAR real)
		} else {
			f.PeriodStart = &periodEnd
		}
		return f
	}
	funds := []storage.Fundamental{
		val("net_earnings", 112000, false), val("shares_outstanding", 15400, true),
		val("shareholders_equity", 62000, true), val("total_liabilities", 302000, true),
		val("free_cash_flow", 98500, false), val("long_term_debt", 98959, true),
		val("short_term_debt", 19987, true), val("cash_and_equivalents", 29943, true),
		val("operating_cash_flow", 118254, false), val("capex", 19749, false),
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
	// M6c B4: derived_metrics now holds BOTH revisions — the 8 of 1.0.0 untouched
	// and the 12 of 2.0.0 — and the fixture has no interest_expense (M6c-T1), so
	// the only 2.0.0 row that MUST exist is interest_coverage with a NULL value.
	seen := map[string]string{}
	for _, m := range mts {
		seen[m.Metric] = m.ModelVersion
	}
	for _, name := range []string{"eps", "pe_ratio", "pb_ratio", "pcf_ratio", "peg_ratio", "roe", "de_ratio", "fcf_yield"} {
		if seen[name] != "1.0.0" {
			t.Errorf("métrica %q ausente de la revisión 1.0.0 (vista como %q)", name, seen[name])
		}
	}
	if seen["interest_coverage"] != "2.0.0" {
		t.Errorf("interest_coverage debe emitirse en 2.0.0 incluso sin interest_expense (M6c-T1), vista como %q", seen["interest_coverage"])
	}
	var icValue *float64
	for _, m := range mts {
		if m.Metric == "interest_coverage" {
			icValue = m.Value
		}
	}
	if icValue != nil {
		t.Errorf("interest_coverage sin interest_expense debe quedar NULL, es %v", *icValue)
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
	// 8 (1.0.0) + interest_coverage (2.0.0, NULL por M6c-T1): la segunda corrida
	// debe seguir dejando exactamente las mismas filas (idempotencia por
	// (security, as_of, model_version, metric)).
	if nMetrics != 9 || nScores != 1 {
		t.Fatalf("idempotencia rota: metrics=%d (esperado 9), scores=%d (esperado 1)", nMetrics, nScores)
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
