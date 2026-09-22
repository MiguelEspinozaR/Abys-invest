package edgar

import (
	"fmt"
	"time"
)

// CanonicalConcept describes how an XBRL concept maps to the canonical
// dictionary (plan §2.3).
type CanonicalConcept struct {
	Canonical  string // canonical name stored in fundamentals.concept
	Unit       string // expected unit ("USD", "shares", "USD/shares")
	PeriodType string // "duration", "instant" or "any"
	Priority   int    // orden en §2.3: 1 = preferido cuando hay variantes
}

// conceptMap is the XBRL concept -> canonical dictionary (plan §2.3).
// Concepts are keyed by name; the same name is unambiguous across the SEC
// namespaces used here ("us-gaap" and "dei", e.g.
// EntityCommonStockSharesOutstanding lives only in "dei").
// PeriodType "any" accepts both instant and duration facts (frames can vary).
// Priority refleja el orden en que §2.3 lista las variantes (1 = preferida).
var conceptMap = map[string]CanonicalConcept{
	// duration — estado de resultados
	"Revenues": {Canonical: "revenues", Unit: "USD", PeriodType: "duration", Priority: 1},
	"RevenueFromContractWithCustomerExcludingAssessedTax": {Canonical: "revenues", Unit: "USD", PeriodType: "duration", Priority: 2},
	"CostOfGoodsAndServicesSold":                          {Canonical: "cost_of_revenue", Unit: "USD", PeriodType: "duration", Priority: 1},
	"CostOfRevenue":                                       {Canonical: "cost_of_revenue", Unit: "USD", PeriodType: "duration", Priority: 2},
	"GrossProfit":                                         {Canonical: "gross_profit", Unit: "USD", PeriodType: "duration", Priority: 1},
	"OperatingIncomeLoss":                                 {Canonical: "operating_income", Unit: "USD", PeriodType: "duration", Priority: 1},
	"NetIncomeLoss":                                       {Canonical: "net_earnings", Unit: "USD", PeriodType: "duration", Priority: 1},
	"DepreciationDepletionAndAmortization":                {Canonical: "depreciation_amortization", Unit: "USD", PeriodType: "duration", Priority: 1},
	"DepreciationAndAmortization":                         {Canonical: "depreciation_amortization", Unit: "USD", PeriodType: "duration", Priority: 2},
	"PaymentsOfDividends":                                 {Canonical: "dividends_paid", Unit: "USD", PeriodType: "duration", Priority: 1},

	// instant — balance
	"Assets":                                 {Canonical: "total_assets", Unit: "USD", PeriodType: "instant", Priority: 1},
	"Liabilities":                            {Canonical: "total_liabilities", Unit: "USD", PeriodType: "instant", Priority: 1},
	"LongTermDebt":                           {Canonical: "long_term_debt", Unit: "USD", PeriodType: "instant", Priority: 1},
	"LongTermDebtAndCapitalLeaseObligations": {Canonical: "long_term_debt", Unit: "USD", PeriodType: "instant", Priority: 2},
	"ShortTermBorrowings":                    {Canonical: "short_term_debt", Unit: "USD", PeriodType: "instant", Priority: 1},
	"ShortTermDebt":                          {Canonical: "short_term_debt", Unit: "USD", PeriodType: "instant", Priority: 2},
	"CommercialPaper":                        {Canonical: "short_term_debt", Unit: "USD", PeriodType: "instant", Priority: 3},
	"StockholdersEquity":                     {Canonical: "shareholders_equity", Unit: "USD", PeriodType: "instant", Priority: 1},
	"CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents": {Canonical: "cash_and_equivalents", Unit: "USD", PeriodType: "instant", Priority: 1},
	"AssetsCurrent":      {Canonical: "current_assets", Unit: "USD", PeriodType: "instant", Priority: 1},
	"LiabilitiesCurrent": {Canonical: "current_liabilities", Unit: "USD", PeriodType: "instant", Priority: 1},

	// duration — cash flow
	"NetCashProvidedByUsedInOperatingActivities": {Canonical: "operating_cash_flow", Unit: "USD", PeriodType: "duration", Priority: 1},
	"PaymentsToAcquirePropertyPlantAndEquipment": {Canonical: "capex", Unit: "USD", PeriodType: "duration", Priority: 1},

	// instant — capital structure / per-share
	"EntityCommonStockSharesOutstanding": {Canonical: "shares_outstanding", Unit: "shares", PeriodType: "instant", Priority: 1},
	"EarningsPerShareBasic":              {Canonical: "eps_basic", Unit: "USD/shares", PeriodType: "duration", Priority: 1},
	"EarningsPerShareDiluted":            {Canonical: "eps_diluted", Unit: "USD/shares", PeriodType: "duration", Priority: 1},
}

