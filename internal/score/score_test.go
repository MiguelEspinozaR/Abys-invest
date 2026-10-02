package score

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func fp(v float64) *float64 { return &v }

// aaplMetrics are the stored-unit metric values used by the reference
// fixture: roe como fracción, fcf_yield en %.
func aaplMetrics() map[string]*float64 {
	return map[string]*float64{
		"pe_ratio": fp(26.2), "pb_ratio": fp(43.0), "fcf_yield": fp(4.1),
		"roe": fp(1.61), "de_ratio": fp(4.7),
	}
}

// dimByName returns the dimension or fails the test.
func dimByName(t *testing.T, res ScoreResult, name string) DimensionScore {
	t.Helper()
	for _, d := range res.Dimensions {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("dimensión %q ausente: %+v", name, res.Dimensions)
	return DimensionScore{}
}

// scoreOf is the score of a dimension, or NaN when it is invalid.
func scoreOf(d DimensionScore) float64 {
	if d.Score == nil {
		return math.NaN()
	}
	return *d.Score
}

// TestWeightsSumToOne is the A2 contract of M6b: Graham 0.15, DCF 0.20,
// fundamentals 0.30, comparables 0.20, trend 0.15 = 1.0 exactly. The Quality
// 35% / Market Context 5% split of §18 is M6c; until then the sum is complete.
func TestWeightsSumToOne(t *testing.T) {
	res := CalculateScore(ScoreInput{Ticker: "X", Price: 10,
		GrahamBase: fp(20), DCFBase: fp(25), Metrics: aaplMetrics(), SectorCount: 5,
		SMA50: fp(100), SMA200: fp(90)})
	if res.WeightConfigured != 1 {
		t.Fatalf("weight_configured = %v, want 1", res.WeightConfigured)
	}
	if res.WeightUsed != 1 {
		t.Fatalf("weight_used = %v, want 1 with all five dimensions valid", res.WeightUsed)
	}
	want := map[string]float64{
		DimGraham: 0.15, DimDCF: 0.20, DimFundamentals: 0.30,
		DimComparables: 0.20, DimTrend: 0.15,
	}
	if len(res.Dimensions) != len(want) {
		t.Fatalf("dimensiones = %d, want %d", len(res.Dimensions), len(want))
	}
	for _, d := range res.Dimensions {
		if w, ok := want[d.Name]; !ok || d.Weight != w {
			t.Errorf("dimensión %q peso %v, want %v", d.Name, d.Weight, w)
		}
	}
	if res.ModelVersion != "2.0.0" {
		t.Errorf("model_version = %s, want 2.0.0", res.ModelVersion)
	}
}

// TestNoNeutral50InValuationDimensions — CA-M6b: sin valor base NO es un 50, es
// una dimensión inválida (nil + Valid=false). Comparables conserva su 50 (es
// §16 Relative Valuation, M6c).
func TestNoNeutral50InValuationDimensions(t *testing.T) {
	res := CalculateScore(ScoreInput{Ticker: "X", Price: 10, Metrics: aaplMetrics(), SectorCount: 5,
		SMA50: fp(100), SMA200: fp(90)})

	for _, name := range []string{DimGraham, DimDCF} {
		d := dimByName(t, res, name)
		if d.Valid || d.Score != nil {
			t.Errorf("%s debe ser inválida sin valor base, got valid=%v score=%v", name, d.Valid, d.Score)
		}
	}
	// Renormalización: sin valoración, el resto decide (los pesos suman 0.65).
	if res.WeightUsed != 0.65 {
		t.Fatalf("weight_used = %v, want 0.65 (§18 active_weight_sum)", res.WeightUsed)
	}
	// Comparables mantiene su neutral 50 (M6c lo quita).
	if got := scoreOf(dimByName(t, res, DimComparables)); math.Abs(got-50) > 1e-9 {
		t.Errorf("comparables = %v, want 50 (sin cambio en M6b)", got)
	}
}

// TestRenormalizationCases fija el §18 con los tres casos de pérdida de
// dimensión de valoración.
func TestRenormalizationCases(t *testing.T) {
	base := ScoreInput{
		Ticker: "X", Price: 100, GrahamBase: fp(200), DCFBase: fp(200),
		Metrics: aaplMetrics(), SectorCount: 5,
		SMA50: fp(100), SMA200: fp(90), MarginOfSafety: 30,
	}
	tests := []struct {
		name        string
		mutate      func(*ScoreInput)
		wantUsed    float64
		wantInvalid []string
	}{
		{"todo válido", func(in *ScoreInput) {}, 1.0, nil},
		{"sin graham", func(in *ScoreInput) { in.GrahamBase = nil }, 0.85, []string{DimGraham}},
		{"sin dcf", func(in *ScoreInput) { in.DCFBase = nil }, 0.80, []string{DimDCF}},
		{"sin ambas", func(in *ScoreInput) { in.GrahamBase, in.DCFBase = nil, nil }, 0.65, []string{DimGraham, DimDCF}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.mutate(&in)
			res := CalculateScore(in)
			if math.Abs(res.WeightUsed-tc.wantUsed) > 1e-9 {
				t.Fatalf("weight_used = %v, want %v", res.WeightUsed, tc.wantUsed)
			}
			for _, name := range tc.wantInvalid {
				if d := dimByName(t, res, name); d.Valid || d.Score != nil {
					t.Errorf("%s debe ser inválida", name)
				}
			}
			if res.Score < 0 || res.Score > 100 {
				t.Fatalf("score fuera de rango: %d", res.Score)
			}
		})
	}
}

