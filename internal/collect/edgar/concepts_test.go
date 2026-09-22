package edgar

import (
	"testing"
	"time"
)

func TestCanonicalConceptsDictionary(t *testing.T) {
	// El diccionario cubre exactamente los 20 conceptos canónicos del plan §2.3.
	seen := map[string]bool{}
	for _, cc := range CanonicalConcepts() {
		seen[cc.Canonical] = true
	}
	if len(seen) != 20 {
		t.Fatalf("diccionario canónico: se esperaban 20 conceptos, hay %d", len(seen))
	}
	for _, required := range []string{
		"revenues", "cost_of_revenue", "gross_profit", "operating_income", "net_earnings",
		"depreciation_amortization", "total_assets", "total_liabilities", "long_term_debt",
		"short_term_debt", "shareholders_equity", "cash_and_equivalents", "operating_cash_flow",
		"capex", "dividends_paid", "shares_outstanding", "eps_basic", "eps_diluted",
		"current_assets", "current_liabilities",
	} {
		if !seen[required] {
			t.Errorf("falta el concepto canónico %q", required)
		}
	}
}

func fact(concept, unit, start, end string, val float64, hasVal bool) XBRLFact {
	f := XBRLFact{Concept: concept, Unit: unit, Value: val, HasValue: hasVal}
	if start != "" {
		s := time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC)
		f.StartDate = &s
	}
	e, _ := time.Parse("2006-01-02", end)
	f.EndDate = e
	fy := 2024
	f.FiscalYear = &fy
	f.FiscalPeriod = "FY"
	f.FormType = "10-K"
	f.FilingDate = time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)
	f.Accession = "0000320193-24-000123"
	if hasVal {
		f.RawValue = "x"
	}
	return f
}

func TestMapToCanonical(t *testing.T) {
	cases := []struct {
		name     string
		in       XBRLFact
		want     string
		wantUnit string
	}{
		{"revenues", fact("Revenues", "USD", "2023-10-01", "2024-09-28", 1, true), "revenues", "USD"},
		{"revenues alt", fact("RevenueFromContractWithCustomerExcludingAssessedTax", "USD", "2023-10-01", "2024-09-28", 1, true), "revenues", "USD"},
		{"net income", fact("NetIncomeLoss", "USD", "2023-10-01", "2024-09-28", 1, true), "net_earnings", "USD"},
		{"assets instant", fact("Assets", "USD", "", "2024-09-28", 1, true), "total_assets", "USD"},
		{"shares", fact("EntityCommonStockSharesOutstanding", "shares", "", "2024-09-28", 1, true), "shares_outstanding", "shares"},
		{"eps", fact("EarningsPerShareBasic", "USD/shares", "2023-10-01", "2024-09-28", 1, true), "eps_basic", "USD/shares"},
		{"capex", fact("PaymentsToAcquirePropertyPlantAndEquipment", "USD", "2023-10-01", "2024-09-28", 1, true), "capex", "USD"},
	}
	for _, tc := range cases {
		got := MapToCanonical(tc.in)
		if got == nil {
			t.Errorf("%s: MapToCanonical devolvió nil", tc.name)
			continue
		}
		if got.Canonical != tc.want || got.Unit != tc.wantUnit {
			t.Errorf("%s: got %s/%s want %s/%s", tc.name, got.Canonical, got.Unit, tc.want, tc.wantUnit)
		}
	}
}

func TestMapToCanonicalRejects(t *testing.T) {
	// Concepto sin mapeo.
	if got := MapToCanonical(fact("WOWUnknown", "USD", "2023-10-01", "2024-09-28", 1, true)); got != nil {
		t.Fatalf("concepto desconocido debería ser nil, got %+v", got)
	}
	// Unidad no esperada (mismo concepto, otra moneda).
	if got := MapToCanonical(fact("Revenues", "EUR", "2023-10-01", "2024-09-28", 1, true)); got != nil {
		t.Fatalf("unidad no esperada debería ser nil, got %+v", got)
	}
	// Assets (instant) reportado con start (duration) → rechazado.
	if got := MapToCanonical(fact("Assets", "USD", "2023-10-01", "2024-09-28", 1, true)); got != nil {
		t.Fatalf("periodo no esperado debería ser nil, got %+v", got)
	}
	// NetIncomeLoss (duration) reportado como instant → rechazado.
	if got := MapToCanonical(fact("NetIncomeLoss", "USD", "", "2024-09-28", 1, true)); got != nil {
		t.Fatalf("duration reportado como instant debería ser nil, got %+v", got)
	}
	// Fact sin valor (null) sí pasa el mapeo (estructura visible).
	got := MapToCanonical(fact("Revenues", "USD", "2023-10-01", "2024-09-28", 0, false))
	if got == nil || got.HasValue {
		t.Fatalf("fact null debería mapearse con HasValue=false, got %+v", got)
	}
}