// CanonicalConcepts returns a copy of the canonical dictionary.
func CanonicalConcepts() map[string]CanonicalConcept {
	out := make(map[string]CanonicalConcept, len(conceptMap))
	for k, v := range conceptMap {
		out[k] = v
	}
	return out
}

// CanonicalFact is an XBRL fact mapped to the canonical dictionary.
type CanonicalFact struct {
	Canonical     string
	SourceConcept string
	Namespace     string // XBRL namespace de origen ("us-gaap", "dei", ...)
	Priority      int    // para desempate determinista entre variantes XBRL
	Value         float64
	HasValue      bool
	Unit          string
	PeriodType    string
	StartDate     *time.Time
	EndDate       time.Time
	FiscalYear    *int
	FiscalPeriod  string
	FormType      string
	FilingDate    time.Time
	Accession     string
	RawValue      string
}

// MapToCanonical maps a raw XBRL fact to the canonical dictionary, or returns
// nil when the concept has no mapping or its unit/period does not match the
// dictionary expectation.
func MapToCanonical(f XBRLFact) *CanonicalFact {
	cc, ok := conceptMap[f.Concept]
	if !ok {
		return nil
	}
	if f.Unit != cc.Unit {
		return nil
	}

	pt := "instant"
	if f.StartDate != nil {
		pt = "duration"
	}
	if cc.PeriodType != "any" && pt != cc.PeriodType {
		return nil
	}

	return &CanonicalFact{
		Canonical:     cc.Canonical,
		SourceConcept: f.Concept,
		Namespace:     f.Namespace,
		Priority:      cc.Priority,
		Value:         f.Value,
		HasValue:      f.HasValue,
		Unit:          f.Unit,
		PeriodType:    pt,
		StartDate:     f.StartDate,
		EndDate:       f.EndDate,
		FiscalYear:    f.FiscalYear,
		FiscalPeriod:  f.FiscalPeriod,
		FormType:      f.FormType,
		FilingDate:    f.FilingDate,
		Accession:     f.Accession,
		RawValue:      f.RawValue,
	}
}

// periodKey identifies a canonical period: instant/duration + end + fy + fp.
func periodKey(f *CanonicalFact) string {
	fy := 0
	if f.FiscalYear != nil {
		fy = *f.FiscalYear
	}
	return fmt.Sprintf("%s|%s|%d|%s", f.PeriodType, f.EndDate.Format("2006-01-02"), fy, f.FiscalPeriod)
}

// derivedByPeriod groups canonical facts by period to compute derived concepts.
type derivedByPeriod map[string]map[string]CanonicalFact

// newerFact reports whether f should replace prev in a period group: lower
// dictionary priority first (variante preferida de §2.3), then most recent
// filing, then larger accession. Determinista.
func newerFact(f, prev *CanonicalFact) bool {
	if f.Priority != prev.Priority {
		return f.Priority < prev.Priority
	}
	if f.FilingDate.After(prev.FilingDate) {
		return true
	}
	if f.FilingDate.Equal(prev.FilingDate) && f.Accession > prev.Accession {
		return true
	}
	return false
}