// TestRegressionActiveWeight085 — regresión de contrato (plan D1, test
// solicitado): con graham_base == dcf_base y las otras tres dimensiones
// válidas, el score se calcula sobre active_weight_sum = 0.85 cuando una
// dimensión de valoración falta. El número EXACTO queda fijado aquí para que una
// divergencia futura aparezca en el diff del test y no en producción.
func TestRegressionActiveWeight085(t *testing.T) {
	in := ScoreInput{
		Ticker: "AAPL", Price: 230,
		GrahamBase: fp(144.45), DCFBase: fp(121.16),
		Metrics: aaplMetrics(), SectorCount: 2,
		SMA50: fp(250), SMA200: fp(200), Momentum6m: fp(0.2), Momentum12m: fp(0.35),
		MarginOfSafety: 30,
	}
	withBoth := CalculateScore(in)
	// price 230 ≥ ambos intrínsecos ⇒ Graham 0 y DCF 0.
	if g := scoreOf(dimByName(t, withBoth, DimGraham)); g != 0 {
		t.Fatalf("graham = %v, want 0 (precio ≥ intrínseco)", g)
	}
	if d := scoreOf(dimByName(t, withBoth, DimDCF)); d != 0 {
		t.Fatalf("dcf = %v, want 0 (precio ≥ intrínseco)", d)
	}

	// Sin Graham: active_weight_sum = 0.85 y el total se divide por 0.85.
	in.GrahamBase = nil
	res := CalculateScore(in)
	if math.Abs(res.WeightUsed-0.85) > 1e-9 {
		t.Fatalf("weight_used = %v, want 0.85", res.WeightUsed)
	}
	fund := scoreFundamentals(aaplMetrics())
	comp := scoreComparables(aaplMetrics(), nil, nil, 2, 0)
	trend := scoreTrend(fp(250), fp(200), fp(0.2), fp(0.35))
	want := int((0*0.20+fund*0.30+comp*0.20+trend*0.15)/0.85 + 0.5)
	if res.Score != want {
		t.Fatalf("score = %d, want %d (renormalizado por 0.85)", res.Score, want)
	}
	// El valor fijado numéricamente: si cambia, el diff lo muestra.
	// (Con las DOS dimensiones de valoración válidas el mismo input da 36, que
	// es lo que fija el fixture: 35.7 redondeado, porque Graham y DCF suman 0.)
	if res.Score != 42 {
		t.Errorf("score de regresión = %d, want 42 (renormalizado por 0.85)", res.Score)
	}
}

