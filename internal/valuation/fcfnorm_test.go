package valuation

import (
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/modelcfg"
)

// normInput builds a company whose FCF MARGIN has collapsed in the current year
// (the C3 trigger): four healthy years around a 16% margin and a current 2%.
// Every other input is held constant so a test can change exactly one thing.
func normInput() Inputs {
	p := func(v float64) *float64 { return &v }
	year := func(y int) time.Time { return time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC) }
	return Inputs{
		Ticker:               "TEST",
		AsOf:                 year(2025),
		Price:                p(50),
		EPS:                  p(2),
		FreeCashFlow:         p(20), // raw current FCF, margin 2% over 1000 revenue
		SharesOutstanding:    p(1000),
		NetDebt:              p(0),
		NormalizedGrowthRate: p(10),
		GrowthConfidence:     ConfidenceHigh,
		WACC:                 p(9),
		WACCSource:           WACCSourceCAPMIndividual,
		WACCConfidence:       ConfidenceHigh,
		BetaObserved:         true,
		RevenueSeries: []FYPoint{
			{PeriodEnd: year(2021), Value: 800},
			{PeriodEnd: year(2022), Value: 850},
			{PeriodEnd: year(2023), Value: 900},
			{PeriodEnd: year(2024), Value: 950},
			{PeriodEnd: year(2025), Value: 1000},
		},
		FreeCashFlowSeries: []FYPoint{
			{PeriodEnd: year(2021), Value: 150},
			{PeriodEnd: year(2022), Value: 150},
			{PeriodEnd: year(2023), Value: 150},
			{PeriodEnd: year(2024), Value: 150},
			{PeriodEnd: year(2025), Value: 20},
		},
	}
}

func TestNormalizeFCF(t *testing.T) {
	in := normInput()
	revNow := lastSeriesValue(in.RevenueSeries)

	raw, norm, applied := normalizeFCF(in.FreeCashFlow, revNow, in.FreeCashFlowSeries, in.RevenueSeries)
	if !applied {
		t.Fatal("la normalización debe aplicarse con el margen desplomado")
	}
	if raw == nil || *raw != 20 {
		t.Fatalf("raw = %v, want 20", raw)
	}
	// median margin de [0.1875,0.1765,0.1667,0.1579,0.02] = 0.1667 → norm ≈ 166.7
	if norm == nil || *norm < 160 || *norm > 170 {
		t.Fatalf("norm = %v, want ≈166.7 (mediana 0.1667 × 1000)", norm)
	}

	// Serie insuficiente (< 3 pares) → no aplica, norm == raw.
	_, n2, a2 := normalizeFCF(in.FreeCashFlow, revNow, in.FreeCashFlowSeries[:2], in.RevenueSeries[:2])
	if a2 || n2 == nil || *n2 != 20 {
		t.Fatalf("con <3 años no debe aplicar: applied=%v norm=%v", a2, n2)
	}

	// Margen actual sano → no aplica.
	healthy := *in.FreeCashFlow
	healthy = 160
	_, n3, a3 := normalizeFCF(&healthy, revNow, in.FreeCashFlowSeries, in.RevenueSeries)
	if a3 || n3 == nil || *n3 != 160 {
		t.Fatalf("margen sano no debe normalizar: applied=%v norm=%v", a3, n3)
	}

	// Sin FCF raw → nada que normalizar ni eco.
	r4, n4, a4 := normalizeFCF(nil, revNow, in.FreeCashFlowSeries, in.RevenueSeries)
	if r4 != nil || n4 != nil || a4 {
		t.Fatalf("sin FCF raw debe devolver todo nil/false: %v %v %v", r4, n4, a4)
	}

	// FCF no positivo: NO se resucita (§21), la normalización no aplica.
	neg := -30.0
	r5, n5, a5 := normalizeFCF(&neg, revNow, in.FreeCashFlowSeries, in.RevenueSeries)
	if a5 || r5 == nil || *r5 != -30 || n5 == nil || *n5 != -30 {
		t.Fatalf("FCF negativo no debe normalizarse: applied=%v raw=%v norm=%v", a5, r5, n5)
	}
}

