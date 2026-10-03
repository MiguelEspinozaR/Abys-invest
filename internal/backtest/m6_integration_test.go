//go:build integration

package backtest

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/growth"
	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/quality"
	"github.com/miky/abys-invest/internal/relative"
	"github.com/miky/abys-invest/internal/score"
	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/testsupport"
	"github.com/miky/abys-invest/internal/valuation"
	"github.com/miky/abys-invest/internal/wacc"
)

// These tests are the fixture PROOF of B13: a persisted 2.1.0 trace replays to
// the same score, and the forward returns come from the adjusted close with the
// §28 lag window. They live in the integration suite because the whole point is
// the round trip through the database.

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		os.Exit(0)
	}
	// Validate and redact DSN before connecting (ADR D30)
	redacted := testsupport.EnsureTestDSN(dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := storage.Connect(ctx, dsn)
	if err != nil {
		panic("backtest integration: Connect: " + err.Error())
	}
	defer pool.Close()
	if err := storage.EnsureTestDatabase(ctx, pool); err != nil {
		panic("backtest integration: guard de BD de test: " + err.Error())
	}
	if _, err := pool.Exec(ctx, `TRUNCATE daily_prices, scores, securities, parameter_sets RESTART IDENTITY CASCADE`); err != nil {
		panic("backtest integration: truncate: " + err.Error())
	}
	if err := storage.RunMigrations(ctx, pool, "../../migrations"); err != nil {
		panic("backtest integration: migraciones: " + err.Error())
	}
	_ = redacted // silence unused warning if not logged
	if code := m.Run(); code != 0 {
		os.Exit(code)
	}
	os.Exit(0)
}

func f64(v float64) *float64 { return &v }

// replaySecurity seeds one security with a 2.1.0 score (trace) and the daily
// bars needed for the three horizons.
func replaySecurity(t *testing.T, pool *pgxpool.Pool, ticker string, asOf time.Time, basePrice float64, withForward bool) int64 {
	t.Helper()
	ctx := context.Background()
	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{Ticker: ticker, CIK: "9000" + ticker, Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("upsert security: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	bars := []storage.DailyPrice{{SecurityID: sec.ID, Date: asOf, Close: basePrice, AdjustedClose: basePrice, Source: "test"}}
	if withForward {
		// Un +10% en cada horizonte, con las fechas dentro de la ventana de 5 días.
		for i, h := range ReplayHorizons {
			bars = append(bars, storage.DailyPrice{
				SecurityID: sec.ID, Date: asOf.AddDate(0, 0, h),
				Close: basePrice * (1 + 0.10), AdjustedClose: basePrice * (1 + 0.10), Source: "test",
			})
			_ = i
		}
	}
	if err := storage.UpsertDailyPrices(ctx, tx, bars); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert daily prices: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	mc := modelcfg.DefaultModelConfig()
	mc.ParameterSetName, mc.ParameterSetID = "base", 0
	// Use values where all 5 dimensions are valid but the weight shift between
	// base and conservative produces a different rounded score.
	// Base weights: graham 0.15, dcf 0.20, quality 0.35, relative 0.15, mc 0.05 (sum=0.90)
	// Conservative: graham 0.10, dcf 0.15, quality 0.40, relative 0.20, mc 0.05 (sum=0.90)
	// With graham=60, dcf=60, quality=90, relative=50, mc=77:
	//   base = (60*0.15 + 60*0.20 + 90*0.35 + 50*0.15 + 77*0.05) / 0.90 = 70.94 → 71
	//   conservative = (60*0.10 + 60*0.15 + 90*0.40 + 50*0.20 + 77*0.05) / 0.90 = 72.06 → 72
	in := score.ScoreInput21{
		Ticker: ticker, AsOf: asOf, Price: basePrice,
		GrahamBase: f64(basePrice * 1.5), DCFBase: f64(basePrice * 1.2),
		Quality:  score.QualityDetailFrom(&quality.Result{Score: f64(90), Coverage: 0.9, Confidence: quality.ConfidenceHigh}),
		Relative: score.RelativeDetailFrom(&relative.Result{Score: f64(50), Coverage: 0.8, Confidence: relative.ConfidenceMedium}),
		SMA50:    f64(basePrice * 1.1), SMA200: f64(basePrice), Momentum6m: f64(0.05), Momentum12m: f64(0.02),
		MarginOfSafety: mc.TargetMarginOfSafety,
	}
	res := score.CalculateScore21(in, mc)
	raw, err := score.BuildTrace21(in, res).Marshal()
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}
	tx2, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin scores: %v", err)
	}
	if err := storage.UpsertScore(ctx, tx2, &storage.Score{
		SecurityID: sec.ID, AsOf: asOf, Score: res.Score, Signal: res.Signal,
		Justification: res.Justification, InputsSnapshot: raw, ModelVersion: res.ModelVersion,
	}); err != nil {
		tx2.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert score: %v", err)
	}
	if err := tx2.Commit(ctx); err != nil {
		t.Fatalf("commit score: %v", err)
	}
	return sec.ID
}

