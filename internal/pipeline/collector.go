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

// RunEdgarJobFresh is RunEdgarJob with `fresh` exposed: each target CIK has its
// `company_facts` staging deleted before re-inserting, so the payload goes
// through `dedupeCanonical` AGAIN with the dictionary in force (W3).
//
// This is the only supported way to make an ALREADY INGESTED company pick up a
// change in `conceptMap`: `InsertStaging` is idempotent by
// (accession, form_type, payload_type) — that is, by (cik, source) for the
// pseudo-accession `companyfacts/<cik>` — so without the delete the insert is a
// no-op and the canonicalization never runs again (fresh=false is exactly what
// `make run-collector` does).
//
// dryRun wins over fresh (dry-run never writes, so there is nothing to reset).
func RunEdgarJobFresh(ctx context.Context, pool *pgxpool.Pool, companies []string, dryRun bool, ua string, fresh bool) (int, error) {
	if dryRun {
		fresh = false
	}
	if fresh {
		slog.Info("job edgar en modo RE-INGESTA (-fresh): el staging por CIK se borra y el payload vuelve a pasar por el canonizador",
			"empresas", len(companies))
	}
	return runEdgarJob(ctx, pool, companies, dryRun, ua, fresh)
}

func runEdgarJob(ctx context.Context, pool *pgxpool.Pool, companies []string, dryRun bool, ua string, fresh bool) (int, error) {
	client, err := edgar.NewClient(ua)
	if err != nil {
		return 0, fmt.Errorf("configuración del cliente EDGAR: %w", err)
	}
	return runEdgarJobWithClient(ctx, pool, client, companies, dryRun, fresh)
}

