package score

import "testing"

// dimension-level regression cases (plan D5 thresholds).
func TestPBands(t *testing.T) {
	cases := []struct {
		v    float64
		want float64
	}{
		{0.8, 100}, {1.5, 75}, {3, 50}, {8, 25}, {12, 10},
	}
	for _, c := range cases {
		metrics := map[string]*float64{"pb_ratio": fp(c.v)}
		if got := scoreFundamentals(metrics); got != c.want {
			t.Fatalf("pb=%v: esperado %v, got %v", c.v, c.want, got)
		}
	}
}

func TestROEBands(t *testing.T) {
	cases := []struct {
		roe  float64 // fracción
		want float64
	}{
		{0.04, 10}, {0.08, 25}, {0.12, 50}, {0.18, 75}, {0.30, 100},
	}
	for _, c := range cases {
		metrics := map[string]*float64{"roe": fp(c.roe)}
		if got := scoreFundamentals(metrics); got != c.want {
			t.Fatalf("roe=%v: esperado %v, got %v", c.roe, c.want, got)
		}
	}
}

func TestFCFYieldBands(t *testing.T) {
	cases := []struct {
		v    float64 // %
		want float64
	}{
		{0.5, 10}, {2, 25}, {4, 50}, {6, 75}, {9, 100},
	}
	for _, c := range cases {
		metrics := map[string]*float64{"fcf_yield": fp(c.v)}
		if got := scoreFundamentals(metrics); got != c.want {
			t.Fatalf("fcf_yield=%v: esperado %v, got %v", c.v, c.want, got)
		}
	}
}

func TestJustificationContainsKeyParts(t *testing.T) {
	input := ScoreInput{
		Ticker: "AAPL", Price: 230, GrahamIntrinsic: fp(144.45),
		Metrics: aaplMetrics(), SectorCount: 2,
		SMA50: fp(250), SMA200: fp(200), MarginOfSafety: 30,
	}
	res := CalculateScore(input)
	for _, want := range []string{"AAPL", "score", "Valoración", "Métricas", "Comparables", "Tendencia", "VENDER"} {
		if !containsStr(res.Justification, want) {
			t.Fatalf("justificación sin %q: %q", want, res.Justification)
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
