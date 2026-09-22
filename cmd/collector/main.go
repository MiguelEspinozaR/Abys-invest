// Command collector is the ingestion worker of Abys-Invest: SEC EDGAR
// fundamentals (M1), Yahoo Finance daily prices and BLS macro series (M2).
//
// Usage:
//
//	go run ./cmd/collector -job edgar -companies AAPL
//	go run ./cmd/collector -job prices -tickers AAPL
//	go run ./cmd/collector -job macro -macro-series CPI
//	go run ./cmd/collector -job all
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/collect/edgar"
	"github.com/miky/abys-invest/internal/collect/macro"
	"github.com/miky/abys-invest/internal/collect/yahoo"
	"github.com/miky/abys-invest/internal/storage"
)

const (
	// userAgentDefault is only a dev fallback; production must set
	// SEC_EDGAR_USER_AGENT (politics SEC).
	userAgentDefault = "AbysInvest/1.0 (dev)"
	migrationsDir    = "migrations"
	defaultCompany   = "AAPL"

	jobEdgar  = "edgar"
	jobPrices = "prices"
	jobMacro  = "macro"
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

	flag.Var(&companies, "companies", "tickers/CIKs a ingerir por EDGAR (CSV); por defecto: "+defaultCompany)
	flag.BoolVar(&dryRun, "dry-run", false, "job edgar: descarga y canoniza en memoria sin escribir en la BD")
	flag.StringVar(&job, "job", jobAll, "job a ejecutar: edgar | prices | macro | all")
	flag.StringVar(&tickers, "tickers", "", "tickers para precios/analytics (CSV); vacío = todos los securities activos en BD")
	flag.StringVar(&macroSeries, "macro-series", "CPI", "series macro a ingerir (CSV)")
	flag.IntVar(&years, "years", 5, "años de histórico macro (startYear = año actual - years)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	switch job {
	case jobEdgar, jobPrices, jobMacro, jobAll:
	default:
		slog.Error("job desconocido", "job", job, "esperado", "edgar|prices|macro|all")
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
		runEdgarJob(ctx, pool, companies, dryRun, ua)

	case jobPrices:
		runPricesJob(ctx, pool, tickers)

	case jobMacro:
		runMacroJob(ctx, pool, macroSeries, years)

	case jobAll:
		runEdgarJob(ctx, pool, companies, dryRun, ua)
		runPricesJob(ctx, pool, tickers)
		runMacroJob(ctx, pool, macroSeries, years)
	}
}

// runEdgarJob is the M1 ingestion path (SEC catalog + companyfacts).
func runEdgarJob(ctx context.Context, pool *pgxpool.Pool, companies stringList, dryRun bool, ua string) {
	client, err := edgar.NewClient(ua)
	if err != nil {
		slog.Error("configuración del cliente EDGAR", "error", err)
		os.Exit(1)
	}

	if len(companies) == 0 {
		companies = stringList{defaultCompany}
	}

	// 1) Catálogo SEC -> securities (no bloqueante si falla).
	var catalog []edgar.CompanyTicker
	rawCatalog, catalogErr := client.GetCompanyTickers(ctx)
	if catalogErr != nil {
		slog.Error("catálogo SEC no disponible (se continúa por CIK directo)", "error", catalogErr)
		catalog = nil
	} else {
		catalog, catalogErr = edgar.ParseCompanyTickers(rawCatalog)
		if catalogErr != nil {
			slog.Error("parseo del catálogo SEC falló (se continúa por CIK directo)", "error", catalogErr)
			catalog = nil
		} else if !dryRun && pool != nil {
			upserted := upsertCatalog(ctx, pool, catalog)
			slog.Info("catálogo persistido", "securities", upserted, "total", len(catalog))
		} else {
			slog.Info("catálogo disponible (dry-run: sin persistir)", "empresas", len(catalog))
		}
	}

	// 2) Empresas objetivo: companyfacts -> staging -> normalización.
	succeeded := 0
	for _, target := range companies {
		name, cik, err := resolveCompany(catalog, target)
		if err != nil {
			slog.Error("empresa no resoluble (continúa)", "entrada", target, "error", err)
			continue
		}
		slog.Info("ingesta de empresa", "ticker", name, "cik", cik, "dry_run", dryRun)

		if dryRun {
			if err := dryRunCompany(ctx, client, cik); err != nil {
				slog.Error("dry-run falló (continúa)", "ticker", name, "error", err)
				continue
			}
			succeeded++
			continue
		}

		if err := ingestCompany(ctx, pool, client, name, cik); err != nil {
			slog.Error("empresa fallida (continúa)", "ticker", name, "error", err)
			continue
		}
		succeeded++
	}

	if !dryRun && succeeded == 0 {
		slog.Error("ninguna empresa objetivo se procesó con éxito")
		os.Exit(1)
	}
}

