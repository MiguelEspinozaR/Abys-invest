package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/metrics"
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
	AnalyticsJobScores    = "scores"
	AnalyticsJobAll       = "all"
)

// AnalyticsResult summarizes an analytics CLI job pass.
type AnalyticsResult struct {
	Tickers   int `json:"tickers"`   // securities objetivo
	Growth    int `json:"growth"`    // securities con growth_metrics persistida OK
	WACC      int `json:"wacc"`      // securities con wacc_metrics persistida OK
	Valuation int `json:"valuation"` // securities con valuation_results persistida OK
	Metrics   int `json:"metrics"`   // securities con métricas persistidas OK
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
	case AnalyticsJobGrowth, AnalyticsJobValuation, AnalyticsJobMetrics, AnalyticsJobScores, AnalyticsJobAll:
	default:
		return AnalyticsResult{}, fmt.Errorf("job desconocido %q (esperado growth|valuation|metrics|scores|all)", job)
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
		g := runGrowthWaccJob(ctx, pool, securities, dryRun)
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
	if job == AnalyticsJobScores || job == AnalyticsJobAll {
		res.Scores = runScoresJob(ctx, pool, securities, growth, dryRun)
	}
	return res, nil
}

// latestFYFundamentals fetches the newest FY row of every required concept.
// Missing concepts simply stay nil (conservative engine rule). Moved verbatim
// from cmd/analytics.
func latestFYFundamentals(ctx context.Context, pool *pgxpool.Pool, securityID int64, concepts []string) (map[string]*float64, error) {
	out := map[string]*float64{}
	rows, err := pool.Query(ctx, `SELECT DISTINCT ON (concept) concept, value
		FROM fundamentals
		WHERE security_id = $1 AND concept = ANY($2) AND fiscal_period = 'FY'
		ORDER BY concept, period_end DESC`, securityID, concepts)
	if err != nil {
		return nil, fmt.Errorf("pipeline: fundamentales FY security %d: %w", securityID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var concept string
		var value *float64
		if err := rows.Scan(&concept, &value); err != nil {
			return nil, fmt.Errorf("pipeline: scan fundamentales: %w", err)
		}
		out[concept] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pipeline: rows fundamentales: %w", err)
	}
	return out, nil
}

// runMetricsJob computes and persists the 8 MVP metrics (SPEC §13.2) into
// derived_metrics for every target security. Idempotente por (security, as_of).
// Returns the number of securities with persisted metrics.
func runMetricsJob(ctx context.Context, pool *pgxpool.Pool, securities []storage.Security, growth float64, dryRun bool) int {
	concepts := []string{"net_earnings", "shares_outstanding", "shareholders_equity", "total_liabilities", "free_cash_flow"}

	succeeded := 0
	for _, sec := range securities {
		funds, err := latestFYFundamentals(ctx, pool, sec.ID, concepts)
		if err != nil {
			slog.Error("fundamentales fallaron (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}

		priceRow, err := storage.GetLatestPrice(ctx, pool, sec.ID)
		if err != nil {
			slog.Warn("sin precio para ticker (métricas price-based serán NULL o se omiten)", "ticker", sec.Ticker, "error", err)
			continue
		}

		input := metrics.MetricInput{
			SecurityID:         sec.ID,
			Ticker:             sec.Ticker,
			AsOf:               priceRow.Date,
			NetEarnings:        funds["net_earnings"],
			SharesOutstanding:  funds["shares_outstanding"],
			Price:              priceRow.Close,
			ShareholdersEquity: funds["shareholders_equity"],
			TotalLiabilities:   funds["total_liabilities"],
			FreeCashFlow:       funds["free_cash_flow"],
			GrowthRate:         growth,
		}

		rows, err := metrics.BuildDerivedMetrics(input, metrics.DefaultModelVersion)
		if err != nil {
			slog.Error("build de métricas falló (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}

		if dryRun {
			for _, m := range rows {
				slog.Info("métrica (dry-run)",
					"ticker", sec.Ticker, "as_of", m.AsOf.Format("2006-01-02"),
					"metric", m.Metric, "value", formatValue(m.Value))
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
		slog.Info("métricas calculadas y persistidas", "ticker", sec.Ticker, "as_of", priceRow.Date.Format("2006-01-02"), "métricas", len(rows))
	}
	slog.Info("job metrics terminado", "exitosos", succeeded, "total", len(securities), "dry_run", dryRun)
	return succeeded
}

// runScoresJob assembles every ScoreInput from persisted M1/M2 data and
// persists the M3 score (idempotente por (security, as_of, modelo)). En
// dry-run solo imprime. Las métricas se leen de derived_metrics; si el ticker
// no las tiene, el score degrada (dimensiones neutrales) sin crear datos.
// Returns the number of securities with persisted scores.
func runScoresJob(ctx context.Context, pool *pgxpool.Pool, securities []storage.Security, growth float64, dryRun bool) int {
	params := parseScoreParams(growth)

	succeeded := 0
	for _, sec := range securities {
		if err := runOneScore(ctx, pool, sec, params, dryRun); err != nil {
			slog.Warn("score no generado (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}
		succeeded++
	}
	slog.Info("job scores terminado", "exitosos", succeeded, "total", len(securities), "dry_run", dryRun)
	return succeeded
}

// runOneScore computes and persists one security's score. as_of = fecha del
// último precio; inputs_snapshot = ScoreInput completo (ADR-0004).
func runOneScore(ctx context.Context, pool *pgxpool.Pool, sec storage.Security, p scoreParams, dryRun bool) error {
	priceRow, err := storage.GetLatestPrice(ctx, pool, sec.ID)
	if err != nil {
		return fmt.Errorf("sin precio previo (%w)", err)
	}

	// Valuation 2.0.0: the score READS the persisted row (M6b D2) and never
	// recomputes a valuation. Without a row for this exact (as_of, 2.0.0) both
	// valuation dimensions are invalid and the score renormalises by
	// active_weight_sum (§18). The warning below is deliberate: a mis-ordered
	// pipeline (`scores` before `valuation`) produces exactly this case, and it
	// must be visible rather than silently scoring a company without valuation.
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

	// Métricas derivadas (ya persistidas por metrics; sin ellas se degrada).
	metricsMap := map[string]*float64{}
	if mts, err := storage.GetLatestMetrics(ctx, pool, sec.ID); err == nil {
		for _, dm := range mts {
			metricsMap[dm.Metric] = dm.Value
		}
	}

	// Comparables sectoriales e históricos (SPEC §13.5.3).
	sectorCount := 0
	var sectorMedian, histMedian map[string]*float64
	if sec.Sector != nil && *sec.Sector != "" {
		sc, err := storage.GetSectorComparables(ctx, pool, *sec.Sector, sec.ID)
		if err != nil {
			return fmt.Errorf("comparables de sector: %w", err)
		}
		sectorCount = sc.SecurityCount
		sectorMedian = sc.Medians
	}
	if hm, err := storage.GetHistoricalMedianMetrics(ctx, pool, sec.ID, p.CompHistoryYears); err == nil {
		histMedian = hm
	}

	// Tendencia: SMA50/SMA200 y momentum sobre ADJUSTED_CLOSE (dividendos y
	// splits corregidos). No es un detalle: sobre `close` sin ajustar, un split
	// 4:1 rompe la serie y la SMA200 mide un salto artificial. El precio de la
	// VALORACIÓN sigue siendo el `close` del último bar (§22).
	sma50, sma200, m6, m12 := trendInputs(ctx, pool, sec.ID)

	input := score.ScoreInput{
		Ticker: sec.Ticker, Price: priceRow.Close,
		GrahamBase: grahamBase, DCFBase: dcfBase,
		GrahamConfidence: grahamConfidence, DCFConfidence: dcfConfidence,
		GrahamReasons: grahamReasons, DCFReasons: dcfReasons,
		Metrics:      metricsMap,
		SectorMedian: sectorMedian, HistoricalMedian: histMedian,
		SectorCount:              sectorCount,
		ComparablesMinSecurities: p.CompMinSecurities,
		SMA50:                    sma50, SMA200: sma200,
		Momentum6m: m6, Momentum12m: m12,
		MarginOfSafety: p.MarginSafety,
	}
	res := score.CalculateScore(input)

	snapshot, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal inputs_snapshot: %w", err)
	}

	if dryRun {
		slog.Info("score (dry-run)",
			"ticker", sec.Ticker, "as_of", priceRow.Date.Format("2006-01-02"),
			"score", res.Score, "signal", res.Signal, "version", res.ModelVersion)
		for _, d := range res.Dimensions {
			slog.Info("  dimensión (dry-run)", "ticker", sec.Ticker,
				"name", d.Name, "score", formatValue(d.Score), "weight", d.Weight, "válida", d.Valid)
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
	slog.Info("score persistido", "ticker", sec.Ticker, "as_of", priceRow.Date.Format("2006-01-02"),
		"score", res.Score, "signal", res.Signal)
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
