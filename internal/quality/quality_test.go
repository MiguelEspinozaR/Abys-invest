package quality

import (
	"math"
	"testing"
	"time"
)

func ptr(v float64) *float64 { return &v }

var asOf = time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)

// aaplInputs are the real AAPL FY2025 figures quoted by the plan (§0.2), plus
// the growth row and a five-year annual series. They are the reference case of
// the smoke (K.4 steps 3, 4, 5, 6).
func aaplInputs() Inputs {
	series := func(vs ...float64) []SeriesPoint {
		out := make([]SeriesPoint, 0, len(vs))
		for i, v := range vs {
			out = append(out, SeriesPoint{PeriodEnd: asOf.AddDate(-len(vs)+i+1, 0, 0), Value: v})
		}
		return out
	}
	return Inputs{
		Ticker: "AAPL", AsOf: asOf,
		Financials: map[string]*float64{
			"operating_income":     ptr(133.050),
			"shareholders_equity":  ptr(73.733),
			"total_debt":           ptr(98.657),
			"cash_and_equivalents": ptr(35.934),
			"net_debt":             ptr(62.723),
			"ebitda":               ptr(144.748),
			"revenues":             ptr(416.161),
			"free_cash_flow":       ptr(98.767),
			"net_earnings":         ptr(112.010),
		},
		Price: 341.07, SharesOutstanding: ptr(14.77e9),
		Growth: GrowthInputs{
			FCFCAGR3y: ptr(-3.9451), FCFCAGR5y: ptr(6.1267),
			EPSCAGR3y: ptr(6.8807), EPSCAGR5y: ptr(17.8618), RevenueCAGR3y: ptr(1.8125),
			Confidence: "high",
		},
		Series: map[string][]SeriesPoint{
			SeriesEPSDiluted:   series(5.61, 6.13, 6.08, 6.08, 7.46),
			SeriesFreeCashFlow: series(111.0, 110.0, 99.6, 108.8, 98.767),
		},
	}
}

func scoreOf(t *testing.T, r Result, name string) float64 {
	t.Helper()
	sub := r.SubScores[name]
	if sub == nil {
		t.Fatalf("sub-bloque %q ausente", name)
	}
	if sub.Score == nil {
		t.Fatalf("sub-bloque %q sin score (coverage %.3f)", name, sub.Coverage)
	}
	return *sub.Score
}

func metricOf(t *testing.T, r Result, sub, metric string) Metric {
	t.Helper()
	for _, m := range r.SubScores[sub].Metrics {
		if m.Name == metric {
			return m
		}
	}
	t.Fatalf("métrica %q ausente del sub-bloque %q", metric, sub)
	return Metric{}
}

// --- smoke de referencia (K.4 pasos 3-6) --------------------------------

func TestAAPLReferenceCase(t *testing.T) {
	res := Calculate(aaplInputs(), DefaultConfig())
	if res.Score == nil {
		t.Fatalf("AAPL debe tener quality: %v", res.Reasons)
	}
	if math.Abs(res.Coverage-16.0/17.0) > 1e-12 {
		t.Fatalf("coverage = %v, esperado 16/17", res.Coverage)
	}
	if res.Confidence != ConfidenceMedium {
		t.Fatalf("confidence = %q, esperado medium", res.Confidence)
	}
	if !hasReason(res.Reasons, ReasonTaxRateConfigured) {
		t.Fatalf("debe figurar tax_rate_configured: %v", res.Reasons)
	}
	wantROIC := (133.050 * 0.79) / (73.733 + 98.657 - 35.934)
	if got := *res.Metrics[MetricROIC]; math.Abs(got-wantROIC) > 1e-9 {
		t.Fatalf("roic = %.10f, esperado %.10f", got, wantROIC)
	}
	if got := *res.Metrics[MetricNetDebtToEBITDA]; math.Abs(got-62.723/144.748) > 1e-9 {
		t.Fatalf("net_debt_to_ebitda = %v", got)
	}
	if got := *res.Metrics[MetricFCFToDebt]; math.Abs(got-98.767/98.657) > 1e-9 {
		t.Fatalf("fcf_to_debt = %v", got)
	}
	if res.Metrics[MetricInterestCoverage] != nil {
		t.Fatal("interest_coverage debe ser nil en M6c")
	}
	if got := res.SubScores[SubSolvency].Coverage; math.Abs(got-2.0/3.0) > 1e-12 {
		t.Fatalf("debt_solvency coverage = %v, esperado 2/3", got)
	}
	if got := metricOf(t, res, SubSolvency, MetricInterestCoverage).Reason; got != ReasonInterestExpenseMissing {
		t.Fatalf("razón de interest_coverage = %q", got)
	}
	if res.AvailableAt != asOf || res.ModelVersion != ModelVersion {
		t.Fatalf("procedencia: %v / %v", res.AvailableAt, res.ModelVersion)
	}
	// Los cinco sub-bloques son válidos (plan A7).
	for _, name := range SubBlockNames() {
		if res.SubScores[name].Score == nil {
			t.Fatalf("sub-bloque %q debería ser válido con los datos de AAPL", name)
		}
		if res.SubScores[name].Weight != 0.20 {
			t.Fatalf("sub-peso %q = %v, esperado 0.20", name, res.SubScores[name].Weight)
		}
	}
}

