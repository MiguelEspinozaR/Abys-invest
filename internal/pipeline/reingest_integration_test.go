//go:build integration

package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/collect/edgar"
	"github.com/miky/abys-invest/internal/storage"
)

// TestM6cT1ReingestIsIdempotent es el CA-9 del plan: la re-ingesta fresca es
// IDEMPOTENTE. Ejecuta el camino REAL de `-fresh` (`ingestCompanyFresh`: borra
// el staging por CIK, reinserta y vuelve a pasar por el canonizador) dos veces
// con el MISMO payload y exige que las filas canónicas de esa empresa no cambien
// ni una.
//
// Por qué importa (es el motivo de W3): `InsertStaging` es idempotente por
// (accession, form_type, payload_type), y el pseudo-accession es
// `companyfacts/<cik>`, o sea idempotente por (cik, source). Sin `-fresh`, la
// segunda pasada es un INSERT ... DO NOTHING y la empresa NO vuelve a pasar por
// `dedupeCanonical`: por eso un cambio del catálogo XBRL no se ve en las
// empresas ya ingeridas. Y por el otro lado, `-fresh` no puede duplicar ni
// perder filas, porque `fundamentals` se upserta por
// (security_id, concept, period_type, period_end, fiscal_year, fiscal_period).
//
// Sin red: el cliente EDGAR apunta a un httptest que sirve el fixture real de
// AAPL (internal/collect/edgar/testdata/aapl_companyfacts.json). Sin TRUNCATE
// de tablas compartidas: todo va acotado al CIK de AAPL y se limpia con un
// DELETE de su staging, porque `make integration` corre los paquetes en
// paralelo (`cmd/api` no toma el advisory lock compartido) y un TRUNCATE global
// desde aquí podría tumbar un /health ajeno. El TestMain del paquete sí toma el
// lock y `EnsureTestDatabase` aborta si la BD no es *_test.
func TestM6cT1ReingestIsIdempotent(t *testing.T) {
	pool := integPool
	if pool == nil {
		t.Skip("DATABASE_URL no configurado o BD no alcanzable; saltando test de re-ingesta")
	}
	ctx := context.Background()
	const cik = "0000320193"

	payload, err := os.ReadFile("../../internal/collect/edgar/testdata/aapl_companyfacts.json")
	if err != nil {
		t.Fatalf("leer fixture AAPL: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	client, err := edgar.NewClient("AbysInvest/test (contact@example.test)",
		edgar.WithBaseURL(srv.URL),
		edgar.WithRetryBase(1),
	)
	if err != nil {
		t.Fatalf("edgar.NewClient: %v", err)
	}

	// Punto de partida limpio SÓLO para este CIK (no hay TRUNCATE: ver cabecera).
	if _, err := pool.Exec(ctx,
		`DELETE FROM edgar_staging WHERE cik = $1 AND payload_type = 'company_facts'`, cik); err != nil {
		t.Fatalf("limpieza del staging de AAPL: %v", err)
	}

	// --- Pasada 1: ingesta fresca desde cero ---------------------------------
	if err := ingestCompanyFresh(ctx, pool, client, "AAPL", cik); err != nil {
		t.Fatalf("pasada 1 de ingestCompanyFresh: %v", err)
	}
	before := dumpAAPLFundamentals(t, pool, cik)
	if len(before) == 0 {
		t.Fatal("pasada 1: la ingesta fresca no escribió fundamentals (el fixture no se canonicalizó)")
	}

	// La pasada 1 DEBE haber borrado y reinsertado el staging: es la diferencia
	// entre "se re-canonizó" y "el INSERT fue un no-op".
	assertStaging(t, pool, cik, 1)

	// Idempotencia del CATÁLOGO dentro de una misma pasada (una fila por
	// (concepto, periodKey)): sin esto, "0 filas cambiadas" podría ser "el mismo
	// duplicado otra vez".
	var dupKeys int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM (
		SELECT f.concept, f.period_type, f.period_end, f.fiscal_year, f.fiscal_period, count(*)
		FROM fundamentals f JOIN securities s ON s.id = f.security_id
		WHERE s.cik = $1
		GROUP BY 1,2,3,4,5 HAVING count(*) > 1) d`, cik).Scan(&dupKeys); err != nil {
		t.Fatalf("duplicados por clave canónica: %v", err)
	}
	if dupKeys != 0 {
		t.Fatalf("hay %d (concepto, periodKey) con más de una fila para %s; la re-ingesta no puede llamarse idempotente", dupKeys, cik)
	}

	// --- Pasada 2: la MISMA ingesta fresca debe ser un no-op en los datos -----
	if err := ingestCompanyFresh(ctx, pool, client, "AAPL", cik); err != nil {
		t.Fatalf("pasada 2 de ingestCompanyFresh: %v", err)
	}
	after := dumpAAPLFundamentals(t, pool, cik)

	if len(before) != len(after) {
		t.Fatalf("la re-ingesta cambió el número de filas de fundamentals de %s: antes=%d después=%d", cik, len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("la re-ingesta cambió la fila %d:\n antes: %s\ndespués: %s", i, before[i], after[i])
		}
	}

	// Y el staging se regeneró de verdad (1 fila, nueva), que es la prueba de que
	// la 2.ª pasada ALSO pasó por el canonizador en vez de saltárselo.
	assertStaging(t, pool, cik, 1)

	t.Logf("re-ingesta idempotente: %d filas de fundamentals de %s idénticas tras 2 pasadas frescas", len(after), cik)
}

// assertStaging comprueba que el CIK tiene exactamente want filas de staging y
// que están normalizadas (o sea: que el canonizador volvió a pasar).
func assertStaging(t *testing.T, pool *pgxpool.Pool, cik string, want int) {
	t.Helper()
	ctx := context.Background()
	var rows int
	var normalized bool
	if err := pool.QueryRow(ctx,
		`SELECT count(*), bool_and(normalized) FROM edgar_staging WHERE cik = $1`, cik).
		Scan(&rows, &normalized); err != nil {
		t.Fatalf("estado del staging de %s: %v", cik, err)
	}
	if rows != want {
		t.Fatalf("staging de %s: se esperaban %d filas, hay %d", cik, want, rows)
	}
	if !normalized {
		t.Fatalf("staging de %s: el payload debe quedar normalizado (el canonizador volvió a pasar)", cik)
	}
}

// dumpAAPLFundamentals devuelve una línea por fila canónica de `fundamentals`
// del CIK indicado, ordenada y con los campos que definen el dato (no el id ni
// created_at, que son bookkeeping del upsert). Es la comparación del CA-9.
func dumpAAPLFundamentals(t *testing.T, pool *pgxpool.Pool, cik string) []string {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT f.concept, coalesce(f.value::text,''), coalesce(f.unit,''), f.period_type,
			coalesce(f.period_start::text,''), coalesce(f.period_end::text,''),
			coalesce(f.fiscal_year::text,''), coalesce(f.fiscal_period,''),
			coalesce(f.filing_date::text,''), coalesce(f.source_fact_id,''), coalesce(f.raw_value,'')
		FROM fundamentals f JOIN securities s ON s.id = f.security_id
		WHERE s.cik = $1
		ORDER BY f.concept, f.period_end, f.fiscal_year, f.fiscal_period, f.source_fact_id`, cik)
	if err != nil {
		t.Fatalf("snapshot de fundamentals de %s: %v", cik, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		cols := make([]string, 11)
		dest := make([]any, len(cols))
		for i := range cols {
			dest[i] = &cols[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatalf("scan del snapshot: %v", err)
		}
		out = append(out, strings.Join(cols, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iteración del snapshot: %v", err)
	}
	return out
}

// TestM6cT1FreshDeletesEveryStagingRowOfTheCIK aísla el mecanismo que distingue
// `-fresh` del camino normal, y es lo que permite que un cambio del catálogo se
// aplique: `ingestCompanyFresh` borra TODO el staging del CIK (no sólo lo que
// casaría por (accession, form_type, payload_type)) antes de reinserir. Sin ese
// borrado, un payload ingested con un accession distinto —o el mismo payload con
// las `notes`/mapeo viejos— se quedaría congelado.
//
// Se siembra una fila de staging ADICIONAL y ya normalizada para el mismo CIK, que
// es justo el estado que un `INSERT ... DO NOTHING` sin `-fresh` dejaría viva, y se
// exige que tras la pasada fresca quede SÓLO la recién insertada.
//
// (Por qué este test NO comprueba el camino NO fresco con `ingestCompany`: ese
// camino, cuando el insert es un no-op, llama a `edgar.NormalizePending(ctx, pool,
// 5)`, que es GLOBAL y no está acotado al ticker, así que en `make integration`
// (paquetes en paralelo sobre la misma BD, y `cmd/api` sin advisory lock) puede
// normalizar filas sembradas por otro paquete y fallar por ellas. No es un test
// hermético; el dato que interesa —que el `-fresh` borre— sí se prueba aquí.)
func TestM6cT1FreshDeletesEveryStagingRowOfTheCIK(t *testing.T) {
	pool := integPool
	if pool == nil {
		t.Skip("DATABASE_URL no configurado o BD no alcanzable; saltando test de borrado del staging")
	}
	ctx := context.Background()
	const cik = "0000320193"

	payload, err := os.ReadFile("../../internal/collect/edgar/testdata/aapl_companyfacts.json")
	if err != nil {
		t.Fatalf("leer fixture AAPL: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()
	client, err := edgar.NewClient("AbysInvest/test (contact@example.test)",
		edgar.WithBaseURL(srv.URL), edgar.WithRetryBase(1))
	if err != nil {
		t.Fatalf("edgar.NewClient: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`DELETE FROM edgar_staging WHERE cik = $1 AND payload_type = 'company_facts'`, cik); err != nil {
		t.Fatalf("limpieza del staging de AAPL: %v", err)
	}

	// Fila fantasma: mismo CIK, accession distinto, ya normalizada (el estado que
	// sobrevive a un `make run-collector` normal).
	stale, err := pool.Exec(ctx, `INSERT INTO edgar_staging
		(cik, ticker, accession, form_type, filing_date, payload, payload_type, normalized)
		VALUES ($1, 'AAPL', '0000320193-24-000123', '10-K', now(), $2, $3, true)`,
		cik, []byte(`{"cik":320193,"facts":{}}`), edgar.CompanyFactsPayloadType)
	if err != nil {
		t.Fatalf("sembrar staging fantasma: %v", err)
	}
	if stale.RowsAffected() != 1 {
		t.Fatalf("staging fantasma: %d filas", stale.RowsAffected())
	}
	assertStaging(t, pool, cik, 1) // sólo la fantasma por ahora

	if err := ingestCompanyFresh(ctx, pool, client, "AAPL", cik); err != nil {
		t.Fatalf("ingesta fresca: %v", err)
	}

	// La fantasma debe haber desaparecido y quedar sólo el payload recién
	// descargado y canonizado.
	assertStaging(t, pool, cik, 1)
	var acc string
	if err := pool.QueryRow(ctx,
		`SELECT accession FROM edgar_staging WHERE cik = $1 AND payload_type = 'company_facts'`, cik).
		Scan(&acc); err != nil {
		t.Fatalf("leer accession sobreviviente: %v", err)
	}
	if acc != "companyfacts/"+cik {
		t.Fatalf("con -fresh debe sobrevivir sólo el payload recién insertado, quedó %q", acc)
	}
}

// TestM6cT1FreshForcesRecanonicalization es la evidencia de que `-fresh`
// RE-CANONIZA, y no sólo vuelve a descargar el payload ("re-fetch"). Los otros
// tests de este archivo prueban que el staging se borra y se reinserta y que la
// operación es idempotente; esto prueba la CONSECUENCIA:
//
//  1. pasada fresca ⇒ las filas canónicas están;
//  2. se BORRAN esas filas, simulando lo que sería una BD con un catálogo
//     anterior (o un `fundamentals` reparado a mano): ahora el dato falta;
//  3. el camino NO fresco (`storage.InsertStaging` con el mismo
//     (accession, form_type, payload_type), que es exactamente lo que hace
//     `make run-collector` cada vez) devuelve `nil` — el `INSERT ... DO NOTHING`
//     es un no-op — y por tanto NO se llama al canonizador y las filas siguen
//     faltando. Ése es exactamente el fallo que hace obligatoria la re-ingesta;
//  4. el camino fresco (`ingestCompanyFresh`, al que `-fresh` enruta) las
//     RESTITUYE, idénticas a la pasada 1.
//
// Si alguien "optimizara" `-fresh` para no borrar el staging (o para saltarse la
// normalización), este test falla: es el que muerde.
//
// Por qué el paso 3 llama a `storage.InsertStaging` y no a `ingestCompany`: el
// `else` de `ingestCompany` (rama no fresca con staging ya presente) invoca
// `edgar.NormalizePending`, que es GLOBAL y no acotado al CIK (deuda M6c-T1-W3-1,
// documentada en el Makefile y en el README del collector), así que en `make
// integration` —paquetes en paralelo sobre la misma BD— puede normalizar filas
// sembradas por otro paquete. Lo que se afirma aquí es la PREMISA de esa rama
// (el insert devuelve nil ⇒ no hay canonización), que es lo que hace que la
// re-ingesta sea necesaria; el comportamiento global de `NormalizePending` no se
// afirma aquí a propósito, y por eso el paso 3 es una aserción explícita de esa
// premisa y no una llamada al job completo.
//
// Ámbito: sólo el CIK de AAPL, sobre `abys_test` (el TestMain del paquete exige
// nombre `*_test` y toma el advisory lock compartido). Sin TRUNCATE de tablas
// compartidas, por el mismo motivo que los tests de arriba. El estado se
// restituye al final y además en `t.Cleanup`, para que un fallo a mitad no deje
// filas canónicas de AAPL ausentes para otro test del paquete.
func TestM6cT1FreshForcesRecanonicalization(t *testing.T) {
	pool := integPool
	if pool == nil {
		t.Skip("DATABASE_URL no configurado o BD no alcanzable; saltando test de re-canonización")
	}
	ctx := context.Background()
	const cik = "0000320193"
	const ticker = "AAPL"

	payload, err := os.ReadFile("../../internal/collect/edgar/testdata/aapl_companyfacts.json")
	if err != nil {
		t.Fatalf("leer fixture AAPL: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	// t.Cleanup y no `defer`: los cleanup corren en orden INVERSO al de registro,
	// así que el servidor (registrado primero) se cierra DESPUÉS de la
	// restauración de abajo. Con `defer srv.Close()` la restauración correría
	// contra un servidor ya caído y AAPL quedaría sin sus filas canónicas.
	t.Cleanup(srv.Close)

	client, err := edgar.NewClient("AbysInvest/test (contact@example.test)",
		edgar.WithBaseURL(srv.URL),
		edgar.WithRetryBase(1),
	)
	if err != nil {
		t.Fatalf("edgar.NewClient: %v", err)
	}

	// Limpieza sólo de este CIK + red de seguridad: pase lo que pase, el test
	// deja AAPL con sus filas canónicas restauradas.
	if _, err := pool.Exec(ctx,
		`DELETE FROM edgar_staging WHERE cik = $1 AND payload_type = 'company_facts'`, cik); err != nil {
		t.Fatalf("limpieza del staging de AAPL: %v", err)
	}
	t.Cleanup(func() {
		if err := ingestCompanyFresh(context.Background(), pool, client, ticker, cik); err != nil {
			t.Logf("t.Cleanup: la re-ingesta fresca de AAPL falló: %v", err)
		}
	})

	// --- 1) Pasada fresca: el catálogo vigente escribe las filas -------------
	if err := ingestCompanyFresh(ctx, pool, client, ticker, cik); err != nil {
		t.Fatalf("pasada fresca inicial: %v", err)
	}
	want := dumpAAPLFundamentals(t, pool, cik)
	if len(want) == 0 {
		t.Fatal("la ingesta fresca no escribió fundamentals (el fixture no se canonicalizó)")
	}
	assertStaging(t, pool, cik, 1)

	// --- 2) Se borran esas filas = "BD con un catálogo anterior" ------------
	// Se borra UN concepto presente en el catálogo vigente, de modo que la
	// única manera de recuperarlo sea volver a canonizar el payload.
	const concept = "total_assets"
	del, err := pool.Exec(ctx,
		`DELETE FROM fundamentals f USING securities s
		 WHERE s.id = f.security_id AND s.cik = $1 AND f.concept = $2`, cik, concept)
	if err != nil {
		t.Fatalf("borrado de las filas de %s: %v", concept, err)
	}
	if del.RowsAffected() == 0 {
		t.Fatalf("el fixture no produjo filas de %s: el test no probaría nada", concept)
	}
	if n := countAAPLConcept(t, pool, cik, concept); n != 0 {
		t.Fatalf("preparación: %s debería estar a 0, hay %d", concept, n)
	}

	// --- 3) Camino NO fresco: el insert es un no-op y NO canoniza -----------
	// Es la premisa de la rama `else` de `ingestCompany` (la que usa
	// `make run-collector`): si el insert no devuelve id, no hay normalización.
	id, err := storage.InsertStaging(ctx, pool, &storage.EdgarStaging{
		CIK: cik, Ticker: ptrTo(ticker), Accession: "companyfacts/" + cik,
		FormType: "10-K", FilingDate: time.Now().UTC(),
		Payload: payload, PayloadType: edgar.CompanyFactsPayloadType,
	})
	if err != nil {
		t.Fatalf("InsertStaging del camino no fresco: %v", err)
	}
	if id != nil {
		t.Fatalf("el camino no fresco insertó la fila %d: la premisa del test (el staging ya está, el insert es un no-op) es falsa", *id)
	}
	if n := countAAPLConcept(t, pool, cik, concept); n != 0 {
		t.Fatalf("con el insert en no-op, %s=no-fresco volvió a aparecer %d filas: alguien está canonizando sin `-fresh`", concept, n)
	}

	// --- 4) Camino fresco: RE-CANONIZA y restituye las filas ----------------
	if err := ingestCompanyFresh(ctx, pool, client, ticker, cik); err != nil {
		t.Fatalf("pasada fresca de recuperación: %v", err)
	}
	got := dumpAAPLFundamentals(t, pool, cik)
	if len(got) != len(want) {
		t.Fatalf("tras `-fresh` hay %d filas de %s; la 1.ª pasada escribió %d (faltan las que sólo puede re-canonizar la fresca)",
			len(got), cik, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("la fila %d no se restituyó idéntica:\n 1.ª pasada: %s\n fresca:     %s", i, want[i], got[i])
		}
	}
	t.Logf("`fresh` re-canoniza de verdad: %s volvió a escribir %d filas (concepto %s) que el camino no fresco dejaba ausentes",
		concept, countAAPLConcept(t, pool, cik, concept), concept)
}

// countAAPLConcept cuenta las filas canónicas de un concepto para el CIK.
func countAAPLConcept(t *testing.T, pool *pgxpool.Pool, cik, concept string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM fundamentals f JOIN securities s ON s.id = f.security_id
		 WHERE s.cik = $1 AND f.concept = $2`, cik, concept).Scan(&n); err != nil {
		t.Fatalf("contar %s de %s: %v", concept, cik, err)
	}
	return n
}

func ptrTo(s string) *string { return &s }

// El otro extremo del camino —que el universo por defecto son las empresas con
// companyfacts normalizado y NO el catálogo de securities (10.461 filas vs ~42
// empresas)— no se prueba aquí sino en internal/storage, junto al selector que lo
// decide (TestStagedNormalizedCIKs): esa pieza es del pipeline de storage, y
// sembrar 25 securities para luego ignorarlos exigiría un TRUNCATE global en un
// paquete que corre en paralelo con cmd/api. La consulta en sí está anclada a
// texto exacto en internal/storage/staging_test.go.
