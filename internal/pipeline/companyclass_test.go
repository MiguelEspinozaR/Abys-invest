package pipeline

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/miky/abys-invest/internal/collect/edgar"
)

// multiclassCatalogClasses son las clases REALES de los 6 CIKs multiclase del
// universo staged (leído de la BD de dev 2026-10-06, 36 filas). El payload
// sintético del test replica este catálogo; los CIKs son los reales.
var multiclassCatalogClasses = []struct{ cik, ticker string }{
	{"0000019617", "AMJB"}, {"0000019617", "JPM"}, {"0000019617", "JPM-PC"},
	{"0000019617", "JPM-PD"}, {"0000019617", "JPM-PJ"}, {"0000019617", "JPM-PK"},
	{"0000019617", "JPM-PL"}, {"0000019617", "JPM-PM"}, {"0000019617", "VYLD"},

	{"0000070858", "BAC"}, {"0000070858", "BAC-PB"}, {"0000070858", "BAC-PE"},
	{"0000070858", "BAC-PK"}, {"0000070858", "BAC-PL"}, {"0000070858", "BAC-PM"},
	{"0000070858", "BAC-PN"}, {"0000070858", "BAC-PO"}, {"0000070858", "BAC-PP"},
	{"0000070858", "BAC-PQ"}, {"0000070858", "BAC-PS"}, {"0000070858", "BACRP"},
	{"0000070858", "BML-PG"}, {"0000070858", "BML-PH"}, {"0000070858", "BML-PJ"},
	{"0000070858", "BML-PL"}, {"0000070858", "MER-PK"},

	{"0001045609", "PLD"}, {"0001045609", "PLDGP"},

	{"0001063761", "SPG"}, {"0001063761", "SPG-PJ"},

	{"0001067983", "BRK-A"}, {"0001067983", "BRK-B"},

	{"0001652044", "GOOG"}, {"0001652044", "GOOGL"}, {"0001652044", "GOOGM"},
	{"0001652044", "GOOGN"},
}

// multiclassCatalogPayload escribe el payload company_tickers.json con las
// clases REVUELTAS (entrelazando CIKs) para que la aleatoriedad del rango del
// map tuviera donde manifestarse antes del fix.
func multiclassCatalogPayload(t *testing.T) []byte {
	t.Helper()
	var sb strings.Builder
	sb.WriteByte('{')
	for i := 0; i < len(multiclassCatalogClasses); i++ {
		// Índice al revés: el orden de las claves del objeto JSON no importa
		// (se decodifica a map), pero revolverlo complica cualquier falso
		// determinismo por orden de bytes.
		c := multiclassCatalogClasses[len(multiclassCatalogClasses)-1-i]
		cikNum, err := strconv.ParseInt(c.cik, 10, 64)
		if err != nil {
			t.Fatalf("CIK %q no numérico: %v", c.cik, err)
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `"%d":{"cik_str":%d,"ticker":%q,"title":"Clase %s"}`, i, cikNum, c.ticker, c.ticker)
	}
	sb.WriteByte('}')
	return []byte(sb.String())
}

// TestResolveCompanyMulticlassDeterministicAcrossRuns es el test de
// determinismo (P0, req. 4): MISMO payload de catálogo, 100 corridas =>
// MISMO ticker para los 6 CIKs multiclase. Se cubren las dos fases del fix:
//   - el orden del catálogo es estable (ParseCompanyTickers ordena CIK asc,
//     ticker asc) aunque el rango del map sea aleatorio;
//   - la preferencia de clase es determinista: con el lookup inyectado con
//     precios (estado real de dev) -> las 6 clases canónicas; sin datos (nil)
//     -> el fallback determinista (menor ticker), también estable.
func TestResolveCompanyMulticlassDeterministicAcrossRuns(t *testing.T) {
	payload := multiclassCatalogPayload(t)

	// Estado real de dev (2026-10-06): las clases con filas en daily_prices.
	canonicalByCIK := map[string]string{
		"0000019617": "JPM",
		"0000070858": "BAC",
		"0001045609": "PLD",
		"0001063761": "SPG",
		"0001067983": "BRK-B",
		"0001652044": "GOOGL",
	}
	pricesFor := func(ticker string) (bool, bool) {
		for _, want := range canonicalByCIK {
			if want == ticker {
				return true, false
			}
		}
		return false, false
	}

	// Fallback sin BD (lookup nulo): menor ticker del CIK (catálogo ordenado).
	fallbackByCIK := map[string]string{
		"0000019617": "AMJB",
		"0000070858": "BAC",
		"0001045609": "PLD",
		"0001063761": "SPG",
		"0001067983": "BRK-A",
		"0001652044": "GOOG",
	}

	for run := 0; run < 100; run++ {
		catalog, err := edgar.ParseCompanyTickers(payload)
		if err != nil {
			t.Fatalf("corrida %d: ParseCompanyTickers: %v", run, err)
		}
		if len(catalog) != len(multiclassCatalogClasses) {
			t.Fatalf("corrida %d: catálogo con %d entradas, se esperaban %d", run, len(catalog), len(multiclassCatalogClasses))
		}
		for cik, wantPrice := range canonicalByCIK {
			got, gotCIK, err := resolveCompany(catalog, cik, pricesFor)
			if err != nil {
				t.Fatalf("corrida %d, CIK %s (con precios): %v", run, cik, err)
			}
			if got != wantPrice || gotCIK != cik {
				t.Fatalf("corrida %d, CIK %s (con precios): resolución inestable: got (%s,%s), want (%s,%s)",
					run, cik, got, gotCIK, wantPrice, cik)
			}
		}
		for cik, wantFallback := range fallbackByCIK {
			got, gotCIK, err := resolveCompany(catalog, cik, nil)
			if err != nil {
				t.Fatalf("corrida %d, CIK %s (fallback): %v", run, cik, err)
			}
			if got != wantFallback || gotCIK != cik {
				t.Fatalf("corrida %d, CIK %s (fallback): resolución inestable: got (%s,%s), want (%s,%s)",
					run, cik, got, gotCIK, wantFallback, cik)
			}
		}
	}
}