func hasReason(rs []string, want string) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}

// --- ROIC / ROE (A2) -----------------------------------------------------

func TestROICFormulaAndRefusals(t *testing.T) {
	in := aaplInputs()
	res := Calculate(in, DefaultConfig())
	got := *res.Metrics[MetricROIC]
	nopat := 133.050 * 0.79
	invested := 73.733 + 98.657 - 35.934
	if math.Abs(got-nopat/invested) > 1e-12 {
		t.Fatalf("roic = %v, esperado %v", got, nopat/invested)
	}

	t.Run("invested_capital <= 0", func(t *testing.T) {
		bad := aaplInputs()
		bad.Financials["shareholders_equity"] = ptr(1.0)
		bad.Financials["total_debt"] = ptr(10.0)
		bad.Financials["cash_and_equivalents"] = ptr(50.0) // invested = -39
		res := Calculate(bad, DefaultConfig())
		if res.Metrics[MetricROIC] != nil {
			t.Fatalf("roic debe ser nil con capital invertido negativo: %v", res.Metrics[MetricROIC])
		}
		if got := metricOf(t, res, SubProfitability, MetricROIC).Reason; got != ReasonNonPositiveInvestedCap {
			t.Fatalf("razón = %q", got)
		}
	})

	t.Run("sin total_debt no se sustituye por net_debt", func(t *testing.T) {
		bad := aaplInputs()
		delete(bad.Financials, "total_debt")
		res := Calculate(bad, DefaultConfig())
		if res.Metrics[MetricROIC] != nil {
			t.Fatalf("roic no puede salir de net_debt: %v", res.Metrics[MetricROIC])
		}
		if got := metricOf(t, res, SubProfitability, MetricROIC).Reason; got != ReasonDebtUnavailable {
			t.Fatalf("razón = %q", got)
		}
		// fcf_to_debt tampoco, pero net_debt_to_ebitda sigue vivo (usa net_debt).
		if res.Metrics[MetricNetDebtToEBITDA] == nil {
			t.Fatal("net_debt_to_ebitda no depende de total_debt")
		}
		if res.Metrics[MetricFCFToDebt] != nil {
			t.Fatal("fcf_to_debt necesita total_debt")
		}
	})
}

func TestROICTaxRateIsConfigurable(t *testing.T) {
	in := aaplInputs()
	cfg := DefaultConfig()
	cfg.TaxRate = 0
	res := Calculate(in, cfg)
	want := 133.050 / (73.733 + 98.657 - 35.934)
	if got := *res.Metrics[MetricROIC]; math.Abs(got-want) > 1e-12 {
		t.Fatalf("con tax 0 roic = %v, esperado %v", got, want)
	}
	cfg.TaxRate = 40
	res = Calculate(in, cfg)
	want = 133.050 * 0.6 / (73.733 + 98.657 - 35.934)
	if got := *res.Metrics[MetricROIC]; math.Abs(got-want) > 1e-12 {
		t.Fatalf("con tax 40 roic = %v, esperado %v", got, want)
	}
}

func TestROERefusesNonPositiveEquity(t *testing.T) {
	in := aaplInputs()
	in.Financials["shareholders_equity"] = ptr(0)
	res := Calculate(in, DefaultConfig())
	if res.Metrics[MetricROE] != nil {
		t.Fatalf("roe con equity 0 debe ser nil: %v", res.Metrics[MetricROE])
	}
	if got := metricOf(t, res, SubProfitability, MetricROE).Reason; got != ReasonNonPositiveEquity {
		t.Fatalf("razón = %q", got)
	}
	// ROIC sobrevive: su denominador incluye la deuda.
	if res.Metrics[MetricROIC] == nil {
		t.Fatal("roic no depende de equity > 0")
	}
}

