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
//
// Thin wrapper (plan M4c): la lógica de métricas y scores vive en
// internal/pipeline; aquí solo se parsean flags, se resuelve el growth de
// GROWTH_RATE_DEFAULT y se delega. CLI/exit codes y salidas de log
// equivalentes a la versión histórica.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/miky/abys-invest/internal/pipeline"
	"github.com/miky/abys-invest/internal/storage"
)

const (
	migrationsDir = "migrations"

	jobMetrics = "metrics"
	jobScores  = "scores"
	jobAll     = "all"
)

func main() {
	var tickersCSV string
	var growth float64
	var dryRun bool
	var job string

	flag.StringVar(&tickersCSV, "tickers", "", "tickers a calcular (CSV); vacío = todos los active con precio")
	flag.Float64Var(&growth, "g", pipeline.DefaultGrowth, "tasa de crecimiento g para PEG (porcentaje)")
	flag.BoolVar(&dryRun, "dry-run", false, "calcula e imprime sin persistir en BD")
	flag.StringVar(&job, "job", jobMetrics, "job a ejecutar: metrics | scores | all")
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
	case jobMetrics, jobScores, jobAll:
	default:
		slog.Error("job desconocido", "job", job, "esperado", "metrics|scores|all")
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
	if err := storage.RunMigrations(ctx, pool, migrationsDir); err != nil {
		slog.Error("aplicación de migraciones", "error", err)
		os.Exit(1)
	}

	if _, err := pipeline.RunAnalytics(ctx, pool, tickersCSV, growth, dryRun, job); err != nil {
		slog.Error("job analytics falló", "error", err)
		os.Exit(1)
	}
}

func parseGrowth(v string) (float64, error) {
	var f float64
	if _, err := fmt.Sscanf(v, "%f", &f); err != nil {
		return 0, err
	}
	return f, nil
}
