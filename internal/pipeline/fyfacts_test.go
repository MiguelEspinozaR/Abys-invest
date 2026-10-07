package pipeline

import (
	"reflect"
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/storage"
)

// fyTestDate parses a YYYY-MM-DD date for the synthetic series.
func fyTestDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("fecha inválida %q: %v", s, err)
	}
	return d
}

// fyTestPoint builds one FYPoint: period_end, filing_date (empty = NULL) and value.
func fyTestPoint(t *testing.T, periodEnd, filingDate string, value float64) storage.FYPoint {
	t.Helper()
	p := storage.FYPoint{Value: value, PeriodEnd: fyTestDate(t, periodEnd)}
	if filingDate != "" {
		p.AvailableAt = fyTestDate(t, filingDate)
	}
	return p
}

const fyTestMaxAge = 550

// (a) Az2: the anchor is the most recent EBIT period and only the concepts of
// that same period_end are returned.
func TestAlignedFYFactsPicksNewestEBITAndSamePeriodConcepts(t *testing.T) {
	asOf := fyTestDate(t, "2026-01-01")
	series := map[string][]storage.FYPoint{
		conceptOperatingIncome: {
			fyTestPoint(t, "2023-12-31", "2024-02-01", 100),
			fyTestPoint(t, "2024-12-31", "2025-02-01", 200),
		},
		conceptInterestExpense:  {fyTestPoint(t, "2024-12-31", "2025-02-01", 10)},
		conceptIncomeTaxExpense: {fyTestPoint(t, "2024-12-31", "2025-02-01", 30)},
		conceptPretaxIncome:     {fyTestPoint(t, "2024-12-31", "2025-02-01", 150)},
	}
	got := alignedFYFacts("TEST", series, asOf, fyTestMaxAge)

	if want := fyTestDate(t, "2024-12-31"); !got.AnchorPeriodEnd.Equal(want) {
		t.Fatalf("ancla = %s, quiero %s", got.AnchorPeriodEnd, want)
	}
	if got.OperatingIncome == nil || *got.OperatingIncome != 200 {
		t.Fatalf("EBIT = %v, quiero 200 (el periodo más reciente, no el de 2023)", got.OperatingIncome)
	}
	if got.InterestExpense == nil || *got.InterestExpense != 10 {
		t.Fatalf("interest = %v, quiero 10", got.InterestExpense)
	}
	if got.IncomeTaxExpense == nil || *got.IncomeTaxExpense != 30 {
		t.Fatalf("income_tax = %v, quiero 30", got.IncomeTaxExpense)
	}
	if got.PretaxIncome == nil || *got.PretaxIncome != 150 {
		t.Fatalf("pretax = %v, quiero 150", got.PretaxIncome)
	}
	if r := got.Reasons(); len(r) != 0 {
		t.Fatalf("alineación completa no debe tener motivos: %v", r)
	}
}

// (b) CA-4 / AAPL: interest exists only in another fiscal year. The value must
// stay nil and the reason must be interest_period_mismatch (never a mix of EBIT
// FY2026 with interest FY2023).
func TestAlignedFYFactsInterestPeriodMismatch(t *testing.T) {
	asOf := fyTestDate(t, "2026-01-01")
	series := map[string][]storage.FYPoint{
		conceptOperatingIncome:  {fyTestPoint(t, "2025-09-30", "2025-11-01", 100)},
		conceptInterestExpense:  {fyTestPoint(t, "2023-09-30", "2023-11-01", 10)},
		conceptIncomeTaxExpense: {fyTestPoint(t, "2025-09-30", "2025-11-01", 20)},
		conceptPretaxIncome:     {fyTestPoint(t, "2025-09-30", "2025-11-01", 100)},
	}
	got := alignedFYFacts("AAPL", series, asOf, fyTestMaxAge)

	if got.InterestExpense != nil {
		t.Fatalf("interest de otro FY debe ser nil, no %v", *got.InterestExpense)
	}
	if got.InterestReason != FYReasonInterestPeriodMismatch {
		t.Fatalf("InterestReason = %q, quiero %q", got.InterestReason, FYReasonInterestPeriodMismatch)
	}
	if got.TaxRateReason != "" {
		t.Fatalf("TaxRateReason = %q, el par fiscal del ancla está completo", got.TaxRateReason)
	}
}

