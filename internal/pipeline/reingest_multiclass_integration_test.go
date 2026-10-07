//go:build integration

package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/collect/edgar"
	"github.com/miky/abys-invest/internal/storage"
)

// TestM6cT1FreshResolvesStableClassForMulticlassCIKs es la prueba E2E del fix
// P0 CA-9/W3 sobre abys_test (requisito 6): dos re-ingestas `-fresh`
// consecutivas de los 6 CIKs multiclase reales resuelven la MISMA clase y
// escriben fundamentals bajo el MISMO security_id; la segunda pasada no crea
// filas nuevas (idempotencia con continuidad).
//
// El estado imita dev (2026-10-06): cada CIK tiene una clase canónica CON
// precios (filas en daily_prices) y otras clases sin precios. Para endurecer
// la preferencia se siembra además una fila de fundamentals en la clase
// alternativa — el nivel 1 (precios) debe mandar sobre el nivel 2
// (fundamentals), que es exactamente el caso JPM/JPM-PM, BAC/BAC-PS o
// GOOGL/GOOGN de dev (las alternativas recibieron el trío dañado de hoy y las
// canónicas no).
//
// Las dos pasadas van por el WIRING REAL de `runEdgarJob` (seam
// `runEdgarJobWithClient`): el job descarga el catálogo por HTTP, lo persiste,
// resuelve el CIK->clase canónica y ejecuta `-fresh`. El test NO fija el ticker
// (review P2-1): lo que se prueba es que la clase canónica sale del código de
// producción, no de una constante del test.
//
// Sin TRUNCATE de tablas compartidas: todo va acotado a los 6 CIKs y se limpia
// al final (DELETE de staging y de securities por CIK; el CASCADE cubre
// fundamentals/daily_prices). Sin red: un único httptest sirve el catálogo
// company_tickers.json y el payload companyfacts (un hecho canónico Revenues)
// por CIK.
func TestM6cT1FreshResolvesStableClassForMulticlassCIKs(t *testing.T) {
	pool := integPool
	if pool == nil {
		t.Skip("DATABASE_URL no configurado o BD no alcanzable; saltando E2E multiclase")
	}
	ctx := context.Background()

	fixtures := []struct {
		cik        string
		canonical  string // clase con precios (continuidad del producto)
		alt        string // clase alternativa sin precios
		fundsOnAlt bool   // sembrar fundamentals en la alternativa (nivel 2)
	}{
		{cik: "0000019617", canonical: "JPM", alt: "JPM-PM", fundsOnAlt: true},
		{cik: "0000070858", canonical: "BAC", alt: "BAC-PS", fundsOnAlt: true},
		{cik: "0001045609", canonical: "PLD", alt: "PLDGP", fundsOnAlt: true},
		{cik: "0001063761", canonical: "SPG", alt: "SPG-PJ", fundsOnAlt: true},
		{cik: "0001067983", canonical: "BRK-B", alt: "BRK-A", fundsOnAlt: true},
		{cik: "0001652044", canonical: "GOOGL", alt: "GOOGN", fundsOnAlt: true},
	}

	ciks := make([]string, 0, len(fixtures))
	for _, fx := range fixtures {
		ciks = append(ciks, fx.cik)
	}

	cleanup := func() {
		// daily_prices/fundamentals referencian securities con FK sin CASCADE:
		// borrar en orden (precios, fundamentals, staging, securities).
		if _, err := pool.Exec(ctx, `
DELETE FROM daily_prices WHERE security_id IN (SELECT id FROM securities WHERE cik = ANY($1))`, ciks); err != nil {
			t.Logf("cleanup daily_prices: %v", err)
		}
		if _, err := pool.Exec(ctx, `
DELETE FROM fundamentals WHERE security_id IN (SELECT id FROM securities WHERE cik = ANY($1))`, ciks); err != nil {
			t.Logf("cleanup fundamentals: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM edgar_staging WHERE cik = ANY($1)`, ciks); err != nil {
			t.Logf("cleanup staging: %v", err)
		}
		if _, err := pool.Exec(ctx,
			`DELETE FROM securities WHERE cik = ANY($1)`, ciks); err != nil {
			t.Logf("cleanup securities: %v", err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	// --- Catálogo real (36 clases) + payloads EDGAR sintéticos --------------
	// El wiring REAL de runEdgarJob descarga el catálogo por HTTP: el httptest
	// sirve company_tickers.json (multiclassCatalogPayload) y companyfacts.
	catalogPayload := multiclassCatalogPayload(t)

	payloads := map[string][]byte{}
	for _, fx := range fixtures {
		payloads[fx.cik] = multiclassCompanyFactsPayload(t, fx.cik)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path.Base(r.URL.Path) == "company_tickers.json" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(catalogPayload)
			return
		}
		cik := strings.TrimSuffix(strings.TrimPrefix(path.Base(r.URL.Path), "CIK"), ".json")
		if payload, ok := payloads[cik]; ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(payload)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client, err := edgar.NewClient("AbysInvest/test (contact@example.test)",
		edgar.WithBaseURL(srv.URL),
		edgar.WithRetryBase(1),
	)
	if err != nil {
		t.Fatalf("edgar.NewClient: %v", err)
	}

	// --- Siembra: securities + precios de la clase canónica + fundamentals --
	//     de la clase alternativa (para probar que el nivel 1 manda al 2).
	//     El job REAL (upsertCatalog) añadirá las demás clases del catálogo.
	canonicalIDByCIK := map[string]int64{} // cik -> security_id de la clase canónica
	altIDByCIK := map[string]int64{}       // cik -> security_id de la clase alternativa
	for _, fx := range fixtures {
		canonical, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
			Ticker: fx.canonical, CIK: fx.cik, Name: "Canonical " + fx.canonical,
			Type: "stock", Currency: "USD", Status: "active",
		})
		if err != nil {
			t.Fatalf("seed %s: %v", fx.canonical, err)
		}
		alt, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
			Ticker: fx.alt, CIK: fx.cik, Name: "Alt " + fx.alt,
			Type: "stock", Currency: "USD", Status: "active",
		})
		if err != nil {
			t.Fatalf("seed %s: %v", fx.alt, err)
		}
		canonicalIDByCIK[fx.cik] = canonical.ID
		altIDByCIK[fx.cik] = alt.ID
		if _, err := pool.Exec(ctx, `
INSERT INTO daily_prices (security_id, date, open, high, low, close, adjusted_close, volume, source)
SELECT $1, d::date, 100, 105, 99, 101, 101, 100000, 'test'
FROM generate_series('2026-09-25'::date, '2026-09-28'::date, interval '1 day') d`,
			canonical.ID); err != nil {
			t.Fatalf("seed precios de %s: %v", fx.canonical, err)
		}
		if fx.fundsOnAlt {
			seedFundamentalsRow(t, ctx, pool, alt.ID, "net_earnings")
		}
	}

	// --- Dos pasadas por el WIRING REAL de runEdgarJob ----------------------
	// runEdgarJobWithClient es el cuerpo de runEdgarJob con el cliente EDGAR
	// inyectado: descarga y persiste el catálogo, resuelve la clase canónica de
	// cada CIK con loadCompanyClassData + pickPreferredClass y ejecuta -fresh.
	// El test NO fija el ticker: si la resolución de producción fuera inestable
	// o eligiera otra clase, las aserciones de abajo fallan.
	runOnce := func(pass int) {
		n, err := runEdgarJobWithClient(ctx, pool, client, ciks, false, true)
		if err != nil {
			t.Fatalf("pasada %d por runEdgarJob: %v", pass, err)
		}
		if n != len(fixtures) {
			t.Fatalf("pasada %d: se procesaron %d empresas, se esperaban %d", pass, n, len(fixtures))
		}
	}

	// Pasada 1: el job resuelve la clase y escribe fundamentals bajo ella.
	runOnce(1)
	for _, fx := range fixtures {
		canonicalID := canonicalIDByCIK[fx.cik]
		altID := altIDByCIK[fx.cik]
		if canonicalID == 0 || altID == 0 || canonicalID == altID {
			t.Fatalf("%s: ids de security inválidos (canonical=%d alt=%d)", fx.cik, canonicalID, altID)
		}
		assertStaging(t, pool, fx.cik, 1)
		assertRevenuesUnderSecurity(t, ctx, pool, fx.cik, canonicalID)
		if n := countRevenuesRowsForSecurity(t, ctx, pool, altID); n != 0 {
			t.Fatalf("%s: la pasada 1 escribió revenues (n=%d) bajo la clase ALTERNATIVA %s",
				fx.cik, n, fx.alt)
		}
	}

	beforeByCIK := map[string]int{}
	for _, fx := range fixtures {
		beforeByCIK[fx.cik] = countFundamentalsForCIK(t, ctx, pool, fx.cik)
	}

	// Pasada 2: MISMA resolución (determinista) y MISMA security_id.
	runOnce(2)
	for _, fx := range fixtures {
		assertStaging(t, pool, fx.cik, 1)
		canonicalID := canonicalIDByCIK[fx.cik]
		assertRevenuesUnderSecurity(t, ctx, pool, fx.cik, canonicalID) // MISMA security_id
		after := countFundamentalsForCIK(t, ctx, pool, fx.cik)
		if beforeByCIK[fx.cik] != after {
			t.Fatalf("%s: la pasada 2 creó %d filas nuevas en fundamentals (antes=%d después=%d)",
				fx.cik, after-beforeByCIK[fx.cik], beforeByCIK[fx.cik], after)
		}
		// La alternativa conserva SÓLO la fila sembrada: la preferencia por el
		// nivel 1 (precios) protege la continuidad histórica.
		if altID := altIDByCIK[fx.cik]; altID != 0 {
			if n := countFundamentalsRowsForSecurity(t, ctx, pool, altID); n != 1 {
				t.Fatalf("%s: la alternativa %s debía conservar SOLO la fila sembrada, tiene %d",
					fx.cik, fx.alt, n)
			}
		}
		t.Logf("%s -> %s (security_id %d): 2 pasadas por runEdgarJob, mismas filas y mismo security_id",
			fx.cik, fx.canonical, canonicalID)
	}
}

