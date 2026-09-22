// Command analytics computes the 8 MVP valuation metrics (SPEC §13) for the
// selected securities and, optionally, the M3 score (0-100) persisted into
// the scores table.
//
// Inputs: latest FY fundamentals (M1), latest daily price (M2), user growth
// parameter g (GROWTH_RATE_DEFAULT, default 7%).
//
// Usage:
//
//	go run ./cmd/analytics -tickers AAPL            # job por defecto: metrics
//	go run ./cmd/analytics -job scores -tickers AAPL
//	go run ./cmd/analytics -job all -tickers AAPL
//	go run ./cmd/analytics -g 8 -dry-run
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/metrics"
	"github.com/miky/abys-invest/internal/score"
	"github.com/miky/abys-invest/internal/storage"
	"github.com/miky/abys-invest/internal/valuation"
)

const (
	migrationsDir = "migrations"
	defaultGrowth = 7.0

	// Env inputs del job scores (mismo contrato que /valuation y /score).
	envMarginOfSafety    = "MARGIN_OF_SAFETY"
	envCompMinSecurities = "COMPARABLES_MIN_SECURITIES"
	envCompHistoryYears  = "COMPARABLES_HISTORY_YEARS"
	defaultMarginSafety  = 30.0
	defaultCompMinSec    = 5
	defaultCompHistYears = 5
	scoreLookbackBars    = 400 // ventana de precios para SMA/momentum

	jobMetrics = "metrics"
	jobScores  = "scores"
	jobAll     = "all"
)

