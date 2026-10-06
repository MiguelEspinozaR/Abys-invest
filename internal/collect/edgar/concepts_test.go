package edgar

import (
	"testing"
	"time"
)

func TestCanonicalConceptsDictionary(t *testing.T) {
	// El diccionario cubre exactamente los 23 conceptos canónicos: los 20 del
	// plan §2.3 más los 3 del trío M6c-T1 (Az1): interest_expense,
	// income_tax_expense y pretax_income.
	seen := map[string]bool{}
	for _, cc := range CanonicalConcepts() {
		seen[cc.Canonical] = true
	}
	if len(seen) != 23 {
		t.Fatalf("diccionario canónico: se esperaban 23 conceptos, hay %d", len(seen))
	}
	for _, required := range []string{
		"revenues", "cost_of_revenue", "gross_profit", "operating_income", "net_earnings",
		"depreciation_amortization", "total_assets", "total_liabilities", "long_term_debt",
		"short_term_debt", "shareholders_equity", "cash_and_equivalents", "operating_cash_flow",
		"capex", "dividends_paid", "shares_outstanding", "eps_basic", "eps_diluted",
		"current_assets", "current_liabilities",
		"interest_expense", "income_tax_expense", "pretax_income",
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

// Az9: el reescalado post-split de un hecho POR ACCIÓN es un NO-OP.
//
// El caso que importa: el 10-K del año siguiente reexpresa el EPS del ejercicio
// anterior en las acciones post-split. Si se aceptara, `eps_diluted` de 2024
// pasaría de 6.40 a 1.60 sin que nadie decidiera nada, y TODA la cadena de M6c
// (growth → quality → score) se calcularía sobre un número que no existió.
func TestAz9ReexpresionPostSplitDeEPSEsNoOp(t *testing.T) {
	end := time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)
	start := time.Date(2023, 9, 25, 0, 0, 0, 0, time.UTC)
	fy := 2024
	orig := CanonicalFact{
		Canonical: "eps_diluted", SourceConcept: "EarningsPerShareDiluted",
		Namespace: "us-gaap", Priority: 1, Value: 6.40, HasValue: true,
		Unit: "USD/shares", PeriodType: "duration", StartDate: &start, EndDate: end,
		FiscalYear: &fy, FiscalPeriod: "FY", FormType: "10-K",
		FilingDate: time.Date(2024, 10, 30, 0, 0, 0, 0, time.UTC),
		Accession:  "000-24-A",
	}
	// Split 4:1 reexpresado en el 10-K siguiente.
	restated := orig
	restated.Value = 1.60 // 6.40 / 4
	restated.FilingDate = time.Date(2025, 10, 31, 0, 0, 0, 0, time.UTC)
	restated.Accession = "000-25-B"

	if !isPostSplitReexpression(&orig, &restated) {
		t.Fatalf("un 4:1 sobre el mismo periodo debe detectarse como reexpresión post-split")
	}
	if newerFact(&restated, &orig) {
		t.Fatalf("la reexpresión post-split NO puede ganar: eps_diluted volvería 6.40 → 1.60")
	}

	facts := dedupeCanonical([]CanonicalFact{restated, orig})
	if len(facts) != 1 {
		t.Fatalf("se esperaba 1 hecho tras el dedupe, got %d", len(facts))
	}
	if facts[0].Value != 6.40 {
		t.Fatalf("el hecho persistido debe ser el ORIGINAL: %v", facts[0].Value)
	}
}

// Un RESTATEMENT real (cambia la ventana de presentación) SÍ sustituye: no es un
// split, es una corrección, y el dato bueno es el nuevo.
func TestAz9RestatementRealSiSustituye(t *testing.T) {
	end := time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)
	start := time.Date(2023, 9, 25, 0, 0, 0, 0, time.UTC)
	fy := 2024
	orig := CanonicalFact{
		Canonical: "eps_diluted", Priority: 1, Value: 6.40, HasValue: true,
		Unit: "USD/shares", PeriodType: "duration", StartDate: &start, EndDate: end,
		FiscalYear: &fy, FiscalPeriod: "FY", FilingDate: time.Date(2024, 10, 30, 0, 0, 0, 0, time.UTC),
	}
	restated := orig
	restated.Value = 6.10
	restated.FilingDate = time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)

	if isPostSplitReexpression(&orig, &restated) {
		t.Fatalf("una corrección de 6.40 a 6.10 no es un reescalado: no debe ser no-op")
	}
	if !newerFact(&restated, &orig) {
		t.Fatalf("un restatement real debe ganar (gana el filing más reciente)")
	}
}

