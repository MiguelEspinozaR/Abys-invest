package valuation

import (
	"math"
	"testing"
)

// legacyDCF is the M3 formula, copied here verbatim as the regression oracle.
// The M6b engine must reproduce it BIT FOR BIT when the growth is flat (§7 with
// the transition disabled), which is what the §26 reproducibility guarantee
// needs: the rows already stored under the 1.x contract stay comparable.
func legacyDCF(fcf, growthPct, waccPct, terminalPct float64, years int, shares, netDebt float64) float64 {
	dr := waccPct / 100
	gt := terminalPct / 100
	pv := 0.0
	ff := fcf
	for t := 1; t <= years; t++ {
		ff = fcf * math.Pow(1+growthPct/100, float64(t))
		pv += ff / math.Pow(1+dr, float64(t))
	}
	terminal := ff * (1 + gt) / (dr - gt)
	pv += terminal / math.Pow(1+dr, float64(years))
	return (pv - netDebt) / shares
}

func TestDCFFlatPathReproducesM3BitForBit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Transition = false
	cfg.HorizonYears = 5
	cfg.TerminalGrowth = 2.5

	cases := []struct {
		name                             string
		fcf, g, wacc, term, shares, debt float64
		years                            int
	}{
		{"m3 default", 100e6, 7, 10, 2.5, 1e9, 0, 5},
		{"with net debt", 100e6, 7, 10, 2.5, 1e9, 500e6, 5},
		{"10 years", 42e6, 3.25, 8.5, 2, 500e6, 12e6, 10},
		{"1 year", 100e6, 7, 10, 2.5, 1e9, 0, 1},
		{"wacc near terminal", 100e6, 5, 6, 5, 1e9, 0, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := growthTransition(tc.g, tc.term, cfg.TransitionCapPP, tc.years, false)
			got := dcfValue(f64v(tc.fcf), path, tc.wacc, tc.term, tc.years, f64v(tc.shares), f64v(tc.debt))
			if got == nil {
				t.Fatal("expected a value")
			}
			want := legacyDCF(tc.fcf, tc.g, tc.wacc, tc.term, tc.years, tc.shares, tc.debt)
			if *got != want {
				t.Fatalf("m6b = %v, m3 = %v (differ by %g)", *got, want, math.Abs(*got-want))
			}
		})
	}
}

func TestGrowthTransition(t *testing.T) {
	t.Run("linear from anchor to terminal", func(t *testing.T) {
		// g_initial 12, terminal 2.5, cap 5 ⇒ anchor = 7.5.
		path := growthTransition(12, 2.5, 5, 5, true)
		want := []float64{7.5, 6.25, 5, 3.75, 2.5}
		if len(path) != len(want) {
			t.Fatalf("len = %d, want %d", len(path), len(want))
		}
		for i := range want {
			if math.Abs(path[i]-want[i]) > 1e-9 {
				t.Errorf("year %d = %v, want %v", i+1, path[i], want[i])
			}
		}
	})

	t.Run("anchor below terminal is not raised", func(t *testing.T) {
		path := growthTransition(1, 2.5, 5, 3, true)
		if path[0] != 1 {
			t.Errorf("year 1 = %v, want 1 (the anchor only caps the jump)", path[0])
		}
		if math.Abs(path[2]-2.5) > 1e-9 {
			t.Errorf("year 3 = %v, want 2.5", path[2])
		}
	})

	t.Run("single year collapses to terminal", func(t *testing.T) {
		path := growthTransition(20, 2.5, 5, 1, true)
		if len(path) != 1 || path[0] != 2.5 {
			t.Errorf("path = %v, want [2.5]", path)
		}
	})

	t.Run("disabled returns a flat path", func(t *testing.T) {
		path := growthTransition(3, 2.5, 5, 4, false)
		if len(path) != 4 {
			t.Fatalf("path = %v, want 4 values", path)
		}
		for i, g := range path {
			if g != 3 {
				t.Errorf("year %d = %v, want a flat 3", i+1, g)
			}
		}
	})

	t.Run("no years", func(t *testing.T) {
		if p := growthTransition(3, 2.5, 5, 0, true); p != nil {
			t.Errorf("path = %v, want nil", p)
		}
	})
}