// B13: el score persistido se reproduce desde su trace, sin tocar el motor.
func TestReplayReproduceElScorePersistido(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	replaySecurity(t, pool, "RPL1", asOf, 100, true)

	rows, err := ReplayScores(context.Background(), pool, ReplayOptions{TickersCSV: "RPL1"})
	if err != nil {
		t.Fatalf("ReplayScores: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("se esperaba 1 fila, got %d", len(rows))
	}
	r := rows[0]
	if r.Error != "" {
		t.Fatalf("error por security: %s", r.Error)
	}
	if !r.Reproduces {
		t.Fatalf("el score no se reproduce: %d vs %d (%s)", r.StoredScore, r.ReplayedScore, r.DivergenceReason)
	}
	if r.StoredScore != r.ReplayedScore {
		t.Fatalf("score almacenado %d != reproducido %d", r.StoredScore, r.ReplayedScore)
	}
	if r.ModelVersion != score.ModelVersion21 {
		t.Fatalf("model_version: esperado %q, got %q", score.ModelVersion21, r.ModelVersion)
	}
	if len(r.Forward) != 3 {
		t.Fatalf("se esperaban 3 horizontes, got %d", len(r.Forward))
	}
	for i, fr := range r.Forward {
		if fr.HorizonDays != ReplayHorizons[i] {
			t.Fatalf("horizonte %d: esperado %d", i, ReplayHorizons[i])
		}
		if fr.ReturnPct == nil {
			t.Fatalf("horizonte %d sin retorno: %s", fr.HorizonDays, fr.Reason)
		}
		if d := *fr.ReturnPct - 10; d > 1e-9 || d < -1e-9 {
			t.Fatalf("horizonte %d: esperado +10%%, got %v", fr.HorizonDays, *fr.ReturnPct)
		}
	}
}

// B13: sin barra en la ventana el retorno es n/d (no_forward_price), NO el bar
// más cercano: un retorno de otro periodo con esta etiqueta sería una mentira.
func TestReplaySinPrecioForwardDaND(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC)
	replaySecurity(t, pool, "RPL2", asOf, 50, false)

	rows, err := ReplayScores(context.Background(), pool, ReplayOptions{TickersCSV: "RPL2"})
	if err != nil {
		t.Fatalf("ReplayScores: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("se esperaba 1 fila, got %d", len(rows))
	}
	if !rows[0].Reproduces {
		t.Fatalf("el score debe reproducirse aunque no haya precio forward: %+v", rows[0])
	}
	for _, fr := range rows[0].Forward {
		if fr.ReturnPct != nil {
			t.Fatalf("horizonte %d: esperado n/d, got %v", fr.HorizonDays, *fr.ReturnPct)
		}
		if fr.Reason != ReasonNoForwardPrice {
			t.Fatalf("horizonte %d: esperado %q, got %q", fr.HorizonDays, ReasonNoForwardPrice, fr.Reason)
		}
	}
}

// B13: la tolerancia de 5 días absorbe un fin de semana sin inventar el retorno.
func TestReplayToleranciaDeCincoDias(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	secID := replaySecurity(t, pool, "RPL3", asOf, 20, false)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	// asOf + 22 días: dentro de [20, 25].
	if err := storage.UpsertDailyPrices(ctx, tx, []storage.DailyPrice{{
		SecurityID: secID, Date: asOf.AddDate(0, 0, 22),
		Close: 22, AdjustedClose: 22, Source: "test",
	}}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	rows, err := ReplayScores(ctx, pool, ReplayOptions{TickersCSV: "RPL3"})
	if err != nil {
		t.Fatalf("ReplayScores: %v", err)
	}
	var h20 *ForwardReturn
	for i := range rows[0].Forward {
		if rows[0].Forward[i].HorizonDays == 20 {
			h20 = &rows[0].Forward[i]
		}
	}
	if h20 == nil || h20.ReturnPct == nil {
		t.Fatalf("el horizonte 20 debe encontrar la barra del día 22: %+v", h20)
	}
	if d := *h20.ReturnPct - 10; d > 1e-9 || d < -1e-9 {
		t.Fatalf("retorno 20d: esperado +10%%, got %v", *h20.ReturnPct)
	}
	// Y un barra 10 días tarde NO cuenta.
	if rows[0].Forward[1].ReturnPct != nil {
		t.Fatalf("el horizonte 60 no debe encontrar precio: %+v", rows[0].Forward[1])
	}
}

// B13: con un parameter set DISTINTO al almacenado el score DEBE cambiar.
// El set `conservative` tiene target_margin_of_safety=40 (vs 30 base) y
// quality_sub_weights {profitability:0.20, growth:0.10, margins:0.10, stability:0.30, debt_solvency:0.30}
// (vs 0.20 cada uno en base). Estos cambios SÍ mueven el score.
func TestReplayConParameterSetDistinto(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	replaySecurity(t, pool, "RPL4", asOf, 80, true)

	same, err := ReplayScores(context.Background(), pool, ReplayOptions{TickersCSV: "RPL4"})
	if err != nil {
		t.Fatalf("ReplayScores: %v", err)
	}
	if !same[0].Reproduces {
		t.Fatalf("con el set por defecto debe reproducirse: %+v", same[0])
	}
	other, err := ReplayScores(context.Background(), pool, ReplayOptions{TickersCSV: "RPL4", ParameterSet: "conservative"})
	if err != nil {
		t.Fatalf("ReplayScores con set: %v", err)
	}
	if other[0].ParameterSet != "conservative" {
		t.Fatalf("esperado parameter_set conservative, got %q", other[0].ParameterSet)
	}
	// El set conservative DEBE producir un score distinto al base.
	// El mecanismo real es el shift de pesos top-level:
	// base: graham 0.15, dcf 0.20, quality 0.35, relative 0.15, mc 0.05 (sum=0.90)
	// conservative: graham 0.10, dcf 0.15, quality 0.40, relative 0.20, mc 0.05 (sum=0.90)
	// target_margin_of_safety: 40 vs 30 también cambia el MOS de Graham/DCF.
	// quality_sub_weights: stability/solvency 0.30 vs 0.20 altera la dimensión quality.
	if other[0].ReplayedScore == other[0].StoredScore {
		t.Fatalf("con parameter_set=conservative el score DEBE ser distinto al base (stored=%d, replayed=%d). Diferencia esperada por shift de pesos top-level, MOS 40 vs 30 y sub-pesos quality alterados",
			other[0].StoredScore, other[0].ReplayedScore)
	}
	if other[0].DivergenceReason != "parameter_set_override" {
		t.Fatalf("una diferencia debe declararse como override: %+v", other[0])
	}
	if other[0].Reproduces {
		t.Fatalf("con otro set el score no 'se reproduce': %+v", other[0])
	}
	// §18 estricto: con las cinco dimensiones válidas el peso USADO es la suma
	// configurada, 0.90 (no 1.00 — el 0.10 restante queda sin asignar a propósito).
	if math.Abs(other[0].WeightUsed-0.90) > 1e-9 {
		t.Fatalf("weight_used con las cinco dimensiones válidas: esperado 0.90 (§18), got %v", other[0].WeightUsed)
	}
}

// Un trace corrupto falla por security y no aborta el lote.
func TestReplayTraceIlegibleNoAbortaElLote(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	replaySecurity(t, pool, "RPL5", asOf, 10, true)
	ctx := context.Background()
	secID := replSecurityID(t, pool, "RPL5")
	if _, err := pool.Exec(ctx, `UPDATE scores SET inputs_snapshot = '{"trace_version":"99"}'::jsonb WHERE security_id = $1`, secID); err != nil {
		t.Fatalf("corromper snapshot: %v", err)
	}
	rows, err := ReplayScores(ctx, pool, ReplayOptions{TickersCSV: "RPL5"})
	if err != nil {
		t.Fatalf("ReplayScores no debe abortar: %v", err)
	}
	if len(rows) != 1 || rows[0].Error == "" {
		t.Fatalf("se esperaba un error por security: %+v", rows)
	}
	if rows[0].Reproduces {
		t.Fatalf("un trace ilegible no puede reproducirse")
	}
}

func replPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := storage.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func replSecurityID(t *testing.T, pool *pgxpool.Pool, ticker string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(), `SELECT id FROM securities WHERE ticker = $1`, ticker).Scan(&id); err != nil {
		t.Fatalf("id de %s: %v", ticker, err)
	}
	return id
}

// TestReplayFullChain verifies that FullChain replay exposes ChainSteps and
// ComputedFrom correctly, and that it handles nil market_context gracefully.
func TestReplayFullChain(t *testing.T) {
	pool := replPool(t)
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)

	// Seed a security with a complete 2.1.0 trace AND the underlying snapshots
	// for growth, wacc, and valuation.
	seedFullChainFixtures(t, pool, "FC1", asOf, 150.0)

	rows, err := ReplayScores(context.Background(), pool, ReplayOptions{
		TickersCSV: "FC1",
		FullChain:  true,
	})
	if err != nil {
		t.Fatalf("ReplayScores FullChain: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("se esperaba 1 fila, got %d", len(rows))
	}
	r := rows[0]

	if r.Error != "" {
		t.Fatalf("error por security: %s", r.Error)
	}

	// Verify ChainSteps is populated
	if r.ChainSteps == nil {
		t.Fatal("ChainSteps debe estar poblado en FullChain")
	}
	if r.ChainSteps.Growth == nil {
		t.Fatal("ChainSteps.Growth no debe ser nil")
	}
	if r.ChainSteps.WACC == nil {
		t.Fatal("ChainSteps.WACC no debe ser nil")
	}
	if r.ChainSteps.Valuation == nil {
		t.Fatal("ChainSteps.Valuation no debe ser nil")
	}
	if r.ChainSteps.Quality == nil {
		t.Fatal("ChainSteps.Quality no debe ser nil")
	}
	if r.ChainSteps.Relative == nil {
		t.Fatal("ChainSteps.Relative no debe ser nil")
	}
	if r.ChainSteps.Score == nil {
		t.Fatal("ChainSteps.Score no debe ser nil")
	}

	// Verify ComputedFrom declares recomputed/traced correctly
	if r.ComputedFrom == nil {
		t.Fatal("ComputedFrom no debe ser nil")
	}
	expectedComputedFrom := map[string]string{
		"growth":         "recomputed",
		"wacc":           "recomputed",
		"valuation":      "recomputed",
		"quality":        "traced",
		"relative":       "traced",
		"market_context": "traced",
	}
	for k, v := range expectedComputedFrom {
		if r.ComputedFrom[k] != v {
			t.Errorf("ComputedFrom[%q]: got %q, want %q", k, r.ComputedFrom[k], v)
		}
	}

	// Verify the replayed score is reasonable (should reproduce or differ
	// only due to parameter set, but here we use the same config)
	if r.ReplayedScore < 0 || r.ReplayedScore > 100 {
		t.Errorf("ReplayedScore fuera de rango: %d", r.ReplayedScore)
	}
}

// seedFullChainFixtures creates a security with all the snapshots needed for
// FullChain replay: growth_metrics, wacc_metrics, valuation_results, and a
// 2.1.0 score trace.
func seedFullChainFixtures(t *testing.T, pool *pgxpool.Pool, ticker string, asOf time.Time, basePrice float64) {
	t.Helper()
	ctx := context.Background()

	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: ticker, CIK: "9001" + ticker, Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("upsert security: %v", err)
	}

	// 1. Daily price for as_of
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := storage.UpsertDailyPrices(ctx, tx, []storage.DailyPrice{{
		SecurityID: sec.ID, Date: asOf, Close: basePrice, AdjustedClose: basePrice, Source: "test",
	}}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert daily prices: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// 2. Growth snapshot
	gIn := growth.Inputs{
		Ticker: ticker, AsOf: asOf,
		Series: growth.Series{
			EPS:     growthSerie(6, 1.0, 0.10),
			FCF:     growthSerie(6, 1.0, 0.12),
			Revenue: growthSerie(6, 100.0, 0.08),
		},
	}
	gCfg := growth.DefaultConfig()
	gRes := growth.Calculate(gIn, gCfg)
	gSnap, err := gRes.Snapshot(gIn, gCfg)
	if err != nil {
		t.Fatalf("growth snapshot: %v", err)
	}

	// 3. WACC snapshot
	wIn := wacc.Inputs{
		Ticker: ticker, AsOf: asOf,
		EquityValue: f64(basePrice * 1e9),
		DebtValue:   f64(40e9),
		Beta:        f64(1.2),
		BetaAsOf:    ptrTime(asOf.AddDate(0, -1, 0)),
		BetaSource:  wacc.BetaSourceHistory,
		Reasons:     []string{"beta_history"},
	}
	wCfg := wacc.ConfigFromEnv() // uses env or defaults
	wRes := wacc.Calculate(wIn, wCfg)
	wSnap, err := wRes.Snapshot(wIn, wCfg)
	if err != nil {
		t.Fatalf("wacc snapshot: %v", err)
	}

	// 4. Valuation snapshot (uses the recomputed growth and wacc results)
	vIn := valuation.Inputs{
		Ticker:               ticker,
		AsOf:                 asOf,
		Price:                f64v(basePrice),
		EPS:                  f64v(5.0),
		FreeCashFlow:         f64v(100e6),
		SharesOutstanding:    f64v(1e9),
		NetDebt:              f64v(0),
		NormalizedGrowthRate: gRes.NormalizedGrowthRate,
		GrowthConfidence:     valuation.Confidence(gRes.Confidence),
		GrowthSource:         gRes.Source,
		GrowthModelVersion:   growth.ModelVersion,
		WACC:                 wRes.WACC,
		CostOfEquity:         wRes.Ke,
		WACCSource:           wRes.Source,
		WACCConfidence:       valuation.Confidence(wRes.Confidence),
		WACCModelVersion:     wacc.ModelVersion,
		BetaObserved:         wRes.BetaObserved,
	}
	vCfg := valuation.DefaultConfig()
	vRes := valuation.Calculate(vIn, vCfg)
	vSnap, err := valuation.MarshalSnapshot(vIn, vCfg, vRes)
	if err != nil {
		t.Fatalf("valuation snapshot: %v", err)
	}

	// 5. Persist growth_metrics
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin growth: %v", err)
	}
	if err := storage.UpsertGrowthMetric(ctx, tx, &storage.GrowthMetric{
		SecurityID: sec.ID, AsOf: asOf,
		RevenueCAGR3y: gRes.RevenueCAGR3y, RevenueCAGR5y: gRes.RevenueCAGR5y,
		EPSCAGR3y: gRes.EPSCAGR3y, EPSCAGR5y: gRes.EPSCAGR5y,
		FCFCAGR3y: gRes.FCFCAGR3y, FCFCAGR5y: gRes.FCFCAGR5y,
		NormalizedGrowthRate: gRes.NormalizedGrowthRate,
		Confidence:           gRes.Confidence, Source: gRes.Source,
		Clamped: gRes.Clamped, RevenueDiscrepancy: gRes.RevenueDiscrepancy,
		InputsSnapshot: gSnap, ModelVersion: growth.ModelVersion,
	}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert growth: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit growth: %v", err)
	}

	// 6. Persist wacc_metrics
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin wacc: %v", err)
	}
	if err := storage.UpsertWaccMetric(ctx, tx, &storage.WaccMetric{
		SecurityID: sec.ID, AsOf: asOf,
		Wacc: wRes.WACC, CostOfEquity: wRes.Ke, CostOfDebtAfterTax: wRes.KdAfterTax,
		WeightEquity: wRes.WeightEquity, WeightDebt: wRes.WeightDebt,
		Beta: wRes.Beta, BetaObserved: wRes.BetaObserved,
		Source: wRes.Source, Confidence: wRes.Confidence,
		// BetaUpdatedAt, BetaSource, Reasons, Resolved* not in storage.WaccMetric
		InputsSnapshot: wSnap, ModelVersion: wacc.ModelVersion,
	}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert wacc: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit wacc: %v", err)
	}

	// 7. Persist valuation_results
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin valuation: %v", err)
	}
	if err := storage.UpsertValuationResult(ctx, tx, &storage.ValuationResult{
		SecurityID: sec.ID, AsOf: asOf,
		GrahamBase: vRes.Graham.Base, GrahamBear: vRes.Graham.Bear, GrahamBull: vRes.Graham.Bull,
		DcfBase: vRes.DCF.Base, DcfBear: vRes.DCF.Bear, DcfBull: vRes.DCF.Bull,
		ValuationStatus: string(vRes.Status), ValuationConfidence: string(vRes.Confidence),
		GrahamStatus: string(vRes.Graham.Status), GrahamConfidence: strPtr(string(vRes.Graham.Confidence)),
		DcfStatus: string(vRes.DCF.Status), DcfConfidence: strPtr(string(vRes.DCF.Confidence)),
		GrahamMos: vRes.MOS.GrahamBase, DcfBaseMos: vRes.MOS.DCFBase,
		DcfBearMos: vRes.MOS.DCFBear, DcfBullMos: vRes.MOS.DCFBull,
		ValuationMean: vRes.Uncertainty.Mean, ValuationStddev: vRes.Uncertainty.StdDev,
		ValuationDispersion: vRes.Uncertainty.Dispersion, ValuationComponents: int16(vRes.Uncertainty.Components),
		Reasons: vRes.Reasons,
		Wacc:    wRes.WACC, WaccSource: wRes.Source, WaccConfidence: strPtr(wRes.Confidence),
		InputsSnapshot: vSnap,
	}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert valuation: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit valuation: %v", err)
	}

	// 8. Create a 2.1.0 score trace (for quality/relative/market_context)
	mc := modelcfg.DefaultModelConfig()
	mc.ParameterSetName, mc.ParameterSetID = "base", 0
	qRes := quality.Result{Score: f64(85), Coverage: 0.9, Confidence: quality.ConfidenceHigh}
	relRes := relative.Result{Score: f64(55), Coverage: 0.8, Confidence: relative.ConfidenceMedium}
	scoreIn := score.ScoreInput21{
		Ticker: ticker, AsOf: asOf, Price: basePrice,
		GrahamBase: f64v(*vRes.Graham.Base), DCFBase: f64v(*vRes.DCF.Base),
		GrahamConfidence: string(vRes.Graham.Confidence), DCFConfidence: string(vRes.DCF.Confidence),
		Quality:  score.QualityDetailFrom(&qRes),
		Relative: score.RelativeDetailFrom(&relRes),
		SMA50:    f64v(basePrice * 1.05), SMA200: f64v(basePrice),
		Momentum6m: f64v(0.03), Momentum12m: f64v(0.01),
		MarginOfSafety: mc.TargetMarginOfSafety,
	}
	scRes := score.CalculateScore21(scoreIn, mc)
	raw, err := score.BuildTrace21(scoreIn, scRes).Marshal()
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin scores: %v", err)
	}
	if err := storage.UpsertScore(ctx, tx, &storage.Score{
		SecurityID: sec.ID, AsOf: asOf, Score: scRes.Score, Signal: scRes.Signal,
		Justification: scRes.Justification, InputsSnapshot: raw, ModelVersion: scRes.ModelVersion,
		ParameterSetID: nil, // no parameter set in test DB
	}); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert score: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit score: %v", err)
	}
}

func growthSerie(n int, start, cagr float64) []growth.Point {
	base := time.Date(2020, 9, 30, 0, 0, 0, 0, time.UTC)
	out := make([]growth.Point, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, growth.Point{
			PeriodEnd:   base.AddDate(i, 0, 0),
			Value:       start * math.Pow(1+cagr, float64(i)),
			AvailableAt: base.AddDate(i, 0, 0).AddDate(0, 2, 0),
		})
	}
	return out
}

func f64v(v float64) *float64 { return &v }

func ptrTime(t time.Time) *time.Time { return &t }

func strPtr(s string) *string { return &s }