// La regla es sólo de las magnitudes POR ACCIÓN: un hecho agregado reescalado en
// el 10-K siguiente es un error de la compañía y el filing nuevo es el bueno.
func TestAz9NoAplicaAHechosNoPorAccion(t *testing.T) {
	end := time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)
	fy := 2024
	orig := CanonicalFact{
		Canonical: "revenues", Priority: 1, Value: 400, HasValue: true,
		Unit: "USD", PeriodType: "duration", EndDate: end,
		FiscalYear: &fy, FiscalPeriod: "FY", FilingDate: time.Date(2024, 10, 30, 0, 0, 0, 0, time.UTC),
	}
	restated := orig
	restated.Value = 100 // exactamente /4
	restated.FilingDate = time.Date(2025, 10, 31, 0, 0, 0, 0, time.UTC)

	if !newerFact(&restated, &orig) {
		t.Fatalf("para revenues el filing más reciente debe ganar siempre")
	}
	if reexpressionAllowedFor("eps_diluted") || reexpressionAllowedFor("eps_basic") {
		t.Fatalf("eps_diluted/eps_basic no admiten reexpresión por acción")
	}
	if !reexpressionAllowedFor("revenues") {
		t.Fatalf("revenues sí admite sustitución por filing posterior")
	}
}

// ---------------------------------------------------------------------------
// M6c-T1 / Az1 — interest_expense, income_tax_expense y pretax_income
// ---------------------------------------------------------------------------
//
// Los tres canónicos que faltaban en el catálogo (por eso `interest_coverage`
// era nil en todo el universo y `tax_rate_source` no podía ser `derived`).
// Conteo de tags medido sobre `edgar_staging` (44 emisores, read-only):
// los 5 de interés, los 2 de impuesto y los 3 de preimpuesto están PRESENTES en
// el dato real, así que la cobertura del catálogo es la cobertura de la fuente.

// interestTaxTags es la lista canónica de Az1: (tag us-gaap, canónico,
// prioridad). Es la MISMA lista que la migración 019 documenta en
// xbrl_concept_map.notes y que fija ADR D31.
var interestTaxTags = []struct {
	tag        string
	canonical  string
	priority   int
	wantPeriod string
}{
	// interest_expense
	{"InterestExpense", "interest_expense", 1, "duration"},
	{"InterestExpenseNonoperating", "interest_expense", 2, "duration"},
	{"InterestAndDebtExpense", "interest_expense", 3, "duration"},
	{"InterestExpenseDebt", "interest_expense", 4, "duration"},
	{"InterestExpenseDebtExcludingAmortization", "interest_expense", 5, "duration"},
	// income_tax_expense
	{"IncomeTaxExpenseBenefit", "income_tax_expense", 1, "duration"},
	{"IncomeTaxExpenseBenefitContinuingOperations", "income_tax_expense", 2, "duration"},
	// pretax_income
	{"IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest", "pretax_income", 1, "duration"},
	{"IncomeLossFromContinuingOperationsBeforeIncomeTaxesMinorityInterestAndIncomeLossFromEquityMethodInvestments", "pretax_income", 2, "duration"},
	{"ResultsOfOperationsIncomeBeforeIncomeTaxes", "pretax_income", 3, "duration"},
}

func TestM6cT1InterestTaxTagsMapped(t *testing.T) {
	if len(interestTaxTags) != 10 {
		t.Fatalf("Az1 fija 10 tags, la lista del test tiene %d", len(interestTaxTags))
	}
	for _, tc := range interestTaxTags {
		// El hecho llega como duration (con start) en USD, que es como lo
		// publica todo emisor en su 10-K anual.
		got := MapToCanonical(fact(tc.tag, "USD", "2023-10-01", "2024-09-28", 1, true))
		if got == nil {
			t.Errorf("%s: MapToCanonical devolvió nil (el tag debe estar en el catálogo)", tc.tag)
			continue
		}
		if got.Canonical != tc.canonical || got.Priority != tc.priority ||
			got.PeriodType != tc.wantPeriod || got.Unit != "USD" {
			t.Errorf("%s: got canonical=%q priority=%d period=%q unit=%q; want %s/%d/%s/USD",
				tc.tag, got.Canonical, got.Priority, got.PeriodType, got.Unit,
				tc.canonical, tc.priority, tc.wantPeriod)
		}
		if got.SourceConcept != tc.tag {
			t.Errorf("%s: SourceConcept no preserva la trazabilidad XBRL, got %q", tc.tag, got.SourceConcept)
		}
		// Rechazo de periodo: los tres canónicos son duration, y un instant
		// con el mismo tag NO debe canonizarse (el dict no usa PeriodType "any").
		if instant := MapToCanonical(fact(tc.tag, "USD", "", "2024-09-28", 1, true)); instant != nil {
			t.Errorf("%s: reportado como instant debería ser nil, got %+v", tc.tag, instant)
		}
	}
}