// TestPickPreferredClassLevels es el test de preferencia (req. 5): con lookup
// inyectado y SIEMPRE desempate determinista por ticker ASC. Cero aleatoriedad.
func TestPickPreferredClassLevels(t *testing.T) {
	candidates := []edgar.CompanyTicker{
		{Ticker: "JPM-PM", CIK: 19617},
		{Ticker: "JPM-PC", CIK: 19617},
		{Ticker: "JPM", CIK: 19617},
	}

	// 1) Sin precios; con fundamentals -> la clase con fundamentals gana
	//    (aunque otra sea la primera del catálogo ordenado).
	withFunds := func(ticker string) (bool, bool) { return false, ticker == "JPM" }
	if got := pickPreferredClass(candidates, withFunds); got.Ticker != "JPM" {
		t.Fatalf("con fundamentals: got %q, want JPM", got.Ticker)
	}

	// 2) Con precios -> la clase con precios gana aunque todas tengan
	//    fundamentals (el nivel 1 manda sobre el 2).
	withPrices := func(ticker string) (bool, bool) { return ticker == "JPM", true }
	if got := pickPreferredClass(candidates, withPrices); got.Ticker != "JPM" {
		t.Fatalf("con precios+fundamentals: got %q, want JPM", got.Ticker)
	}

	// 3) Sin ninguna -> la primera del catálogo ordenado (fallback determinista
	//    = menor ticker del CIK).
	if got := pickPreferredClass(candidates, nil); got.Ticker != "JPM" {
		t.Fatalf("sin datos: got %q, want JPM (menor ticker)", got.Ticker)
	}

	// 4) Desempate dentro del NIVEL 1 (dos clases con precios) -> ticker ASC.
	twoWithPrices := []edgar.CompanyTicker{
		{Ticker: "BRK-B", CIK: 1067983},
		{Ticker: "BRK-A", CIK: 1067983},
	}
	bothPrices := func(string) (bool, bool) { return true, true }
	if got := pickPreferredClass(twoWithPrices, bothPrices); got.Ticker != "BRK-A" {
		t.Fatalf("empate con precios: got %q, want BRK-A (ticker asc)", got.Ticker)
	}

	// 5) Desempate dentro del NIVEL 2 (dos clases con fundamentals) -> ticker ASC.
	twoWithFunds := []edgar.CompanyTicker{
		{Ticker: "GOOGN", CIK: 1652044},
		{Ticker: "GOOGL", CIK: 1652044},
		{Ticker: "GOOG", CIK: 1652044},
	}
	onlyFunds := func(string) (bool, bool) { return false, true }
	if got := pickPreferredClass(twoWithFunds, onlyFunds); got.Ticker != "GOOG" {
		t.Fatalf("empate con fundamentals: got %q, want GOOG (ticker asc)", got.Ticker)
	}

	// 6) Sin lookup (nil) con empate de catálogo -> orden estable (ticker asc).
	if got := pickPreferredClass(twoWithFunds, nil); got.Ticker != "GOOG" {
		t.Fatalf("empate sin lookup: got %q, want GOOG (ticker asc)", got.Ticker)
	}

	// 7) Candidatos vacíos -> zero value sin pánico.
	if got := pickPreferredClass(nil, nil); got.Ticker != "" {
		t.Fatalf("candidatos vacíos: got %q, want zero value", got.Ticker)
	}
}