// multiclassCompanyFactsPayload construye un companyfacts mínimo (formato SEC
// real: concepto -> units -> [valores]) con UN hecho canónico (Revenues,
// FY2024) para que la normalización escriba al menos una fila de fundamentals
// bajo la security que resuelva el job.
func multiclassCompanyFactsPayload(t *testing.T, cik string) []byte {
	t.Helper()
	cikNum := strings.TrimLeft(cik, "0")
	if cikNum == "" {
		cikNum = "0"
	}
	return []byte(fmt.Sprintf(
		`{"cik":%s,"entityName":"Multiclass %s","facts":{"us-gaap":{"Revenues":{"label":"Revenues","units":{"USD":[{"start":"2024-01-01","end":"2024-12-31","val":123456789,"accn":"%s-24-000007","fy":2024,"fp":"FY","form":"10-K","filed":"2024-12-31","frame":"CY2024"}]}}}}}`,
		cikNum, cik, cik))
}

// --- helpers acotados al CIK (mismo patrón que reingest_integration_test.go:
//     sin TRUNCATE, DELETE scoped por CIK y consultas de sólo lectura).

// assertRevenuesUnderSecurity exige que el hecho canónico del payload quede
// bajo EXACTAMENTE wantID (y que exista).
func assertRevenuesUnderSecurity(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cik string, wantID int64) {
	t.Helper()
	var n int
	var distinct int
	if err := pool.QueryRow(ctx, `
SELECT count(*), count(DISTINCT f.security_id)
FROM fundamentals f JOIN securities s ON s.id = f.security_id
WHERE s.cik = $1 AND f.concept = 'revenues'`, cik).Scan(&n, &distinct); err != nil {
		t.Fatalf("%s: revenue rows query: %v", cik, err)
	}
	if n == 0 {
		t.Fatalf("%s: el hecho canónico Revenues no se escribió en ninguna clase", cik)
	}
	if distinct != 1 {
		t.Fatalf("%s: Revenues bajo %d security_id distintos (se esperaba 1): la re-ingesta duplica clases", cik, distinct)
	}
	var gotID int64
	if err := pool.QueryRow(ctx, `
SELECT DISTINCT f.security_id
FROM fundamentals f JOIN securities s ON s.id = f.security_id
WHERE s.cik = $1 AND f.concept = 'revenues'`, cik).Scan(&gotID); err != nil {
		t.Fatalf("%s: security_id de revenues: %v", cik, err)
	}
	if gotID != wantID {
		t.Fatalf("%s: Revenues bajo security_id %d, se esperaba %d (la re-ingesta cambió de clase)",
			cik, gotID, wantID)
	}
}

