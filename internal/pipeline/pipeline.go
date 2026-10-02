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
// M5.1 añade el progreso por etapa (StepFunc), el pipeline PARCIAL de la
// watchlist (WatchlistRefresh, sin EDGAR ni catálogo) y el universo del
// force-refresh (RefreshUniverse = watchlist ∪ securities activas con precio).
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
	"sort"
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
	Tickers   int `json:"tickers"`   // securities objetivo seleccionadas
	Growth    int `json:"growth"`    // securities con growth_metrics persistida OK
	WACC      int `json:"wacc"`      // securities con wacc_metrics persistida OK
	Valuation int `json:"valuation"` // securities con valuation_results persistida OK
	Metrics   int `json:"metrics"`   // securities con métricas persistidas OK
	Scores    int `json:"scores"`    // securities con score persistido OK
}

// ForceResult summarizes a full pipeline force-refresh pass.
type ForceResult struct {
	Tickers   int            `json:"tickers"`   // securities objetivo (metrics+scores)
	Growth    int            `json:"growth"`    // securities con growth_metrics persistida OK
	WACC      int            `json:"wacc"`      // securities con wacc_metrics persistida OK
	Valuation int            `json:"valuation"` // securities con valuation_results persistida OK
	Metrics   int            `json:"metrics"`   // securities con métricas persistidas OK
	Scores    int            `json:"scores"`    // securities con score persistido OK
	Steps     map[string]int `json:"steps"`     // conteos por etapa (edgar/prices/sector/growth/valuation/metrics/scores)
}

var errPoolNil = errors.New("pipeline: pool nil (BD no disponible)")

// RefreshMetricsAndScores re-computes and persists growth_metrics + wacc_metrics
// + valuation_results + derived_metrics + scores (plan M4c "Refresh" + M6a/M6b,
// sin red) for every active security with a price. dryRun logs without
// persisting. Returns the processed counts.
//
// ORDEN (M6b C2): growth → valuation → metrics → scores. Cada etapa consume la
// fila persistida por la anterior; los scores además LEEN valuation_results.
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

	// growth ANTES de valuation: la valoración USA el growth ya persistido.
	g := runGrowthWaccJob(ctx, pool, securities, dryRun)
	// valuation ANTES de metrics/scores: los scores leen valuation_results y,
	// sin fila, renormalizan sus pesos sin las dimensiones de valoración.
	v := runValuationJob(ctx, pool, securities, dryRun)

	return ScoresResult{
		Tickers:   len(securities),
		Growth:    g.Growth,
		WACC:      g.WACC,
		Valuation: v.Inserted,
		Metrics:   runMetricsJob(ctx, pool, securities, growth, dryRun),
		Scores:    runScoresJob(ctx, pool, securities, growth, dryRun),
	}, nil
}

// StepFunc reporta el avance de una etapa (edgar|prices|sector|metrics|scores)
// al consumidor (runner de la API → GET /pipeline/status). nil = sin progreso.
// Se invoca DESPUÉS de que la etapa termina, con el conteo de securities
// procesados con éxito (0 si la etapa no encontró trabajo).
type StepFunc func(step string, n int)

// ForceRefresh runs the full pipeline in order: edgar (fresh re-ingest:
// staging del CIK borrado antes de re-insertar) -> prices -> sector ->
// metrics -> scores. `companies` accepts tickers/CIKs; when empty the target
// universe is resolved from the catalog (active securities with price), with a
// DefaultCompany fallback (bootstrap).
//
// Es el wrapper de OnStep=nil de ForceRefreshWithProgress (M5.1): mismo
// comportamiento, sin callbacks de progreso. cmd/collector, cmd/analytics y los
// tests de M1-M4 conservan esta firma.
func ForceRefresh(ctx context.Context, pool *pgxpool.Pool, companies []string, growth float64, ua string, dryRun bool) (ForceResult, error) {
	return ForceRefreshWithProgress(ctx, pool, companies, growth, ua, dryRun, nil)
}