func TestNonFiniteInputsAreRefused(t *testing.T) {
	in := aaplInputs()
	in.Financials["operating_income"] = ptr(math.NaN())
	res := Calculate(in, DefaultConfig())
	if res.Metrics[MetricROIC] != nil {
		t.Fatal("un NaN no puede entrar en un ratio")
	}
	for _, name := range MetricNames() {
		if v, ok := res.Metrics[name]; ok && (math.IsNaN(*v) || math.IsInf(*v, 0)) {
			t.Fatalf("métrica %s con valor no finito: %v", name, *v)
		}
	}
}

// --- márgenes y yields (A4) ---------------------------------------------

func TestMarginsAndYields(t *testing.T) {
	res := Calculate(aaplInputs(), DefaultConfig())
	if got, want := *res.Metrics[MetricOperatingMargin], 133.050/416.161; math.Abs(got-want) > 1e-12 {
		t.Fatalf("operating_margin = %v, esperado %v", got, want)
	}
	if got, want := *res.Metrics[MetricFCFMargin], 98.767/416.161; math.Abs(got-want) > 1e-12 {
		t.Fatalf("fcf_margin = %v, esperado %v", got, want)
	}
	// market_cap = price × shares = 341.07 × 14.77e9
	wantYield := 98.767 / (341.07 * 14.77e9)
	if got := *res.Metrics[MetricFCFYield]; math.Abs(got-wantYield) > 1e-12 {
		t.Fatalf("fcf_yield = %v, esperado %v", got, wantYield)
	}
}

func TestMarginsRefuseNonPositiveDenominator(t *testing.T) {
	in := aaplInputs()
	in.Financials["revenues"] = ptr(0)
	delete(in.Financials, "revenue")
	res := Calculate(in, DefaultConfig())
	for _, name := range []string{MetricOperatingMargin, MetricFCFMargin} {
		if res.Metrics[name] != nil {
			t.Fatalf("%s con revenue 0 debe ser nil", name)
		}
		if got := metricOf(t, res, SubMargins, name).Reason; got != ReasonNonPositiveRevenue {
			t.Fatalf("%s razón = %q", name, got)
		}
	}
	// Sin market cap no hay fcf_yield, pero el resto del sub-bloque sigue vivo.
	in2 := aaplInputs()
	in2.SharesOutstanding = nil
	in2.MarketCap = nil
	res2 := Calculate(in2, DefaultConfig())
	if res2.Metrics[MetricFCFYield] != nil {
		t.Fatal("fcf_yield sin market cap debe ser nil")
	}
	if res2.SubScores[SubMargins].Score == nil {
		t.Fatal("el sub-bloque de márgenes sigue siendo válido con 2 de 3")
	}
	if res2.Metrics[MetricOperatingMargin] == nil {
		t.Fatal("operating_margin no depende del market cap")
	}
}

// --- crecimiento (A3, ADR D7) ------------------------------------------

func TestGrowthMetricsAreReadNotRecomputed(t *testing.T) {
	res := Calculate(aaplInputs(), DefaultConfig())
	if got := *res.Metrics[MetricFCFCAGR3y]; got != -3.9451 {
		t.Fatalf("fcf_cagr_3y = %v, debe leerse literal", got)
	}
	if got := *res.Metrics[MetricEPSCAGR5y]; got != 17.8618 {
		t.Fatalf("eps_cagr_5y = %v", got)
	}
	// Un CAGR NEGATIVO es válido y con score (no es un dato ausente).
	m := metricOf(t, res, SubGrowth, MetricFCFCAGR3y)
	if m.Value == nil || m.Score == nil || m.Reason != "" {
		t.Fatalf("CAGR negativo debe ser válido: %+v", m)
	}
}

func TestGrowthUnreliable(t *testing.T) {
	in := aaplInputs()
	in.Growth.FCFCAGR3y = nil
	in.Growth.Confidence = "low"
	in.Growth.EPSCAGR3y = ptr(6.8807) // presente pero con confidence low
	res := Calculate(in, DefaultConfig())
	for _, name := range []string{MetricFCFCAGR3y, MetricEPSCAGR3y} {
		if res.Metrics[name] != nil {
			t.Fatalf("%s no debe ser válido: %v", name, res.Metrics[name])
		}
		if got := metricOf(t, res, SubGrowth, name).Reason; got != ReasonGrowthUnreliable {
			t.Fatalf("%s razón = %q", name, got)
		}
	}
	// growth_confidence es una propiedad de la FILA: con "low" el Growth Engine
	// vuelve sobre su propia base, así que TODOS los CAGRs de esa fila dejan de
	// ser válidos. Aceptar los que "casualmente" tienen número metería calidad de
	// una medición que el motor de crecimiento ya marcó como no fiable.
	for _, name := range []string{MetricFCFCAGR5y, MetricEPSCAGR5y, MetricRevenueCAGR3y} {
		if res.Metrics[name] != nil {
			t.Fatalf("%s no debe ser válido con growth_confidence=low", name)
		}
	}
	if res.SubScores[SubGrowth].Score != nil {
		t.Fatal("el sub-bloque de crecimiento debe quedar inválido")
	}
}