// latestFYFundamentals fetches the newest FY row of every required concept.
// Missing concepts simply stay nil (conservative engine rule).
func latestFYFundamentals(ctx context.Context, pool *pgxpool.Pool, securityID int64, concepts []string) (map[string]*float64, error) {
	out := map[string]*float64{}
	rows, err := pool.Query(ctx, `SELECT DISTINCT ON (concept) concept, value
		FROM fundamentals
		WHERE security_id = $1 AND concept = ANY($2) AND fiscal_period = 'FY'
		ORDER BY concept, period_end DESC`, securityID, concepts)
	if err != nil {
		return nil, fmt.Errorf("analytics: fundamentales FY security %d: %w", securityID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var concept string
		var value *float64
		if err := rows.Scan(&concept, &value); err != nil {
			return nil, fmt.Errorf("analytics: scan fundamentales: %w", err)
		}
		out[concept] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("analytics: rows fundamentales: %w", err)
	}
	return out, nil
}

func main() {
	var tickersCSV string
	var growth float64
	var dryRun bool
	var job string

	flag.StringVar(&tickersCSV, "tickers", "", "tickers a calcular (CSV); vacío = todos los active con precio")
	flag.Float64Var(&growth, "g", defaultGrowth, "tasa de crecimiento g para PEG (porcentaje)")
	flag.BoolVar(&dryRun, "dry-run", false, "calcula e imprime sin persistir en BD")
	flag.StringVar(&job, "job", jobMetrics, "job a ejecutar: metrics | scores | all")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if v := os.Getenv("GROWTH_RATE_DEFAULT"); v != "" && growth == defaultGrowth {
		if parsed, err := parseGrowth(v); err == nil {
			growth = parsed
		} else {
			slog.Warn("GROWTH_RATE_DEFAULT inválido, usando default", "value", v, "error", err)
		}
	}
	if growth <= 0 {
		growth = defaultGrowth
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL requerido")
		os.Exit(1)
	}
	pool, err := storage.Connect(ctx, dsn)
	if err != nil {
		slog.Error("conexión a Postgres", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := storage.RunMigrations(ctx, pool, migrationsDir); err != nil {
		slog.Error("aplicación de migraciones", "error", err)
		os.Exit(1)
	}

	switch job {
	case jobMetrics, jobScores, jobAll:
	default:
		slog.Error("job desconocido", "job", job, "esperado", "metrics|scores|all")
		os.Exit(2)
	}

	securities, err := resolveTargets(ctx, pool, tickersCSV)
	if err != nil {
		slog.Error("resolución de tickers", "error", err)
		os.Exit(1)
	}
	if len(securities) == 0 {
		slog.Warn("sin securities objetivo")
		return
	}

	switch job {
	case jobMetrics:
		runMetricsJob(ctx, pool, securities, growth, dryRun)
	case jobScores:
		runScoresJob(ctx, pool, securities, growth, dryRun)
	case jobAll:
		runMetricsJob(ctx, pool, securities, growth, dryRun)
		runScoresJob(ctx, pool, securities, growth, dryRun)
	}
}

// runMetricsJob computes and persists the 8 MVP metrics (SPEC §13.2) into
// derived_metrics for every target security. Idempotente por (security, as_of).
func runMetricsJob(ctx context.Context, pool *pgxpool.Pool, securities []storage.Security, growth float64, dryRun bool) {
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
}

// runScoresJob assembles every ScoreInput from persisted M1/M2 data and
// persists the M3 score (idempotente por (security, as_of, modelo)). En
// dry-run solo imprime. Las métricas se leen de derived_metrics; si el ticker
// no las tiene, el score degrada (dimensiones neutrales) sin crear datos.
func runScoresJob(ctx context.Context, pool *pgxpool.Pool, securities []storage.Security, growth float64, dryRun bool) {
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
}

// scoreParams bucket de los parámetros de entorno del job scores.
type scoreParams struct {
	Growth            float64
	MarginSafety      float64
	CompMinSecurities int
	CompHistoryYears  int
}

// parseScoreParams lee los parámetros del job scores (defaults idénticos a
// los de /valuation y /score de la API).
func parseScoreParams(growth float64) scoreParams {
	margin := envFloatCfg("MARGIN_OF_SAFETY", defaultMarginSafety)
	minSec := envIntCfg("COMPARABLES_MIN_SECURITIES", defaultCompMinSec)
	histYears := envIntCfg("COMPARABLES_HISTORY_YEARS", defaultCompHistYears)
	return scoreParams{
		Growth: growth, MarginSafety: margin,
		CompMinSecurities: minSec, CompHistoryYears: histYears,
	}
}

// envFloatCfg reads a float env var with default.
func envFloatCfg(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envIntCfg(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
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

// resolveTargets: explicit tickers or all active securities with a price.
func resolveTargets(ctx context.Context, pool *pgxpool.Pool, tickersCSV string) ([]storage.Security, error) {
	if strings.TrimSpace(tickersCSV) != "" {
		var out []storage.Security
		for _, t := range strings.Split(tickersCSV, ",") {
			t = strings.TrimSpace(strings.ToUpper(t))
			if t == "" {
				continue
			}
			sec, err := storage.GetSecurityByTicker(ctx, pool, t)
			if err != nil {
				return nil, fmt.Errorf("security %q: %w", t, err)
			}
			out = append(out, *sec)
		}
		return out, nil
	}

	ids, err := storage.ListSecuritiesWithPrices(ctx, pool)
	if err != nil {
		return nil, err
	}
	var out []storage.Security
	for _, id := range ids {
		sec, err := getSecurityByID(ctx, pool, id)
		if err != nil {
			slog.Warn("security omitida (id no resoluble)", "id", id, "error", err)
			continue
		}
		if sec.Status == "active" {
			out = append(out, *sec)
		}
	}
	return out, nil
}

// getSecurityByID fetches a security row by its primary key.
func getSecurityByID(ctx context.Context, pool *pgxpool.Pool, id int64) (*storage.Security, error) {
	row := pool.QueryRow(ctx, `SELECT id, ticker, cik, name, type, currency, status, exchange, sector, industry, created_at, updated_at
		FROM securities WHERE id = $1`, id)
	s := &storage.Security{}
	if err := row.Scan(&s.ID, &s.Ticker, &s.CIK, &s.Name, &s.Type, &s.Currency, &s.Status,
		&s.Exchange, &s.Sector, &s.Industry, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, fmt.Errorf("analytics: get security by id %d: %w", id, err)
	}
	return s, nil
}

func parseGrowth(v string) (float64, error) {
	var f float64
	if _, err := fmt.Sscanf(v, "%f", &f); err != nil {
		return 0, err
	}
	return f, nil
}

func formatValue(v *float64) string {
	if v == nil {
		return "NULL"
	}
	return fmt.Sprintf("%.6f", *v)
}