func TestDCFValueNilCases(t *testing.T) {
	path := growthTransition(7, 2.5, 5, 5, false)
	shares := f64v(1e9)

	t.Run("shares nil", func(t *testing.T) {
		if v := dcfValue(f64v(100e6), path, 10, 2.5, 5, nil, nil); v != nil {
			t.Error("expected nil")
		}
	})
	t.Run("shares zero", func(t *testing.T) {
		if v := dcfValue(f64v(100e6), path, 10, 2.5, 5, f64v(0), nil); v != nil {
			t.Error("expected nil")
		}
	})
	t.Run("wacc below terminal", func(t *testing.T) {
		if v := dcfValue(f64v(100e6), path, 2, 2.5, 5, shares, nil); v != nil {
			t.Error("expected nil for a degenerate perpetuity")
		}
	})
	t.Run("wacc equal terminal", func(t *testing.T) {
		if v := dcfValue(f64v(100e6), path, 2.5, 2.5, 5, shares, nil); v != nil {
			t.Error("expected nil when wacc == terminal")
		}
	})
	t.Run("empty path", func(t *testing.T) {
		if v := dcfValue(f64v(100e6), nil, 10, 2.5, 5, shares, nil); v != nil {
			t.Error("expected nil")
		}
	})
	t.Run("net debt above equity", func(t *testing.T) {
		if v := dcfValue(f64v(100e6), path, 10, 2.5, 5, shares, f64v(1e12)); v != nil {
			t.Error("expected nil when the equity after net debt is negative")
		}
	})
	t.Run("net debt nil means zero", func(t *testing.T) {
		withDebt := dcfValue(f64v(100e6), path, 10, 2.5, 5, shares, f64v(0))
		nilDebt := dcfValue(f64v(100e6), path, 10, 2.5, 5, shares, nil)
		if withDebt == nil || nilDebt == nil || *withDebt != *nilDebt {
			t.Errorf("a nil net debt must behave as 0: %v vs %v", withDebt, nilDebt)
		}
	})
}

func TestDiscountRatePrecedence(t *testing.T) {
	cfg := DefaultConfig()

	t.Run("level 1 uses wacc_metrics.wacc verbatim", func(t *testing.T) {
		r := resolveDiscountRate(Inputs{WACC: f64v(8.5), WACCSource: WACCSourceCAPMHybrid}, cfg)
		if r.level != 1 || r.rate != 8.5 || r.source != WACCSourceCAPMHybrid || r.reason != "" {
			t.Errorf("rate = %+v, want level 1 / 8.5 / capm_hybrid / no reason", r)
		}
	})

	t.Run("level 1 accepts an empty source without relabelling", func(t *testing.T) {
		r := resolveDiscountRate(Inputs{WACC: f64v(8.5)}, cfg)
		if r.level != 1 || r.source != WACCSourceCAPMIndividual {
			t.Errorf("rate = %+v", r)
		}
	})

	t.Run("level 2 uses the cost of equity with a reason", func(t *testing.T) {
		r := resolveDiscountRate(Inputs{CostOfEquity: f64v(11)}, cfg)
		if r.level != 2 || r.rate != 11 || r.source != WACCSourceCostOfEquity || r.reason != ReasonWACCCostOfEquity {
			t.Errorf("rate = %+v", r)
		}
	})

	t.Run("level 3 uses WACC_FALLBACK", func(t *testing.T) {
		cfg3 := cfg
		cfg3.WACCFallback = 9
		r := resolveDiscountRate(Inputs{}, cfg3)
		if r.level != 3 || r.rate != 9 || r.source != WACCSourceConfiguredFB || r.reason != ReasonWACCConfigured {
			t.Errorf("rate = %+v", r)
		}
	})

	t.Run("level 4 uses the legacy DCF_DISCOUNT_RATE", func(t *testing.T) {
		cfg4 := cfg
		cfg4.WACCFallback = 0
		cfg4.DiscountFallback = 10
		r := resolveDiscountRate(Inputs{}, cfg4)
		if r.level != 4 || r.rate != 10 || r.source != WACCSourceLegacyDiscountE || r.reason != ReasonWACCLegacyEnv {
			t.Errorf("rate = %+v", r)
		}
	})

	t.Run("no rate at all is never fabricated", func(t *testing.T) {
		cfg0 := cfg
		cfg0.WACCFallback = 0
		cfg0.DiscountFallback = 0
		r := resolveDiscountRate(Inputs{WACC: f64v(0), CostOfEquity: f64v(-3)}, cfg0)
		if r.level != 0 || !math.IsNaN(r.rate) {
			t.Errorf("rate = %+v, want level 0 and NaN", r)
		}
	})
}

