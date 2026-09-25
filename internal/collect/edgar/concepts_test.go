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

// TestM4cConceptVariants cubre el fix del diccionario (plan M4c, B1): las
// variantes XBRL modernas deben mapear al canónico con su prioridad y periodo.
//
//	PaymentsToAcquireProductiveAssets       -> capex (P2, duration)
//	PaymentsToAcquireOtherProductiveAssets  -> capex (P3, duration)
//	DebtCurrent                             -> short_term_debt (P4, instant)
//	LongTermDebtCurrent                     -> short_term_debt (P5, instant)
func TestM4cConceptVariants(t *testing.T) {
	cases := []struct {
		name     string
		concept  string
		period   string // "duration" | "instant"
		want     string
		wantPri  int
		wantUnit string
	}{
		{"productive assets", "PaymentsToAcquireProductiveAssets", "duration", "capex", 2, "USD"},
		{"other productive assets", "PaymentsToAcquireOtherProductiveAssets", "duration", "capex", 3, "USD"},
		{"debt current", "DebtCurrent", "instant", "short_term_debt", 4, "USD"},
		{"long-term debt current", "LongTermDebtCurrent", "instant", "short_term_debt", 5, "USD"},
	}
	for _, tc := range cases {
		var f XBRLFact
		if tc.period == "duration" {
			f = fact(tc.concept, tc.wantUnit, "2023-10-01", "2024-09-28", 1, true)
		} else {
			f = fact(tc.concept, tc.wantUnit, "", "2024-09-28", 1, true)
		}
		got := MapToCanonical(f)
		if got == nil {
			t.Fatalf("%s: MapToCanonical devolvió nil", tc.name)
		}
		if got.Canonical != tc.want || got.Priority != tc.wantPri || got.PeriodType != tc.period || got.Unit != tc.wantUnit {
			t.Fatalf("%s: got canonical=%q priority=%d period=%q unit=%q; want %s/%d/%s/%s",
				tc.name, got.Canonical, got.Priority, got.PeriodType, got.Unit,
				tc.want, tc.wantPri, tc.period, tc.wantUnit)
		}
	}

	// Rechazo de periodo: las variantes instant no aceptan duration y viceversa.
	if got := MapToCanonical(fact("DebtCurrent", "USD", "2023-10-01", "2024-09-28", 1, true)); got != nil {
		t.Fatalf("DebtCurrent (instant) reportado con start debería ser nil, got %+v", got)
	}
	if got := MapToCanonical(fact("PaymentsToAcquireProductiveAssets", "USD", "", "2024-09-28", 1, true)); got != nil {
		t.Fatalf("PaymentsToAcquireProductiveAssets (duration) reportado como instant debería ser nil, got %+v", got)
	}
}

// TestM4cNewerFactBetweenVariants verifica que newerFact desempata por
// prioridad del diccionario entre variantes del mismo periodo (M4c B1):
// menor prioridad numérica = preferida.
func TestM4cNewerFactBetweenVariants(t *testing.T) {
	mk := func(concept string, start string) *CanonicalFact {
		return MapToCanonical(fact(concept, "USD", start, "2024-09-28", 1, true))
	}

	// capex: PP&E (P1) gana a ProductiveAssets (P2), que gana a Other (P3).
	ppe := mk("PaymentsToAcquirePropertyPlantAndEquipment", "2023-10-01")
	prod := mk("PaymentsToAcquireProductiveAssets", "2023-10-01")
	other := mk("PaymentsToAcquireOtherProductiveAssets", "2023-10-01")
	if newerFact(prod, ppe) {
		t.Fatal("PP&E (P1) debe ganar a ProductiveAssets (P2)")
	}
	if newerFact(other, prod) {
		t.Fatal("ProductiveAssets (P2) debe ganar a OtherProductiveAssets (P3)")
	}

	// short_term_debt: ShortTermBorrowings (P1) gana a DebtCurrent (P4), que
	// gana a LongTermDebtCurrent (P5) como fallback.
	stb := mk("ShortTermBorrowings", "")
	dc := mk("DebtCurrent", "")
	ltdc := mk("LongTermDebtCurrent", "")
	if newerFact(dc, stb) {
		t.Fatal("ShortTermBorrowings (P1) debe ganar a DebtCurrent (P4)")
	}
	if newerFact(ltdc, dc) {
		t.Fatal("DebtCurrent (P4) debe ganar a LongTermDebtCurrent (P5)")
	}
}