// (c) CA-4 / GE-JNJ: the newest anchor itself is outside the freshness window,
// so not even a complete pair is usable. Staleness dominates completeness.
func TestAlignedFYFactsStaleAnchor(t *testing.T) {
	asOf := fyTestDate(t, "2026-01-01")
	series := map[string][]storage.FYPoint{
		conceptOperatingIncome:  {fyTestPoint(t, "2012-12-31", "2013-02-01", 100)},
		conceptInterestExpense:  {fyTestPoint(t, "2012-12-31", "2013-02-01", 10)},
		conceptIncomeTaxExpense: {fyTestPoint(t, "2012-12-31", "2013-02-01", 20)},
		conceptPretaxIncome:     {fyTestPoint(t, "2012-12-31", "2013-02-01", 100)},
	}
	got := alignedFYFacts("GE", series, asOf, fyTestMaxAge)

	if got.InterestReason != FYReasonInterestStale {
		t.Fatalf("InterestReason = %q, quiero %q", got.InterestReason, FYReasonInterestStale)
	}
	if got.TaxRateReason != FYReasonTaxRateStale {
		t.Fatalf("TaxRateReason = %q, quiero %q", got.TaxRateReason, FYReasonTaxRateStale)
	}
	// The values belong to the anchor and are still returned for logging, but
	// the reason tells W5 not to use them.
	if got.AnchorPeriodEnd.IsZero() || got.OperatingIncome == nil || got.InterestExpense == nil {
		t.Fatalf("el ancla y sus valores deben seguir presentes para el log: %+v", got)
	}
	if r := got.Reasons(); len(r) != 2 {
		t.Fatalf("Reasons() = %v, quiero los dos motivos de staleness", r)
	}
}

// (d) CAT/GEV: no EBIT at all -> no_anchor; EBIT fresh but interest absent from
// every fiscal year -> interest_unavailable.
func TestAlignedFYFactsNoAnchorAndInterestUnavailable(t *testing.T) {
	asOf := fyTestDate(t, "2026-01-01")

	t.Run("no_anchor sin operating_income", func(t *testing.T) {
		series := map[string][]storage.FYPoint{
			conceptInterestExpense:  {fyTestPoint(t, "2024-12-31", "2025-02-01", 10)},
			conceptIncomeTaxExpense: {fyTestPoint(t, "2024-12-31", "2025-02-01", 30)},
			conceptPretaxIncome:     {fyTestPoint(t, "2024-12-31", "2025-02-01", 150)},
		}
		got := alignedFYFacts("GEV", series, asOf, fyTestMaxAge)
		if !got.AnchorPeriodEnd.IsZero() {
			t.Fatalf("sin EBIT el ancla debe ser cero, got %s", got.AnchorPeriodEnd)
		}
		if got.InterestReason != FYReasonNoAnchor || got.TaxRateReason != FYReasonNoAnchor {
			t.Fatalf("motivos = %q/%q, quiero no_anchor/no_anchor", got.InterestReason, got.TaxRateReason)
		}
		if r := got.Reasons(); !reflect.DeepEqual(r, []string{FYReasonNoAnchor}) {
			t.Fatalf("Reasons() = %v, quiero [no_anchor] sin duplicar", r)
		}
	})

	t.Run("interest_unavailable con EBIT fresco", func(t *testing.T) {
		series := map[string][]storage.FYPoint{
			conceptOperatingIncome:  {fyTestPoint(t, "2024-12-31", "2025-02-01", 100)},
			conceptIncomeTaxExpense: {fyTestPoint(t, "2024-12-31", "2025-02-01", 30)},
			conceptPretaxIncome:     {fyTestPoint(t, "2024-12-31", "2025-02-01", 150)},
		}
		got := alignedFYFacts("CAT", series, asOf, fyTestMaxAge)
		if got.InterestExpense != nil {
			t.Fatalf("interest ausente debe ser nil, no %v", *got.InterestExpense)
		}
		if got.InterestReason != FYReasonInterestUnavailable {
			t.Fatalf("InterestReason = %q, quiero %q", got.InterestReason, FYReasonInterestUnavailable)
		}
		if got.TaxRateReason != "" {
			t.Fatalf("el par fiscal del ancla está completo, TaxRateReason = %q", got.TaxRateReason)
		}
	})
}

