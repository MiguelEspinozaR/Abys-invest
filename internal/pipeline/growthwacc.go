package pipeline

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/growth"
	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/quality"
	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/wacc"
)

// growthWaccResult resume el stage de M6a por security.
type growthWaccResult struct {
	Growth, WACC int
}

// growthConcepts son los conceptos canónicos que pide el Growth Engine. eps_basic
// es el fallback de eps_diluted (prioridad documentada): la alternativa
// net_earnings/shares_outstanding se descarta porque mezcla un beneficio
// reexpresado con acciones sin ajustar y rompe el CAGR en un split (riesgo R7).
var growthConcepts = []string{"revenues", "eps_diluted", "eps_basic", "free_cash_flow"}

// waccConcepts son los conceptos FY que dan la estructura de capital: E =
// valuation_price x shares_outstanding, D = total_debt (derivado en la
// normalización EDGAR, concepts.go).
var waccConcepts = []string{"shares_outstanding", "total_debt"}

// runGrowthWaccJob calcula y persiste growth_metrics + wacc_metrics para cada
// security (SPEC v2 §5 y §7, plan M6a D1).
//
// Solo lee fundamentals/daily_prices y el storage.Security ya resuelto (que
// incluye la beta observada de Yahoo, D17): SIN RED, por lo que se ejecuta
// también en POST /refresh. Los errores por security se loguean y la pasada
// continúa (mismo contrato que runMetricsJob). Devuelve los conteos de filas.
// mc es la configuración resuelta (code < env < parameter set). Si es nil, se
// usa ModelConfigFromEnv() (para jobs standalone sin parameter set).
func runGrowthWaccJob(ctx context.Context, pool *pgxpool.Pool, securities []storage.Security, dryRun bool, mc *modelcfg.ModelConfig) growthWaccResult {
	if mc == nil {
		mcPtr := modelcfg.ModelConfigFromEnv()
		mc = &mcPtr
	}
	gcfg := growth.ConfigFromEnv()
	wcfg := wacc.ConfigFromModelConfig(*mc)

	counts := growthWaccResult{}
	for _, sec := range securities {
		g, w, err := runGrowthWaccOne(ctx, pool, sec, gcfg, wcfg, dryRun)
		if err != nil {
			slog.Warn("growth/wacc no generados (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}
		if g {
			counts.Growth++
		}
		if w {
			counts.WACC++
		}
	}
	slog.Info("job growth+wacc terminado", "growth", counts.Growth, "wacc", counts.WACC,
		"total", len(securities), "dry_run", dryRun)
	return counts
}

// runGrowthWaccOne computes and persists both rows of ONE security inside a
// single transaction: either growth_metrics and wacc_metrics exist or neither
// does (risk R9). The returned booleans report what was persisted (both true
// together, or both false when the security has no price).
func runGrowthWaccOne(ctx context.Context, pool *pgxpool.Pool, sec storage.Security, gcfg growth.Config, wcfg wacc.Config, dryRun bool) (bool, bool, error) {
	// (1) as_of = fecha del último precio (igual que derived_metrics/scores).
	last, err := storage.GetLatestPrice(ctx, pool, sec.ID)
	if err != nil {
		return false, false, err
	}
	asOf := last.Date

	// (2) Series anuales (filtro SQL: FY + 330-400 días + filing_date <= as_of).
	series, err := storage.GetFYAnnualSeries(ctx, pool, sec.ID, append(append([]string(nil), growthConcepts...), "operating_income", "interest_expense", "income_tax_expense", "pretax_income"), asOf, gcfg.AnnualMinDays, gcfg.AnnualMaxDays)
	if err != nil {
		return false, false, err
	}
	eps := series["eps_diluted"]
	if len(eps) == 0 {
		eps = series["eps_basic"]
	}
	gin := growth.Inputs{
		Ticker: sec.Ticker,
		AsOf:   asOf,
		Series: growth.Series{
			Revenue: toGrowthPoints(series["revenues"]),
			EPS:     toGrowthPoints(eps),
			FCF:     toGrowthPoints(series["free_cash_flow"]),
		},
	}
	gr := growth.Calculate(gin, gcfg)
	if gr.NormalizedGrowthRate == nil {
		slog.Warn("growth sin datos; las fórmulas siguen usando GROWTH_RATE_DEFAULT (M6b lo cableará)",
			"ticker", sec.Ticker, "source", gr.Source)
	}

	// (3) Estructura de capital: E = close (valuation_price, §22) x shares.
	// M6a-F1: corte temporal filing_date <= as_of para evitar look-ahead.
	funds, _, err := storage.GetLatestFYFundamentalsAsOf(ctx, pool, sec.ID, waccConcepts, asOf)
	if err != nil {
		return false, false, err
	}
	var equity *float64
	if shares := funds["shares_outstanding"]; shares != nil && *shares > 0 && last.Close > 0 {
		v := *shares * last.Close
		equity = &v
	}
	// (4) Beta OBSERVADA, ahora desde beta_history (ADR D29 / E4, Az8).
	//
	// M6a leía `securities.beta`, la CACHÉ de la última observación, sin fecha de
	// la observación: un beta de hace dos años entraba al CAPM con el mismo peso
	// que uno de ayer. GetBetaAsOf devuelve la observación VIGENTE en asOf o un
	// motivo (`beta_missing` / `beta_stale`); un beta caducado NO se usa, se
	// degrada a configured_fallback/low diciendo por qué.
	obs, betaReason, err := storage.GetBetaAsOf(ctx, pool, sec.ID, asOf, storage.DefaultBetaMaxAgeDays)
	if err != nil {
		return false, false, err
	}
	var beta *float64
	var betaAsOf *time.Time
	if obs != nil {
		b := obs.Beta
		beta, betaAsOf = &b, &obs.AsOf
	} else {
		slog.Debug("beta no vigente; WACC configured_fallback", "ticker", sec.Ticker, "reason", betaReason)
	}
	win := wacc.Inputs{
		Ticker: sec.Ticker, AsOf: asOf,
		EquityValue: equity, DebtValue: funds["total_debt"], Beta: beta,
		BetaAsOf: betaAsOf,
	}
	if beta == nil && betaReason != "" {
		win.Reasons = []string{betaReason}
	}
	// Align for observed tax rate: NO second query. The `series` of step (2)
	// was requested with growthConcepts + the trio (operating_income,
	// interest_expense, income_tax_expense, pretax_income), so it already carries
	// everything alignedFYFacts reads.
	//
	// Az4: the SAME observed rate that feeds NOPAT feeds Kd after-tax; a
	// rejected or degraded pair simply leaves the configured rate in place.
	fyMax := quality.ConfigFromEnv().FYMaxAgeDays
	aligned := alignedFYFacts(sec.Ticker, series, asOf, fyMax)
	if t, rateReason := alignedTaxRate(aligned); rateReason == "" {
		win.TaxRate = &t
	}
	wr := wacc.Calculate(win, wcfg)

	// (5) available_at = max(filing_date) de los hechos usados; sin hechos, as_of.
	availableAt := availableAtFrom(gin.Series, asOf)
	fundamentalsAsOf := lastFundamentalsPeriod(gin.Series)

	gsnap, err := gr.Snapshot(gin, gcfg)
	if err != nil {
		return false, false, err
	}
	wsnap, err := wr.Snapshot(win, wcfg)
	if err != nil {
		return false, false, err
	}

	grow := &storage.GrowthMetric{
		SecurityID: sec.ID, AsOf: asOf, AvailableAt: &availableAt, FundamentalsAsOf: fundamentalsAsOf,
		RevenueCAGR3y: gr.RevenueCAGR3y, RevenueCAGR5y: gr.RevenueCAGR5y,
		EPSCAGR3y: gr.EPSCAGR3y, EPSCAGR5y: gr.EPSCAGR5y,
		FCFCAGR3y: gr.FCFCAGR3y, FCFCAGR5y: gr.FCFCAGR5y,
		NormalizedGrowthRate: gr.NormalizedGrowthRate,
		Confidence:           gr.Confidence, Source: gr.Source,
		Clamped: gr.Clamped, RevenueDiscrepancy: gr.RevenueDiscrepancy,
		InputsSnapshot: gsnap, ModelVersion: growth.ModelVersion,
	}
	waccRow := &storage.WaccMetric{
		SecurityID: sec.ID, AsOf: asOf, AvailableAt: &availableAt,
		EquityValue: equity, DebtValue: funds["total_debt"],
		// BetaUpdatedAt is the DATE OF THE OBSERVATION used (wr.BetaAsOf), not the
		// date the cache row was written: "which beta produced this WACC" is the
		// question the column answers (ADR D29, riesgo R4).
		Beta: wr.Beta, BetaObserved: wr.BetaObserved, BetaUpdatedAt: wr.BetaAsOf,
		CostOfEquity: wr.Ke, CostOfDebtPreTax: wr.CostOfDebt, CostOfDebtAfterTax: wr.KdAfterTax,
		TaxRate: wr.TaxRate, RiskFreeRate: wr.RiskFreeRate, EquityRiskPremium: wr.EquityRiskPremium,
		Wacc: wr.WACC, WeightEquity: wr.WeightEquity, WeightDebt: wr.WeightDebt,
		Source: wr.Source, Confidence: wr.Confidence,
		InputsSnapshot: wsnap, ModelVersion: wacc.ModelVersion,
	}

	if dryRun {
		slog.Info("growth+wacc (dry-run)", "ticker", sec.Ticker, "as_of", asOf.Format("2006-01-02"),
			"growth", formatValue(gr.NormalizedGrowthRate), "growth_source", gr.Source,
			"wacc", formatValue(wr.WACC), "wacc_source", wr.Source)
		return true, true, nil
	}

	// (6) Una sola transacción por security: las dos filas o ninguna (R9).
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, false, err
	}
	if err := storage.UpsertGrowthMetric(ctx, tx, grow); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return false, false, err
	}
	if err := storage.UpsertWaccMetric(ctx, tx, waccRow); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return false, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, false, err
	}
	slog.Info("growth+wacc persistidos", "ticker", sec.Ticker, "as_of", asOf.Format("2006-01-02"),
		"growth", formatValue(gr.NormalizedGrowthRate), "growth_source", gr.Source, "growth_confidence", gr.Confidence,
		"wacc", formatValue(wr.WACC), "wacc_source", wr.Source, "beta_observed", wr.BetaObserved)
	return true, true, nil
}

