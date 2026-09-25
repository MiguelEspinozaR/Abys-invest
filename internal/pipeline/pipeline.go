// Package pipeline hosts the reusable ingestion/refresh jobs of Abys-Invest.
//
// The logic was extracted from cmd/collector and cmd/analytics (that are now
// thin CLI wrappers delegating here) so the HTTP API can trigger the same
// deterministic jobs from the dashboard (plan M4c):
//
//   - RefreshMetricsAndScores: re-computes derived_metrics + scores for the
//     active securities with a price (fast, without network).
//   - ForceRefresh: full pipeline edgar (fresh re-ingest) -> prices -> sector
//     -> metrics -> scores.
//
// All functions are deterministic per input, log with slog and never abort the
// host process; errors are returned so CLI wrappers decide exit codes.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
)

const (
	// DefaultGrowth is GROWTH_RATE_DEFAULT (g = 7%) when no value is provided.
	DefaultGrowth = 7.0
	// DefaultCompany is the default EDGAR ingestion target (same CLI contract
	// than the historical cmd/collector -companies default).
	DefaultCompany = "AAPL"

	// env inputs of the metrics/scores job (same contract as the API loader):
	// defaults are identical to the /valuation and /score endpoints.
	envMarginOfSafety    = "MARGIN_OF_SAFETY"
	envCompMinSecurities = "COMPARABLES_MIN_SECURITIES"
	envCompHistoryYears  = "COMPARABLES_HISTORY_YEARS"
	defaultMarginSafety  = 30.0
	defaultCompMinSec    = 5
	defaultCompHistYears = 5
	scoreLookbackBars    = 400 // ventana de precios para SMA/momentum
)

// ScoresResult summarizes a metrics + scores refresh pass.
type ScoresResult struct {
	Tickers int `json:"tickers"` // securities objetivo seleccionadas
	Metrics int `json:"metrics"` // securities con métricas persistidas OK
	Scores  int `json:"scores"`  // securities con score persistido OK
}

// ForceResult summarizes a full pipeline force-refresh pass.
type ForceResult struct {
	Tickers int            `json:"tickers"` // securities objetivo (metrics+scores)
	Metrics int            `json:"metrics"` // securities con métricas persistidas OK
	Scores  int            `json:"scores"`  // securities con score persistido OK
	Steps   map[string]int `json:"steps"`   // conteos por etapa (edgar/prices/sector/metrics/scores)
}

var errPoolNil = errors.New("pipeline: pool nil (BD no disponible)")

// RefreshMetricsAndScores re-computes and persists derived_metrics + scores
// (plan M4c "Refresh", sin red) for every active security with a price.
// dryRun logs without persisting. Returns the processed counts.
func RefreshMetricsAndScores(ctx context.Context, pool *pgxpool.Pool, growth float64, dryRun bool) (ScoresResult, error) {
	if pool == nil {
		return ScoresResult{}, errPoolNil
	}
	if growth <= 0 {
		growth = DefaultGrowth
	}

	securities, err := resolveTargets(ctx, pool, "")
	if err != nil {
		return ScoresResult{}, fmt.Errorf("pipeline: resolución de tickers: %w", err)
	}
	if len(securities) == 0 {
		slog.Warn("sin securities objetivo para refresh")
		return ScoresResult{}, nil
	}

	return ScoresResult{
		Tickers: len(securities),
		Metrics: runMetricsJob(ctx, pool, securities, growth, dryRun),
		Scores:  runScoresJob(ctx, pool, securities, growth, dryRun),
	}, nil
}

// ForceRefresh runs the full pipeline in order: edgar (fresh re-ingest:
// staging del CIK borrado antes de re-insertar) -> prices -> sector ->
// metrics -> scores. `companies` accepts tickers/CIKs; when empty the target
// universe is resolved from the catalog (active securities with price), with a
// DefaultCompany fallback (bootstrap).
func ForceRefresh(ctx context.Context, pool *pgxpool.Pool, companies []string, growth float64, ua string, dryRun bool) (ForceResult, error) {
	if pool == nil {
		return ForceResult{}, errPoolNil
	}
	if growth <= 0 {
		growth = DefaultGrowth
	}

	if len(companies) == 0 {
		universe, err := activeUniverse(ctx, pool)
		if err != nil {
			return ForceResult{}, fmt.Errorf("pipeline: universo de empresas: %w", err)
		}
		companies = universe
		if len(companies) == 0 {
			companies = []string{DefaultCompany}
		}
	}
	tickersCSV := strings.Join(companies, ",")

	res := ForceResult{Steps: map[string]int{}}

	// 1) EDGAR: catálogo SEC + companyfacts con re-ingesta fresca por CIK.
	n, err := runEdgarJob(ctx, pool, companies, dryRun, ua, true)
	if err != nil {
		return res, fmt.Errorf("pipeline: edgar: %w", err)
	}
	res.Steps["edgar"] = n

	// 2) Precios Yahoo para el mismo universo.
	n, err = RunPricesJob(ctx, pool, tickersCSV)
	if err != nil {
		return res, fmt.Errorf("pipeline: prices: %w", err)
	}
	res.Steps["prices"] = n

	// 3) Sector/industria.
	n, err = RunSectorJob(ctx, pool, tickersCSV)
	if err != nil {
		return res, fmt.Errorf("pipeline: sector: %w", err)
	}
	res.Steps["sector"] = n

	// 4) Métricas + 5) scores.
	securities, err := resolveTargets(ctx, pool, tickersCSV)
	if err != nil {
		return res, fmt.Errorf("pipeline: resolución de tickers: %w", err)
	}
	if len(securities) == 0 {
		slog.Warn("sin securities objetivo tras la ingesta")
		return res, nil
	}
	res.Tickers = len(securities)
	res.Metrics = runMetricsJob(ctx, pool, securities, growth, dryRun)
	res.Scores = runScoresJob(ctx, pool, securities, growth, dryRun)
	res.Steps["metrics"] = res.Metrics
	res.Steps["scores"] = res.Scores
	return res, nil
}

// activeUniverse returns the tickers of active securities that have at least
// one price row (the dashboard universe), ordered by ticker.
func activeUniverse(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	ids, err := storage.ListSecuritiesWithPrices(ctx, pool)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, id := range ids {
		sec, err := getSecurityByID(ctx, pool, id)
		if err != nil {
			slog.Warn("security omitida (id no resoluble)", "id", id, "error", err)
			continue
		}
		if sec.Status == "active" {
			out = append(out, sec.Ticker)
		}
	}
	return out, nil
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
	margin := envFloatCfg(envMarginOfSafety, defaultMarginSafety)
	minSec := envIntCfg(envCompMinSecurities, defaultCompMinSec)
	histYears := envIntCfg(envCompHistoryYears, defaultCompHistYears)
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
		return nil, fmt.Errorf("pipeline: get security by id %d: %w", id, err)
	}
	return s, nil
}