// runPricesJob ingests Yahoo OHLCV history + daily quote for the selected
// tickers. Individual ticker errors are logged and never abort the job.
func runPricesJob(ctx context.Context, pool *pgxpool.Pool, tickers string) {
	yc := yahoo.NewClient()
	if ua := os.Getenv("SEC_EDGAR_USER_AGENT"); ua != "" {
		yc = yahoo.NewClient(yahoo.WithUserAgent(ua))
	}

	securities, err := resolveSecurities(ctx, pool, tickers)
	if err != nil {
		slog.Error("resolución de tickers para precios", "error", err)
		os.Exit(1)
	}
	if len(securities) == 0 {
		slog.Warn("sin securities activas para ingesta de precios")
		return
	}

	succeeded := 0
	for _, sec := range securities {
		n, err := yc.IngestPrices(ctx, pool, sec.ID, sec.Ticker)
		if err != nil {
			slog.Error("ingesta de precios falló (continúa)", "ticker", sec.Ticker, "error", err)
			continue
		}
		slog.Info("precios históricos ingeridos", "ticker", sec.Ticker, "barras", n)
		if _, err := yc.IngestQuote(ctx, pool, sec.ID, sec.Ticker); err != nil {
			slog.Warn("quote actual falló (continúa)", "ticker", sec.Ticker, "error", err)
		} else {
			slog.Info("quote actual ingerido", "ticker", sec.Ticker)
		}
		succeeded++
	}
	slog.Info("job prices terminado", "exitosos", succeeded, "total", len(securities))
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

// resolveSecurities turns -tickers into securities rows; an empty flag means
// every active security in the catalog.
func resolveSecurities(ctx context.Context, pool *pgxpool.Pool, tickers string) ([]storage.Security, error) {
	if strings.TrimSpace(tickers) != "" {
		var out []storage.Security
		for _, t := range splitCSV(tickers) {
			sec, err := storage.GetSecurityByTicker(ctx, pool, strings.ToUpper(t))
			if err != nil {
				return nil, fmt.Errorf("security %q: %w", t, err)
			}
			out = append(out, *sec)
		}
		return out, nil
	}

	all, err := storage.ListSecurities(ctx, pool, 100000, 0)
	if err != nil {
		return nil, err
	}
	var active []storage.Security
	for _, s := range all {
		if s.Status == "active" {
			active = append(active, s)
		}
	}
	return active, nil
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

// resolveCompany maps a -companies entry (ticker or CIK) to (ticker, CIK).
// Un CIK falla si no es numérico y el catálogo no está disponible.
func resolveCompany(catalog []edgar.CompanyTicker, target string) (string, string, error) {
	if cik, err := edgar.NormalizeCIK(target); err == nil {
		if catalog != nil {
			for _, ct := range catalog {
				if fmt.Sprintf("%010d", ct.CIK) == cik {
					return ct.Ticker, cik, nil
				}
			}
		}
		return cik, cik, nil // CIK directo sin catálogo
	}
	// Entrada no numérica: ticker, requiere catálogo para resolver el CIK.
	if catalog != nil {
		for _, ct := range catalog {
			if ct.Ticker == strings.ToUpper(target) {
				return ct.Ticker, fmt.Sprintf("%010d", ct.CIK), nil
			}
		}
		return "", "", fmt.Errorf("ticker %q no encontrado en el catálogo SEC", target)
	}
	return "", "", fmt.Errorf("ticker %q requiere el catálogo SEC (no disponible)", target)
}

func upsertCatalog(ctx context.Context, pool *pgxpool.Pool, catalog []edgar.CompanyTicker) int {
	n := 0
	for _, ct := range catalog {
		sec := &storage.Security{
			Ticker: ct.Ticker, CIK: fmt.Sprintf("%010d", ct.CIK),
			Name: ct.Title, Type: "stock", Currency: "USD", Status: "active",
		}
		if _, err := storage.UpsertSecurity(ctx, pool, sec); err != nil {
			slog.Warn("upsert de security falló", "ticker", ct.Ticker, "error", err)
			continue
		}
		n++
	}
	return n
}

// dryRunCompany downloads companyfacts and runs the canonicalization in memory.
func dryRunCompany(ctx context.Context, client *edgar.Client, cik string) error {
	payload, err := client.GetCompanyFacts(ctx, cik)
	if err != nil {
		return fmt.Errorf("companyfacts %s: %w", cik, err)
	}
	rep, err := edgar.NormalizeDryRun(payload)
	if err != nil {
		return fmt.Errorf("dry-run %s: %w", cik, err)
	}
	slog.Info("dry-run: canonicalización en memoria",
		"cik", cik, "canonical_facts", rep.CanonicalFacts,
		"derived_facts", rep.DerivedFacts, "periods", rep.Periods, "sample", rep.Sample)
	return nil
}

// ingestCompany downloads companyfacts, stages it (idempotente por CIK) and
// normalizes; nunca aborta el proceso completo en caso de error.
func ingestCompany(ctx context.Context, pool *pgxpool.Pool, client *edgar.Client, ticker, cik string) error {
	payload, err := client.GetCompanyFacts(ctx, cik)
	if err != nil {
		return fmt.Errorf("companyfacts %s: %w", ticker, err)
	}

	row := &storage.EdgarStaging{
		CIK: cik, Ticker: &ticker,
		// M1: el payload de companyfacts es corporativo; el pseudo-accession
		// por CIK hace la ingesta idempotente (clave única de staging).
		Accession:   "companyfacts/" + cik,
		FormType:    "10-K",
		FilingDate:  time.Now().UTC(),
		Payload:     payload,
		PayloadType: edgar.CompanyFactsPayloadType,
	}
	inserted, err := storage.InsertStaging(ctx, pool, row)
	if err != nil {
		return fmt.Errorf("staging %s: %w", ticker, err)
	}

	if inserted != nil {
		if err := edgar.NormalizeStaging(ctx, pool, *inserted); err != nil {
			return fmt.Errorf("normalizar %s (staging %d): %w", ticker, *inserted, err)
		}
	} else {
		// Ya ingerido previamente (misma CIK): reprocesar pendientes. No es un
		// error que no haya nada pendiente: la ingesta es idempotente.
		n, err := edgar.NormalizePending(ctx, pool, 5)
		if err != nil {
			return fmt.Errorf("reprocesar pendientes %s: %w", ticker, err)
		}
		slog.Info("empresa ya ingerida; pendientes reprocesados", "ticker", ticker, "normalizadas", n)
		return nil
	}
	slog.Info("empresa normalizada", "ticker", ticker)
	return nil
}
