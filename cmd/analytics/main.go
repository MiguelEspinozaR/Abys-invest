// Command analytics computes growth/wacc (M6a), valuation 2.0.0 (M6b),
// metrics (MVP) and scores 2.0.0 (M4/M6b) for the selected securities.
//
// Inputs: latest FY fundamentals (M1), latest daily price (M2), user growth
// parameter g (GROWTH_RATE_DEFAULT, default 7%).
//
// Usage:
//
//	go run ./cmd/analytics -tickers AAPL            # job por defecto: metrics
//	go run ./cmd/analytics -job growth -tickers AAPL     # growth_metrics + wacc_metrics
//	go run ./cmd/analytics -job valuation -tickers AAPL  # valuation_results
//	go run ./cmd/analytics -job metrics -tickers AAPL    # derived_metrics
//	go run ./cmd/analytics -job scores -tickers AAPL     # scores
//	go run ./cmd/analytics -job all -tickers AAPL        # growth → valuation → metrics → scores
//	go run ./cmd/analytics -g 8 -dry-run
//
// Thin wrapper (plan M4c): la lógica de métricas y scores vive en
// internal/pipeline; aquí solo se parsean flags, se resuelve el growth de
// GROWTH_RATE_DEFAULT y se delega. CLI/exit codes y salidas de log
// equivalentes a la versión histórica.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/backtest"
	"github.com/miky/abys-invest/internal/pipeline"
	"github.com/miky/abys-invest/internal/storage"
)

const (
	migrationsDir = "migrations"

	// jobGrowth (M6a) calcula SOLO growth_metrics + wacc_metrics: el job
	// aislado para recalcular el motor individual sin tocar las métricas MVP.
	jobGrowth    = "growth"
	jobValuation = "valuation"
	jobMetrics   = "metrics"
	jobQuality   = "quality"
	jobRelative  = "relative"
	jobScores    = "scores"
	jobAll       = "all"
	// jobBacktest (B14) NO persiste nada: reproduce los scores ya escritos y mide
	// los retornos forward. Vive en este binario porque comparte la conexión y la
	// resolución de treamas con los jobs de escritura, no porque escriba.
	jobBacktest = "backtest"
)