// TestM6cT1ExcludedInterestTaxTags fija la EXCLUSIÓN de Az1: los tags que
// existen en el dato real (todos verificados en edgar_staging) y que NO deben
// canonizarse nunca. Cada exclusión es una decisión, no un descuido:
//
//   - InterestIncomeExpenseNet (7/44) es el resultado NETO de interés e
//     ingreso: puede ser NEGATIVO (WMT FY2024) y `interest_coverage` necesita
//     un gasto BRUTO. Mapearlo daría un ratio con el signo cambiado.
//   - …BeforeIncomeTaxesForeign (38/44) y …Domestic (36/44) son el desglose
//     GEOGRÁFICO del mismo total: elegir uno es un subtotal, sumarlos es el
//     total (que ya entra por su tag). Doble conteo.
//   - Deferred*/Current*/Federal*/StateAndLocal*IncomeTaxExpenseBenefit son
//     COMPONENTES del impuesto (corriente vs diferido, federal vs estatal):
//     mapear uno no es el impuesto total.
//   - EffectiveIncomeTaxRateContinuingOperations (41/44) viene en unidad
//     `pure`/`Rate`, no en USD: es una TASA declarada por la compañía. Se usa
//     como control de coherencia en el futuro score, nunca como fuente del
//     impuesto; y el catálogo solo acepta la unidad que declara cada fila.
func TestM6cT1ExcludedInterestTaxTags(t *testing.T) {
	excluded := []struct {
		name string
		tag  string
		unit string
	}{
		{"neto (signo ambiguo)", "InterestIncomeExpenseNet", "USD"},
		{"desglose geográfico foreign", "IncomeLossFromContinuingOperationsBeforeIncomeTaxesForeign", "USD"},
		{"desglose geográfico domestic", "IncomeLossFromContinuingOperationsBeforeIncomeTaxesDomestic", "USD"},
		{"componente diferido", "DeferredIncomeTaxExpenseBenefit", "USD"},
		{"componente corriente", "CurrentIncomeTaxExpenseBenefit", "USD"},
		{"componente federal", "FederalIncomeTaxExpenseBenefitContinuingOperations", "USD"},
		{"componente estatal", "StateAndLocalIncomeTaxExpenseBenefitContinuingOperations", "USD"},
		{"componente extranjero", "ForeignIncomeTaxExpenseBenefitContinuingOperations", "USD"},
		{"tasa declarada (pure)", "EffectiveIncomeTaxRateContinuingOperations", "pure"},
		{"tasa declarada (Rate)", "EffectiveIncomeTaxRateContinuingOperations", "Rate"},
	}
	dict := CanonicalConcepts()
	for _, tc := range excluded {
		if _, ok := dict[tc.tag]; ok {
			t.Errorf("%s: %q NO debe estar en el catálogo (excluido por Az1)", tc.name, tc.tag)
		}
		if got := MapToCanonical(fact(tc.tag, tc.unit, "2023-10-01", "2024-09-28", 1, true)); got != nil {
			t.Errorf("%s: MapToCanonical(%q, %q) devolvió %+v; debe ser nil",
				tc.name, tc.tag, tc.unit, got)
		}
	}

	// `InterestExpenseNonOperating` (O mayúscula) NO es un tag us-gaap: la
	// grafía real es `InterestExpenseNonoperating` (minúscula), verificada
	// contra los 44 payloads de edgar_staging (26 emisores con el tag real, 0
	// con la variante en mayúscula). Si alguien "corrige" el catálogo a la
	// grafía del plan, este test lo detecta en vez de dejar interest_expense
	// con 5 emisores menos de cobertura.
	if got := MapToCanonical(fact("InterestExpenseNonOperating", "USD", "2023-10-01", "2024-09-28", 1, true)); got != nil {
		t.Fatalf("InterestExpenseNonOperating (grafía inexistente) no debe canonizarse, got %+v", got)
	}
}

