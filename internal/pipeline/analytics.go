package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/metrics"
	"github.com/miky/abys-invest/internal/score"
	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/valuation"
)

// Analytics jobs accepted by RunAnalytics (same contracts as -job in
// cmd/analytics).
const (
	AnalyticsJobMetrics = "metrics"
	AnalyticsJobScores  = "scores"
	AnalyticsJobAll     = "all"
)

// AnalyticsResult summarizes an analytics CLI job pass.
type AnalyticsResult struct {
	Tickers int `json:"tickers"` // securities objetivo
	Metrics int `json:"metrics"` // securities con métricas persistidas OK
	Scores  int `json:"scores"`  // securities con score persistido OK
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
	case AnalyticsJobMetrics, AnalyticsJobScores, AnalyticsJobAll:
	default:
		return AnalyticsResult{}, fmt.Errorf("job desconocido %q (esperado metrics|scores|all)", job)
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

	funds, err := storage.GetLatestFYFundamentals(ctx, pool, sec.ID, valuationConcepts())
	if err != nil {
		return fmt.Errorf("fundamentales FY: %w", err)
	}

	// Intravalo: Graham y DCF con inputs FY.
	iv := valuation.CalcIntrinsicValue(valuation.IntrinsicInput{
		GrowthRate:        p.Growth,
		DCFDiscountRate:   envFloatCfg("DCF_DISCOUNT_RATE", 10),
		DCFHorizon:        envIntCfg("DCF_HORIZON_YEARS", 5),
		TerminalGrowth:    envFloatCfg("DCF_TERMINAL_GROWTH", 2.5),
		EPS:               ratioF(funds["net_earnings"], funds["shares_outstanding"]),
		FreeCashFlow:      fcfFromF(funds),
		SharesOutstanding: funds["shares_outstanding"],
		NetDebt:           netDebtFromF(funds),
	})

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

	// Tendencia: SMA50/SMA200 y momentum sobre cierres (Close, documentado).
	sma50, sma200, m6, m12 := trendInputs(ctx, pool, sec.ID)

	input := score.ScoreInput{
		Ticker: sec.Ticker, Price: priceRow.Close,
		GrahamIntrinsic: iv.Graham, DCFIntrinsic: iv.DCF,
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
				"name", d.Name, "score", d.Score, "weight", d.Weight)
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

// valuationConcepts recoge los conceptos FY necesarios para Graham y DCF.
func valuationConcepts() []string {
	return []string{
		"net_earnings", "shares_outstanding", "free_cash_flow",
		"long_term_debt", "short_term_debt", "cash_and_equivalents",
		"operating_cash_flow", "capex",
	}
}

func ratioF(a, b *float64) *float64 {
	if a == nil || b == nil || *b <= 0 {
		return nil
	}
	out := *a / *b
	return &out
}

func fcfFromF(funds map[string]*float64) *float64 {
	if v := funds["free_cash_flow"]; v != nil {
		return v
	}
	ocf, capex := funds["operating_cash_flow"], funds["capex"]
	if ocf == nil || capex == nil {
		return nil
	}
	out := *ocf + *capex
	return &out
}

func netDebtFromF(funds map[string]*float64) *float64 {
	lt, st, cash := funds["long_term_debt"], funds["short_term_debt"], funds["cash_and_equivalents"]
	if lt == nil || st == nil || cash == nil {
		return nil
	}
	out := *lt + *st - *cash
	return &out
}

// trendInputs calcula SMA50, SMA200 y momentum 6m/12m sobre cierres
// ajustados de los últimos scoreLookbackBars días (documentado: Close).
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