// ForceRefreshWithProgress es el cuerpo de ForceRefresh con el reporte de
// avance por etapa (M5.1). onStep nil equivale a ForceRefresh.
func ForceRefreshWithProgress(ctx context.Context, pool *pgxpool.Pool, companies []string, growth float64, ua string, dryRun bool, onStep StepFunc) (ForceResult, error) {
	if pool == nil {
		return ForceResult{}, errPoolNil
	}
	report := func(step string, n int) {
		if onStep != nil {
			onStep(step, n)
		}
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
	report("edgar", n)

	// 2) Precios Yahoo para el mismo universo.
	n, err = RunPricesJob(ctx, pool, tickersCSV)
	if err != nil {
		return res, fmt.Errorf("pipeline: prices: %w", err)
	}
	res.Steps["prices"] = n
	report("prices", n)

	// 3) Sector/industria.
	n, err = RunSectorJob(ctx, pool, tickersCSV)
	if err != nil {
		return res, fmt.Errorf("pipeline: sector: %w", err)
	}
	res.Steps["sector"] = n
	report("sector", n)

	// 4) growth/wacc → 5) valuation 2.0.0 → 6) métricas → 7) scores.
	//    El orden es parte del contrato (M6b C2): cada etapa lee la fila
	//    persistida por la anterior.
	securities, err := resolveTargets(ctx, pool, tickersCSV)
	if err != nil {
		return res, fmt.Errorf("pipeline: resolución de tickers: %w", err)
	}
	if len(securities) == 0 {
		slog.Warn("sin securities objetivo tras la ingesta")
		return res, nil
	}
	res.Tickers = len(securities)
	g := runGrowthWaccJob(ctx, pool, securities, dryRun)
	res.Growth, res.WACC = g.Growth, g.WACC
	res.Steps["growth"] = g.Growth
	report("growth", g.Growth)
	v := runValuationJob(ctx, pool, securities, dryRun)
	res.Valuation = v.Inserted
	res.Steps["valuation"] = v.Inserted
	report("valuation", v.Inserted)
	res.Metrics = runMetricsJob(ctx, pool, securities, growth, dryRun)
	res.Scores = runScoresJob(ctx, pool, securities, growth, dryRun)
	res.Steps["metrics"] = res.Metrics
	res.Steps["scores"] = res.Scores
	report("metrics", res.Metrics)
	report("scores", res.Scores)
	return res, nil
}

// WatchlistRefresh (M5.1) es el pipeline PARCIAL de los tickers ya catalogados
// de la watchlist: prices -> sector -> growth/wacc -> valuation -> metrics ->
// scores, en ese orden y SIN EDGAR ni catálogo (el ticker ya existe en securities: lo
// garantiza el PUT /watchlist/{ticker}, que devuelve 404 si no). Devuelve
// ForceResult con Steps{prices,sector,growth,metrics,scores} (sin clave "edgar").
//
// Métricas y scores se piden en dos llamadas a RunAnalytics (AnalyticsJobMetrics
// y luego AnalyticsJobScores) en vez de AnalyticsJobAll: el progreso avanza en
// dos saltos observables y un fallo en scores no pierde el resultado de métricas.
// Coste: una resolución de targets extra (1 query por ticker).
//
// Un ticker sin datos en Yahoo NO produce estado de error: RunPricesJob loguea y
// continúa (0 exitosos) y runScoresJob omite el ticker sin precio previo, así
// que el job termina `done` con steps en 0. Es lo correcto para la UX (el ticker
// recién añadido puede no existir todavía en Yahoo).
func WatchlistRefresh(ctx context.Context, pool *pgxpool.Pool, tickers []string, growth float64, dryRun bool, onStep StepFunc) (ForceResult, error) {
	if pool == nil {
		return ForceResult{}, errPoolNil
	}
	report := func(step string, n int) {
		if onStep != nil {
			onStep(step, n)
		}
	}
	if growth <= 0 {
		growth = DefaultGrowth
	}

	list := normalizeTickerList(tickers)
	if len(list) == 0 {
		return ForceResult{}, errors.New("pipeline: WatchlistRefresh sin tickers")
	}
	tickersCSV := strings.Join(list, ",")

	res := ForceResult{Steps: map[string]int{}}

	// 1) Precios Yahoo (ticker por ticker; los fallos se loguean, no abortan).
	n, err := RunPricesJob(ctx, pool, tickersCSV)
	if err != nil {
		return res, fmt.Errorf("pipeline: prices: %w", err)
	}
	res.Steps["prices"] = n
	report("prices", n)

	// 2) Sector/industria.
	n, err = RunSectorJob(ctx, pool, tickersCSV)
	if err != nil {
		return res, fmt.Errorf("pipeline: sector: %w", err)
	}
	res.Steps["sector"] = n
	report("sector", n)

	// 3) Growth + WACC (M6a). Sin red: lee fundamentals/precios y la beta
	// observada que el paso 2 acaba de refrescar, así que va DESPUÉS de sector
	// y ANTES de valuation/metrics/scores.
	securities, err := resolveTargets(ctx, pool, tickersCSV)
	if err != nil {
		return res, fmt.Errorf("pipeline: resolución de tickers: %w", err)
	}
	if len(securities) == 0 {
		slog.Warn("sin securities objetivo tras la ingesta")
		return res, nil
	}
	g := runGrowthWaccJob(ctx, pool, securities, dryRun)
	res.Growth, res.WACC = g.Growth, g.WACC
	res.Steps["growth"] = g.Growth
	res.Tickers = len(securities)
	report("growth", g.Growth)

	// 4) Valuation 2.0.0 (M6b). Etapa propia y OBSERVABLE (step "valuation")
	// porque los scores leen valuation_results: sin esta fila el score se
	// renormaliza sin las dimensiones Graham/DCF, y eso debe verse en el
	// progreso del job, no aparecer como un score mysteriously más bajo.
	v, err := RunAnalytics(ctx, pool, tickersCSV, growth, dryRun, AnalyticsJobValuation)
	if err != nil {
		return res, fmt.Errorf("pipeline: valuation: %w", err)
	}
	res.Valuation = v.Valuation
	res.Steps["valuation"] = v.Valuation
	report("valuation", v.Valuation)

	// 5) Métricas derivadas.
	m, err := RunAnalytics(ctx, pool, tickersCSV, growth, dryRun, AnalyticsJobMetrics)
	if err != nil {
		return res, fmt.Errorf("pipeline: metrics: %w", err)
	}
	res.Steps["metrics"] = m.Metrics
	report("metrics", m.Metrics)

	// 6) Scores (job aparte: progreso en dos saltos y métricas ya persistidas).
	s, err := RunAnalytics(ctx, pool, tickersCSV, growth, dryRun, AnalyticsJobScores)
	if err != nil {
		return res, fmt.Errorf("pipeline: scores: %w", err)
	}
	res.Steps["scores"] = s.Scores
	report("scores", s.Scores)
	return res, nil
}

// RefreshUniverse (M5.1) es el universo del force-refresh: la watchlist ∪ las
// securities activas con precio, deduplicado y ordenado (determinismo: dos
// ejecuciones con la misma BD producen el mismo CSV para el pipeline). Un ticker
// de la watchlist entra aunque esté delistado o sin precios: es exactamente el
// caso del reporte original (NVDA en la watchlist sin procesar).
func RefreshUniverse(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	if pool == nil {
		return nil, errPoolNil
	}
	active, err := activeUniverse(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("pipeline: universo de empresas: %w", err)
	}
	watch, err := storage.ListWatchlistTickers(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("pipeline: universo de watchlist: %w", err)
	}
	seen := make(map[string]struct{}, len(active)+len(watch))
	out := make([]string, 0, len(active)+len(watch))
	for _, group := range [][]string{active, watch} {
		for _, t := range normalizeTickerList(group) {
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out, nil
}

// normalizeTickerList normaliza una lista de tickers (TrimSpace + Upper),
// descarta vacíos y deduplica preservando el orden de entrada (FIFO visible en
// la cola del runner de la API).
func normalizeTickerList(tickers []string) []string {
	out := make([]string, 0, len(tickers))
	seen := make(map[string]struct{}, len(tickers))
	for _, t := range tickers {
		t = strings.ToUpper(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
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

// getSecurityByID fetches a security row by its primary key. It includes
// beta/beta_updated_at (M6a D17): the WACC stage reads the OBSERVED beta from
// this row instead of issuing a second query per security.
func getSecurityByID(ctx context.Context, pool *pgxpool.Pool, id int64) (*storage.Security, error) {
	row := pool.QueryRow(ctx, `SELECT id, ticker, cik, name, type, currency, status, exchange, sector, industry, beta, beta_updated_at, created_at, updated_at
		FROM securities WHERE id = $1`, id)
	s := &storage.Security{}
	if err := row.Scan(&s.ID, &s.Ticker, &s.CIK, &s.Name, &s.Type, &s.Currency, &s.Status,
		&s.Exchange, &s.Sector, &s.Industry, &s.Beta, &s.BetaUpdatedAt, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, fmt.Errorf("pipeline: get security by id %d: %w", id, err)
	}
	return s, nil
}
