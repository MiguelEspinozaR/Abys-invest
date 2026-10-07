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
// Re-ingesta de fundamentals (plan M6c-T1 W3): `make run-collector` corre EDGAR
// con fresh=false y `InsertStaging` es idempotente por (cik, source), así que
// una empresa YA ingerida nunca vuelve a pasar por el canonizador. Tras cambiar
// el catálogo XBRL (`internal/collect/edgar/concepts.go`) la re-ingesta es
// OBLIGATORIA, y ése es el motivo de los dos flags siguientes:
//
//	go run ./cmd/collector -job universe -universe staged   # lista el universo (sólo lectura)
//	go run ./cmd/collector -job edgar -fresh -universe staged
//
// o, con lista explícita (que tiene prioridad sobre `-universe`):
//
//	go run ./cmd/collector -job edgar -fresh -companies NVDA,WMT
//
// Thin wrapper (plan M4c): la lógica de los jobs edgar/prices/sector vive en
// internal/pipeline; aquí solo se parsean flags, se conecta la BD y se
// delega. El job macro sigue residiendo en este paquete.
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

	"github.com/miky/abys-invest/internal/collect/macro"
	"github.com/miky/abys-invest/internal/pipeline"
	"github.com/miky/abys-invest/internal/storage"
)

const (
	// userAgentDefault is only a dev fallback; production must set
	// SEC_EDGAR_USER_AGENT (politics SEC).
	userAgentDefault = "AbysInvest/1.0 (dev)"
	migrationsDir    = "migrations"

	jobEdgar    = "edgar"
	jobPrices   = "prices"
	jobMacro    = "macro"
	jobSector   = "sector"
	jobAll      = "all"
	jobUniverse = "universe"

	// universeStaged is the only supported universe selector: the companies whose
	// companyfacts payload is already normalized in edgar_staging (the ones a
	// catalog change can actually affect). Never `securities`: that table holds
	// the whole SEC catalog (10k+ rows) of which ~40 have ever been ingested.
	universeStaged = "staged"
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

// edgarFlags is the validated, normalized shape of the EDGAR-related flags. It
// exists so the rules ("-fresh sólo con edgar|all", "-companies gana sobre
// -universe", ...) are testable as a pure function instead of being spread over
// the branches of main().
type edgarFlags struct {
	job       string
	companies []string // tal cual pasó -companies (vacío = el operador no lo pasó)
	universe  string   // selector de -universe ("" = ninguno)
	fresh     bool
	dryRun    bool
}

// validateEdgarFlags rejects flag combinations that would silently do something
// other than what the operator asked for. It is deliberately LOUD: a re-ingest
// that quietly ran on the wrong universe (or never ran at all) is invisible in
// the data, which is the failure mode W3 exists to prevent.
//
// Unknown job and unknown universe selector are NOT checked here (they have
// their own messages and exit code 2 in main).
func validateEdgarFlags(f edgarFlags) error {
	switch f.job {
	case jobEdgar, jobAll, jobUniverse:
	default:
		// prices/macro/sector: los otros jobs no tocan el canonizador, así que
		// -fresh/-universe no tendrían ningún efecto y callar sería mentir.
		if f.fresh || f.universe != "" {
			return fmt.Errorf("-fresh/-universe sólo aplican a -job edgar|all (o a -job universe), no a %q", f.job)
		}
	}

	if f.fresh && f.dryRun {
		return fmt.Errorf("-fresh y -dry-run son incompatibles: -dry-run no escribe nada, así que no hay staging que borrar ni payload que re-canonizar")
	}

	if f.job == jobUniverse {
		if f.fresh {
			return fmt.Errorf("-fresh no aplica a -job universe: ese job sólo LISTA el universo, no ingesta")
		}
		if f.dryRun {
			return fmt.Errorf("-dry-run no aplica a -job universe: ese job sólo LISTA el universo, no canoniza en memoria")
		}
		if f.universe == "" {
			return fmt.Errorf("-job universe requiere -universe %s (dice explícitamente de dónde sale la lista)", universeStaged)
		}
	}

	// Un selector de universo se resuelve contra la BD, y en dry-run el pipeline
	// abre la BD sólo si se le pasa (job edgar + -dry-run corre sin BD para no
	// escribir ni migrar). Sin esta regla, `-dry-run -universe staged` fallaría
	// más tarde con un error de DSN, que no dice nada de la causa real.
	if f.dryRun && f.universe != "" {
		return fmt.Errorf("-dry-run y -universe son incompatibles: el selector %q se resuelve contra la BD y el dry-run de edgar corre sin BD", f.universe)
	}

	if f.universe != "" && len(f.companies) > 0 {
		return fmt.Errorf("-companies y -universe son excluyentes: con -companies el universo es el explícito (para el universo por defecto, quita -companies)")
	}

	return nil
}

// edgarTargets resuelve la lista de empresas del job edgar: la explícita
// (-companies) si la hay, o la del selector -universe. Sin ninguna de las dos
// devuelve nil y es `runEdgarJob` quien aplica su DefaultCompany (contrato
// histórico intacto).
func edgarTargets(f edgarFlags, ciks []string) []string {
	if len(f.companies) > 0 {
		return f.companies
	}
	return ciks
}

func main() {
	var companies stringList
	var dryRun bool
	var fresh bool
	var job string
	var tickers string
	var universe string
	var macroSeries string
	var years int

	flag.Var(&companies, "companies", "tickers/CIKs a ingerir por EDGAR (CSV); por defecto: "+pipeline.DefaultCompany)
	flag.BoolVar(&dryRun, "dry-run", false, "job edgar: descarga y canoniza en memoria sin escribir en la BD")
	flag.BoolVar(&fresh, "fresh", false, "job edgar/all: RE-INGESTA — borra el staging por CIK y reinserta, de modo que el payload vuelve a pasar por el canonizador con el catálogo vigente (obligatorio tras cambiar el catálogo XBRL; incompatible con -dry-run)")
	flag.StringVar(&job, "job", jobAll, "job a ejecutar: edgar | prices | macro | sector | universe | all")
	flag.StringVar(&tickers, "tickers", "", "tickers para precios/analytics (CSV); vacío = todos los securities activos en BD")
	flag.StringVar(&universe, "universe", "", "job edgar/all: empresas a ingerir cuando NO se pasa -companies. Valores: "+universeStaged+" (empresas con companyfacts ya normalizado en edgar_staging; NO el catálogo de securities). Excluyente con -companies")
	flag.StringVar(&macroSeries, "macro-series", "CPI", "series macro a ingerir (CSV)")
	flag.IntVar(&years, "years", 5, "años de histórico macro (startYear = año actual - years)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	switch job {
	case jobEdgar, jobPrices, jobMacro, jobSector, jobUniverse, jobAll:
	default:
		slog.Error("job desconocido", "job", job, "esperado", "edgar|prices|macro|sector|universe|all")
		os.Exit(2)
	}

	eflags := edgarFlags{
		job:       job,
		companies: companies,
		universe:  universe,
		fresh:     fresh,
		dryRun:    dryRun,
	}
	if err := validateEdgarFlags(eflags); err != nil {
		slog.Error("flags incompatibles", "error", err)
		os.Exit(2)
	}

	// El User-Agent sólo hace falta para los jobs que hacen red (EDGAR/Yahoo);
	// `-job universe` no habla con nadie, así que no se avisa de que falta.
	ua := os.Getenv("SEC_EDGAR_USER_AGENT")
	if ua == "" {
		ua = userAgentDefault
		if job != jobUniverse {
			slog.Warn("SEC_EDGAR_USER_AGENT no definido, usando fallback de desarrollo")
		}
	}

	var pool *pgxpool.Pool
	needDB := job != jobEdgar || !dryRun
	// `-job universe` es de SOLO LECTURA (por eso existe: resolver el universo
	// antes de tocar nada), así que no aplica migraciones.
	readOnly := job == jobUniverse
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
		if !readOnly {
			if err := storage.RunMigrations(ctx, pool, migrationsDir); err != nil {
				slog.Error("aplicación de migraciones", "error", err)
				os.Exit(1)
			}
			slog.Info("base lista, migraciones aplicadas")
		}
	}

	switch job {
	case jobUniverse:
		ciks, err := resolveUniverse(ctx, pool, universe)
		if err != nil {
			slog.Error("resolución del universo", "selector", universe, "error", err)
			os.Exit(1)
		}
		if len(ciks) == 0 {
			slog.Error("universo vacío: no hay empresas con companyfacts normalizado en edgar_staging",
				"selector", universe, "pista", "ingerí primero con -job edgar, o usa -companies <lista>")
			os.Exit(1)
		}
		// stdout y sólo stdout: es la salida que el Makefile captura para
		// pasársela tal cual a -companies.
		fmt.Println(strings.Join(ciks, ","))

	case jobEdgar:
		targets, err := resolveEdgarTargets(ctx, pool, eflags)
		if err != nil {
			slog.Error("resolución de empresas del job edgar", "error", err)
			os.Exit(1)
		}
		if _, err := pipeline.RunEdgarJobFresh(ctx, pool, targets, dryRun, ua, fresh); err != nil {
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
		targets, err := resolveEdgarTargets(ctx, pool, eflags)
		if err != nil {
			slog.Error("resolución de empresas del job edgar", "error", err)
			os.Exit(1)
		}
		if _, err := pipeline.RunEdgarJobFresh(ctx, pool, targets, dryRun, ua, fresh); err != nil {
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

// resolveEdgarTargets devuelve la lista final de empresas del job edgar: la
// explícita, la del selector -universe, o nil (DefaultCompany del pipeline).
func resolveEdgarTargets(ctx context.Context, pool *pgxpool.Pool, f edgarFlags) ([]string, error) {
	if f.universe == "" {
		return edgarTargets(f, nil), nil
	}
	ciks, err := resolveUniverse(ctx, pool, f.universe)
	if err != nil {
		return nil, err
	}
	targets := edgarTargets(f, ciks)
	slog.Info("universo de ingesta resuelto", "selector", f.universe, "empresas", len(targets), "fresh", f.fresh)
	return targets, nil
}

// resolveUniverse traduce el valor de -universe a la lista de empresas. Hoy sólo
// existe `staged`; un selector desconocido es un error, no un fallback
// silencioso (un universo distinto del que el operador cree es peor que un
// error).
func resolveUniverse(ctx context.Context, pool *pgxpool.Pool, selector string) ([]string, error) {
	switch selector {
	case universeStaged:
		if pool == nil {
			return nil, fmt.Errorf("el selector %q necesita DATABASE_URL", universeStaged)
		}
		ciks, err := storage.StagedNormalizedCIKs(ctx, pool)
		if err != nil {
			return nil, err
		}
		slog.Info("universo por defecto", "selector", universeStaged,
			"descripcion", "empresas con companyfacts normalizado en edgar_staging (no el catálogo de securities)", "empresas", len(ciks))
		return ciks, nil
	default:
		return nil, fmt.Errorf("selector de universo %q desconocido (soportado: %s)", selector, universeStaged)
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
