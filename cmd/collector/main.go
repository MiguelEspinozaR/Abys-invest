// Command collector is the M1 ingestion worker: SEC EDGAR catalog, company
// facts and normalization into the Abys-Invest PostgreSQL store.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/collect/edgar"
	"github.com/miky/abys-invest/internal/storage"
)

const (
	// userAgentDefault is only a dev fallback; production must set
	// SEC_EDGAR_USER_AGENT (politics SEC).
	userAgentDefault = "AbysInvest/1.0 (dev)"
	migrationsDir    = "migrations"
	defaultCompany   = "AAPL"
)

// stringList implements flag.Var for CSV -companies.
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
	flag.Var(&companies, "companies", "tickers/CIKs a ingerir (CSV); por defecto: "+defaultCompany)
	flag.BoolVar(&dryRun, "dry-run", false, "descarga y canoniza en memoria sin escribir en la BD")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	ua := os.Getenv("SEC_EDGAR_USER_AGENT")
	if ua == "" {
		ua = userAgentDefault
		slog.Warn("SEC_EDGAR_USER_AGENT no definido, usando fallback de desarrollo")
	}
	client, err := edgar.NewClient(ua)
	if err != nil {
		slog.Error("configuración del cliente EDGAR", "error", err)
		os.Exit(1)
	}

	if len(companies) == 0 {
		companies = stringList{defaultCompany}
	}

	var pool *pgxpool.Pool
	if !dryRun {
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			slog.Error("DATABASE_URL requerido (o use -dry-run)")
			os.Exit(1)
		}
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