// TestM6cT1UnitMustBeUSD: los 10 tags son USD/duration. Cualquier otra unidad
// (ni siquiera `shares`/`USD/shares` de otros canónicos) no se canoniza: es la
// garanzía estructural de que una tasa (`pure`), un porcentaje o una moneda
// distinta no pueden entrar en `fundamentals.concept` como si fueran dólares.
func TestM6cT1UnitMustBeUSD(t *testing.T) {
	for _, tc := range interestTaxTags {
		for _, unit := range []string{"pure", "Rate", "EUR", "shares", "USD/shares", ""} {
			if got := MapToCanonical(fact(tc.tag, unit, "2023-10-01", "2024-09-28", 1, true)); got != nil {
				t.Errorf("%s con unit=%q no debe canonizarse, got %+v", tc.tag, unit, got)
			}
		}
	}
	// Y el caso espejo: el mismo tag con la unidad declarada sí entra. Sin esta
	// aserción el test anterior pasaría también con un catálogo vacío.
	got := MapToCanonical(fact("InterestExpenseNonoperating", "USD", "2023-10-01", "2024-09-28", 259e6, true))
	if got == nil || got.Canonical != "interest_expense" || got.Value != 259e6 {
		t.Fatalf("InterestExpenseNonoperating en USD debe canonizarse, got %+v", got)
	}
}

// TestM6cT1PriorityOrderInterestTax comprueba el ORDEN de Az1 con newerFact:
// menor número = tag preferido cuando dos variantes compiten por el MISMO
// (concepto, periodo).
func TestM6cT1PriorityOrderInterestTax(t *testing.T) {
	mk := func(tag string) *CanonicalFact {
		return MapToCanonical(fact(tag, "USD", "2023-10-01", "2024-09-28", 1, true))
	}
	interest := []string{
		"InterestExpense", "InterestExpenseNonoperating", "InterestAndDebtExpense",
		"InterestExpenseDebt", "InterestExpenseDebtExcludingAmortization",
	}
	for i := 0; i+1 < len(interest); i++ {
		winner, loser := mk(interest[i]), mk(interest[i+1])
		if !newerFact(winner, loser) {
			t.Errorf("%s (P%d) debe ganar a %s (P%d)", interest[i], i+1, interest[i+1], i+2)
		}
		if newerFact(loser, winner) {
			t.Errorf("%s (P%d) NO debe ganar a %s (P%d)", interest[i+1], i+2, interest[i], i+1)
		}
	}
	tax := []string{"IncomeTaxExpenseBenefit", "IncomeTaxExpenseBenefitContinuingOperations"}
	for i := 0; i+1 < len(tax); i++ {
		if newerFact(mk(tax[i+1]), mk(tax[i])) {
			t.Errorf("%s (P1) debe ganar a %s (P2)", tax[i], tax[i+1])
		}
	}
	pretax := []string{
		"IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest",
		"IncomeLossFromContinuingOperationsBeforeIncomeTaxesMinorityInterestAndIncomeLossFromEquityMethodInvestments",
		"ResultsOfOperationsIncomeBeforeIncomeTaxes",
	}
	for i := 0; i+1 < len(pretax); i++ {
		if newerFact(mk(pretax[i+1]), mk(pretax[i])) {
			t.Errorf("%s (P%d) debe ganar a %s (P%d)", pretax[i], i+1, pretax[i+1], i+2)
		}
	}
}