// --- estabilidad (A5) ---------------------------------------------------

func TestStabilityMetrics(t *testing.T) {
	res := Calculate(aaplInputs(), DefaultConfig())
	if got := *res.Metrics[MetricPositiveEPSYears]; got != 5 {
		t.Fatalf("positive_eps_years = %v, esperado 5", got)
	}
	if got := *res.Metrics[MetricPositiveFCFYears]; got != 5 {
		t.Fatalf("positive_fcf_years = %v, esperado 5", got)
	}
	eps := []float64{5.61, 6.13, 6.08, 6.08, 7.46}
	m, _ := mean(eps)
	sd, _ := stddev(eps, m)
	if got := *res.Metrics[MetricEPSVolatility]; math.Abs(got-sd/math.Abs(m)) > 1e-12 {
		t.Fatalf("eps_volatility = %v, esperado %v", got, sd/math.Abs(m))
	}
	for _, name := range []string{MetricEPSVolatility, MetricFCFVolatility} {
		if v := res.Metrics[name]; v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0)) {
			t.Fatalf("%s no finito", name)
		}
	}
}

func TestStabilityInsufficientHistory(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		in := aaplInputs()
		in.Series = map[string][]SeriesPoint{
			SeriesEPSDiluted:   {{PeriodEnd: asOf, Value: 1}},
			SeriesFreeCashFlow: {{PeriodEnd: asOf, Value: 1}},
		}
		in.Series[SeriesEPSDiluted] = in.Series[SeriesEPSDiluted][:0]
		in.Series[SeriesFreeCashFlow] = in.Series[SeriesFreeCashFlow][:0]
		if n > 0 {
			for i := 0; i < n; i++ {
				in.Series[SeriesEPSDiluted] = append(in.Series[SeriesEPSDiluted],
					SeriesPoint{PeriodEnd: asOf.AddDate(-i, 0, 0), Value: float64(i + 1)})
				in.Series[SeriesFreeCashFlow] = append(in.Series[SeriesFreeCashFlow],
					SeriesPoint{PeriodEnd: asOf.AddDate(-i, 0, 0), Value: float64(i + 1)})
			}
		}
		res := Calculate(in, DefaultConfig())
		if res.SubScores[SubStability].Score != nil {
			t.Fatalf("con %d puntos el sub-bloque de estabilidad debe ser nil", n)
		}
		for _, name := range []string{MetricPositiveEPSYears, MetricPositiveFCFYears, MetricEPSVolatility, MetricFCFVolatility} {
			if res.Metrics[name] != nil {
				t.Fatalf("%s con %d puntos debe ser nil", name, n)
			}
			if got := metricOf(t, res, SubStability, name).Reason; got != ReasonInsufficientHistory {
				t.Fatalf("%s razón = %q (n=%d)", name, got, n)
			}
		}
	}
}

func TestStabilityZeroMeanNeverInf(t *testing.T) {
	in := aaplInputs()
	in.Series[SeriesEPSDiluted] = []SeriesPoint{
		{PeriodEnd: asOf.AddDate(-3, 0, 0), Value: 1},
		{PeriodEnd: asOf.AddDate(-2, 0, 0), Value: -1},
		{PeriodEnd: asOf.AddDate(-1, 0, 0), Value: 1},
		{PeriodEnd: asOf, Value: -1},
	}
	res := Calculate(in, DefaultConfig())
	if res.Metrics[MetricEPSVolatility] != nil {
		t.Fatalf("volatilidad con media 0 debe ser nil: %v", res.Metrics[MetricEPSVolatility])
	}
	if got := metricOf(t, res, SubStability, MetricEPSVolatility).Reason; got != ReasonZeroMeanBase {
		t.Fatalf("razón = %q", got)
	}
	if !hasReason(res.Reasons, "stability_unavailable") && res.SubScores[SubStability].Score == nil {
		t.Fatalf("el sub-bloque de estabilidad ya no tiene volatilidades: %v", res.Reasons)
	}
}

