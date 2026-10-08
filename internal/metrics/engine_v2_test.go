package metrics

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

// aaplV2 is a complete input set: every required fact present, including the
// interest expense that the universe does NOT have (so the NULL path is tested
// explicitly rather than by accident).
func aaplV2() MetricInputV2 {
	return MetricInputV2{
		SecurityID:         2225,
		Ticker:             "AAPL",
		AsOf:               time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC),
		NetEarnings:        f(94.360e9),
		SharesOutstanding:  f(14.8e9),
		Price:              f2(260.15),
		ShareholdersEquity: f(62.1e9),
		TotalLiabilities:   f(285.0e9),
		FreeCashFlow:       f(99.6e9),
		OperatingIncome:    f(123.2e9),
		Revenue:            f(416.2e9),
		TotalDebt:          f(105.4e9),
		Cash:               f(54.5e9),
		EBITDA:             f(136.0e9),
		InterestExpense:    f(11.0e9),
		NormalizedTaxRate:  f(0.21),
		EPSSeries: []MetricSeriesPoint{
			{PeriodEnd: time.Date(2021, 9, 25, 0, 0, 0, 0, time.UTC), Value: 5.61},
			{PeriodEnd: time.Date(2022, 9, 24, 0, 0, 0, 0, time.UTC), Value: 6.11},
			{PeriodEnd: time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC), Value: 6.13},
			{PeriodEnd: time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC), Value: 6.08},
			{PeriodEnd: time.Date(2025, 9, 27, 0, 0, 0, 0, time.UTC), Value: 6.37},
		},
		FCFSeries: []MetricSeriesPoint{
			{PeriodEnd: time.Date(2021, 9, 25, 0, 0, 0, 0, time.UTC), Value: 69.7e9},
			{PeriodEnd: time.Date(2022, 9, 24, 0, 0, 0, 0, time.UTC), Value: 111.4e9},
			{PeriodEnd: time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC), Value: 110.0e9},
			{PeriodEnd: time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC), Value: 108.8e9},
			{PeriodEnd: time.Date(2025, 9, 27, 0, 0, 0, 0, time.UTC), Value: 99.6e9},
		},
	}
}

func f2(v float64) float64 { return v }

func near(t *testing.T, name string, got *float64, want float64, tol float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, esperado %v", name, want)
	}
	if math.Abs(*got-want) > tol {
		t.Fatalf("%s = %v, esperado %v (±%v)", name, *got, want, tol)
	}
}

func nilv(t *testing.T, name string, got *float64) {
	t.Helper()
	if got != nil {
		t.Fatalf("%s = %v, esperado nil", name, *got)
	}
}

func TestROICFormula(t *testing.T) {
	// NOPAT = 123.2e9 * 0.79 = 97.328e9; invested = 62.1 + 105.4 - 54.5 = 113.0e9.
	nopat := 123.2e9 * (1 - 0.21)
	invested := 62.1e9 + 105.4e9 - 54.5e9
	near(t, "roic", CalcROIC(f(123.2e9), f(62.1e9), f(105.4e9), f(54.5e9), f(0.21)), nopat/invested, 1e-12)
	// InvestedCapital <= 0 → nil (SPEC §13 lo dice explícitamente).
	nilv(t, "roic invested=0", CalcROIC(f(10), f(50), f(0), f(50), f(0.21)))
	nilv(t, "roic invested<0", CalcROIC(f(10), f(10), f(0), f(50), f(0.21)))
	// Falta cualquier hecho → nil.
	nilv(t, "roic sin equity", CalcROIC(f(10), nil, f(5), f(1), f(0.21)))
	nilv(t, "roic sin cash", CalcROIC(f(10), f(50), f(5), nil, f(0.21)))
	nilv(t, "roic sin tax", CalcROIC(f(10), f(50), f(5), f(1), nil))
	// Una tasa impositiva fuera de [0,1) no es una tasa normalizada.
	nilv(t, "roic tax=1", CalcROIC(f(10), f(50), f(5), f(1), f(1)))
	nilv(t, "roic tax=-0.1", CalcROIC(f(10), f(50), f(5), f(1), f(-0.1)))
	// ROIC negativo es un dato legítimo (pérdidas con capital positivo).
	if got := CalcROIC(f(-10), f(100), f(0), f(0), f(0.21)); got == nil || *got >= 0 {
		t.Fatalf("ROIC negativo debe conservarse: %v", got)
	}
}