// (e) Az3: the tax rate needs the COMPLETE pair at the anchor. Either half
// missing means tax_rate_unavailable, not a rate computed from a nil.
func TestAlignedFYFactsTaxRatePairIncomplete(t *testing.T) {
	asOf := fyTestDate(t, "2026-01-01")
	base := func() map[string][]storage.FYPoint {
		return map[string][]storage.FYPoint{
			conceptOperatingIncome: {fyTestPoint(t, "2024-12-31", "2025-02-01", 100)},
			conceptInterestExpense: {fyTestPoint(t, "2024-12-31", "2025-02-01", 10)},
		}
	}

	t.Run("sin income_tax_expense", func(t *testing.T) {
		series := base()
		series[conceptPretaxIncome] = []storage.FYPoint{fyTestPoint(t, "2024-12-31", "2025-02-01", 150)}
		got := alignedFYFacts("MCD", series, asOf, fyTestMaxAge)
		if got.TaxRateReason != FYReasonTaxRateUnavailable {
			t.Fatalf("TaxRateReason = %q, quiero %q", got.TaxRateReason, FYReasonTaxRateUnavailable)
		}
		if got.IncomeTaxExpense != nil {
			t.Fatalf("income_tax ausente debe ser nil, no %v", *got.IncomeTaxExpense)
		}
		if got.InterestReason != "" {
			t.Fatalf("el par EBIT/interés está completo, InterestReason = %q", got.InterestReason)
		}
	})

	t.Run("sin pretax_income", func(t *testing.T) {
		series := base()
		series[conceptIncomeTaxExpense] = []storage.FYPoint{fyTestPoint(t, "2024-12-31", "2025-02-01", 30)}
		got := alignedFYFacts("SPG", series, asOf, fyTestMaxAge)
		if got.TaxRateReason != FYReasonTaxRateUnavailable {
			t.Fatalf("TaxRateReason = %q, quiero %q", got.TaxRateReason, FYReasonTaxRateUnavailable)
		}
		if got.PretaxIncome != nil {
			t.Fatalf("pretax ausente debe ser nil, no %v", *got.PretaxIncome)
		}
	})
}

// (f) No look-ahead: a fact filed AFTER asOf is invisible even when the storage
// filter is bypassed (the unit test feeds the series directly).
func TestAlignedFYFactsIgnoresFactsFiledAfterAsOf(t *testing.T) {
	asOf := fyTestDate(t, "2025-06-01")
	series := map[string][]storage.FYPoint{
		conceptOperatingIncome: {
			fyTestPoint(t, "2024-12-31", "2025-02-01", 100), // public before asOf
			fyTestPoint(t, "2025-03-31", "2025-08-01", 999), // filed after asOf
		},
		conceptInterestExpense:  {fyTestPoint(t, "2024-12-31", "2025-02-01", 10)},
		conceptIncomeTaxExpense: {fyTestPoint(t, "2024-12-31", "2025-02-01", 20)},
		conceptPretaxIncome:     {fyTestPoint(t, "2024-12-31", "2025-02-01", 100)},
	}
	got := alignedFYFacts("AAPL", series, asOf, fyTestMaxAge)

	if want := fyTestDate(t, "2024-12-31"); !got.AnchorPeriodEnd.Equal(want) {
		t.Fatalf("ancla = %s, quiero %s (el hecho futuro no debe usarse)", got.AnchorPeriodEnd, want)
	}
	if got.OperatingIncome == nil || *got.OperatingIncome != 100 {
		t.Fatalf("EBIT = %v, quiero 100 y no el 999 del hecho posterior al as_of", got.OperatingIncome)
	}
	if r := got.Reasons(); len(r) != 0 {
		t.Fatalf("con el hecho futuro ignorado la alineación es completa: %v", r)
	}
}

