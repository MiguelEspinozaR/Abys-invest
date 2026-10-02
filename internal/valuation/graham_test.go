package valuation

import (
	"math"
	"testing"
)

// f64v is the test helper for a *float64 literal.
func f64v(v float64) *float64 { return &v }

func nearly(t *testing.T, got, want float64, label string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

func TestGrahamIntrinsicFormula(t *testing.T) {
	tests := []struct {
		name string
		eps  float64
		g    float64
		want float64
	}{
		{"legacy 7 reproduces M3 (x22.5)", 4.0, 7.0, 90.0},
		{"individual growth 8", 5.0, 8.0, 122.5},
		{"individual growth 0.5", 10.0, 0.5, 95.0},
		{"individual growth 20", 1.0, 20.0, 48.5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := grahamValue(tc.eps, tc.g)
			if got == nil {
				t.Fatal("expected a value")
			}
			nearly(t, *got, tc.want, "graham")
		})
	}
}

func TestGrahamNilOnNonPositiveOrNonFinite(t *testing.T) {
	neg := f64v(-1.0)
	zero := f64v(0.0)
	nan := math.NaN()
	inf := math.Inf(1)
	if v := grahamValue(*neg, 8); v != nil {
		t.Errorf("negative EPS: expected nil, got %v", *v)
	}
	if v := grahamValue(*zero, 8); v != nil {
		t.Errorf("zero EPS: expected nil, got %v", *v)
	}
	if v := grahamValue(1, nan); v != nil {
		t.Errorf("NaN growth: expected nil, got %v", *v)
	}
	if v := grahamValue(1, inf); v != nil {
		t.Errorf("Inf growth: expected nil, got %v", *v)
	}
	// g = -4 makes the multiple 0.5: positive, so it is a real (bad) value and
	// must NOT be swallowed.
	if v := grahamValue(1, -4); v == nil || *v != 0.5 {
		t.Errorf("g=-4: expected 0.5, got %v", v)
	}
}

func TestGrahamScenarios(t *testing.T) {
	cfg := DefaultConfig()
	in := Inputs{Ticker: "TEST", EPS: f64v(5.0), NormalizedGrowthRate: f64v(8.0), GrowthConfidence: ConfidenceHigh}

	m := CalcGrahamScenarios(in, cfg, 8.0, false)
	if m.Status != StatusAvailable {
		t.Fatalf("status = %s, want available (%v)", m.Status, m.Reasons)
	}
	if m.Confidence != ConfidenceHigh {
		t.Errorf("confidence = %s, want high", m.Confidence)
	}
	if m.scenarioCount() != 3 {
		t.Fatalf("scenarios = %d, want 3", m.scenarioCount())
	}
	nearly(t, *m.Bear, 5*(2*4+8.5), "bear")
	nearly(t, *m.Base, 5*(2*8+8.5), "base")
	nearly(t, *m.Bull, 5*(2*11+8.5), "bull")
}

func TestGrahamUnavailableCases(t *testing.T) {
	cfg := DefaultConfig()

	t.Run("EPS nil", func(t *testing.T) {
		m := CalcGrahamScenarios(Inputs{NormalizedGrowthRate: f64v(8)}, cfg, 8, false)
		if m.Status != StatusUnavailable || !hasReason(m.Reasons, ReasonEPSNonPositive) {
			t.Errorf("status=%s reasons=%v, want unavailable/%s", m.Status, m.Reasons, ReasonEPSNonPositive)
		}
		if m.Base != nil {
			t.Error("base must be nil, never 0")
		}
	})

	t.Run("EPS zero", func(t *testing.T) {
		m := CalcGrahamScenarios(Inputs{EPS: f64v(0), NormalizedGrowthRate: f64v(8)}, cfg, 8, false)
		if m.Status != StatusUnavailable || !hasReason(m.Reasons, ReasonEPSNonPositive) {
			t.Errorf("status=%s reasons=%v", m.Status, m.Reasons)
		}
	})

	t.Run("growth non finite", func(t *testing.T) {
		m := CalcGrahamScenarios(Inputs{EPS: f64v(2)}, cfg, math.NaN(), false)
		if m.Status != StatusUnavailable || !hasReason(m.Reasons, ReasonGrowthUnavailable) {
			t.Errorf("status=%s reasons=%v", m.Status, m.Reasons)
		}
	})

	t.Run("bear growth collapses to 0 but base survives", func(t *testing.T) {
		// g=3 with a -4 delta: the bear scenario has g=-1 ⇒ dropped. The method
		// stays available with 2 scenarios (degraded, not discarded).
		m := CalcGrahamScenarios(Inputs{EPS: f64v(2), NormalizedGrowthRate: f64v(3)}, cfg, 3, false)
		if m.Status != StatusAvailable {
			t.Fatalf("status = %s, want available", m.Status)
		}
		if m.Bear != nil {
			t.Error("bear must be nil when g+delta <= 0")
		}
		if m.scenarioCount() != 2 {
			t.Errorf("scenarios = %d, want 2", m.scenarioCount())
		}
		if !hasReason(m.Reasons, ReasonGrowthNonPositive) {
			t.Errorf("reasons = %v, want %s", m.Reasons, ReasonGrowthNonPositive)
		}
	})

	t.Run("legacy fallback leaves no confidence marker", func(t *testing.T) {
		// A1: with the fallback the method is computed normally and does NOT
		// advertise a "fallback 7" confidence of its own.
		m := CalcGrahamScenarios(Inputs{EPS: f64v(4)}, cfg, 7, true)
		if m.Status != StatusAvailable {
			t.Fatalf("status = %s, want available", m.Status)
		}
		nearly(t, *m.Base, 4*22.5, "base with legacy 7")
		if m.Confidence != "" {
			t.Errorf("confidence = %q, want empty with the fallback", m.Confidence)
		}
		if hasReason(m.Reasons, ReasonGrowthFallback) {
			t.Errorf("reasons = %v, want no fallback marker on the method", m.Reasons)
		}
	})
}

func hasReason(reasons []string, want string) bool {
	for _, r := range reasons {
		if r == want {
			return true
		}
	}
	return false
}