func countRevenuesRowsForSecurity(t *testing.T, ctx context.Context, pool *pgxpool.Pool, securityID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM fundamentals WHERE security_id = $1 AND concept = 'revenues'`, securityID).Scan(&n); err != nil {
		t.Fatalf("count revenues %d: %v", securityID, err)
	}
	return n
}

func countFundamentalsForCIK(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cik string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM fundamentals f JOIN securities s ON s.id = f.security_id WHERE s.cik = $1`, cik).Scan(&n); err != nil {
		t.Fatalf("count fundamentals %s: %v", cik, err)
	}
	return n
}

func countFundamentalsRowsForSecurity(t *testing.T, ctx context.Context, pool *pgxpool.Pool, securityID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM fundamentals WHERE security_id = $1`, securityID).Scan(&n); err != nil {
		t.Fatalf("count fundamentals %d: %v", securityID, err)
	}
	return n
}

// seedFundamentalsRow inserta una fila de fundamentals "histórica" (ya escrita
// por un emisor) para poder probar el nivel 2 de la preferencia. El periodo es
// FY2023, distinto del payload (FY2024), para que no colisione con el upsert
// normal de la re-ingesta.
func seedFundamentalsRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, securityID int64, concept string) {
	t.Helper()
	close_, periodType, fy, fp, src := 9.9, "duration", int16(2023), "FY", "test"
	start := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)
	sourceID := fmt.Sprintf("%s#%d", concept, securityID)
	unit := "USD"
	if _, err := pool.Exec(ctx, `
INSERT INTO fundamentals (security_id, concept, value, unit, period_type, period_start, period_end, fiscal_year, fiscal_period, filing_date, source, source_fact_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
ON CONFLICT DO NOTHING`,
		securityID, concept, &close_, &unit, periodType, &start, end, &fy, &fp, &end, src, &sourceID); err != nil {
		t.Fatalf("seed fundamentals %s(%d): %v", concept, securityID, err)
	}
}