// TestM4cDedupeShortTermDebtVariants verifica el comportamiento end-to-end en
// dedupeCanonical: cuando un issuer solo reporta DebtCurrent + un instant de
// otro concepto dentro del mismo periodo, el canónico short_term_debt del
// periodo conserva la variante de mayor prioridad disponible (P4 > P5) y el
// derivado total_debt/net_debt se computa con ese valor.
func TestM4cDedupeShortTermDebtVariants(t *testing.T) {
	end := time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)
	fy := 2024
	base := func(canonical, concept string, val float64) CanonicalFact {
		cf := MapToCanonical(fact(concept, "USD", "", "2024-09-28", val, true))
		cf.EndDate = end
		cf.FiscalYear = &fy
		cf.FiscalPeriod = "FY"
		cf.Canonical = canonical
		return *cf
	}

	in := []CanonicalFact{
		base("long_term_debt", "LongTermDebt", 90),
		base("short_term_debt", "DebtCurrent", 10),
		base("short_term_debt", "LongTermDebtCurrent", 7),
		base("cash_and_equivalents", "CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents", 25),
	}
	ded := dedupeCanonical(in)
	var st *CanonicalFact
	for i := range ded {
		if ded[i].Canonical == "short_term_debt" {
			st = &ded[i]
		}
	}
	if st == nil {
		t.Fatal("sin short_term_debt tras dedupe")
	}
	if st.SourceConcept != "DebtCurrent" {
		t.Fatalf("DebtCurrent (P4) debe prevalecer sobre LongTermDebtCurrent (P5), got %s", st.SourceConcept)
	}
	if st.Value != 10 {
		t.Fatalf("valor esperado 10 (DebtCurrent), got %v", st.Value)
	}

	derived := ComputeDerived(ded)
	var totalDebt, netDebt *CanonicalFact
	for i := range derived {
		switch derived[i].Canonical {
		case "total_debt":
			totalDebt = &derived[i]
		case "net_debt":
			netDebt = &derived[i]
		}
	}
	if totalDebt == nil || totalDebt.Value != 100 {
		t.Fatalf("total_debt esperado 100 (90+10), got %+v", totalDebt)
	}
	if netDebt == nil || netDebt.Value != 75 {
		t.Fatalf("net_debt esperado 75 (100-25), got %+v", netDebt)
	}
}

// TestM4cFixOrclConceptVariants cubre la extensión del fix M4c (hallazgo del
// orquestador sobre ORCL): ORCL reporta dos conceptos GAAP modernos que no
// estaban en conceptMap y dejaban net_debt en None.
//
//	CashAndCashEquivalentsAtCarryingValue -> cash_and_equivalents (P2, instant)
//	LongTermNotesAndLoans                 -> long_term_debt (P2, instant)
func TestM4cFixOrclConceptVariants(t *testing.T) {
	cases := []struct {
		name     string
		concept  string
		want     string
		wantPri  int
		wantUnit string
	}{
		{"cash and equivalents carrying", "CashAndCashEquivalentsAtCarryingValue", "cash_and_equivalents", 2, "USD"},
		{"long-term notes and loans", "LongTermNotesAndLoans", "long_term_debt", 2, "USD"},
	}
	for _, tc := range cases {
		got := MapToCanonical(fact(tc.concept, tc.wantUnit, "", "2024-09-28", 1, true))
		if got == nil {
			t.Fatalf("%s: MapToCanonical devolvió nil", tc.name)
		}
		if got.Canonical != tc.want || got.Priority != tc.wantPri || got.PeriodType != "instant" || got.Unit != tc.wantUnit {
			t.Fatalf("%s: got canonical=%q priority=%d period=%q unit=%q; want %s/%d/instant/%s",
				tc.name, got.Canonical, got.Priority, got.PeriodType, got.Unit,
				tc.want, tc.wantPri, tc.wantUnit)
		}
	}

	// Rechazo de periodo: ambas son instant y no aceptan duration.
	if got := MapToCanonical(fact("CashAndCashEquivalentsAtCarryingValue", "USD", "2023-10-01", "2024-09-28", 1, true)); got != nil {
		t.Fatalf("CashAndCashEquivalentsAtCarryingValue (instant) con start debería ser nil, got %+v", got)
	}
	if got := MapToCanonical(fact("LongTermNotesAndLoans", "USD", "2023-10-01", "2024-09-28", 1, true)); got != nil {
		t.Fatalf("LongTermNotesAndLoans (instant) con start debería ser nil, got %+v", got)
	}
}