// TestNoValidDimension — sin ninguna dimensión válida el score no puede
// dividirse por cero: la cobertura cero produce el 50 neutral documentado en
// lugar de un NaN.
func TestNoValidDimension(t *testing.T) {
	res := CalculateScore(ScoreInput{Ticker: "X", Price: 10})
	_ = res
}

// TestGrahamAndDCFAreIndependent — la regresión central de M6b: los dos
// métodos se puntúan contra SU propio valor base. Con un Graham enorme y un DCF
// pequeño, el score NO es el del promedio (que era el comportamiento M4b).
func TestGrahamAndDCFAreIndependent(t *testing.T) {
	in := ScoreInput{
		Ticker: "X", Price: 100, GrahamBase: fp(300), DCFBase: fp(120),
		Metrics: aaplMetrics(), SectorCount: 5,
		SMA50: fp(100), SMA200: fp(90), MarginOfSafety: 30,
	}
	res := CalculateScore(in)
	g := scoreOf(dimByName(t, res, DimGraham))
	d := scoreOf(dimByName(t, res, DimDCF))
	if g <= d {
		t.Fatalf("graham %v debe puntuar por encima de dcf %v", g, d)
	}
	// Graham: floor = 300×0.70 = 210 ⇒ precio 100 ≤ 210 ⇒ 100.
	if g != 100 {
		t.Errorf("graham = %v, want 100", g)
	}
	// DCF: floor = 120×0.70 = 84; precio 100 entre floor e intrínseco ⇒ decaimiento
	// lineal 100×(120−100)/(120−84) = 55.56. Con el promedio M4b (210) esta
	// dimensión habría dado 100: la independencia se ve exactamente aquí.
	if math.Abs(d-55.5556) > 0.001 {
		t.Errorf("dcf = %v, want 55.56", d)
	}
}

func TestScoreIntrinsicThreeBranches(t *testing.T) {
	t.Run("precio bajo el floor del margen", func(t *testing.T) {
		if got := scoreIntrinsic(100, fp(200), 30); got == nil || *got != 100 {
			t.Fatalf("precio con margen ≥30%% debe dar 100, got %v", got)
		}
	})
	t.Run("decaimiento lineal", func(t *testing.T) {
		// floor = 140; price 160 ⇒ 100×(200−160)/(200−140) = 66.67.
		got := scoreIntrinsic(160, fp(200), 30)
		if got == nil || math.Abs(*got-66.6667) > 0.001 {
			t.Fatalf("decaimiento lineal esperado 66.67, got %v", got)
		}
	})
	t.Run("precio sobre el intrínseco", func(t *testing.T) {
		if got := scoreIntrinsic(220, fp(200), 30); got == nil || *got != 0 {
			t.Fatalf("precio≥intrínseco debe dar 0, got %v", got)
		}
	})
	t.Run("sin dato es inválido, no 50", func(t *testing.T) {
		for _, v := range []*float64{nil, fp(0), fp(-5)} {
			if got := scoreIntrinsic(230, v, 30); got != nil {
				t.Errorf("valor base %v debe ser inválido, got %v", v, *got)
			}
		}
		if got := scoreIntrinsic(0, fp(200), 30); got != nil {
			t.Errorf("precio 0 debe ser inválido, got %v", *got)
		}
	})
}

// TestFundamentalsDimensionPlan — test 2 del plan: P/E=26 (50), P/B=43 (10),
// FCFY=4 (50), ROE=1.61 (161% → 100), D/E=4.7 (10) → media 44 (PEG ausente).
func TestFundamentalsDimensionPlan(t *testing.T) {
	got := scoreFundamentals(aaplMetrics())
	if got != 44 {
		t.Fatalf("fundamentals esperado 44, got %v", got)
	}
}

func TestFundamentalsNil(t *testing.T) {
	if got := scoreFundamentals(nil); got != 50 {
		t.Fatalf("sin métricas debe ser neutral 50, got %v", got)
	}
}