// (g) Determinism: unordered and duplicated series must give the same result.
// The conflicting duplicate at the same period_end is resolved by latest filing
// (the storage "most recent restatement wins"), independent of slice order.
func TestAlignedFYFactsDeterministicOnUnorderedDuplicatedSeries(t *testing.T) {
	asOf := fyTestDate(t, "2026-01-01")
	ordered := map[string][]storage.FYPoint{
		conceptOperatingIncome: {
			fyTestPoint(t, "2024-12-31", "2025-02-01", 100),
			fyTestPoint(t, "2024-12-31", "2025-08-01", 111), // restatement wins
		},
		conceptInterestExpense: {
			fyTestPoint(t, "2023-12-31", "2024-02-01", 9),
			fyTestPoint(t, "2024-12-31", "2025-02-01", 10),
			fyTestPoint(t, "2024-12-31", "2025-02-01", 10), // exact duplicate
			fyTestPoint(t, "2024-12-31", "2025-08-01", 12), // restatement wins
		},
		conceptIncomeTaxExpense: {fyTestPoint(t, "2024-12-31", "2025-02-01", 30)},
		conceptPretaxIncome:     {fyTestPoint(t, "2024-12-31", "2025-02-01", 150)},
	}
	shuffled := map[string][]storage.FYPoint{
		conceptOperatingIncome: {
			fyTestPoint(t, "2024-12-31", "2025-08-01", 111),
			fyTestPoint(t, "2024-12-31", "2025-02-01", 100),
		},
		conceptInterestExpense: {
			fyTestPoint(t, "2024-12-31", "2025-08-01", 12),
			fyTestPoint(t, "2024-12-31", "2025-02-01", 10),
			fyTestPoint(t, "2024-12-31", "2025-02-01", 10),
			fyTestPoint(t, "2023-12-31", "2024-02-01", 9),
		},
		conceptIncomeTaxExpense: {fyTestPoint(t, "2024-12-31", "2025-02-01", 30)},
		conceptPretaxIncome:     {fyTestPoint(t, "2024-12-31", "2025-02-01", 150)},
	}

	a := alignedFYFacts("TEST", ordered, asOf, fyTestMaxAge)
	b := alignedFYFacts("TEST", shuffled, asOf, fyTestMaxAge)
	whileA := alignedFYFacts("TEST", ordered, asOf.Add(72*time.Hour), fyTestMaxAge)

	if !reflect.DeepEqual(a, b) {
		t.Fatalf("resultado no determinista:\n a=%+v\n b=%+v", a, b)
	}
	if !reflect.DeepEqual(a, whileA) {
		t.Fatalf("el resultado no depende del instante exacto dentro de la ventana:\n a=%+v\n whileA=%+v", a, whileA)
	}
	if a.OperatingIncome == nil || *a.OperatingIncome != 111 {
		t.Fatalf("EBIT = %v, quiero 111 (restatement más reciente)", a.OperatingIncome)
	}
	if a.InterestExpense == nil || *a.InterestExpense != 12 {
		t.Fatalf("interest = %v, quiero 12 (restatement más reciente)", a.InterestExpense)
	}
}