// TestM6cT1DedupeOneRowPerConceptAndPeriodKey documenta el comportamiento REAL
// del dedupe (W1: `normalize.go` NO se toca, no hace falta tocarlo).
//
// LO QUE ESTÁ GARANTIZADO: una fila por `(concepto, periodKey)`, donde
// `periodKey = periodType|end|fy|fp`. Es decir, la unicidad incluye la ETIQUETA
// DE EJERCICIO, no sólo la fecha de cierre.
//
// LO QUE NO ESTÁ GARANTIZADO, Y ANTES SE AFIRMABA QUE SÍ: una fila por
// `(concepto, periodo)`. Companyfacts reexpresa el mismo start/end del mismo
// hecho en cada 10-K posterior, y lo publica con el `fy`/`fp` de ESE filing: el
// mismo dato llega tres veces con tres `periodKey` distintos y sobrevive al
// dedupe tres veces. Medido sobre los 44 payloads de `edgar_staging`:
// NVDA `interest_expense`, rango 2023-01-30→2024-01-28 (257 M en los tres casos)
//
//	InterestExpense           fy2024  10-K 2024-02-21
//	InterestExpenseNonoperating fy2025  10-K 2025-02-26
//	InterestExpenseNonoperating fy2026  10-K 2026-02-25
//
// ⇒ 3 filas del mismo concepto y el mismo `period_end`. Este test fija ese
// comportamiento con los MISMOS datos, no con un caso que lo contradiga: por eso
// la unicidad se asserta por `(concepto, periodKey)` (que sí tiene dientes: un
// dedupe que agrupase sólo por `period_end` dejaría 1 fila y el test fallaría) y
// por separado se asserta que el `period_end` a secas devuelve 3 filas, cada una
// con su `fy` y con el tag que gana en su etiqueta de ejercicio.
func TestM6cT1DedupeOneRowPerConceptAndPeriodKey(t *testing.T) {
	// Mismo rango y mismo `period_end` que el caso real de NVDA, con el `fy`
	// variable: esto es lo que `fact()` no podía expresar (fija fy=2024).
	mk := func(tag string, end string, fy int, filed time.Time, accn string, val float64) CanonicalFact {
		f := fact(tag, "USD", "2023-01-30", end, val, true)
		f.FiscalYear = &fy
		f.FilingDate = filed
		f.Accession = accn
		cf := MapToCanonical(f)
		if cf == nil {
			t.Fatalf("MapToCanonical(%q) devolvió nil", tag)
		}
		return *cf
	}
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("fecha de test inválida %q: %v", s, err)
		}
		return d
	}

	in := []CanonicalFact{
		// --- interest_expense en 2024-01-28, una etiqueta de ejercicio por
		// fila. Orden de llegada ADVERSO en cada grupo: primero el P4.
		mk("InterestExpenseDebt", "2024-01-28", 2024, day("2024-02-21"), "0001045810-24-000017", 120),
		mk("InterestExpenseNonoperating", "2024-01-28", 2024, day("2024-02-21"), "0001045810-24-000017", 130),
		mk("InterestExpense", "2024-01-28", 2024, day("2024-02-21"), "0001045810-24-000017", 257),
		mk("InterestExpenseDebt", "2024-01-28", 2025, day("2025-02-26"), "0001045810-25-000023", 118),
		mk("InterestExpenseNonoperating", "2024-01-28", 2025, day("2025-02-26"), "0001045810-25-000023", 257),
		mk("InterestExpenseNonoperating", "2024-01-28", 2026, day("2026-02-25"), "0001045810-26-000009", 257),
		// Un periodo de verdad distinto (no una reexpresión): no compite con
		// ninguno de los anteriores y conserva su P1 propio.
		mk("InterestExpense", "2023-01-29", 2023, day("2023-02-24"), "0001045810-23-000017", 262),
		// --- El resto del trío, en el FY ancla (fy2025), cada uno con sus
		// variantes y también en orden adverso.
		mk("IncomeTaxExpenseBenefitContinuingOperations", "2024-01-28", 2025, day("2025-02-26"), "0001045810-25-000023", 21),
		mk("IncomeTaxExpenseBenefit", "2024-01-28", 2025, day("2025-02-26"), "0001045810-25-000023", 22),
		mk("ResultsOfOperationsIncomeBeforeIncomeTaxes", "2024-01-28", 2025, day("2025-02-26"), "0001045810-25-000023", 200),
		mk("IncomeLossFromContinuingOperationsBeforeIncomeTaxesMinorityInterestAndIncomeLossFromEquityMethodInvestments", "2024-01-28", 2025, day("2025-02-26"), "0001045810-25-000023", 205),
		mk("IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest", "2024-01-28", 2025, day("2025-02-26"), "0001045810-25-000023", 210),
	}

	ded := dedupeCanonical(in)
	got := map[string]map[string]CanonicalFact{} // canonical -> periodKey -> fact
	for _, f := range ded {
		k := periodKey(&f)
		if got[f.Canonical] == nil {
			got[f.Canonical] = map[string]CanonicalFact{}
		}
		// Unicidad REAL: por (concepto, periodKey). Con los fixtures de arriba
		// esta comprobación ya no es tautológica: si el dedupe agrupase sólo por
		// `period_end`, las 3 etiquetas de ejercicio de 2024-01-28 colapsarían
		// en una y el `Fatalf` saltaría.
		if prev, dup := got[f.Canonical][k]; dup {
			t.Fatalf("dos filas para (%s, %s): %s y %s", f.Canonical, k, prev.SourceConcept, f.SourceConcept)
		}
		got[f.Canonical][k] = f
	}

	// 3 etiquetas de ejercicio en 2024-01-28 + 1 periodo real distinto.
	if n := len(got["interest_expense"]); n != 4 {
		t.Fatalf("interest_expense: 4 periodKey esperados (fy2024/fy2025/fy2026 sobre 2024-01-28, más 2023-01-29), hay %d", n)
	}

	// Y el reverso, que es lo que la garantía NO dice: agrupado SÓLO por
	// `period_end`, interest_expense devuelve 3 filas para 2024-01-28.
	byPeriodEnd := map[string][]CanonicalFact{}
	for _, f := range ded {
		if f.Canonical != "interest_expense" {
			continue
		}
		k := f.EndDate.Format("2006-01-02")
		byPeriodEnd[k] = append(byPeriodEnd[k], f)
	}
	rows := byPeriodEnd["2024-01-28"]
	if len(rows) != 3 {
		t.Fatalf("interest_expense en 2024-01-28: 3 filas esperadas (3 etiquetas de ejercicio), hay %d", len(rows))
	}
	// Cada fila conserva SU `fy` y gana por SU prioridad: dentro de fy2024 gana
	// el P1 (InterestExpense), dentro de fy2025 gana el P2 (el P1 no viene en
	// ese filing) y fy2026 trae sólo el P2.
	want := []struct {
		fy        int
		tag       string
		priority  int
		val       float64
		explicaci string
	}{
		{2024, "InterestExpense", 1, 257, "fy2024: P1 presente en ese 10-K"},
		{2025, "InterestExpenseNonoperating", 2, 257, "fy2025: reexpresión sólo con el P2"},
		{2026, "InterestExpenseNonoperating", 2, 257, "fy2026: reexpresión sólo con el P2"},
	}
	for _, w := range want {
		var hit *CanonicalFact
		for i := range rows {
			if rows[i].FiscalYear != nil && *rows[i].FiscalYear == w.fy {
				hit = &rows[i]
			}
		}
		if hit == nil {
			t.Errorf("interest_expense 2024-01-28: falta la fila de fy%d (%s)", w.fy, w.explicaci)
			continue
		}
		if hit.SourceConcept != w.tag || hit.Priority != w.priority || hit.Value != w.val {
			t.Errorf("interest_expense 2024-01-28 fy%d debe venir de %s (P%d, %v) — %s —, got %s (P%d, %v)",
				w.fy, w.tag, w.priority, w.val, w.explicaci, hit.SourceConcept, hit.Priority, hit.Value)
		}
	}
	// El `period_end` de verdad distinto conserva su P1 propio.
	if f, ok := got["interest_expense"]["duration|2023-01-29|2023|FY"]; !ok || f.SourceConcept != "InterestExpense" || f.Value != 262 {
		t.Errorf("interest_expense 2023-01-29 debe conservar su P1 propio (262), got %+v", f)
	}

	// El resto del trío: una fila por (concepto, periodKey) en el FY ancla, y
	// gana la menor prioridad de su etiqueta de ejercicio.
	fyAncla := "duration|2024-01-28|2025|FY"
	if f := got["income_tax_expense"][fyAncla]; f.SourceConcept != "IncomeTaxExpenseBenefit" || f.Value != 22 {
		t.Errorf("income_tax_expense en el ancla debe venir de IncomeTaxExpenseBenefit (P1, 22), got %+v", f)
	}
	if f := got["pretax_income"][fyAncla]; f.SourceConcept != "IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest" || f.Value != 210 {
		t.Errorf("pretax_income en el ancla debe venir del P1 (210), got %+v", f)
	}
	// Los tres canónicos del FY ancla (CON su fy) quedan cubiertos: eso es
	// exactamente lo que W4/W5 alinearán, y por eso el par se lee con su fy/fp y
	// no sólo por `period_end`.
	for _, c := range []string{"interest_expense", "income_tax_expense", "pretax_income"} {
		if _, ok := got[c][fyAncla]; !ok {
			t.Errorf("%s falta en el FY ancla %s", c, fyAncla)
		}
	}
}