func main() {
	var tickersCSV string
	var growth float64
	var dryRun bool
	var job string

	flag.StringVar(&tickersCSV, "tickers", "", "tickers a calcular (CSV); vacío = todos los active con precio")
	flag.Float64Var(&growth, "g", pipeline.DefaultGrowth, "tasa de crecimiento g para PEG (porcentaje)")
	flag.BoolVar(&dryRun, "dry-run", false, "calcula e imprime sin persistir en BD")
	flag.StringVar(&job, "job", jobMetrics, "job a ejecutar: growth | valuation | metrics | quality | relative | scores | all | backtest")
	// Flags del replay (B14). asOf vacío = último score de cada ticker;
	// parameterSet vacío = reproducir con los pesos guardados en el trace.
	var asOf string
	var parameterSet string
	var jsonOut bool
	var fullChain bool
	var allRevisions bool
	flag.StringVar(&asOf, "as-of", "", "fecha del replay AAAA-MM-DD (vacío = último score de cada ticker)")
	flag.StringVar(&parameterSet, "parameter-set", "", "parameter set con el que RE-puntuar el trace (vacío = reproducción exacta)")
	flag.BoolVar(&jsonOut, "json", false, "salida JSON en vez de tabla")
	flag.BoolVar(&fullChain, "full-chain", false, "replay completo growth→wacc→valuation→quality→relative→score desde snapshots (B13)")
	// -all-revisions audita el HISTORIAL: todas las filas de score (todas las
	// as_of, todas las revisiones) en vez de solo el max(as_of) por ticker.
	// El default (false) deja intacto el reporte: una fila por ticker.
	flag.BoolVar(&allRevisions, "all-revisions", false, "replay de TODAS las revisiones persistidas (todas las as_of) en vez de solo la última por ticker")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if v := os.Getenv("GROWTH_RATE_DEFAULT"); v != "" && growth == pipeline.DefaultGrowth {
		if parsed, err := parseGrowth(v); err == nil {
			growth = parsed
		} else {
			slog.Warn("GROWTH_RATE_DEFAULT inválido, usando default", "value", v, "error", err)
		}
	}

	switch job {
	case jobGrowth, jobValuation, jobMetrics, jobQuality, jobRelative, jobScores, jobAll, jobBacktest:
	default:
		slog.Error("job desconocido", "job", job, "esperado", "growth|valuation|metrics|quality|relative|scores|all|backtest")
		os.Exit(2)
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

	// El replay NO escribe nada y por eso NO corre migraciones antes: un comando
	// de verificación que modifica el esquema de la BD a la que apunta es la forma
	// más rápida de convertir una comprobación en un incidente.
	if job == jobBacktest {
		if err := runBacktest(ctx, pool, tickersCSV, asOf, parameterSet, jsonOut, fullChain, allRevisions); err != nil {
			slog.Error("job backtest falló", "error", err)
			os.Exit(1)
		}
		return
	}

	if err := storage.RunMigrations(ctx, pool, migrationsDir); err != nil {
		slog.Error("aplicación de migraciones", "error", err)
		os.Exit(1)
	}

	if _, err := pipeline.RunAnalytics(ctx, pool, tickersCSV, growth, dryRun, job); err != nil {
		slog.Error("job analytics falló", "error", err)
		os.Exit(1)
	}
}

// runBacktest is B14: replay + retornos forward, en tabla o JSON.
func runBacktest(ctx context.Context, pool *pgxpool.Pool, tickersCSV, asOf, parameterSet string, jsonOut bool, fullChain bool, allRevisions bool) error {
	opts := backtest.ReplayOptions{
		TickersCSV: tickersCSV, ParameterSet: parameterSet,
		FullChain: fullChain, AllRevisions: allRevisions,
	}
	if asOf != "" {
		d, err := time.Parse("2006-01-02", asOf)
		if err != nil {
			return fmt.Errorf("as-of inválido %q (AAAA-MM-DD): %w", asOf, err)
		}
		opts.AsOf = d
	}
	rows, err := backtest.ReplayScores(ctx, pool, opts)
	if err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	fmt.Fprintln(os.Stdout, "TICKER     AS_OF       STORED REPLAY OK  P_SET              20d         60d         365d")
	for _, r := range rows {
		ok := "sí"
		switch {
		case strings.HasPrefix(r.Error, "skip:"):
			ok = "omitida"
		case r.Error != "":
			ok = "error"
		case !r.Reproduces:
			ok = "no:" + r.DivergenceReason
		}
		fmt.Fprintf(os.Stdout, "%-10s %-10s %6d %6d %-5s %-18s %s\n",
			r.Ticker, r.AsOf.Format("2006-01-02"), r.StoredScore, r.ReplayedScore, ok, r.ParameterSet,
			formatForward(r.Forward))
		if r.Error != "" {
			fmt.Fprintf(os.Stdout, "  └─ %s\n", r.Error)
		}
	}
	// El resumen es la mitad del valor: 20/20 reproducible sobre 40 filas es un
	// titular distinto de 20/40, y sin el conteo el table anterior se lee como si
	// todo estuviera bien.
	var total, reproduced, errors, skipped int
	for _, r := range rows {
		total++
		switch {
		case strings.HasPrefix(r.Error, "skip:"):
			skipped++
		case r.Error != "":
			errors++
		case r.Reproduces:
			reproduced++
		}
	}
	fmt.Fprintf(os.Stdout, "\nreproducidos %d/%d (omitidas %d, errores %d)\n", reproduced, total, skipped, errors)
	return nil
}

// formatForward renders the three horizons, with the §28 n/d when there is no bar.
func formatForward(fwd []backtest.ForwardReturn) string {
	parts := make([]string, 0, len(fwd))
	for _, f := range fwd {
		if f.ReturnPct == nil {
			parts = append(parts, fmt.Sprintf("%dd n/d", f.HorizonDays))
			continue
		}
		parts = append(parts, fmt.Sprintf("%dd %+.2f%%", f.HorizonDays, *f.ReturnPct))
	}
	return strings.Join(parts, "  ")
}

func parseGrowth(v string) (float64, error) {
	var f float64
	if _, err := fmt.Sscanf(v, "%f", &f); err != nil {
		return 0, err
	}
	return f, nil
}