func TestDCFScenarios(t *testing.T) {
	cfg := DefaultConfig()
	in := Inputs{
		FreeCashFlow:         f64v(100e6),
		SharesOutstanding:    f64v(1e9),
		NetDebt:              f64v(0),
		NormalizedGrowthRate: f64v(7),
		GrowthConfidence:     ConfidenceHigh,
		WACC:                 f64v(10),
		WACCSource:           WACCSourceCAPMIndividual,
		WACCConfidence:       ConfidenceHigh,
		BetaObserved:         true,
	}
	rate := resolveDiscountRate(in, cfg)
	m := CalcDCFScenarios(in, cfg, 7, rate)

	if m.Status != StatusAvailable {
		t.Fatalf("status = %s (%v)", m.Status, m.Reasons)
	}
	if m.Confidence != ConfidenceHigh {
		t.Errorf("confidence = %s, want high", m.Confidence)
	}
	if m.Bear == nil || m.Base == nil || m.Bull == nil {
		t.Fatal("the three scenarios must exist")
	}
	// §8 deltas: bear g-4, wacc+1.5, terminal-0.5; bull g+3, wacc-1, terminal+0.5.
	bearWant := dcfValue(f64v(100e6), growthTransition(3, 2.0, 5, 5, cfg.Transition), 11.5, 2.0, 5, in.SharesOutstanding, in.NetDebt)
	bullWant := dcfValue(f64v(100e6), growthTransition(10, 3.0, 5, 5, cfg.Transition), 9.0, 3.0, 5, in.SharesOutstanding, in.NetDebt)
	nearly(t, *m.Bear, *bearWant, "bear")
	nearly(t, *m.Bull, *bullWant, "bull")
	if !(*m.Bear < *m.Base && *m.Base < *m.Bull) {
		t.Errorf("expected bear < base < bull, got %v %v %v", *m.Bear, *m.Base, *m.Bull)
	}
}