func TestStabilitySeriesIsSortedAndDeduplicated(t *testing.T) {
	in := aaplInputs()
	// El mismo fiscal year dos veces, en orden inverso: el loader lo deduplica,
	// pero el motor no debe depender de ese orden.
	in.Series[SeriesEPSDiluted] = []SeriesPoint{
		{PeriodEnd: asOf, Value: 7.46},
		{PeriodEnd: asOf.AddDate(-1, 0, 0), Value: 6.08},
		{PeriodEnd: asOf.AddDate(-2, 0, 0), Value: 6.08},
		{PeriodEnd: asOf.AddDate(-3, 0, 0), Value: 6.13},
		{PeriodEnd: asOf.AddDate(-4, 0, 0), Value: 5.61},
	}
	res := Calculate(in, DefaultConfig())
	if got := *res.Metrics[MetricPositiveEPSYears]; got != 5 {
		t.Fatalf("positive_eps_years = %v, esperado 5", got)
	}
	shuffled := Calculate(Inputs{
		AsOf: asOf,
		Series: map[string][]SeriesPoint{SeriesEPSDiluted: {
			{PeriodEnd: asOf.AddDate(-2, 0, 0), Value: 6.08},
			{PeriodEnd: asOf, Value: 7.46},
			{PeriodEnd: asOf.AddDate(-4, 0, 0), Value: 5.61},
			{PeriodEnd: asOf.AddDate(-1, 0, 0), Value: 6.08},
			{PeriodEnd: asOf.AddDate(-3, 0, 0), Value: 6.13},
		}},
	}, DefaultConfig())
	if *shuffled.Metrics[MetricEPSVolatility] != *res.Metrics[MetricEPSVolatility] {
		t.Fatal("el orden de la serie no puede cambiar el resultado")
	}
}

// --- solvencia (A6) ------------------------------------------------------

func TestSolvencyMetrics(t *testing.T) {
	res := Calculate(aaplInputs(), DefaultConfig())
	if got, want := *res.Metrics[MetricNetDebtToEBITDA], 62.723/144.748; math.Abs(got-want) > 1e-12 {
		t.Fatalf("net_debt_to_ebitda = %v, esperado %v", got, want)
	}
	if got, want := *res.Metrics[MetricFCFToDebt], 98.767/98.657; math.Abs(got-want) > 1e-12 {
		t.Fatalf("fcf_to_debt = %v, esperado %v", got, want)
	}
	// interest_coverage: nil + razón, NUNCA 50 (Az2b / ADR D6).
	if res.Metrics[MetricInterestCoverage] != nil {
		t.Fatalf("interest_coverage = %v, debe ser nil", res.Metrics[MetricInterestCoverage])
	}
	m := metricOf(t, res, SubSolvency, MetricInterestCoverage)
	if m.Score != nil || m.Reason != ReasonInterestExpenseMissing {
		t.Fatalf("interest_coverage: %+v", m)
	}
	// de_ratio es SECUNDARIA: se expone, no puntúa y no cuenta para coverage.
	d := metricOf(t, res, SubSolvency, MetricDERatio)
	if d.Value == nil || d.Score != nil {
		t.Fatalf("de_ratio debe tener valor y ningún score: %+v", d)
	}
	if want := 98.657 / 73.733; math.Abs(*d.Value-want) > 1e-12 {
		t.Fatalf("de_ratio = %v, esperado %v", *d.Value, want)
	}
	if contains(MetricNames(), MetricDERatio) {
		t.Fatal("de_ratio NO puede contar como métrica aplicable")
	}
	if len(ApplicableMetrics()) != 17 {
		t.Fatalf("métricas aplicables = %d, esperado 17 (§13/§14)", len(ApplicableMetrics()))
	}
}

func TestSolvencyRefusesNonPositiveDenominators(t *testing.T) {
	in := aaplInputs()
	in.Financials["ebitda"] = ptr(0)
	in.Financials["total_debt"] = ptr(0)
	res := Calculate(in, DefaultConfig())
	if res.Metrics[MetricNetDebtToEBITDA] != nil {
		t.Fatal("ebitda 0 debe hacer nil la métrica")
	}
	if got := metricOf(t, res, SubSolvency, MetricNetDebtToEBITDA).Reason; got != ReasonNonPositiveEBITDA {
		t.Fatalf("razón = %q", got)
	}
	if res.Metrics[MetricFCFToDebt] != nil {
		t.Fatal("total_debt 0 debe hacer nil la métrica")
	}
	if got := metricOf(t, res, SubSolvency, MetricFCFToDebt).Reason; got != ReasonNonPositiveTotalDebt {
		t.Fatalf("razón = %q", got)
	}
}