func TestFCFNormalizationChangesDCFAndCapsConfidence(t *testing.T) {
	in := normInput()

	// ON: the DCF runs on the normalised FCF, echoes raw + norm, records the
	// reason and never claims a high confidence.
	on := Calculate(in, DefaultConfig())
	if on.Inputs.FreeCashFlowNorm == nil {
		t.Fatal("ON: free_cash_flow_norm debe estar presente")
	}
	if on.Inputs.FreeCashFlowRaw == nil || *on.Inputs.FreeCashFlowRaw != 20 {
		t.Fatalf("ON: free_cash_flow_raw = %v, want 20", on.Inputs.FreeCashFlowRaw)
	}
	if on.DCF.Base == nil {
		t.Fatal("ON: el DCF debe seguir disponible")
	}
	if on.DCF.Confidence == ConfidenceHigh {
		t.Fatalf("ON: la confianza DCF no puede ser high (%q)", on.DCF.Confidence)
	}
	if on.Confidence == ConfidenceHigh {
		t.Fatalf("ON: la confianza global no puede ser high (%q)", on.Confidence)
	}
	if !containsReason(on.Reasons, ReasonFCFNormalized) {
		t.Fatalf("ON: falta el motivo %q en %v", ReasonFCFNormalized, on.Reasons)
	}

	// OFF: same inputs, normalisation disabled → norm absent and the DCF uses
	// the raw FCF (a different, lower value).
	cfg := DefaultConfig()
	cfg.FCFNormalization = false
	off := Calculate(in, cfg)
	if off.Inputs.FreeCashFlowNorm != nil {
		t.Fatalf("OFF: free_cash_flow_norm debe ser nil, got %v", off.Inputs.FreeCashFlowNorm)
	}
	if off.DCF.Base == nil {
		t.Fatal("OFF: el DCF debe seguir disponible")
	}
	if on.DCF.Base == nil || *on.DCF.Base == *off.DCF.Base {
		t.Fatalf("normalizar debe cambiar el DCF: on=%v off=%v", on.DCF.Base, off.DCF.Base)
	}
	if containsReason(off.Reasons, ReasonFCFNormalized) {
		t.Fatalf("OFF: no debe llevar el motivo de normalización: %v", off.Reasons)
	}
}

func TestGrowthSignalsDiverge(t *testing.T) {
	p := func(v float64) *float64 { return &v }

	if !growthSignalsDiverge(p(25), p(-5)) {
		t.Fatal("signos opuestos con >20pp deben divergir")
	}
	if growthSignalsDiverge(p(25), p(15)) {
		t.Fatal("mismo signo no diverge")
	}
	if growthSignalsDiverge(p(5), p(-2)) {
		t.Fatal("signos opuestos con <=20pp no divergen")
	}
	if growthSignalsDiverge(nil, p(-5)) || growthSignalsDiverge(p(5), nil) {
		t.Fatal("un nil no puede divergir")
	}
	if growthSignalsDiverge(p(0), p(-30)) {
		t.Fatal("0 no tiene signo opuesto")
	}
}

func TestGrowthSignalsDivergeLowersConfidenceOnly(t *testing.T) {
	in := normInput()
	base := Calculate(in, DefaultConfig())

	div := normInput()
	div.EPSCAGR5y = f64ptr(25)
	div.FCFCAGR5y = f64ptr(-5)
	got := Calculate(div, DefaultConfig())

	if !containsReason(got.Reasons, ReasonGrowthSignalsDiverge) {
		t.Fatalf("falta %q en %v", ReasonGrowthSignalsDiverge, got.Reasons)
	}
	if got.Inputs.GrowthConfidence != ConfidenceLow {
		t.Fatalf("growth.confidence = %q, want low", got.Inputs.GrowthConfidence)
	}
	// El VALOR no cambia: mismo DCF base que sin divergencia.
	if base.DCF.Base == nil || got.DCF.Base == nil || *base.DCF.Base != *got.DCF.Base {
		t.Fatalf("la divergencia no debe cambiar el valor: base=%v got=%v", base.DCF.Base, got.DCF.Base)
	}
}

func TestConfigFromModelConfigHonoursFCFNormalization(t *testing.T) {
	mc := modelcfg.DefaultModelConfig()
	if !ConfigFromModelConfig(mc).FCFNormalization {
		t.Fatal("el default debe propagar la normalización ON")
	}
	mc.FCFNormalization = false
	if ConfigFromModelConfig(mc).FCFNormalization {
		t.Fatal("un parameter set con la normalización OFF debe propagarse")
	}
}

func f64ptr(v float64) *float64 { return &v }

func containsReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}