func TestComputeDerived(t *testing.T) {
	end := time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)
	fy := 2024

	// Hechos canónicos de un mismo periodo FY2024.
	mk := func(canonical, unit, periodType string, val float64) CanonicalFact {
		start := (*time.Time)(nil)
		pt := "instant"
		if periodType == "duration" {
			s := time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC)
			start = &s
			pt = "duration"
		}
		return CanonicalFact{
			Canonical: canonical, Unit: unit, Value: val, HasValue: true, PeriodType: pt,
			StartDate: start, EndDate: end, FiscalYear: &fy, FiscalPeriod: "FY",
			FormType: "10-K", FilingDate: time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC),
			Accession: "accn-1",
		}
	}

	in := []CanonicalFact{
		mk("long_term_debt", "USD", "instant", 96662000000),
		mk("short_term_debt", "USD", "instant", 9967000000),
		mk("cash_and_equivalents", "USD", "instant", 29943000000),
		mk("operating_income", "USD", "duration", 123216000000),
		mk("depreciation_amortization", "USD", "duration", 11445000000),
		mk("operating_cash_flow", "USD", "duration", 118254000000),
		mk("capex", "USD", "duration", 9447000000),
	}

	derived := ComputeDerived(in)
	got := map[string]float64{}
	for _, d := range derived {
		got[d.Canonical] = d.Value
	}

	want := map[string]float64{
		"total_debt":     106629000000, // 96662000000 + 9967000000
		"net_debt":       76686000000,  // 106629000000 - 29943000000
		"ebitda":         134661000000, // 123216000000 + 11445000000
		"free_cash_flow": 108807000000, // 118254000000 - 9447000000
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("derivado %s: got %v want %v", k, got[k], v)
		}
	}
	if len(derived) != len(want) {
		t.Fatalf("se esperaban %d derivados, hay %d: %+v", len(want), len(derived), derived)
	}

	// Todos los derivados heredan el periodo.
	for _, d := range derived {
		if !d.EndDate.Equal(end) || d.FiscalPeriod != "FY" || d.FormType != "10-K" {
			t.Errorf("derivado %s con metadatos incorrectos: %+v", d.Canonical, d)
		}
	}
}

func TestComputeDerivedSkipsIncompletePeriods(t *testing.T) {
	end := time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)
	fy := 2024
	mk := func(canonical, periodType string, val float64) CanonicalFact {
		var start *time.Time
		if periodType == "duration" {
			s := time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC)
			start = &s
		}
		f := CanonicalFact{Canonical: canonical, Unit: "USD", Value: val, HasValue: true,
			PeriodType: periodType, StartDate: start, EndDate: end, FiscalYear: &fy,
			FiscalPeriod: "FY", FormType: "10-K"}
		return f
	}

	// Sin short_term_debt -> no hay total_debt ni net_debt. Sin capex -> no FCF.
	in := []CanonicalFact{
		mk("long_term_debt", "instant", 10),
		mk("cash_and_equivalents", "instant", 3),
		mk("operating_income", "duration", 5),
		mk("depreciation_amortization", "duration", 2),
		mk("operating_cash_flow", "duration", 7),
	}
	derived := ComputeDerived(in)
	for _, d := range derived {
		if d.Canonical == "total_debt" || d.Canonical == "net_debt" || d.Canonical == "free_cash_flow" {
			t.Errorf("no debería emitirse %s con inputs incompletos", d.Canonical)
		}
	}
	if len(derived) != 1 || derived[0].Canonical != "ebitda" {
		t.Fatalf("solo debería emitirse ebitda, hay %+v", derived)
	}

	// Sin valor (null) tampoco participa.
	inNull := []CanonicalFact{
		mk("operating_income", "duration", 0),
		mk("depreciation_amortization", "duration", 2),
	}
	inNull[0].HasValue = false
	if got := ComputeDerived(inNull); len(got) != 0 {
		t.Fatalf("inputs con null no deberían producir derivados, got %+v", got)
	}
}

func TestAAPLFixtureEndToEndExtraction(t *testing.T) {
	facts, err := ParseCompanyFacts(loadAAPLFixture(t))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	// Mapear y filtrar 10-K anual.
	var annual []CanonicalFact
	for _, f := range facts {
		if f.FormType != "10-K" {
			continue
		}
		if cf := MapToCanonical(f); cf != nil {
			annual = append(annual, *cf)
		}
	}

	derived := ComputeDerived(annual)

	// Valores esperados de AAPL FY2024 (10-K filed 2024-11-01, fixtura real).
	// El 10-K contiene comparativos de años anteriores también etiquetados con
	// fy=2024: el filtro exige el period_end exacto del FY2024.
	wantEnd := time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)
	want := map[string]float64{
		"ebitda":         134661000000,
		"free_cash_flow": 108807000000,
		"total_debt":     106629000000,
		"net_debt":       76686000000,
	}
	got := map[string]float64{}
	for _, d := range derived {
		if d.FiscalYear != nil && *d.FiscalYear == 2024 &&
			d.FiscalPeriod == "FY" && d.EndDate.Equal(wantEnd) {
			got[d.Canonical] = d.Value
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("AAPL FY2024 %s: got %v want %v", k, got[k], v)
		}
	}
}