func TestInterestCoverageFormulaExistsForM6cT1(t *testing.T) {
	in := aaplInputs()
	in.Financials["interest_expense"] = ptr(12.0)
	res := Calculate(in, DefaultConfig())
	if res.Metrics[MetricInterestCoverage] == nil {
		t.Fatal("con interest_expense la métrica debe calcularse (ruta de M6c-T1)")
	}
	if got, want := *res.Metrics[MetricInterestCoverage], 133.050/12.0; math.Abs(got-want) > 1e-12 {
		t.Fatalf("interest_coverage = %v, esperado %v", got, want)
	}
	if math.Abs(res.Coverage-1.0) > 1e-12 {
		t.Fatalf("con las 17 métricas la coverage debe ser 1: %v", res.Coverage)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// --- agregación, coverage y confidence (A7) -----------------------------

func TestCoverageInvariant(t *testing.T) {
	cases := map[string]Inputs{
		"completo": aaplInputs(),
		"vacío":    {},
		"solo roic": {Financials: map[string]*float64{
			"operating_income": ptr(10), "total_debt": ptr(10),
			"shareholders_equity": ptr(10), "cash_and_equivalents": ptr(0),
		}},
	}
	for name, in := range cases {
		in.AsOf = asOf
		res := Calculate(in, DefaultConfig())
		// Invariante §31/ADR D4: Coverage > 0 ⟺ Score != nil.
		if (res.Coverage > 0) != (res.Score != nil) {
			t.Fatalf("%s: coverage=%v score=%v rompen la invariante", name, res.Coverage, res.Score)
		}
		if res.Score != nil && (math.IsNaN(*res.Score) || math.IsInf(*res.Score, 0)) {
			t.Fatalf("%s: score no finito", name)
		}
		if res.Score != nil && (*res.Score < 0 || *res.Score > 100) {
			t.Fatalf("%s: score fuera de [0,100]: %v", name, *res.Score)
		}
		// Cada métrica con score es válida y viceversa.
		for _, sub := range res.SubScores {
			valid := 0
			for _, m := range sub.Metrics {
				if m.Name == MetricDERatio {
					continue
				}
				if m.Score != nil {
					valid++
					if m.Value == nil {
						t.Fatalf("%s: %s con score y sin valor", name, m.Name)
					}
				}
			}
			if valid > 0 && sub.Score == nil {
				t.Fatalf("%s: sub-bloque %s con métricas válidas y sin score", name, sub.Name)
			}
			if valid == 0 && sub.Score != nil {
				t.Fatalf("%s: sub-bloque %s con score sin métricas válidas", name, sub.Name)
			}
		}
	}
}

func TestEmptyInputsAreNilNotNeutral(t *testing.T) {
	res := Calculate(Inputs{AsOf: asOf}, DefaultConfig())
	if res.Score != nil {
		t.Fatalf("sin datos el score debe ser nil, no 50: %v", *res.Score)
	}
	if res.Coverage != 0 {
		t.Fatalf("coverage = %v", res.Coverage)
	}
	if res.Confidence != ConfidenceLow {
		t.Fatalf("confidence = %q", res.Confidence)
	}
	if !hasReason(res.Reasons, ReasonNoValidMetric) {
		t.Fatalf("razones: %v", res.Reasons)
	}
}

func TestConfidenceCapWithConfiguredTax(t *testing.T) {
	in := aaplInputs() // coverage 16/17 ≈ 0.941 → base high
	cfg := DefaultConfig()
	res := Calculate(in, cfg)
	if res.Confidence != ConfidenceMedium {
		t.Fatalf("tope ADR D26: %q", res.Confidence)
	}
	// Con un tax rate DERIVED (fuera de M6c, pero el motor lo soporta) el tope
	// desaparece y la confianza sube a high. `derived` exige la tasa (P2-1): sin
	// TaxRate el motor se niega a declarar una derivación y el tope se mantiene.
	derived := in
	derived.TaxRate = ptr(21)
	derived.TaxRateSource = "derived"
	res = Calculate(derived, cfg)
	if res.Confidence != ConfidenceHigh {
		t.Fatalf("con tax derivada la confianza debe ser high: %q", res.Confidence)
	}
	if hasReason(res.Reasons, ReasonTaxRateConfigured) {
		t.Fatal("sin tope no debe aparecer el motivo tax_rate_configured")
	}
}

func TestConfidenceNeverResurrectedByHighScore(t *testing.T) {
	in := Inputs{AsOf: asOf, Financials: map[string]*float64{
		// Solo dos métricas, pero con valores excelentes.
		"operating_income":    ptr(1000),
		"total_debt":          ptr(1),
		"shareholders_equity": ptr(1),
		"net_earnings":        ptr(1000),
	}}
	res := Calculate(in, DefaultConfig())
	if res.Score == nil {
		t.Fatal("debe haber score")
	}
	if *res.Score < 90 {
		t.Fatalf("score %v por debajo de lo esperado para datos excelentes", *res.Score)
	}
	if res.Confidence != ConfidenceLow {
		t.Fatalf("con coverage 2/17 la confianza debe seguir siendo low: %q", res.Confidence)
	}
}

func TestSubWeightsChangeTheAggregate(t *testing.T) {
	in := aaplInputs()
	base := Calculate(in, DefaultConfig())

	cfg := DefaultConfig()
	cfg.SubWeights[SubProfitability] = 0.60
	cfg.SubWeights[SubGrowth] = 0.10
	cfg.SubWeights[SubMargins] = 0.10
	cfg.SubWeights[SubStability] = 0.10
	cfg.SubWeights[SubSolvency] = 0.10
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	alt := Calculate(in, cfg)
	if math.Abs(*base.Score-*alt.Score) < 1e-9 {
		t.Fatalf("mover sub-pesos debe mover el score (%v vs %v)", *base.Score, *alt.Score)
	}
	// Y con el sub-bloque de solvencia nil, el agregado renormaliza por los demás.
	in2 := aaplInputs()
	delete(in2.Financials, "net_debt")
	delete(in2.Financials, "total_debt")
	in2.Financials["ebitda"] = nil
	res := Calculate(in2, DefaultConfig())
	if res.SubScores[SubSolvency].Score != nil {
		t.Fatal("sin deuda el sub-bloque de solvencia debe ser nil")
	}
	if res.Score == nil {
		t.Fatal("el resto de sub-bloques mantiene el score")
	}
	if res.Coverage != res.SubScores[SubProfitability].Coverage {
		// sanity: la cobertura global sigue contando las 17 aplicables
		if res.Coverage >= 1 {
			t.Fatalf("coverage global = %v", res.Coverage)
		}
	}
	_ = scoreOf(t, res, SubProfitability)
}

func TestDeterminism(t *testing.T) {
	in := aaplInputs()
	cfg := DefaultConfig()
	a := Calculate(in, cfg)
	b := Calculate(in, cfg)
	if a.Score == nil || b.Score == nil || *a.Score != *b.Score {
		t.Fatalf("dos ejecuciones difieren: %v %v", a.Score, b.Score)
	}
	if a.Coverage != b.Coverage || a.Confidence != b.Confidence {
		t.Fatal("coverage/confidence no deterministas")
	}
	if len(a.Reasons) != len(b.Reasons) {
		t.Fatal("razones no deterministas")
	}
	for i := range a.Reasons {
		if a.Reasons[i] != b.Reasons[i] {
			t.Fatalf("razón %d difiere: %q vs %q", i, a.Reasons[i], b.Reasons[i])
		}
	}
}

// --- bandas (A8) ---------------------------------------------------------

func TestBandBoundaries(t *testing.T) {
	b := Band{Good: 10, Bad: 0, HigherIsBetter: true}
	for _, tc := range []struct{ v, want float64 }{
		{10, 100}, {20, 100}, {0, 0}, {-5, 0}, {5, 50},
	} {
		if got := b.Score(tc.v); math.Abs(got-tc.want) > 1e-12 {
			t.Fatalf("banda(%.1f) = %v, esperado %v", tc.v, got, tc.want)
		}
	}
	l := Band{Good: 1, Bad: 4, HigherIsBetter: false}
	for _, tc := range []struct{ v, want float64 }{
		{1, 100}, {0.5, 100}, {4, 0}, {5, 0}, {2.5, 50},
	} {
		if got := l.Score(tc.v); math.Abs(got-tc.want) > 1e-12 {
			t.Fatalf("banda menor(%.2f) = %v, esperado %v", tc.v, got, tc.want)
		}
	}
	if got := b.Score(math.NaN()); got != 0 {
		t.Fatalf("NaN debe puntuar 0, no propagarse: %v", got)
	}
	if got := b.Score(math.Inf(1)); got != 0 {
		t.Fatalf("Inf debe puntuar 0: %v", got)
	}
	// Banda degenerada: paso determinista, nunca división por cero.
	d := Band{Good: 5, Bad: 5, HigherIsBetter: true}
	if got := d.Score(5); got != 100 {
		t.Fatalf("degenerada en el corte = %v", got)
	}
	if got := d.Score(4.9); got != 0 {
		t.Fatalf("degenerada bajo el corte = %v", got)
	}
}

func TestBandValidation(t *testing.T) {
	if err := DefaultBands().Validate(); err != nil {
		t.Fatalf("las bandas por defecto deben ser válidas: %v", err)
	}
	for _, bad := range []Band{
		{Good: 1, Bad: 1, HigherIsBetter: true},
		{Good: 1, Bad: 5, HigherIsBetter: true},  // good < bad con "mayor es mejor"
		{Good: 5, Bad: 1, HigherIsBetter: false}, // good > bad con "menor es mejor"
		{Good: math.NaN(), Bad: 1, HigherIsBetter: true},
	} {
		if err := bad.Valid(); err == nil {
			t.Fatalf("banda inválida aceptada: %+v", bad)
		}
	}
}

func TestDefaultBandsAnchorRealAAPLValues(t *testing.T) {
	b := DefaultBands()
	// Los tres anclajes que el plan fija explícitamente (A8).
	if got := b.ROIC.Score(0.7702); got != 100 {
		t.Fatalf("roic 0.7702 → %v, esperado 100", got)
	}
	if got := b.NetDebtToEBITDA.Score(0.4333); got != 100 {
		t.Fatalf("net_debt_to_ebitda 0.4333 → %v, esperado 100", got)
	}
	if got := b.FCFToDebt.Score(1.0011); got != 100 {
		t.Fatalf("fcf_to_debt 1.0011 → %v, esperado 100", got)
	}
}

// --- config --------------------------------------------------------------

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("QUALITY_TAX_RATE", "25")
	t.Setenv("QUALITY_COVERAGE_HIGH", "0.90")
	t.Setenv("QUALITY_COVERAGE_MEDIUM", "0.50")
	t.Setenv("QUALITY_STABILITY_MIN_YEARS", "6")
	t.Setenv("QUALITY_WEIGHT_GROWTH", "0.40")
	cfg := ConfigFromEnv()
	if cfg.TaxRate != 25 || cfg.CoverageHigh != 0.90 || cfg.CoverageMedium != 0.50 ||
		cfg.MinStabilityYears != 6 || cfg.SubWeights[SubGrowth] != 0.40 {
		t.Fatalf("env no aplicado: %+v", cfg)
	}
}

func TestConfigRejectsInvalidEnv(t *testing.T) {
	t.Setenv("QUALITY_TAX_RATE", "abc")
	t.Setenv("QUALITY_COVERAGE_HIGH", "NaN")
	t.Setenv("QUALITY_STABILITY_MIN_YEARS", "-3")
	cfg := ConfigFromEnv()
	if cfg.TaxRate != DefaultTaxRate {
		t.Fatalf("tax rate = %v", cfg.TaxRate)
	}
	if cfg.CoverageHigh != DefaultCoverageHigh {
		t.Fatalf("coverage high = %v", cfg.CoverageHigh)
	}
	if cfg.MinStabilityYears != DefaultMinStabilityYears {
		t.Fatalf("min years = %v", cfg.MinStabilityYears)
	}
}

func TestConfigValidate(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("default config inválida: %v", err)
	}
	cfg := DefaultConfig()
	cfg.CoverageMedium = 0.9
	if err := cfg.Validate(); err == nil {
		t.Fatal("medium >= high debe rechazarse")
	}
	cfg = DefaultConfig()
	cfg.MinStabilityYears = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("min years 0 debe rechazarse")
	}
	cfg = DefaultConfig()
	cfg.TaxRate = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("tax rate 0 debe rechazarse")
	}
	cfg = DefaultConfig()
	delete(cfg.SubWeights, SubMargins)
	if err := cfg.Validate(); err == nil {
		t.Fatal("sub-peso ausente debe rechazarse")
	}
	cfg = DefaultConfig()
	cfg.Bands.ROIC = Band{Good: 0.01, Bad: 0.30, HigherIsBetter: true}
	if err := cfg.Validate(); err == nil {
		t.Fatal("una banda invertida debe rechazarse")
	}
}

func TestSubWeightFallback(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.SubWeight("inexistente"); got != 0.20 {
		t.Fatalf("sub-bloque ausente = %v, esperado 0.20", got)
	}
	if got := cfg.SubWeight(SubStability); got != 0.20 {
		t.Fatalf("sub-peso presente = %v", got)
	}
}