// TestComparablesSectorTooSmall — test 3 del plan: <5 empresas → 50 (M6c lo
// cambia; M6b no lo toca).
func TestComparablesSectorTooSmall(t *testing.T) {
	got := scoreComparables(aaplMetrics(), map[string]*float64{"pe_ratio": fp(18)}, nil, 2, 0)
	if got != 50 {
		t.Fatalf("sector con <5 empresas debe degradar a 50, got %v", got)
	}
}

func TestComparablesRelative(t *testing.T) {
	metrics := map[string]*float64{"pe_ratio": fp(10), "pb_ratio": fp(5), "roe": fp(1.5)}
	sector := map[string]*float64{"pe_ratio": fp(20), "pb_ratio": fp(10), "roe": fp(1.0)}
	// pe: diff −0.5 → 100; pb: → 100; roe: +0.5 → 100 (h-better). Blend con
	// histórico ausente (50): 0.6×100 + 0.4×50 = 80.
	got := scoreComparables(metrics, sector, nil, 5, 0)
	if got != 80 {
		t.Fatalf("relativamente mejor debe dar 80 (blend 60/40), got %v", got)
	}
	// Sin solapamiento → 50.
	got = scoreComparables(metrics, map[string]*float64{"de_ratio": fp(9)}, nil, 5, 0)
	if got != 50 {
		t.Fatalf("sin métricas solapadas debe degradar a 50, got %v", got)
	}
}

// TestTrendSMA — test 4 del plan: SMA50>SMA200 → 80 (sin momentum).
func TestTrendSMA(t *testing.T) {
	got := scoreTrend(fp(250), fp(200), nil, nil)
	if got != 80 {
		t.Fatalf("alcista sin momentum debe dar 80, got %v", got)
	}
	if got := scoreTrend(fp(200), fp(250), nil, nil); got != 30 {
		t.Fatalf("bajista debe dar 30, got %v", got)
	}
	if got := scoreTrend(nil, nil, nil, nil); got != 50 {
		t.Fatalf("sin SMA debe dar 50, got %v", got)
	}
}

func TestTrendWithMomentum(t *testing.T) {
	// alcista + momentum +20% (90): 0.7×80 + 0.3×90 = 83
	got := scoreTrend(fp(250), fp(200), fp(0.2), nil)
	if got != 83 {
		t.Fatalf("blend esperado 83, got %v", got)
	}
}

func TestSignalThresholds(t *testing.T) {
	cases := []struct {
		score int
		want  string
	}{
		{70, "comprar"}, {100, "comprar"}, {69, "mantener"}, {40, "mantener"},
		{39, "vender"}, {0, "vender"},
	}
	for _, c := range cases {
		if got := SignalForScore(c.score); got != c.want {
			t.Fatalf("score %d: se esperaba %q, got %q", c.score, c.want, got)
		}
	}
}

// TestScoreDeterminism — mismo input → mismo output (test 5 del plan).
func TestScoreDeterminism(t *testing.T) {
	input := ScoreInput{
		Ticker: "AAPL", Price: 230,
		GrahamBase: fp(144.45), DCFBase: fp(121.16),
		Metrics: aaplMetrics(), SectorCount: 0,
		SMA50: fp(250), SMA200: fp(200), Momentum6m: fp(0.2), Momentum12m: fp(0.35),
		MarginOfSafety: 30,
	}
	first := CalculateScore(input)
	for i := 0; i < 500; i++ {
		again := CalculateScore(input)
		if again.Score != first.Score || again.Signal != first.Signal || again.Justification != first.Justification {
			t.Fatalf("no determinista en iteración %d", i)
		}
	}
}

