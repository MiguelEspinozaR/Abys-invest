package edgar

import (
	"os"
	"testing"
	"time"
)

func loadAAPLFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/aapl_companyfacts.json")
	if err != nil {
		t.Fatalf("leer fixture AAPL: %v", err)
	}
	return data
}

func TestParseCompanyFactsAAPLFixture(t *testing.T) {
	facts, err := ParseCompanyFacts(loadAAPLFixture(t))
	if err != nil {
		t.Fatalf("ParseCompanyFacts falló con fixture real: %v", err)
	}
	if len(facts) == 0 {
		t.Fatal("no se extrajeron hechos del fixture")
	}

	// Hecho conocido: ingresos FY2024 (10-K original de AAPL, filed 2024-11-01).
	// La reexpresión del 10-K de FY2025 repite el mismo periodo con fy=2025;
	// el extractor crudo devuelve ambas, por eso se busca la de fy=2024.
	wantEnd := time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)
	var found bool
	for _, f := range facts {
		if f.Concept == "RevenueFromContractWithCustomerExcludingAssessedTax" &&
			f.Unit == "USD" && f.EndDate.Equal(wantEnd) && f.FormType == "10-K" &&
			f.FiscalYear != nil && *f.FiscalYear == 2024 {
			found = true
			if !f.HasValue || f.Value != 391035000000 {
				t.Fatalf("valor ingresos FY2024 inesperado: %v (has=%v)", f.Value, f.HasValue)
			}
			if f.FiscalPeriod != "FY" {
				t.Fatalf("fiscal period inesperado: %q", f.FiscalPeriod)
			}
		}
	}
	if !found {
		t.Fatal("no se encontró el hecho de ingresos FY2024 (fy=2024) en el fixture")
	}

	// El fixture debe contener hechos instant y duration.
	var instant, duration int
	for _, f := range facts {
		if f.StartDate == nil {
			instant++
		} else {
			duration++
		}
	}
	if instant == 0 || duration == 0 {
		t.Fatalf("se esperaban hechos instant y duration (instant=%d duration=%d)", instant, duration)
	}
}

func TestParseCompanyFactsInvalidJSON(t *testing.T) {
	if _, err := ParseCompanyFacts([]byte(`{"facts": [not json`)); err == nil {
		t.Fatal("se esperaba error para JSON inválido")
	}
	if _, err := ParseCompanyFacts([]byte(``)); err == nil {
		t.Fatal("se esperaba error para payload vacío")
	}
}

func TestParseCompanyFactsEdgeCases(t *testing.T) {
	data := []byte(`{
	  "cik": 320193, "entityName": "APPLE INC",
	  "facts": {"us-gaap": {
	    "NetIncomeLoss": {"units": {"USD": [
	      {"start": "2023-10-01", "end": "2024-09-28", "val": 93736000000,
	       "accn": "a1", "fy": 2024, "fp": "FY", "form": "10-K", "filed": "2024-11-01"},
	      {"start": "2023-10-01", "end": "2024-09-28", "val": null,
	       "accn": "a2", "fy": 2024, "fp": "FY", "form": "10-K", "filed": "2024-11-01"},
	      {"end": "2024-09-28", "val": 364980000000,
	       "accn": "a3", "fy": 2024, "fp": "FY", "form": "10-K", "filed": "2024-11-01"},
	      {"end": "malformato", "val": 1, "accn": "a4", "form": "10-K"}
	    ]}}
	  }}
	}`)

	facts, err := ParseCompanyFacts(data)
	if err != nil {
		t.Fatalf("ParseCompanyFacts falló: %v", err)
	}

	// La entrada con end malformado se descarta.
	if len(facts) != 3 {
		t.Fatalf("se esperaban 3 hechos válidos, hay %d", len(facts))
	}

	// Hecho sin valor: se conserva con HasValue=false.
	nullFact := facts[1]
	if nullFact.HasValue {
		t.Fatal("el hecho con val null debería tener HasValue=false")
	}
	if nullFact.RawValue != "" {
		t.Fatalf("raw value inesperado: %q", nullFact.RawValue)
	}

	// Hecho instant (sin start).
	instantFact := facts[2]
	if instantFact.StartDate != nil {
		t.Fatalf("el hecho sin start debería ser instant, start=%v", instantFact.StartDate)
	}
	if !instantFact.HasValue || instantFact.Value != 364980000000 {
		t.Fatalf("valor instant inesperado: %v", instantFact.Value)
	}
}

