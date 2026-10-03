package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/metrics"
	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/score"
	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/valuation"
)

// Analytics jobs accepted by RunAnalytics (same contracts as -job in
// cmd/analytics).
const (
	// AnalyticsJobGrowth computes ONLY growth_metrics + wacc_metrics (M6a):
	// an isolated job to recompute them without touching the MVP metrics.
	AnalyticsJobGrowth = "growth"
	// AnalyticsJobValuation computes ONLY valuation_results (M6b): an isolated
	// job so a valuation can be recomputed (e.g. after a config change) without
	// recomputing the metrics or the scores, and so "no valuation is computable"
	// is a visible count instead of a line inside the scores job.
	AnalyticsJobValuation = "valuation"
	AnalyticsJobMetrics   = "metrics"
	// AnalyticsJobQuality / AnalyticsJobRelative evaluate the two M6c engines in
	// isolation. They PERSIST nothing (their persisted form is the score trace);
	// they exist so that "quality is degrading everywhere" is a number instead of
	// something you infer from score dimension weights.
	AnalyticsJobQuality  = "quality"
	AnalyticsJobRelative = "relative"
	AnalyticsJobScores   = "scores"
	AnalyticsJobAll      = "all"
)

// AnalyticsResult summarizes an analytics CLI job pass.
type AnalyticsResult struct {
	Tickers   int `json:"tickers"`   // securities objetivo
	Growth    int `json:"growth"`    // securities con growth_metrics persistida OK
	WACC      int `json:"wacc"`      // securities con wacc_metrics persistida OK
	Valuation int `json:"valuation"` // securities con valuation_results persistida OK
	Metrics   int `json:"metrics"`   // securities con métricas persistidas OK
	Quality   int `json:"quality"`   // securities con quality evaluable (§13)
	Relative  int `json:"relative"`  // securities con relative evaluable (§16)
	Scores    int `json:"scores"`    // securities con score persistido OK
}

// RunAnalytics replicates the CLI contract of cmd/analytics: resolves the
// target securities (empty tickersCSV = all active with a price), runs the
// requested job(s) and persists deterministically. Per-security errors never
// abort the pass; only resolution-level failures return an error.
func RunAnalytics(ctx context.Context, pool *pgxpool.Pool, tickersCSV string, growth float64, dryRun bool, job string) (AnalyticsResult, error) {
	if pool == nil {
		return AnalyticsResult{}, errPoolNil
	}
	if growth <= 0 {
		growth = DefaultGrowth
	}
	switch job {
	case AnalyticsJobGrowth, AnalyticsJobValuation, AnalyticsJobMetrics, AnalyticsJobQuality, AnalyticsJobRelative, AnalyticsJobScores, AnalyticsJobAll:
	default:
		return AnalyticsResult{}, fmt.Errorf("job desconocido %q (esperado growth|valuation|metrics|quality|relative|scores|all)", job)
	}

	securities, err := resolveTargets(ctx, pool, tickersCSV)
	if err != nil {
		return AnalyticsResult{}, err
	}
	if len(securities) == 0 {
		slog.Warn("sin securities objetivo")
		return AnalyticsResult{}, nil
	}

	res := AnalyticsResult{Tickers: len(securities)}
	// ORDEN del job `all` (M6a D1 + M6b C2): growth → valuation → metrics →
	// scores. Cada etapa consume la fila de la anterior:
	//   - growth antes que valuation: la valuation USA el growth persistido.
	//   - valuation antes que metrics: sin métricas no hay comparables, y scores
	//     necesita las dimensiones de valoración (graham_base/dcf_base).
	//   - metrics antes que scores: los comparables salen de derived_metrics.
	// Invertir este orden produce scores sin dimensiones de valoración, no un
	// error visible: por eso el orden es código, no una nota.
	if job == AnalyticsJobGrowth || job == AnalyticsJobAll {
		mc := modelcfg.ModelConfigFromEnv()
		g := runGrowthWaccJob(ctx, pool, securities, dryRun, &mc)
		res.Growth, res.WACC = g.Growth, g.WACC
	}
	if job == AnalyticsJobValuation || job == AnalyticsJobAll {
		v := runValuationJob(ctx, pool, securities, dryRun)
		res.Valuation = v.Inserted
		if v.SkippedNoPrice > 0 || v.Unavailable > 0 {
			slog.Info("valoración: sin fila para algunas securities",
				"sin_precio", v.SkippedNoPrice, "sin_valoracion", v.Unavailable)
		}
	}
	if job == AnalyticsJobMetrics || job == AnalyticsJobAll {
		res.Metrics = runMetricsJob(ctx, pool, securities, growth, dryRun)
	}
	// Los dos motores de M6c (B9/B10) van DESPUÉS de metrics: quality necesita las
	// series de fundamentals y relative necesita derived_metrics 2.0.0 y los
	// comparables. `all` los evalúa para que un diagnóstico previo exista, y el
	// job scores los vuelve a evaluar dentro del mismo pass (§27: un cálculo por
	// as_of) — el eval doble es deliberado y barato frente al riesgo de que el
	// score dependa de un resultado que nadie persistió.
	if job == AnalyticsJobQuality || job == AnalyticsJobAll {
		res.Quality, _ = RunQualityJob(ctx, pool, securities, dryRun)
	}
	if job == AnalyticsJobRelative || job == AnalyticsJobAll {
		res.Relative, _ = RunRelativeJob(ctx, pool, securities, dryRun)
	}
	if job == AnalyticsJobScores || job == AnalyticsJobAll {
		mc, err := resolveScoreConfig(ctx, pool)
		if err != nil {
			return AnalyticsResult{}, err
		}
		res.Scores = runScoresJob(ctx, pool, securities, growth, dryRun, mc)
	}
	return res, nil
}