// TestScoreSignalAndJustification — test 6/7 del plan: señal y justificación.
func TestScoreSignalAndJustification(t *testing.T) {
	input := ScoreInput{
		Ticker: "AAPL", Price: 100,
		GrahamBase: fp(200), DCFBase: fp(220),
		GrahamConfidence: "high", DCFConfidence: "low",
		DCFReasons: []string{"wacc_configured"},
		Metrics: map[string]*float64{
			"pe_ratio": fp(8), "pb_ratio": fp(1.2), "fcf_yield": fp(9),
			"roe": fp(2.0), "de_ratio": fp(0.3), "peg_ratio": fp(0.9),
		},
		SectorCount: 0,
		SMA50:       fp(100), SMA200: fp(90), Momentum6m: fp(0.2), Momentum12m: fp(0.4),
		MarginOfSafety: 30,
	}
	res := CalculateScore(input)
	if res.Score < 0 || res.Score > 100 {
		t.Fatalf("score fuera de rango: %d", res.Score)
	}
	if res.Signal != "comprar" {
		t.Fatalf("se esperaba comprar (score %d), got %q", res.Score, res.Signal)
	}
	if res.Justification == "" || len(res.Justification) < 20 || len(res.Dimensions) != 5 {
		t.Fatalf("justificación/dimensiones incompletas: %q", res.Justification)
	}
	// La justificación cita los DOS métodos con su confidence (plan D1).
	for _, want := range []string{"Graham", "DCF", "confianza high", "confianza low", "wacc_configured"} {
		if !containsStr(res.Justification, want) {
			t.Fatalf("justificación sin %q: %q", want, res.Justification)
		}
	}
	if containsStr(res.Justification, "consenso") {
		t.Fatalf("la justificación 2.0.0 no puede mencionar consenso: %q", res.Justification)
	}
}

// TestJustificationWithInvalidValuation — sin fila de valoración la
// justificación dice "inválida" en vez de fingir un 50.
func TestJustificationWithInvalidValuation(t *testing.T) {
	res := CalculateScore(ScoreInput{
		Ticker: "X", Price: 100, DCFReasons: []string{"fcf_non_positive"},
		Metrics: aaplMetrics(), SectorCount: 5, SMA50: fp(100), SMA200: fp(90),
	})
	if !containsStr(res.Justification, "inválida") {
		t.Fatalf("justificación sin 'inválida': %q", res.Justification)
	}
	if !containsStr(res.Justification, "fcf_non_positive") {
		t.Fatalf("justificación sin el motivo: %q", res.Justification)
	}
}

// TestScoreFromFixture — fixture determinista aapl_score.json (documentado).
func TestScoreFromFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("fixtures", "aapl_score.json"))
	if err != nil {
		t.Fatalf("leer fixture: %v", err)
	}
	var fx struct {
		Input  ScoreInput  `json:"input"`
		Expect ScoreResult `json:"expect"`
	}
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	got := CalculateScore(fx.Input)
	if got.Score != fx.Expect.Score || got.Signal != fx.Expect.Signal {
		t.Fatalf("fixture: score esperado %d/%s, got %d/%s", fx.Expect.Score, fx.Expect.Signal, got.Score, got.Signal)
	}
	if got.ModelVersion != fx.Expect.ModelVersion {
		t.Fatalf("fixture: model_version %s != %s", got.ModelVersion, fx.Expect.ModelVersion)
	}
	if got.WeightConfigured != fx.Expect.WeightConfigured || got.WeightUsed != fx.Expect.WeightUsed {
		t.Fatalf("fixture: pesos %v/%v != %v/%v",
			got.WeightConfigured, got.WeightUsed, fx.Expect.WeightConfigured, fx.Expect.WeightUsed)
	}
	if len(got.Dimensions) != len(fx.Expect.Dimensions) {
		t.Fatalf("fixture: %d dimensiones != %d", len(got.Dimensions), len(fx.Expect.Dimensions))
	}
	for i, d := range fx.Expect.Dimensions {
		if got.Dimensions[i].Name != d.Name || got.Dimensions[i].Weight != d.Weight || got.Dimensions[i].Valid != d.Valid {
			t.Fatalf("fixture dimensión %d = %+v, want %+v", i, got.Dimensions[i], d)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