func TestDCFUnavailableAndDegraded(t *testing.T) {
	cfg := DefaultConfig()
	rate := resolveDiscountRate(Inputs{WACC: f64v(10), WACCSource: WACCSourceCAPMIndividual}, cfg)

	t.Run("FCF nil", func(t *testing.T) {
		m := CalcDCFScenarios(Inputs{SharesOutstanding: f64v(1e9)}, cfg, 7, rate)
		if m.Status != StatusUnavailable || !hasReason(m.Reasons, ReasonFCFNonPositive) {
			t.Errorf("status=%s reasons=%v", m.Status, m.Reasons)
		}
	})
	t.Run("FCF negative", func(t *testing.T) {
		m := CalcDCFScenarios(Inputs{FreeCashFlow: f64v(-1e6), SharesOutstanding: f64v(1e9)}, cfg, 7, rate)
		if m.Status != StatusUnavailable || !hasReason(m.Reasons, ReasonFCFNonPositive) {
			t.Errorf("status=%s reasons=%v", m.Status, m.Reasons)
		}
	})
	t.Run("shares nil", func(t *testing.T) {
		m := CalcDCFScenarios(Inputs{FreeCashFlow: f64v(1e6)}, cfg, 7, rate)
		if m.Status != StatusUnavailable || !hasReason(m.Reasons, ReasonSharesNonPositive) {
			t.Errorf("status=%s reasons=%v", m.Status, m.Reasons)
		}
	})
	t.Run("invalid horizon", func(t *testing.T) {
		bad := cfg
		bad.HorizonYears = 0
		m := CalcDCFScenarios(Inputs{FreeCashFlow: f64v(1e6), SharesOutstanding: f64v(1e9)}, bad, 7, rate)
		if m.Status != StatusUnavailable || !hasReason(m.Reasons, ReasonInvalidHorizon) {
			t.Errorf("status=%s reasons=%v", m.Status, m.Reasons)
		}
	})
	t.Run("no discount rate", func(t *testing.T) {
		none := resolveDiscountRate(Inputs{}, Config{})
		m := CalcDCFScenarios(Inputs{FreeCashFlow: f64v(1e6), SharesOutstanding: f64v(1e9)}, cfg, 7, none)
		if m.Status != StatusUnavailable || !hasReason(m.Reasons, ReasonWACCConfigured) {
			t.Errorf("status=%s reasons=%v", m.Status, m.Reasons)
		}
	})
	t.Run("wacc below terminal kills every scenario without degrading", func(t *testing.T) {
		// terminal 3 and wacc 1 kill all three scenarios: base (1<=3), bear
		// (2.5<=2.5) and bull (0<=3).
		bad := cfg
		bad.TerminalGrowth = 3
		weak := resolveDiscountRate(Inputs{WACC: f64v(1), WACCSource: WACCSourceCAPMIndividual}, bad)
		m := CalcDCFScenarios(Inputs{FreeCashFlow: f64v(1e8), SharesOutstanding: f64v(1e9)}, bad, 7, weak)
		if m.Status != StatusUnavailable || !hasReason(m.Reasons, ReasonWACCBelowTerminal) {
			t.Errorf("status=%s reasons=%v", m.Status, m.Reasons)
		}
	})
	t.Run("unknown net debt is recorded but valued", func(t *testing.T) {
		m := CalcDCFScenarios(Inputs{FreeCashFlow: f64v(1e8), SharesOutstanding: f64v(1e9)}, cfg, 7, rate)
		if m.Status != StatusAvailable {
			t.Fatalf("status = %s", m.Status)
		}
		if !hasReason(m.Reasons, ReasonNetDebtUnknown) {
			t.Errorf("reasons = %v, want %s", m.Reasons, ReasonNetDebtUnknown)
		}
	})
}

func TestSensitivityGrid(t *testing.T) {
	cfg := DefaultConfig()
	in := Inputs{
		FreeCashFlow:         f64v(100e6),
		SharesOutstanding:    f64v(1e9),
		NetDebt:              f64v(0),
		NormalizedGrowthRate: f64v(7),
	}
	rate := resolveDiscountRate(Inputs{WACC: f64v(10), WACCSource: WACCSourceCAPMIndividual}, cfg)
	grid := calcSensitivity(in, cfg, 7, rate)

	if len(grid) != 9 { // 3x3
		t.Fatalf("points = %d, want 9", len(grid))
	}
	center := grid[4] // wacc outer / growth inner
	if center.Growth != 7 || math.Abs(center.WACC-10) > 1e-9 {
		t.Errorf("center = %+v, want growth 7 / wacc 10", center)
	}
	baseWant := dcfValue(f64v(100e6), growthTransition(7, 2.5, 5, 5, cfg.Transition), 10, 2.5, 5, in.SharesOutstanding, in.NetDebt)
	nearly(t, center.DCFBase, *baseWant, "center value")
	// The first cell is the bear corner (wacc+1.5, g-4) and the last the bull
	// corner (wacc-1, g+3).
	if !(grid[0].DCFBase < grid[len(grid)-1].DCFBase) {
		t.Errorf("bear corner %v must be below the bull corner %v", grid[0].DCFBase, grid[len(grid)-1].DCFBase)
	}

	t.Run("no discount rate means no grid", func(t *testing.T) {
		if g := calcSensitivity(in, cfg, 7, resolveDiscountRate(Inputs{}, Config{})); g != nil {
			t.Errorf("grid = %v, want nil", g)
		}
	})
}