// toGrowthPoints converts the storage rows into engine points (growth does not
// import storage: the engine stays a pure module without persistence types).
func toGrowthPoints(rows []storage.FYPoint) []growth.Point {
	if len(rows) == 0 {
		return nil
	}
	out := make([]growth.Point, 0, len(rows))
	for _, r := range rows {
		out = append(out, growth.Point{PeriodEnd: r.PeriodEnd, Value: r.Value, AvailableAt: r.AvailableAt})
	}
	return out
}

// availableAtFrom is the max(filing_date) of the points actually used; with no
// usable point it falls back to as_of so the row is never left without an
// availability date (SPEC §4).
func availableAtFrom(s growth.Series, asOf time.Time) time.Time {
	var max time.Time
	for _, points := range [][]growth.Point{s.Revenue, s.EPS, s.FCF} {
		for _, p := range points {
			if p.AvailableAt.IsZero() {
				continue
			}
			if p.AvailableAt.After(max) {
				max = p.AvailableAt
			}
		}
	}
	if max.IsZero() {
		return asOf
	}
	return max
}

// lastFundamentalsPeriod is the period_end of the last annual fiscal year used
// (traceability §4: on which books the growth was measured).
func lastFundamentalsPeriod(s growth.Series) *time.Time {
	var last time.Time
	for _, points := range [][]growth.Point{s.Revenue, s.EPS, s.FCF} {
		for _, p := range points {
			if p.PeriodEnd.After(last) {
				last = p.PeriodEnd
			}
		}
	}
	if last.IsZero() {
		return nil
	}
	return &last
}