// TestResolveCompanyCandidates cubre el contrato de entrada de resolveCompany:
// CIK multiclase devuelve TODAS sus clases (en orden del catálogo), ticker
// devuelve su única clase, y los casos sin catálogo/CIK desconocido mantienen
// el contrato original.
func TestResolveCompanyCandidates(t *testing.T) {
	catalog, err := edgar.ParseCompanyTickers(multiclassCatalogPayload(t))
	if err != nil {
		t.Fatalf("ParseCompanyTickers: %v", err)
	}

	// CIK multiclase: todas las clases, en orden determinista del catálogo.
	cik, cands, err := resolveCompanyCandidates(catalog, "0000019617")
	if err != nil {
		t.Fatalf("resolveCompanyCandidates(CIK): %v", err)
	}
	if cik != "0000019617" {
		t.Fatalf("cik: got %q, want 0000019617", cik)
	}
	if len(cands) != 9 {
		t.Fatalf("JPM: %d clases, se esperaban 9", len(cands))
	}
	if cands[0].Ticker != "AMJB" || cands[len(cands)-1].Ticker != "VYLD" {
		t.Fatalf("clases de JPM fuera de orden: %v", cands)
	}

	// Ticker: única clase y su CIK.
	ticker, single, err := resolveCompanyCandidates(catalog, "brk-b")
	if err != nil {
		t.Fatalf("resolveCompanyCandidates(ticker): %v", err)
	}
	if ticker != "0001067983" || len(single) != 1 || single[0].Ticker != "BRK-B" {
		t.Fatalf("ticker BRK-B: got (%s, %v), want (0001067983, [BRK-B])", ticker, single)
	}

	// Sin catálogo: CIK directo, y ticker con error explícito.
	if cik, cands, err := resolveCompanyCandidates(nil, "0001067983"); err != nil || cik != "0001067983" || cands != nil {
		t.Fatalf("CIK sin catálogo: got (%s, %v, %v), want (0001067983, nil, nil)", cik, cands, err)
	}
	if _, _, err := resolveCompanyCandidates(nil, "BRK-B"); err == nil {
		t.Fatal("ticker sin catálogo debe fallar")
	}

	// CIK numérico sin clases en el catálogo -> candidatos vacíos (el caller
	// continúa por CIK directo), no un error.
	if cik, cands, err := resolveCompanyCandidates(catalog, "0000320194"); err != nil || cik != "0000320194" || len(cands) != 0 {
		t.Fatalf("CIK sin clases: got (%s, %v, %v), want (0000320194, [], nil)", cik, cands, err)
	}

	// Ticker inexistente en el catálogo -> error.
	if _, _, err := resolveCompanyCandidates(catalog, "NOSOYTICKER"); err == nil {
		t.Fatal("ticker inexistente debe fallar")
	}
}

// TestResolveCompanyMulticlassViaCatalog cubre el contrato externo de
// resolveCompany (el que mantienen los callers) sobre el catálogo real: la
// entrada por CIK multiclase resuelve a la clase preferida y la entrada por
// ticker resuelve a ese ticker exacto.
func TestResolveCompanyMulticlassViaCatalog(t *testing.T) {
	catalog, err := edgar.ParseCompanyTickers(multiclassCatalogPayload(t))
	if err != nil {
		t.Fatalf("ParseCompanyTickers: %v", err)
	}
	withPrices := func(ticker string) (bool, bool) { return ticker == "BRK-B", ticker == "BRK-A" }

	name, cik, err := resolveCompany(catalog, "0001067983", withPrices)
	if err != nil {
		t.Fatalf("resolveCompany(CIK): %v", err)
	}
	if name != "BRK-B" || cik != "0001067983" {
		t.Fatalf("BRK con precios en BRK-B: got (%s, %s), want (BRK-B, 0001067983)", name, cik)
	}

	// Entrada por ticker: se respeta la clase pedida (aunque el empuje del
	// lookup diga otra cosa: la preferencia multiclase sólo aplica a CIKs).
	name, cik, err = resolveCompany(catalog, "BRK-A", withPrices)
	if err != nil {
		t.Fatalf("resolveCompany(ticker): %v", err)
	}
	if name != "BRK-A" || cik != "0001067983" {
		t.Fatalf("entrada por ticker: got (%s, %s), want (BRK-A, 0001067983)", name, cik)
	}
}
