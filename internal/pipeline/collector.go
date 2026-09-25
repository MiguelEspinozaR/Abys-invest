package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/collect/edgar"
	"github.com/miky/abys-invest/internal/collect/yahoo"
	"github.com/miky/abys-invest/internal/storage"
)

// secEdgarUserAgentEnv is the environment variable carrying the identifying
// User-Agent required by the SEC EDGAR fair-access policy.
const secEdgarUserAgentEnv = "SEC_EDGAR_USER_AGENT"

// EDGARUserAgent resolves the SEC EDGAR User-Agent: environment first, with a
// dev-only fallback (politics SEC: production must set SEC_EDGAR_USER_AGENT).
func EDGARUserAgent() string {
	if ua := os.Getenv(secEdgarUserAgentEnv); ua != "" {
		return ua
	}
	slog.Warn("SEC_EDGAR_USER_AGENT no definido, usando fallback de desarrollo")
	return "AbysInvest/1.0 (dev)"
}

// RunEdgarJob is the M1 ingestion path (SEC catalog + companyfacts), moved
// verbatim from cmd/collector. `fresh` re-ingests each CIK from scratch:
// pending companyfacts staging rows are deleted before re-inserting so the
// payload is re-normalized with the current dictionary (plan M4c).
//
// Returns the number of companies successfully ingested; a non-nil error means
// no target was processed (the CLI wrapper exits 1, same contract as before).
func RunEdgarJob(ctx context.Context, pool *pgxpool.Pool, companies []string, dryRun bool, ua string) (int, error) {
	return runEdgarJob(ctx, pool, companies, dryRun, ua, false)
}

func runEdgarJob(ctx context.Context, pool *pgxpool.Pool, companies []string, dryRun bool, ua string, fresh bool) (int, error) {
	client, err := edgar.NewClient(ua)
	if err != nil {
		return 0, fmt.Errorf("configuración del cliente EDGAR: %w", err)
	}

	if len(companies) == 0 {
		companies = []string{DefaultCompany}
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

		if fresh {
			if err := ingestCompanyFresh(ctx, pool, client, name, cik); err != nil {
				slog.Error("empresa fallida (continúa)", "ticker", name, "error", err)
				continue
			}
		} else if err := ingestCompany(ctx, pool, client, name, cik); err != nil {
			slog.Error("empresa fallida (continúa)", "ticker", name, "error", err)
			continue
		}
		succeeded++
	}

	if !dryRun && succeeded == 0 {
		slog.Error("ninguna empresa objetivo se procesó con éxito")
		return 0, fmt.Errorf("ninguna empresa objetivo se procesó con éxito")
	}
	return succeeded, nil
}

// RunPricesJob ingests Yahoo OHLCV history + daily quote for the selected
// tickers. Individual ticker errors are logged and never abort the job.
// Returns the number of securities with prices ingested.
func RunPricesJob(ctx context.Context, pool *pgxpool.Pool, tickers string) (int, error) {
	yc := yahoo.NewClient()
	if ua := os.Getenv(secEdgarUserAgentEnv); ua != "" {
		yc = yahoo.NewClient(yahoo.WithUserAgent(ua))
	}

	securities, err := resolveSecurities(ctx, pool, tickers)
	if err != nil {
		return 0, fmt.Errorf("resolución de tickers para precios: %w", err)
	}
	if len(securities) == 0 {
		slog.Warn("sin securities activas para ingesta de precios")
		return 0, nil
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
	return succeeded, nil
}

// RunSectorJob enriches sector/industry of the selected tickers (Yahoo
// quoteSummary primary, Finviz fallback; job sector de M3). Returns the number
// of updated securities.
func RunSectorJob(ctx context.Context, pool *pgxpool.Pool, tickers string) (int, error) {
	securities, err := resolveSecurities(ctx, pool, tickers)
	if err != nil {
		return 0, fmt.Errorf("resolución de tickers para sector: %w", err)
	}
	if len(securities) == 0 {
		slog.Warn("sin securities activas para enriquecer sector")
		return 0, nil
	}
	var ts []string
	for _, s := range securities {
		ts = append(ts, s.Ticker)
	}
	updated, err := yahoo.EnrichSectors(ctx, pool, ts)
	if err != nil {
		return 0, fmt.Errorf("enriquecimiento de sector falló: %w", err)
	}
	slog.Info("job sector terminado", "exitosos", updated, "total", len(ts))
	return updated, nil
}

// resolveSecurities turns a tickers CSV into securities rows; an empty value
// means every active security in the catalog.
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

// ingestCompanyFresh re-ingests a company from scratch: pending staging of the
// CIK is deleted before downloading and re-inserting (re-ingesta fresca del
// plan M4c, garantiza que el mapeo corregido se aplique al payload completo).
func ingestCompanyFresh(ctx context.Context, pool *pgxpool.Pool, client *edgar.Client, ticker, cik string) error {
	n, err := storage.DeleteStagingByCIK(ctx, pool, cik)
	if err != nil {
		return fmt.Errorf("reset staging %s: %w", ticker, err)
	}
	if n > 0 {
		slog.Info("staging previo eliminado (re-ingesta fresca)", "ticker", ticker, "cik", cik, "filas", n)
	}
	return ingestCompany(ctx, pool, client, ticker, cik)
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