// ComputeDerived calculates the derived concepts (plan §2.3):
//
//	total_debt     = long_term_debt + short_term_debt          (instant)
//	net_debt       = total_debt - cash_and_equivalents         (instant)
//	ebitda         = operating_income + depreciation_amortization (duration)
//	free_cash_flow = operating_cash_flow - capex               (duration)
//
// A derived row is emitted only when ALL its inputs are present in the same
// period; otherwise it is skipped (conservative, traceable). All components must
// carry a value (HasValue).
func ComputeDerived(facts []CanonicalFact) []CanonicalFact {
	groups := derivedByPeriod{}
	for i := range facts {
		f := &facts[i]
		if !f.HasValue {
			continue
		}
		key := periodKey(f)
		if groups[key] == nil {
			groups[key] = map[string]CanonicalFact{}
		}
		// Dedup por canonical dentro del periodo: se conserva el hecho del
		// filing más reciente (reexpresiones/restatements), con accession como
		// desempate. Determinista.
		if prev, dup := groups[key][f.Canonical]; !dup || newerFact(f, &prev) {
			groups[key][f.Canonical] = *f
		}
	}

	var derived []CanonicalFact
	for _, m := range groups {
		// La fuente de metadatos de cada derivado es fija, garantizando
		// resultados deterministas aunque el orden de iteración del mapa varíe.
		emit := func(canonical string, value float64, src *CanonicalFact, trace string) {
			d := CanonicalFact{
				Canonical:     canonical,
				SourceConcept: "derived:" + canonical,
				Namespace:     src.Namespace,
				Value:         value,
				HasValue:      true,
				Unit:          "USD",
				PeriodType:    src.PeriodType,
				StartDate:     src.StartDate,
				EndDate:       src.EndDate,
				FiscalYear:    src.FiscalYear,
				FiscalPeriod:  src.FiscalPeriod,
				FormType:      src.FormType,
				FilingDate:    src.FilingDate,
				Accession:     src.Accession,
				RawValue:      trace,
			}
			derived = append(derived, d)
		}

		lt, okLT := m["long_term_debt"]
		st, okST := m["short_term_debt"]
		if okLT && okST && lt.PeriodType == "instant" && st.PeriodType == "instant" {
			totalDebt := lt.Value + st.Value
			emit("total_debt", totalDebt, &lt,
				fmt.Sprintf("long_term_debt(%s)+short_term_debt(%s)", numTrace(lt), numTrace(st)))
			if cash, ok := m["cash_and_equivalents"]; ok && cash.HasValue {
				emit("net_debt", totalDebt-cash.Value, &cash,
					fmt.Sprintf("total_debt(%v)-cash_and_equivalents(%s)", totalDebt, numTrace(cash)))
			}
		}

		oi, okOI := m["operating_income"]
		da, okDA := m["depreciation_amortization"]
		if okOI && okDA && oi.PeriodType == "duration" && da.PeriodType == "duration" {
			emit("ebitda", oi.Value+da.Value, &oi,
				fmt.Sprintf("operating_income(%s)+depreciation_amortization(%s)", numTrace(oi), numTrace(da)))
		}

		ocf, okOCF := m["operating_cash_flow"]
		capex, okCap := m["capex"]
		if okOCF && okCap && ocf.PeriodType == "duration" && capex.PeriodType == "duration" {
			emit("free_cash_flow", ocf.Value-capex.Value, &ocf,
				fmt.Sprintf("operating_cash_flow(%s)-capex(%s)", numTrace(ocf), numTrace(capex)))
		}
	}
	return derived
}

// numTrace formats a numeric fact for traceability in derived raw_value.
func numTrace(f CanonicalFact) string {
	if f.RawValue != "" {
		return f.RawValue
	}
	return fmt.Sprintf("%v", f.Value)
}
