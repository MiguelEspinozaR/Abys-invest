package score

import (
	"encoding/json"
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

// TestValuationDimensionPlan — inputs del plan D5 (test 1): price=230,
// graham=144, dcf=195, margin=30. Con la decisión 2026-09-23 el promedio
// (144+195)/2=169.5; precio 230 ≥ promedio → 0 (misma regla D5; el "≈35"
// del enunciado del plan es inconsistente con su propia regla y NO se
// implementa — desviación documentada en el reporte).
func TestValuationDimensionPlan(t *testing.T) {
	got := scoreValuation(230, fp(144), fp(195), 30)
	if got != 0 {
		t.Fatalf("precio≥intrínseco debe dar 0 según D5, got %v", got)
	}
}

func TestValuationSingleGraham(t *testing.T) {
	// floor = 200×0.70 = 140. price=100 ≤ floor → 100.
	if got := scoreValuation(100, fp(200), nil, 30); got != 100 {
		t.Fatalf("precio con margen ≥30%% debe dar 100, got %v", got)
	}
	// price=160: lineal 100×(200−160)/60 = 66.67.
	if got := scoreValuation(160, fp(200), nil, 30); got < 66.666 || got > 66.667 {
		t.Fatalf("decaimiento lineal esperado 66.67, got %v", got)
	}
	// price=220 ≥ intrinsic → 0.
	if got := scoreValuation(220, fp(200), nil, 30); got != 0 {
		t.Fatalf("precio≥intrínseco debe dar 0, got %v", got)
	}
	// Sin intrínseco → neutral 50.
	if got := scoreValuation(230, nil, nil, 30); got != 50 {
		t.Fatalf("sin datos debe dar 50, got %v", got)
	}
}

func TestValuationAverageBoth(t *testing.T) {
	// g=150, d=200 → promedio de intrínsecos = 175, floor = 175×0.70 = 122.5
	// (decisión 2026-09-23: el score puntúa el precio contra el promedio).
	// price=130: 100×(175−130)/(175−122.5) = 100×45/52.5 = 85.71…
	got := scoreValuation(130, fp(150), fp(200), 30)
	if got < 85.70 || got > 85.72 {
		t.Fatalf("promedio esperado 85.71, got %v", got)
	}
	// price=160: 100×(175−160)/52.5 = 28.57…
	got = scoreValuation(160, fp(150), fp(200), 30)
	if got < 28.56 || got > 28.58 {
		t.Fatalf("decaimiento lineal esperado 28.57, got %v", got)
	}
	// price ≥ promedio → 0.
	if got := scoreValuation(180, fp(150), fp(200), 30); got != 0 {
		t.Fatalf("precio≥promedio debe dar 0, got %v", got)
	}
	// price ≤ floor → 100.
	if got := scoreValuation(100, fp(150), fp(200), 30); got != 100 {
		t.Fatalf("precio con margen ≥30%% debe dar 100, got %v", got)
	}
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

// TestComparablesSectorTooSmall — test 3 del plan: <5 empresas → 50.
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
		t.Fatalf("relativo claramente mejor debe dar 80 (blend 60/40), got %v", got)
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
		GrahamIntrinsic: fp(144.45), DCFIntrinsic: fp(121.16),
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
		GrahamIntrinsic: fp(200), DCFIntrinsic: fp(220),
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
	if res.Justification == "" ||
		len(res.Justification) < 20 ||
		res.Dimensions == nil || len(res.Dimensions) != 4 {
		t.Fatalf("justificación/dimensiones incompletas: %q", res.Justification)
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
}