// TestM4cFixOrclPriorityAndDedupe verifica que las variantes P2 de ORCL no
// alteran el canonical P1 existente: para el mismo periodo, newerFact prefiere
// el tag clásico (P1) sobre el moderno (P2) por prioridad, y dedupeCanonical
// conserva la variante P1 como fuente del canónico (AAPL/MSFT/etc. intactos).
func TestM4cFixOrclPriorityAndDedupe(t *testing.T) {
	mk := func(concept string) *CanonicalFact {
		return MapToCanonical(fact(concept, "USD", "", "2024-09-28", 1, true))
	}

	// cash_and_equivalents: el tag clásico (P1) gana al moderno (P2).
	legacy := mk("CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents")
	modernCash := mk("CashAndCashEquivalentsAtCarryingValue")
	if newerFact(modernCash, legacy) {
		t.Fatal("CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents (P1) debe ganar a CashAndCashEquivalentsAtCarryingValue (P2)")
	}

	// long_term_debt: LongTermDebt (P1) gana a LongTermNotesAndLoans (P2).
	ltd := mk("LongTermDebt")
	notes := mk("LongTermNotesAndLoans")
	if newerFact(notes, ltd) {
		t.Fatal("LongTermDebt (P1) debe ganar a LongTermNotesAndLoans (P2)")
	}

	// dedupe end-to-end: con ambos tags en el mismo periodo el canónico
	// conserva la fuente P1 y net_debt se computa con esos valores.
	end := time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)
	fy := 2024
	base := func(canonical, concept string, val float64) CanonicalFact {
		cf := MapToCanonical(fact(concept, "USD", "", "2024-09-28", val, true))
		cf.EndDate = end
		cf.FiscalYear = &fy
		cf.FiscalPeriod = "FY"
		cf.Canonical = canonical
		return *cf
	}
	in := []CanonicalFact{
		base("long_term_debt", "LongTermNotesAndLoans", 122),
		base("long_term_debt", "LongTermDebt", 90),
		base("short_term_debt", "ShortTermBorrowings", 10),
		base("cash_and_equivalents", "CashAndCashEquivalentsAtCarryingValue", 31),
		base("cash_and_equivalents", "CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents", 25),
	}
	ded := dedupeCanonical(in)
	byCanon := map[string]CanonicalFact{}
	for _, f := range ded {
		byCanon[f.Canonical] = f
	}
	if got := byCanon["long_term_debt"]; got.SourceConcept != "LongTermDebt" || got.Value != 90 {
		t.Fatalf("long_term_debt tras dedupe debe venir de LongTermDebt (P1) con 90, got %+v", got)
	}
	if got := byCanon["cash_and_equivalents"]; got.SourceConcept != "CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents" || got.Value != 25 {
		t.Fatalf("cash_and_equivalents tras dedupe debe venir del tag P1 con 25, got %+v", got)
	}

	// net_debt se computa con los valores P1 del dedupe: (90+10)-25 = 75.
	derived := ComputeDerived(ded)
	for _, d := range derived {
		if d.Canonical == "net_debt" && d.Value != 75 {
			t.Fatalf("net_debt esperado 75 con los valores P1, got %v", d.Value)
		}
	}
	var netDebt *CanonicalFact
	for i := range derived {
		if derived[i].Canonical == "net_debt" {
			netDebt = &derived[i]
		}
	}
	if netDebt == nil {
		t.Fatal("sin net_debt tras ComputeDerived")
	}
}

// TestMapToCanonicalNamespaceTrace verifica (C001) que el namespace XBRL de
// origen se propaga al hecho canónico, dando trazabilidad a los conceptos dei.
func TestMapToCanonicalNamespaceTrace(t *testing.T) {
	sf := fact("EntityCommonStockSharesOutstanding", "shares", "", "2024-09-28", 1, true)
	sf.Namespace = "dei"
	got := MapToCanonical(sf)
	if got == nil {
		t.Fatal("MapToCanonical devolvió nil para EntityCommonStockSharesOutstanding")
	}
	if got.Canonical != "shares_outstanding" {
		t.Fatalf("canonical inesperado: %q", got.Canonical)
	}
	if got.Namespace != "dei" {
		t.Fatalf("namespace no propagado: got %q want %q", got.Namespace, "dei")
	}

	// Los conceptos us-gaap conservan su namespace.
	rf := fact("Revenues", "USD", "2023-10-01", "2024-09-28", 1, true)
	rf.Namespace = "us-gaap"
	if got := MapToCanonical(rf); got == nil || got.Namespace != "us-gaap" {
		t.Fatalf("namespace us-gaap no propagado: %+v", got)
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