func TestMarginFormulas(t *testing.T) {
	near(t, "operating_margin", CalcOperatingMargin(f(123.2e9), f(416.2e9)), 123.2/416.2, 1e-12)
	near(t, "fcf_margin", CalcFCFMargin(f(99.6e9), f(416.2e9)), 99.6/416.2, 1e-12)
	// Denominador <= 0 o ausente → nil (nunca 0 ni 50).
	nilv(t, "margen sin revenue", CalcOperatingMargin(f(10), nil))
	nilv(t, "margen revenue=0", CalcOperatingMargin(f(10), f(0)))
	nilv(t, "margen revenue<0", CalcFCFMargin(f(10), f(-5)))
}

func TestNetDebtAndLeverageFormulas(t *testing.T) {
	near(t, "net_debt", CalcNetDebt(f(105.4e9), f(54.5e9)), 50.9e9, 1e-6)
	near(t, "net_debt_to_ebitda", CalcNetDebtToEBITDA(f(50.9e9), f(136.0e9)), 50.9/136.0, 1e-12)
	near(t, "interest_coverage", CalcInterestCoverage(f(123.2e9), f(11.0e9)), 123.2/11.0, 1e-12)
	near(t, "fcf_to_debt", CalcFCFToDebt(f(99.6e9), f(105.4e9)), 99.6/105.4, 1e-12)
	// Net cash: valor negativo legítimo.
	near(t, "net_debt_to_ebitda con caja", CalcNetDebtToEBITDA(f(-10), f(20)), -0.5, 1e-12)
	// EBITDA <= 0 no es un denominador.
	nilv(t, "nde ebitda=0", CalcNetDebtToEBITDA(f(10), f(0)))
	nilv(t, "nde ebitda<0", CalcNetDebtToEBITDA(f(10), f(-5)))
	// interest_expense ausente → nil (M6c-T1), nunca 50.
	nilv(t, "interest_coverage sin gasto", CalcInterestCoverage(f(123.2e9), nil))
	nilv(t, "interest_coverage gasto=0", CalcInterestCoverage(f(123.2e9), f(0)))
	// Un debts de 0 tampoco es un denominador.
	nilv(t, "fcf_to_debt debt=0", CalcFCFToDebt(f(10), f(0)))
}

func TestEVFormulas(t *testing.T) {
	mc := 260.15 * 14.8e9
	netDebt := 50.9e9
	near(t, "ev_ebitda", CalcEVEBITDA(f(mc), f(netDebt), f(136.0e9)), (mc+netDebt)/136.0e9, 1e-6)
	near(t, "ev_ebit", CalcEVEBIT(f(mc), f(netDebt), f(123.2e9)), (mc+netDebt)/123.2e9, 1e-6)
	nilv(t, "ev_ebitda sin ebitda", CalcEVEBITDA(f(mc), f(netDebt), nil))
	nilv(t, "ev_ebitda ebitda<=0", CalcEVEBITDA(f(mc), f(netDebt), f(0)))
	nilv(t, "ev_ebit sin ebit", CalcEVEBIT(f(mc), f(netDebt), f(-1)))
	nilv(t, "ev_ebit sin market_cap", CalcEVEBIT(f(0), f(netDebt), f(123.2e9)))
}

func TestPositiveYearsAndVolatility(t *testing.T) {
	near(t, "positive_eps_years", CalcPositiveYears([]float64{1, -2, 3, 0, 5}), 3, 0)
	near(t, "positive_fcf_years", CalcPositiveYears([]float64{-1, -2}), 0, 0)
	nilv(t, "positive_years vacío", CalcPositiveYears(nil))
	// CV = stdev(muestra) / |media|.
	vals := []float64{5.61, 6.11, 6.13, 6.08, 6.37}
	mean := (5.61 + 6.11 + 6.13 + 6.08 + 6.37) / 5
	var ss float64
	for _, v := range vals {
		ss += (v - mean) * (v - mean)
	}
	want := math.Sqrt(ss/4) / math.Abs(mean)
	near(t, "eps_volatility", CalcCoefficientOfVariation(vals), want, 1e-12)
	// media 0 y un solo punto → nil.
	nilv(t, "cv mean=0", CalcCoefficientOfVariation([]float64{-2, 2}))
	nilv(t, "cv n=1", CalcCoefficientOfVariation([]float64{3}))
	nilv(t, "cv vacío", CalcCoefficientOfVariation(nil))
	// Un NaN anywhere invalida la serie entera: una media contaminada no es un
	// dato, es un promedio de basura.
	nilv(t, "cv con NaN", CalcCoefficientOfVariation([]float64{1, math.NaN(), 3}))
	// Escala invariante: CV(FCF) == CV(FCF x 1000) (por qué es un ratio).
	base := []float64{69.7, 111.4, 110.0, 108.8, 99.6}
	scaled := make([]float64, len(base))
	for i, v := range base {
		scaled[i] = v * 1000
	}
	a, b := CalcCoefficientOfVariation(base), CalcCoefficientOfVariation(scaled)
	if a == nil || b == nil || math.Abs(*a-*b) > 1e-9 {
		t.Fatalf("el CV debe ser invariante a la escala: %v vs %v", a, b)
	}
}