// runEdgarJobWithClient es runEdgarJob con el cliente EDGAR inyectado. Existe
// para que los tests de integración puedan ejecutar el WIRING REAL (descarga
// del catálogo -> resolución CIK->clase -> ingesta) contra un httptest, sin
// reescribir a mano la resolución. El camino de producción siempre construye el
// cliente con edgar.NewClient en runEdgarJob, así que este seam no cambia el
// comportamiento externo; sólo hace testeable que la clase canónica de un CIK
// multi-clase sale de `runEdgarJob` y no del test (review P2-1 de W3).
func runEdgarJobWithClient(ctx context.Context, pool *pgxpool.Pool, client *edgar.Client, companies []string, dryRun bool, fresh bool) (int, error) {
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
		cik, candidates, err := resolveCompanyCandidates(catalog, target)
		if err != nil {
			slog.Error("empresa no resoluble (continúa)", "entrada", target, "error", err)
			continue
		}
		name := cik
		if len(candidates) > 0 {
			// Preferencia multiclase con continuidad: la clase con datos (precios
			// primero, fundamentos después) gana; sin datos, la primera del
			// catálogo ordenado. Sin BD (dry-run) el lookup es nil -> fallback
			// determinista puro.
			var dataFor classLookup
			if pool != nil {
				dataFor, err = loadCompanyClassData(ctx, pool, cik)
				if err != nil {
					slog.Warn("estado de datos por clase no consultable; se usa el fallback determinista del catálogo",
						"entrada", target, "cik", cik, "error", err)
					dataFor = nil
				}
			}
			name = pickPreferredClass(candidates, dataFor).Ticker
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

// RunSectorJob enriches the reference data of the selected tickers (Yahoo
// quoteSummary primary for sector/industry AND beta, Finviz fallback for
// sector/industry only; job sector de M3 + beta de M6a, plan D17). It is the
// ONLY path with network access that touches the catalog, and it is also where
// the observed beta is refreshed: adding the module to the existing
// quoteSummary call costs no extra request. Returns the number of updated
// securities.
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
	updated, err := yahoo.EnrichReferenceData(ctx, pool, ts)
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
//
// Para un CIK multiclase (un emisor con varias clases, p. ej. JPM y JPM-PM
// comparten CIK) la clase elegida importa: `staging.ticker` acaba en
// `storage.UpsertSecurity(Ticker, CIK)` y los `fundamentals` se escriben bajo
// ese `security_id`. Elegir la "primera coincidencia" de un catálogo recién
// parseado era ALEATORIO (el rango de un map varía en cada corrida de Go) y una
// re-ingesta (`-fresh`) podía escribir el MISMO payload bajo un security_id
// distinto cada pasada (P0 CA-9 de W3). La resolución es ahora determinista y
// con continuidad: preferencia por datos de mercado, luego fundamentals, luego
// fallback por orden de catálogo (ver pickPreferredClass).
//
// dataFor es el lookup inyectado ticker -> (tiene precios, tiene fundamentals);
// nil equivale a "sin datos" (fallback determinista puro, testeable sin BD).
func resolveCompany(catalog []edgar.CompanyTicker, target string, dataFor classLookup) (string, string, error) {
	cik, candidates, err := resolveCompanyCandidates(catalog, target)
	if err != nil {
		return "", "", err
	}
	if len(candidates) == 0 {
		return cik, cik, nil // CIK directo sin catálogo (o sin clases mapeadas)
	}
	return pickPreferredClass(candidates, dataFor).Ticker, cik, nil
}

// companyDataFlags describe la disponibilidad de datos de una clase: filas en
// daily_prices (datos de mercado) y/o filas en fundamentals.
type companyDataFlags struct {
	HasPrices       bool
	HasFundamentals bool
}

// classLookup reports, per ticker, whether its security already has market
// data (daily_prices) and/or fundamentals in the database. Se inyecta a la
// resolución de clases para que `resolveCompany`/`pickPreferredClass` sean
// puramente testeables sin BD.
type classLookup func(ticker string) (hasPrices, hasFundamentals bool)

// resolveCompanyCandidates maps a -companies entry (ticker or CIK) to the CIK
// y a TODOS los candidatos del catálogo con ese CIK/ticker, en el orden
// determinista del catálogo (CIK asc, luego ticker asc). Es pura (sin BD).
//
//   - entrada numérica (CIK): devuelve el CIK normalizado y todas sus clases;
//     si el catálogo no está disponible o no menciona el CIK, candidatos vacíos
//     (el caller continúa por CIK directo, mismo contrato que antes).
//   - entrada no numérica (ticker): requiere catálogo; devuelve el CIK y el
//     único candidato que casa por ticker.
func resolveCompanyCandidates(catalog []edgar.CompanyTicker, target string) (string, []edgar.CompanyTicker, error) {
	if cik, err := edgar.NormalizeCIK(target); err == nil {
		if catalog == nil {
			return cik, nil, nil
		}
		var out []edgar.CompanyTicker
		for _, ct := range catalog {
			if fmt.Sprintf("%010d", ct.CIK) == cik {
				out = append(out, ct)
			}
		}
		return cik, out, nil
	}
	// Entrada no numérica: ticker, requiere catálogo para resolver el CIK.
	if catalog == nil {
		return "", nil, fmt.Errorf("ticker %q requiere el catálogo SEC (no disponible)", target)
	}
	for _, ct := range catalog {
		if ct.Ticker == strings.ToUpper(target) {
			return fmt.Sprintf("%010d", ct.CIK), []edgar.CompanyTicker{ct}, nil
		}
	}
	return "", nil, fmt.Errorf("ticker %q no encontrado en el catálogo SEC", target)
}

// pickPreferredClass elige la clase canónica entre los candidatos de un CIK.
// Resolución determinista y con continuidad, CERO aleatoriedad:
//
//  1. la clase que ya tiene datos de mercado (filas en daily_prices);
//  2. si ninguna la tiene, la clase que ya tiene fundamentals;
//  3. si ninguna la tiene (BD nueva / sin lookup), la primera del catálogo ya
//     ordenado (fallback determinista = menor ticker del CIK).
//
// Un empate dentro de un mismo nivel se deshace por ticker ASC. dataFor == nil
// equivale a "ninguna clase tiene datos" (fallback puro). candidates debería
// tener al menos un elemento; si está vacío devuelve el zero value.
func pickPreferredClass(candidates []edgar.CompanyTicker, dataFor classLookup) edgar.CompanyTicker {
	if len(candidates) == 0 {
		return edgar.CompanyTicker{}
	}
	best := candidates[0]
	bestLevel := classLevel(best, dataFor)
	for _, c := range candidates[1:] {
		l := classLevel(c, dataFor)
		if l < bestLevel || (l == bestLevel && c.Ticker < best.Ticker) {
			best, bestLevel = c, l
		}
	}
	return best
}

// classLevel ranquea un candidato: 0 = tiene precios, 1 = tiene fundamentals
// (sin precios), 2 = sin datos. Menor = preferida.
func classLevel(ct edgar.CompanyTicker, dataFor classLookup) int {
	if dataFor == nil {
		return 2
	}
	prices, funds := dataFor(ct.Ticker)
	switch {
	case prices:
		return 0
	case funds:
		return 1
	default:
		return 2
	}
}

// loadCompanyClassData construye un classLookup para todas las clases de un CIK
// (las clases de un mismo emisor comparten CIK corporativo) con una sola
// consulta de SOLO LECTURA: ticker -> (tiene precios, tiene fundamentals). Una
// clase sin fila en securities equivale a (false, false). Devuelve nil si el
// CIK aún no tiene securities catalogados (fallback determinista).
func loadCompanyClassData(ctx context.Context, pool *pgxpool.Pool, cik string) (classLookup, error) {
	rows, err := pool.Query(ctx, `
SELECT s.ticker,
       EXISTS (SELECT 1 FROM daily_prices dp WHERE dp.security_id = s.id) AS has_prices,
       EXISTS (SELECT 1 FROM fundamentals  f  WHERE f.security_id = s.id) AS has_fundamentals
FROM securities s
WHERE s.cik = $1`, cik)
	if err != nil {
		return nil, fmt.Errorf("lookup de datos por clase %s: %w", cik, err)
	}
	defer rows.Close()

	flags := map[string]companyDataFlags{}
	for rows.Next() {
		var ticker string
		var f companyDataFlags
		if err := rows.Scan(&ticker, &f.HasPrices, &f.HasFundamentals); err != nil {
			return nil, fmt.Errorf("scan del lookup por clase %s: %w", cik, err)
		}
		flags[ticker] = f
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterar el lookup por clase %s: %w", cik, err)
	}
	if len(flags) == 0 {
		return nil, nil
	}
	return func(ticker string) (bool, bool) {
		f := flags[ticker]
		return f.HasPrices, f.HasFundamentals
	}, nil
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
