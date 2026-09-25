// Command collector is the ingestion worker of Abys-Invest: SEC EDGAR
// fundamentals (M1), Yahoo Finance daily prices and BLS macro series (M2),
// and sector/industry enrichment (M3, Yahoo quoteSummary + Finviz fallback).
//
// Usage:
//
//	go run ./cmd/collector -job edgar -companies AAPL
//	go run ./cmd/collector -job prices -tickers AAPL
//	go run ./cmd/collector -job macro -macro-series CPI
//	go run ./cmd/collector -job sector -tickers AAPL
//	go run ./cmd/collector -job all
//
// Thin wrapper (plan M4c): la lógica de los jobs edgar/prices/sector vive en
// internal/pipeline; aquí solo se parsean flags, se conecta la BD y se
// delega. El job macro sigue residiendo en este paquete.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/collect/macro"
	"github.com/miky/abys-invest/internal/pipeline"
	"github.com/miky/abys-invest/internal/storage"
)

const (
	// userAgentDefault is only a dev fallback; production must set
	// SEC_EDGAR_USER_AGENT (politics SEC).
	userAgentDefault = "AbysInvest/1.0 (dev)"
	migrationsDir    = "migrations"

	jobEdgar  = "edgar"
	jobPrices = "prices"
	jobMacro  = "macro"
	jobSector = "sector"
	jobAll    = "all"
)

// stringList implements flag.Var for CSV flag values.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }

func (s *stringList) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*s = append(*s, p)
		}
	}
	return nil
}

func main() {
	var companies stringList
	var dryRun bool
	var job string
	var tickers string
	var macroSeries string
	var years int

	flag.Var(&companies, "companies", "tickers/CIKs a ingerir por EDGAR (CSV); por defecto: "+pipeline.DefaultCompany)
	flag.BoolVar(&dryRun, "dry-run", false, "job edgar: descarga y canoniza en memoria sin escribir en la BD")
	flag.StringVar(&job, "job", jobAll, "job a ejecutar: edgar | prices | macro | sector | all")
	flag.StringVar(&tickers, "tickers", "", "tickers para precios/analytics (CSV); vacío = todos los securities activos en BD")
	flag.StringVar(&macroSeries, "macro-series", "CPI", "series macro a ingerir (CSV)")
	flag.IntVar(&years, "years", 5, "años de histórico macro (startYear = año actual - years)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	switch job {
	case jobEdgar, jobPrices, jobMacro, jobSector, jobAll:
	default:
		slog.Error("job desconocido", "job", job, "esperado", "edgar|prices|macro|sector|all")
		os.Exit(2)
	}

	ua := os.Getenv("SEC_EDGAR_USER_AGENT")
	if ua == "" {
		ua = userAgentDefault
		slog.Warn("SEC_EDGAR_USER_AGENT no definido, usando fallback de desarrollo")
	}

	var pool *pgxpool.Pool
	needDB := job != jobEdgar || !dryRun
	if needDB {
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			slog.Error("DATABASE_URL requerido (o use -job edgar -dry-run)")
			os.Exit(1)
		}
		var err error
		pool, err = storage.Connect(ctx, dsn)
		if err != nil {
			slog.Error("conexión a Postgres", "error", err)
			os.Exit(1)
		}
		defer pool.Close()
		if err := storage.RunMigrations(ctx, pool, migrationsDir); err != nil {
			slog.Error("aplicación de migraciones", "error", err)
			os.Exit(1)
		}
		slog.Info("base lista, migraciones aplicadas")
	}

	switch job {
	case jobEdgar:
		if _, err := pipeline.RunEdgarJob(ctx, pool, companies, dryRun, ua); err != nil {
			slog.Error("job edgar falló", "error", err)
			os.Exit(1)
		}

	case jobPrices:
		if _, err := pipeline.RunPricesJob(ctx, pool, tickers); err != nil {
			slog.Error("job prices falló", "error", err)
			os.Exit(1)
		}

	case jobMacro:
		runMacroJob(ctx, pool, macroSeries, years)

	case jobSector:
		if _, err := pipeline.RunSectorJob(ctx, pool, tickers); err != nil {
			slog.Error("job sector falló", "error", err)
			os.Exit(1)
		}

	case jobAll:
		if _, err := pipeline.RunEdgarJob(ctx, pool, companies, dryRun, ua); err != nil {
			slog.Error("job edgar falló", "error", err)
			os.Exit(1)
		}
		if _, err := pipeline.RunPricesJob(ctx, pool, tickers); err != nil {
			slog.Error("job prices falló", "error", err)
			os.Exit(1)
		}
		runMacroJob(ctx, pool, macroSeries, years)
		if _, err := pipeline.RunSectorJob(ctx, pool, tickers); err != nil {
			slog.Error("job sector falló", "error", err)
			os.Exit(1)
		}
	}
}

// runMacroJob ingests the selected macro series (default CPI) for the last N
// years (default 5).
func runMacroJob(ctx context.Context, pool *pgxpool.Pool, seriesCSV string, years int) {
	series := splitCSV(seriesCSV)
	if len(series) == 0 {
		series = []string{"CPI"}
	}
	endYear := time.Now().UTC().Year()
	startYear := endYear - years
	if err := macro.ValidateYearRange(strconv.Itoa(startYear), strconv.Itoa(endYear)); err != nil {
		slog.Error("rango de años macro inválido", "error", err)
		os.Exit(1)
	}

	mc := macro.NewClient(os.Getenv("BLS_API_KEY"))
	succeeded := 0
	for _, key := range series {
		n, err := mc.IngestSeries(ctx, pool, key, strconv.Itoa(startYear), strconv.Itoa(endYear))
		if err != nil {
			slog.Error("ingesta macro falló (continúa)", "serie", key, "error", err)
			continue
		}
		slog.Info("serie macro ingerida", "serie", key, "observaciones", n)
		succeeded++
	}
	slog.Info("job macro terminado", "exitosos", succeeded, "total", len(series))
}

// splitCSV splits comma-separated values, trimming empties.
func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