func TestAllTwelveEmittedWithFullInput(t *testing.T) {
	res := CalculateMetricsV2(aaplV2())
	got := map[string]*float64{}
	for _, r := range res {
		got[r.Metric] = r.Value
	}
	for _, name := range NewMetricNames() {
		if _, ok := got[name]; !ok {
			t.Fatalf("métrica 2.1.0 no emitida con input completo: %s", name)
		}
	}
	if len(res) != 12 {
		t.Fatalf("métricas emitidas = %d, esperado 12: %v", len(res), got)
	}
	// Determinista y ordenada.
	for i := 1; i < len(res); i++ {
		if res[i].Metric <= res[i-1].Metric {
			t.Fatalf("orden no determinista: %s antes de %s", res[i-1].Metric, res[i].Metric)
		}
	}
	// Coherencia con las fórmulas puras.
	near(t, "roic emitido", got[MetricROIC], *CalcROIC(f(123.2e9), f(62.1e9), f(105.4e9), f(54.5e9), f(0.21)), 1e-12)
	if got[MetricInterestCover] == nil {
		t.Fatal("con interest_expense presente debe emitirse con valor")
	}
}

func TestNoEmissionWithoutInput(t *testing.T) {
	// Sin hechos: NO se emite ninguna fila salvo interest_coverage (§14 / M6c-T1).
	res := CalculateMetricsV2(MetricInputV2{Ticker: "XXXX", AsOf: time.Now().UTC()})
	if len(res) != 1 || res[0].Metric != MetricInterestCover {
		names := []string{}
		for _, r := range res {
			names = append(names, r.Metric)
		}
		t.Fatalf("solo interest_coverage debe emitirse sin datos: %v", names)
	}
	if res[0].Value != nil {
		t.Fatalf("interest_coverage debe ser NULL: %v", *res[0].Value)
	}
	// Un hecho a medias emite solo lo que puede emitir.
	in := aaplV2()
	in.Revenue = nil
	in.TotalDebt = nil
	in.Cash = nil
	in.EPSSeries, in.FCFSeries = nil, nil
	res = CalculateMetricsV2(in)
	for _, r := range res {
		switch r.Metric {
		case MetricROIC, MetricOperatingMargin, MetricFCFMargin, MetricNetDebtToEBITDA,
			MetricFCFToDebt, MetricEVEBITDA, MetricEVEBIT, MetricPositiveEPSYrs,
			MetricPositiveFCFYrs, MetricEPSVolatility, MetricFCFVolatility:
			t.Fatalf("%s no debía emitirse sin sus insumos", r.Metric)
		}
	}
	// interest_coverage sigue presente (EBIT e interés sí están).
	found := false
	for _, r := range res {
		if r.Metric == MetricInterestCover {
			found = true
		}
	}
	if !found {
		t.Fatal("interest_coverage debe emitirse siempre")
	}
}