// metricConceptsV2 maps the MetricInputV2 facts to the concept slugs the EDGAR
// collector actually persists.
//
// The mapping is NOT cosmetic: `revenue` and `cash` do not exist in `fundamentals`,
// the collector writes `revenues` and `cash_and_equivalents`. Asking for the SPEC
// names would have returned an empty map, every margin would have been nil, and the
// quality sub-blocks would have degraded to `insufficient_history` with no error and
// no reason — the exact class of silent degradation M6c exists to end.
//
// interest_expense is absent from the whole catalogue (M6c-T1): it is listed here so
// the gap is explicit in the code rather than an omission.
var metricConceptsV2 = []string{
	"net_earnings", "shares_outstanding", "shareholders_equity", "total_liabilities",
	"free_cash_flow", "operating_income", "revenues", "total_debt",
	"cash_and_equivalents", "ebitda", "interest_expense",
}

// qualityTaxRate is the NORMALIZED tax rate of M6c while the XBRL plumbing of
// M6c-T1 lands (SPEC §13: ROIC needs one, and `configured` provenance caps the
// quality confidence at `medium` per ADR D26 — the cap is the honest reading, not
// an obstacle to work around).
const qualityTaxRate = 0.21

// runMetricsJob computes and persists the metrics of BOTH revisions into
// derived_metrics (SPEC §13.2/§13.4 and ADR D12): the 8 of 1.0.0 unchanged and the
// 12 of 2.0.0 added. Idempotent by (security, as_of, model_version).
//
// NO LOOK-AHEAD (ADR D14, R-M6c-2): the fundamentals are read with
// GetLatestFYFundamentalsAsOf(cutoff = the price date), so a 10-K filed after the
// valuation date can no longer inform it. The previous latestFYFundamentals had no
// temporal filter at all and was deleted.
//
// Returns the number of securities with persisted metrics.
func runMetricsJob(ctx context.Context, pool *pgxpool.Pool, securities []storage.Security, growth float64, dryRun bool) int {
	succeeded := 0
	for _, sec := range securities {
		priceRow, err := storage.GetLatestPrice(ctx, pool, sec.ID)
		if err != nil {
			slog.Warn("sin precio para ticker (métricas price-based serán NULL o se omiten)", "ticker", sec.Ticker, "error", err)
			continue
		}
		asOf := priceRow.Date

		funds, _, err := storage.GetLatestFYFundamentalsAsOf(ctx, pool, sec.ID, metricConceptsV2, asOf)
		if err != nil {
			slog.Error("fundamentals fallaron (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}

		// Annual series (already deduplicated by period and cut at asOf) for the
		// stability metrics of 2.0.0.
		series, err := storage.GetFYAnnualSeries(ctx, pool, sec.ID, []string{"eps_diluted", "free_cash_flow"}, asOf, 330, 400)
		if err != nil {
			slog.Warn("series anuales fallaron (continúa)", "ticker", sec.Ticker, "error", err)
		}

		v1 := metrics.MetricInput{
			SecurityID:         sec.ID,
			Ticker:             sec.Ticker,
			AsOf:               asOf,
			NetEarnings:        funds["net_earnings"],
			SharesOutstanding:  funds["shares_outstanding"],
			Price:              priceRow.Close,
			ShareholdersEquity: funds["shareholders_equity"],
			TotalLiabilities:   funds["total_liabilities"],
			FreeCashFlow:       funds["free_cash_flow"],
			GrowthRate:         growth,
		}
		rows, err := metrics.BuildDerivedMetrics(v1, metrics.DefaultModelVersion)
		if err != nil {
			slog.Error("build de métricas falló (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}

		tax := qualityTaxRate
		v2 := metrics.MetricInputV2{
			SecurityID:         sec.ID,
			Ticker:             sec.Ticker,
			AsOf:               asOf,
			NetEarnings:        funds["net_earnings"],
			SharesOutstanding:  funds["shares_outstanding"],
			Price:              priceRow.Close,
			ShareholdersEquity: funds["shareholders_equity"],
			TotalLiabilities:   funds["total_liabilities"],
			FreeCashFlow:       funds["free_cash_flow"],
			OperatingIncome:    funds["operating_income"],
			Revenue:            funds["revenues"],
			TotalDebt:          funds["total_debt"],
			Cash:               funds["cash_and_equivalents"],
			EBITDA:             funds["ebitda"],
			InterestExpense:    funds["interest_expense"],
			NormalizedTaxRate:  &tax,
			EPSSeries:          toMetricSeries(series["eps_diluted"]),
			FCFSeries:          toMetricSeries(series["free_cash_flow"]),
		}
		rowsV2, err := metrics.BuildDerivedMetricsV2(v2, metrics.ModelVersion2)
		if err != nil {
			slog.Error("build de métricas 2.0.0 falló (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}
		rows = append(rows, rowsV2...)

		if dryRun {
			for _, m := range rows {
				slog.Info("métrica (dry-run)",
					"ticker", sec.Ticker, "as_of", m.AsOf.Format("2006-01-02"),
					"metric", m.Metric, "model_version", m.ModelVersion, "value", formatValue(m.Value))
			}
			succeeded++
			continue
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			slog.Error("begin tx métricas falló (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}
		if err := storage.UpsertDerivedMetrics(ctx, tx, rows); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			slog.Error("persistir métricas falló (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}
		if err := tx.Commit(ctx); err != nil {
			slog.Error("commit métricas falló (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}
		succeeded++
		slog.Info("métricas calculadas y persistidas", "ticker", sec.Ticker, "as_of", asOf.Format("2006-01-02"),
			"v1", 8, "v2", len(rowsV2))
	}
	slog.Info("job metrics terminado", "exitosos", succeeded, "total", len(securities), "dry_run", dryRun)
	return succeeded
}

// toMetricSeries converts storage points into metrics series points.
func toMetricSeries(pts []storage.FYPoint) []metrics.MetricSeriesPoint {
	out := make([]metrics.MetricSeriesPoint, 0, len(pts))
	for _, p := range pts {
		out = append(out, metrics.MetricSeriesPoint{PeriodEnd: p.PeriodEnd, Value: p.Value})
	}
	return out
}

// runScoresJob assembles every ScoreInput from persisted M1/M2 data and
// persists the M3 score (idempotente por (security, as_of, modelo)). En
// dry-run solo imprime. Las métricas se leen de derived_metrics; si el ticker
// no las tiene, el score degrada (dimensiones neutrales) sin crear datos.
// Returns the number of securities with persisted scores.
// resolveScoreConfig resolves the ACTIVE parameter set for the score job
// (precedence code < env < set, ADR D20): PARAMETER_SET, or "base" when unset.
//
// A missing/unreadable set is NOT fatal: the engine falls back to the codes
// defaults with the set marked as absent. Refusing to score because a configuration
// row is missing would turn a configuration problem into a data outage.
func resolveScoreConfig(ctx context.Context, pool *pgxpool.Pool) (modelcfg.ModelConfig, error) {
	fallback := modelcfg.ModelConfigFromEnv()
	name := modelcfg.DefaultParameterSetName()
	_, mc, err := modelcfg.Resolve(ctx, pool, name)
	if err != nil {
		slog.Warn("parameter set activo no resoluble: se usan los defaults del código",
			"parameter_set", name, "error", err)
		return fallback, nil
	}
	return mc, nil
}

func runScoresJob(ctx context.Context, pool *pgxpool.Pool, securities []storage.Security, growth float64, dryRun bool, mc modelcfg.ModelConfig) int {
	succeeded := 0
	for _, sec := range securities {
		if err := runOneScore(ctx, pool, sec, growth, dryRun, mc); err != nil {
			slog.Warn("score no generado (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}
		succeeded++
	}
	slog.Info("job scores terminado", "exitosos", succeeded, "total", len(securities),
		"dry_run", dryRun, "model_version", score.ModelVersion21, "parameter_set", mc.ParameterSetName)
	return succeeded
}

// runOneScore computes and persists one security's score 2.1.0 (B11 wiring).
//
// The chain is: growth (persisted, M6a) → valuation 2.0.0 (persisted, M6b) →
// metrics 1.0.0/2.0.0 (persisted, B4) → QUALITY → RELATIVE → SCORE. The two new
// engines run HERE, in the scores stage, and their results are persisted inside
// the score's trace (see the header of qualityrelative.go for why there is no
// table of their own).
//
// The valuation row is READ, never recomputed (§9), and the two medians are the
// version-filtered ones (ADR D13), so no stage of this chain mixes revisions
// without saying so.
func runOneScore(ctx context.Context, pool *pgxpool.Pool, sec storage.Security, growth float64, dryRun bool, mc modelcfg.ModelConfig) error {
	priceRow, err := storage.GetLatestPrice(ctx, pool, sec.ID)
	if err != nil {
		return fmt.Errorf("sin precio previo (%w)", err)
	}

	// Valuation 2.0.0: the score READS the persisted row (M6b D2) and never
	// recomputes a valuation. Without a row for this exact (as_of, 2.0.0) both
	// valuation dimensions are invalid and the score renormalises by
	// active_weight_sum (§18). The warning is deliberate: a mis-ordered pipeline
	// (`scores` before `valuation`) produces exactly this case, and it must be
	// visible rather than silently scoring a company without valuation.
	var grahamBase, dcfBase *float64
	var grahamConfidence, dcfConfidence string
	var grahamReasons, dcfReasons []string
	vr, err := storage.GetValuationResultAsOf(ctx, pool, sec.ID, priceRow.Date, valuation.ModelVersion)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		slog.Warn("sin fila de valoración 2.0.0 para este as_of: el score se renormaliza sin las dimensiones de valoración (§18)",
			"ticker", sec.Ticker, "as_of", priceRow.Date.Format("2006-01-02"), "model_version", valuation.ModelVersion)
	case err != nil:
		return fmt.Errorf("valuation_results: %w", err)
	default:
		grahamBase, dcfBase = vr.GrahamBase, vr.DcfBase
		if vr.GrahamConfidence != nil {
			grahamConfidence = *vr.GrahamConfidence
		}
		if vr.DcfConfidence != nil {
			dcfConfidence = *vr.DcfConfidence
		}
		grahamReasons = methodReasons(vr.Reasons, vr.GrahamStatus)
		dcfReasons = methodReasons(vr.Reasons, vr.DcfStatus)
	}

	// Quality (§13). Su propio stage: lee fundamentals, series y growth AS OF
	// priceRow.Date.
	qres, err := ComputeQualityStage(ctx, pool, sec, priceRow.Date, priceRow.Close, mc)
	if err != nil {
		return fmt.Errorf("quality: %w", err)
	}
	// Relative (§16). El p_fcf sale del snapshot de la misma fila 2.0.0.
	pFcf, err := valuationPFcf(ctx, pool, sec, priceRow.Date)
	if err != nil {
		return fmt.Errorf("relative: %w", err)
	}
	rres, sectorCount, err := ComputeRelativeStage(ctx, pool, sec, priceRow.Date, pFcf, mc)
	if err != nil {
		return fmt.Errorf("relative: %w", err)
	}

	// Tendencia: SMA50/SMA200 y momentum sobre ADJUSTED_CLOSE (dividendos y
	// splits corregidos). No es un detalle: sobre `close` sin ajustar, un split
	// 4:1 rompe la serie y la SMA200 mide un salto artificial. El precio de la
	// VALORACIÓN sigue siendo el `close` del último bar (§22).
	sma50, sma200, m6, m12 := trendInputs(ctx, pool, sec.ID)

	input := score.ScoreInput21{
		Ticker: sec.Ticker, AsOf: priceRow.Date, Price: priceRow.Close,
		GrahamBase: grahamBase, DCFBase: dcfBase,
		GrahamConfidence: grahamConfidence, DCFConfidence: dcfConfidence,
		GrahamReasons: grahamReasons, DCFReasons: dcfReasons,
		Quality:  score.QualityDetailFrom(qres),
		Relative: score.RelativeDetailFrom(rres),
		SMA50:    sma50, SMA200: sma200,
		Momentum6m: m6, Momentum12m: m12,
		MarginOfSafety: mc.TargetMarginOfSafety,
	}
	res := score.CalculateScore21(input, mc)

	// El trace ES el inputs_snapshot (ADR D27): score + quality + relative +
	// market context + procedencia. B12/B13 se re-alimentan de aquí.
	trace := score.BuildTrace21(input, res)
	snapshot, err := trace.Marshal()
	if err != nil {
		return fmt.Errorf("marshal trace 2.1.0: %w", err)
	}

	if qres.Score == nil || rres.Score == nil {
		slog.Info("dimensiones de M6c sin score: §18 renormaliza",
			"ticker", sec.Ticker,
			"quality_nil", qres.Score == nil, "quality_coverage", qres.Coverage, "quality_reasons", qres.Reasons,
			"relative_nil", rres.Score == nil, "sector_peers", sectorCount,
			"hist_as_of", rres.PeerCount, "relative_reasons", rres.Reasons)
	}

	if dryRun {
		slog.Info("score 2.1.0 (dry-run)",
			"ticker", sec.Ticker, "as_of", priceRow.Date.Format("2006-01-02"),
			"score", res.Score, "signal", res.Signal, "weight_used", res.WeightUsed,
			"parameter_set", mc.ParameterSetName)
		for _, d := range res.Dimensions {
			slog.Info("  dimensión (dry-run)", "ticker", sec.Ticker,
				"name", d.Name, "score", formatValue(d.Score), "weight_configured", d.Weight, "válida", d.Valid)
		}
		return nil
	}

	row := &storage.Score{
		SecurityID:     sec.ID,
		AsOf:           priceRow.Date,
		Score:          res.Score,
		Signal:         res.Signal,
		Justification:  res.Justification,
		InputsSnapshot: snapshot,
		ModelVersion:   res.ModelVersion,
	}
	// parameter_set_id es la identidad de la configuración (§26). Un 0 significa
	// "no hay set": la fila se persiste con NULL y NO con un id inventado.
	if mc.ParameterSetID != 0 {
		id := mc.ParameterSetID
		row.ParameterSetID = &id
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx scores: %w", err)
	}
	if err := storage.UpsertScore(ctx, tx, row); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		return fmt.Errorf("upsert score: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit score: %w", err)
	}
	slog.Info("score 2.1.0 persistido", "ticker", sec.Ticker, "as_of", priceRow.Date.Format("2006-01-02"),
		"score", res.Score, "signal", res.Signal, "weight_used", res.WeightUsed,
		"parameter_set_id", mc.ParameterSetID)
	return nil
}

// methodReasons keeps, for the justification, only the reasons that explain the
// state of ONE method. valuation_results stores a single valuation-level reason
// array (the same one the API returns), so the filter is what keeps the text
// honest: the Graham sentence must not be blamed for a discount rate that only
// affected the DCF.
func methodReasons(reasons []string, methodStatus string) []string {
	if len(reasons) == 0 {
		return nil
	}
	// Reasons attributable to a single method vs reasons common to both.
	dcfOnly := map[string]bool{
		valuation.ReasonFCFNonPositive:    true,
		valuation.ReasonSharesNonPositive: true,
		valuation.ReasonNetDebtUnknown:    true,
		valuation.ReasonInvalidHorizon:    true,
		valuation.ReasonWACCBelowTerminal: true,
		valuation.ReasonWACCConfigured:    true,
		valuation.ReasonWACCCostOfEquity:  true,
		valuation.ReasonWACCLegacyEnv:     true,
		valuation.ReasonNoPrice:           true,
	}
	grahamOnly := map[string]bool{
		valuation.ReasonEPSNonPositive: true,
	}
	// Reasons that describe the valuation as a whole (growth fallback, input
	// confidence, incomplete scenarios) are relevant to a method only when the
	// method has no value: if it produced one, the reason is context.
	common := map[string]bool{
		valuation.ReasonGrowthFallback:      true,
		valuation.ReasonInputConfidence:     true,
		valuation.ReasonIncompleteScenarios: true,
		valuation.ReasonGrowthNonPositive:   true,
		valuation.ReasonGrowthUnavailable:   true,
		valuation.ReasonGrowthNotReliable:   true,
	}
	available := methodStatus == string(valuation.StatusAvailable)
	var out []string
	for _, r := range reasons {
		switch {
		case available:
			// A method WITH values is not explained by its own failure reasons.
			continue
		case dcfOnly[r], grahamOnly[r], common[r]:
			out = append(out, r)
		}
	}
	return out
}

// trendInputs calcula SMA50, SMA200 y momentum 6m/12m sobre el ADJUSTED_CLOSE
// de los últimos scoreLookbackBars días (dividendos y splits ya corregidos por
// el proveedor). Se distingue del `close` a propósito: la valoración usa el
// `close` (§22) y la tendencia usa el ajustado.
func trendInputs(ctx context.Context, pool *pgxpool.Pool, securityID int64) (*float64, *float64, *float64, *float64) {
	prices, err := storage.GetLastClosePrices(ctx, pool, securityID, scoreLookbackBars)
	if err != nil || len(prices) < 200 {
		return nil, nil, nil, nil
	}
	closes := make([]float64, len(prices))
	for i := range prices {
		closes[i] = prices[i].AdjustedClose
	}
	var sma50, sma200 float64
	{
		var sum float64
		for _, c := range closes[len(closes)-50:] {
			sum += c
		}
		sma50 = sum / 50
		var sum2 float64
		for _, c := range closes[len(closes)-200:] {
			sum2 += c
		}
		sma200 = sum2 / 200
	}
	momentum := func(n int) *float64 {
		if len(closes) <= n {
			return nil
		}
		out := (closes[len(closes)-1]/closes[len(closes)-1-n] - 1)
		return &out
	}
	return &sma50, &sma200, momentum(126), momentum(252)
}

func formatValue(v *float64) string {
	if v == nil {
		return "NULL"
	}
	return fmt.Sprintf("%.6f", *v)
}
