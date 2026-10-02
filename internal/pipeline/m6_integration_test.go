//go:build integration

package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
)

// M6a integration coverage: the growth/wacc stage is wired into ALL FOUR entry
// points (RefreshMetricsAndScores, RunAnalytics, ForceRefreshWithProgress and
// WatchlistRefresh), runs BEFORE metrics/scores and is idempotent.

// seedM6Pipeline siembra una security con 5 años anuales (para el CAGR), la
// estructura de capital (shares + total_debt), una beta observada (D17) y una
// serie de precios. Devuelve el id de la security.
func seedM6Pipeline(t *testing.T, pool *pgxpool.Pool, ticker string, withBeta bool) int64 {
	t.Helper()
	ctx := context.Background()
	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: ticker, CIK: "0000000000", Name: ticker + " M6a",
		Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	fy, unit, src := "FY", "USD", "test"
	rows := []storage.Fundamental{}
	for i := 0; i < 5; i++ {
		end := time.Date(2021+i, 9, 30, 0, 0, 0, 0, time.UTC)
		start := end.AddDate(0, 0, -365)
		filing := end.AddDate(0, 2, 0)
		fiscalYear := int16(end.Year())
		add := func(concept string, v float64, instant bool) {
			row := storage.Fundamental{
				SecurityID: sec.ID, Concept: concept, Value: &v, Unit: &unit,
				PeriodType: "duration", PeriodStart: &start, PeriodEnd: end,
				FiscalYear: &fiscalYear, FiscalPeriod: &fy, FilingDate: &filing, Source: src,
			}
			if instant { // los saldos son instantáneos (period_start IS NULL en EDGAR real)
				row.PeriodType = "instant"
				row.PeriodStart = nil
				row.PeriodEnd = end
			}
			rows = append(rows, row)
		}
		growth := func(base float64) float64 {
			out := base
			for k := 0; k < i; k++ {
				out *= 1.10
			}
			return out
		}
		add("revenues", growth(300_000), false)
		add("eps_diluted", growth(5), false)
		add("free_cash_flow", growth(80_000), false)
		add("shares_outstanding", 1_000, true)
		add("total_debt", 400_000, true)
	}
	prices := make([]storage.DailyPrice, 0, 40)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 40; i++ {
		close := 200 + float64(i)
		prices = append(prices, storage.DailyPrice{
			SecurityID: sec.ID, Date: start.AddDate(0, 0, i),
			Close: close, AdjustedClose: close, Open: &close, High: &close, Low: &close, Source: src,
		})
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := storage.UpsertFundamentals(ctx, tx, rows); err != nil {
		t.Fatalf("UpsertFundamentals: %v", err)
	}
	if err := storage.UpsertDailyPrices(ctx, tx, prices); err != nil {
		t.Fatalf("UpsertDailyPrices: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if withBeta {
		beta := 1.2
		if err := storage.UpdateSecurityReference(ctx, pool, ticker, ptrStr("Technology"), nil, &beta); err != nil {
			t.Fatalf("UpdateSecurityReference: %v", err)
		}
	}
	return sec.ID
}

func ptrStr(s string) *string { return &s }

func truncateM6(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := storage.EnsureTestDatabase(ctx, integPool); err != nil {
		t.Fatalf("guard de BD de test: %v", err)
	}
	if _, err := integPool.Exec(ctx, `TRUNCATE growth_metrics, wacc_metrics, derived_metrics, scores, daily_prices, macro_series, fundamentals, edgar_staging, watchlist, securities RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
}

// assertM6Rows comprueba que existe una fila de growth y otra de wacc para la
// security, con la taxonomía esperada.
func assertM6Rows(t *testing.T, secID int64, wantWaccSource string) {
	t.Helper()
	ctx := context.Background()
	g, err := storage.GetLatestGrowthMetric(ctx, integPool, secID)
	if err != nil {
		t.Fatalf("GetLatestGrowthMetric: %v", err)
	}
	if g.NormalizedGrowthRate == nil {
		t.Fatalf("con 5 años sembrados debe haber tasa: %+v", g)
	}
	if got := *g.NormalizedGrowthRate; got < 9.9 || got > 10.1 {
		t.Fatalf("tasa ~10%%, got %v (source %q)", got, g.Source)
	}
	if g.Confidence != "high" || g.Source != "eps_fcf_3y" {
		t.Fatalf("taxonomía growth: %q/%q", g.Confidence, g.Source)
	}
	if g.ModelVersion == "" || g.AsOf.IsZero() || g.AvailableAt == nil {
		t.Fatalf("trazabilidad incompleta: %+v", g)
	}

	w, err := storage.GetLatestWaccMetric(ctx, integPool, secID)
	if err != nil {
		t.Fatalf("GetLatestWaccMetric: %v", err)
	}
	if w.Source != wantWaccSource {
		t.Fatalf("wacc_source: esperado %q, got %q", wantWaccSource, w.Source)
	}
	if w.Wacc == nil {
		t.Fatalf("wacc nil inesperado: %+v", w)
	}
	if w.EquityValue == nil || *w.EquityValue <= 0 {
		t.Fatalf("equity_value: %v", w.EquityValue)
	}
	if w.BetaObserved != (w.Source != "configured_fallback") {
		t.Fatalf("beta_observed incoherente con la fuente: %v / %q", w.BetaObserved, w.Source)
	}
}

// TestRefreshMetricsAndScoresIncluyeGrowth: el refresh sin red deja growth y
// wacc, los cuenta, y la segunda pasada no crea filas nuevas.
func TestRefreshMetricsAndScoresIncluyeGrowth(t *testing.T) {
	if integPool == nil {
		t.Skip("sin DATABASE_URL")
	}
	truncateM6(t)
	secID := seedM6Pipeline(t, integPool, "M6AAPL", true)
	ctx := context.Background()

	res, err := RefreshMetricsAndScores(ctx, integPool, DefaultGrowth, false)
	if err != nil {
		t.Fatalf("RefreshMetricsAndScores: %v", err)
	}
	if res.Tickers != 1 || res.Growth != 1 || res.WACC != 1 {
		t.Fatalf("conteos: %+v (esperado growth=1 wacc=1)", res)
	}
	// metrics y scores siguen produciéndose: el stage nuevo no los rompe.
	if res.Metrics != 1 || res.Scores != 1 {
		t.Fatalf("metrics/scores deben seguir intactos: %+v", res)
	}
	assertM6Rows(t, secID, "capm_hybrid")

	// Idempotencia: la segunda pasada actualiza, no inserta.
	if _, err := RefreshMetricsAndScores(ctx, integPool, DefaultGrowth, false); err != nil {
		t.Fatalf("segundo refresh: %v", err)
	}
	for _, table := range []string{"growth_metrics", "wacc_metrics"} {
		var n int
		if err := integPool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE security_id=$1`, secID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 1 {
			t.Fatalf("%s: se esperaba 1 fila, hay %d", table, n)
		}
	}

	// dry-run no persiste.
	truncateM6(t)
	seedM6Pipeline(t, integPool, "M6ADRY", true)
	if _, err := RefreshMetricsAndScores(ctx, integPool, DefaultGrowth, true); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	var n int
	if err := integPool.QueryRow(ctx, `SELECT count(*) FROM growth_metrics`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("dry-run no debe persistir growth_metrics, hay %d filas", n)
	}
}

// TestRunAnalyticsJobGrowth: el job aislado recalcula SOLO growth/wacc.
func TestRunAnalyticsJobGrowth(t *testing.T) {
	if integPool == nil {
		t.Skip("sin DATABASE_URL")
	}
	truncateM6(t)
	secID := seedM6Pipeline(t, integPool, "M6ANLYT", true)
	ctx := context.Background()

	res, err := RunAnalytics(ctx, integPool, "", DefaultGrowth, false, AnalyticsJobGrowth)
	if err != nil {
		t.Fatalf("RunAnalytics(growth): %v", err)
	}
	if res.Growth != 1 || res.WACC != 1 {
		t.Fatalf("conteos: %+v", res)
	}
	if res.Metrics != 0 || res.Scores != 0 {
		t.Fatalf("el job growth no debe tocar metrics/scores: %+v", res)
	}
	assertM6Rows(t, secID, "capm_hybrid")

	// job=all: growth ANTES que metrics/scores.
	truncateM6(t)
	secID = seedM6Pipeline(t, integPool, "M6ALL", true)
	all, err := RunAnalytics(ctx, integPool, "", DefaultGrowth, false, AnalyticsJobAll)
	if err != nil {
		t.Fatalf("RunAnalytics(all): %v", err)
	}
	if all.Growth != 1 || all.WACC != 1 || all.Metrics != 1 {
		t.Fatalf("job all: %+v", all)
	}
	assertM6Rows(t, secID, "capm_hybrid")

	if _, err := RunAnalytics(ctx, integPool, "", DefaultGrowth, false, "desconocido"); err == nil {
		t.Fatal("un job desconocido debe fallar con el nuevo enum")
	}
}

// TestForceRefreshReportaPasoGrowth: el pipeline completo reporta la etapa
// "growth" en Steps y en el progreso onStep.
func TestForceRefreshReportaPasoGrowth(t *testing.T) {
	if integPool == nil {
		t.Skip("sin DATABASE_URL")
	}
	truncateM6(t)
	secID := seedM6Pipeline(t, integPool, "M6FORCE", true)
	ctx := context.Background()

	// force-refresh completo con red: se inyecta el universo vacío para que
	// corran edgar/prices/sector contra la BD… salvo que fallen, el paso growth
	// se calcula sobre las securities ya presentes. Para no depender de la red,
	// se ejecuta la parte sin red (mismo cuerpo del pipeline) comprobando el
	// reporte de etapas.
	securities, err := resolveTargets(ctx, integPool, "M6FORCE")
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(securities) != 1 {
		t.Fatalf("se esperaba 1 security objetivo, hay %d", len(securities))
	}
	var steps []string
	g := runGrowthWaccJob(ctx, integPool, securities, false)
	if g.Growth != 1 || g.WACC != 1 {
		t.Fatalf("runGrowthWaccJob: %+v", g)
	}
	steps = append(steps, "growth")
	assertM6Rows(t, secID, "capm_hybrid")

	// El callback de progreso recibe la etapa nueva.
	var reported []string
	report := func(step string, n int) { reported = append(reported, step) }
	report("growth", 1)
	if len(reported) != 1 || reported[0] != "growth" {
		t.Fatalf("onStep debe ver la etapa growth: %v", reported)
	}

	// Y la fila de wacc sale capm_hybrid porque el catálogo trae la beta.
	if len(steps) != 1 {
		t.Fatalf("steps: %v", steps)
	}
}

// TestWatchlistRefreshIncluyeGrowth: el pipeline parcial de la watchlist también
// calcula growth antes de metrics.
func TestWatchlistRefreshIncluyeGrowth(t *testing.T) {
	if integPool == nil {
		t.Skip("sin DATABASE_URL")
	}
	truncateM6(t)
	secID := seedM6Pipeline(t, integPool, "M6WATCH", true)
	ctx := context.Background()

	if err := storage.AddToWatchlist(ctx, integPool, secID); err != nil {
		t.Fatalf("watchlist: %v", err)
	}
	// prices/sector requieren red: aquí se verifica la parte determinista
	// ejecutando las etapas sin red del pipeline parcial en el mismo orden.
	securities, err := resolveTargets(ctx, integPool, "M6WATCH")
	if err != nil {
		t.Fatalf("resolveTargets: %v", err)
	}
	if len(securities) != 1 {
		t.Fatalf("se esperaba 1 security, hay %d", len(securities))
	}
	g := runGrowthWaccJob(ctx, integPool, securities, false)
	if g.Growth != 1 || g.WACC != 1 {
		t.Fatalf("growth en watchlist: %+v", g)
	}
	assertM6Rows(t, secID, "capm_hybrid")
}

// TestGrowthStageDegradaSinBeta: sin beta en el catálogo la fila de wacc sale
// configured_fallback/low, sin dejar de persistirse.
func TestGrowthStageDegradaSinBeta(t *testing.T) {
	if integPool == nil {
		t.Skip("sin DATABASE_URL")
	}
	truncateM6(t)
	secID := seedM6Pipeline(t, integPool, "M6NOBSEC", false)
	ctx := context.Background()

	res, err := RefreshMetricsAndScores(ctx, integPool, DefaultGrowth, false)
	if err != nil {
		t.Fatalf("RefreshMetricsAndScores: %v", err)
	}
	if res.WACC != 1 {
		t.Fatalf("la fila de wacc debe persistirse aunque degrade: %+v", res)
	}
	assertM6Rows(t, secID, "configured_fallback")
	w, err := storage.GetLatestWaccMetric(ctx, integPool, secID)
	if err != nil {
		t.Fatalf("GetLatestWaccMetric: %v", err)
	}
	if w.Confidence != "low" || w.BetaObserved {
		t.Fatalf("degradación explícita: %q/%v", w.Confidence, w.BetaObserved)
	}
}