func TestInterestCoverageNULLCarriesReasonSnapshot(t *testing.T) {
	in := aaplV2()
	in.InterestExpense = nil
	res := CalculateMetricsV2(in)
	var row *MetricResult
	for i := range res {
		if res[i].Metric == MetricInterestCover {
			row = &res[i]
		}
	}
	if row == nil {
		t.Fatal("interest_coverage ausente")
	}
	if row.Value != nil {
		t.Fatalf("value debe ser NULL: %v", *row.Value)
	}
	if row.InputsSnapshot["reason"] != ReasonInterestCoverageUnavailable {
		t.Fatalf("snapshot sin motivo: %v", row.InputsSnapshot)
	}
	if row.InputsSnapshot["missing_input"] != "interest_expense" {
		t.Fatalf("snapshot sin input ausente: %v", row.InputsSnapshot)
	}
	// El snapshot serializa (JSONB) y explica el valor ausente.
	rows, err := BuildDerivedMetricsV2(in, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, r := range rows {
		if r.Metric != MetricInterestCover {
			continue
		}
		if r.ModelVersion != ModelVersion21 {
			t.Fatalf("model_version = %q", r.ModelVersion)
		}
		var snap map[string]any
		if err := json.Unmarshal(r.InputsSnapshot, &snap); err != nil {
			t.Fatalf("snapshot no serializable: %v", err)
		}
		if snap["interest_expense"] != nil {
			t.Fatalf("el snapshot debe mostrar el input ausente como null: %v", snap)
		}
		if snap["reason"] != ReasonInterestCoverageUnavailable {
			t.Fatalf("motivo perdido al serializar: %v", snap)
		}
	}
}

func TestDefiningVersionForComparablesReaders(t *testing.T) {
	// Los 8 de 1.0.0 siguen siendo 1.0.0 (ADR D12: no se tocan).
	for _, name := range []string{MetricEPS, MetricPE, MetricPB, MetricPCF, MetricPEG, MetricROE, MetricDE, MetricFCFYield} {
		if got := DefiningVersion(name); got != DefaultModelVersion {
			t.Fatalf("%s debe seguir en %s, es %s", name, DefaultModelVersion, got)
		}
	}
	// Los 12 modernos (ADR D12) se definen en la revisión VIGENTE 2.1.0
	// (M6c-T1 W6a): las filas 2.0.0 quedan como historia, pero el reader por
	// métrica apunta a 2.1.0.
	if ModelVersion21 != "2.1.0" {
		t.Fatalf("ModelVersion21 cambió a %q (debe ser 2.1.0)", ModelVersion21)
	}
	if got := DefiningVersion(MetricROIC); got != ModelVersion21 {
		t.Fatalf("roic debe estar en %s, es %s", ModelVersion21, got)
	}
	for _, name := range NewMetricNames() {
		if got := DefiningVersion(name); got != ModelVersion21 {
			t.Fatalf("%s debe estar en %s, es %s", name, ModelVersion21, got)
		}
	}
	// Una métrica desconocida cae en 1.0.0 (fail-safe: no inventa una versión).
	if DefiningVersion("no_existe") != DefaultModelVersion {
		t.Fatal("métrica desconocida debe caer en la versión por defecto")
	}
}

// CA-M6c-14: las fórmulas de M6b intactas.
func TestLegacyFormulasUntouched(t *testing.T) {
	in := MetricInput{
		NetEarnings: f(94.360e9), SharesOutstanding: f(14.8e9), Price: 260.15,
		ShareholdersEquity: f(62.1e9), TotalLiabilities: f(285.0e9),
		FreeCashFlow: f(99.6e9), GrowthRate: 7,
	}
	res := CalculateMetrics(in)
	if len(res) != 8 {
		t.Fatalf("1.0.0 debe seguir emitting 8 métricas, emitió %d", len(res))
	}
	if DefaultModelVersion != "1.0.0" {
		t.Fatalf("DefaultModelVersion cambió a %q", DefaultModelVersion)
	}
	byName := map[string]*float64{}
	for _, r := range res {
		byName[r.Metric] = r.Value
	}
	// PEG = PE / 7 idéntico a M6b.
	pe := *byName[MetricPE]
	near(t, "peg", byName[MetricPEG], pe/7, 1e-12)
	near(t, "pe", byName[MetricPE], 260.15/(94.360e9/14.8e9), 1e-9)
	near(t, "roe", byName[MetricROE], 94.360/62.1, 1e-12)
	// p_fcf (CalcPCF) intacto: M6b lo consume desde valuation_results.
	near(t, "pcf", byName[MetricPCF], 260.15*14.8e9/99.6e9, 1)
	// El motor 1.0.0 no conoce ninguna métrica nueva.
	for name := range byName {
		for _, n := range NewMetricNames() {
			if name == n {
				t.Fatalf("1.0.0 no debe emitir %s", n)
			}
		}
	}
}