func TestParseCompanyFactsSkipsEmptyPayload(t *testing.T) {
	data := []byte(`{"cik":320193,"facts":{"us-gaap":{"X":null}}}`)
	if _, err := ParseCompanyFacts(data); err != nil {
		t.Fatalf("payload con concept null no debería fallar: %v", err)
	}
}

// TestParseCompanyFactsDEI verifica la corrección C001: SEC EDGAR guarda
// EntityCommonStockSharesOutstanding en el namespace "dei" (no "us-gaap"), por
// lo que ParseCompanyFacts debe procesar ambos namespaces y etiquetar cada
// hecho con su namespace de origen.
func TestParseCompanyFactsDEI(t *testing.T) {
	facts, err := ParseCompanyFacts(loadAAPLFixture(t))
	if err != nil {
		t.Fatalf("ParseCompanyFacts falló con fixture real: %v", err)
	}

	// Ambos namespaces presentes en el payload y etiquetados.
	var seenDEI, seenUSGAAP bool
	for _, f := range facts {
		switch f.Namespace {
		case "dei":
			seenDEI = true
		case "us-gaap":
			seenUSGAAP = true
		default:
			t.Fatalf("fact %s con namespace inesperado %q", f.Concept, f.Namespace)
		}
	}
	if !seenDEI || !seenUSGAAP {
		t.Fatalf("se esperaban facts de los namespaces dei y us-gaap (dei=%v us-gaap=%v)", seenDEI, seenUSGAAP)
	}

	// shares_outstanding = EntityCommonStockSharesOutstanding (dei, 10-K).
	// Datos reales del payload SEC de AAPL.
	want := []struct {
		fy   int
		end  string
		val  float64
		accn string
	}{
		{2024, "2024-10-18", 15115823000, "0000320193-24-000123"},
		{2025, "2025-10-17", 14776353000, "0000320193-25-000079"},
	}
	for _, w := range want {
		var found bool
		for _, f := range facts {
			if f.Concept != "EntityCommonStockSharesOutstanding" || f.Namespace != "dei" {
				continue
			}
			if f.FormType != "10-K" || f.FiscalYear == nil || *f.FiscalYear != w.fy {
				continue
			}
			if f.EndDate.Format("2006-01-02") != w.end {
				continue
			}
			found = true
			if !f.HasValue || f.Value != w.val {
				t.Fatalf("shares_outstanding FY%d: got %v (has=%v) want %v", w.fy, f.Value, f.HasValue, w.val)
			}
			if f.Unit != "shares" {
				t.Fatalf("shares_outstanding FY%d: unit inesperado %q", w.fy, f.Unit)
			}
			if f.Accession != w.accn {
				t.Fatalf("shares_outstanding FY%d: accn %q want %q", w.fy, f.Accession, w.accn)
			}
			if f.StartDate != nil {
				t.Fatalf("shares_outstanding FY%d: debería ser instant, start=%v", w.fy, f.StartDate)
			}
		}
		if !found {
			t.Fatalf("no se extrajo EntityCommonStockSharesOutstanding FY%d (dei)", w.fy)
		}
	}
}

// TestParseCompanyFactsOnlyDEI cubre el escenario exacto del bug F001: un
// payload que SOLO tiene el namespace "dei" debe extraer shares_outstanding.
func TestParseCompanyFactsOnlyDEI(t *testing.T) {
	data := []byte(`{
	  "cik": 320193, "entityName": "APPLE INC",
	  "facts": {"dei": {
	    "EntityCommonStockSharesOutstanding": {"label": "Entity Common Stock, Shares Outstanding", "units": {"shares": [
	      {"end": "2024-10-18", "val": 15115823000, "accn": "0000320193-24-000123",
	       "fy": 2024, "fp": "FY", "form": "10-K", "filed": "2024-11-01", "frame": "CY2024Q3I"}
	    ]}}
	  }}
	}`)

	facts, err := ParseCompanyFacts(data)
	if err != nil {
		t.Fatalf("ParseCompanyFacts falló: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("se esperaba 1 hecho (dei), hay %d", len(facts))
	}
	f := facts[0]
	if f.Namespace != "dei" || f.Concept != "EntityCommonStockSharesOutstanding" {
		t.Fatalf("fact inesperado: namespace=%q concept=%q", f.Namespace, f.Concept)
	}
	if !f.HasValue || f.Value != 15115823000 || f.Unit != "shares" {
		t.Fatalf("shares_outstanding esperado 15115823000 shares, got %v (%v)", f.Value, f.Unit)
	}
}
