package valuation

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// goodInputs is a fully covered case: an observed WACC, a normalised growth,
// both methods computable and a price. Everything else in the tests starts
// here and removes ONE input at a time.
func goodInputs() Inputs {
	return Inputs{
		Ticker:               "AAPL",
		AsOf:                 time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC),
		Price:                f64v(100),
		EPS:                  f64v(5),
		FreeCashFlow:         f64v(100e6),
		SharesOutstanding:    f64v(1e9),
		NetDebt:              f64v(0),
		NormalizedGrowthRate: f64v(7),
		GrowthConfidence:     ConfidenceHigh,
		GrowthSource:         "individual_normalized",
		GrowthModelVersion:   "1.0.0",
		WACC:                 f64v(9.5),
		WACCSource:           WACCSourceCAPMIndividual,
		WACCConfidence:       ConfidenceHigh,
		WACCModelVersion:     "1.0.0",
		BetaObserved:         true,
	}
}

// flatConfig is DefaultConfig with the §7 transition disabled: the exact M3
// behaviour, used by the regression assertions.
func flatConfig() Config {
	cfg := DefaultConfig()
	cfg.Transition = false
	return cfg
}

func TestCalculateFullCoverage(t *testing.T) {
	in := goodInputs()
	res := Calculate(in, DefaultConfig())

	if res.Status != StatusAvailable {
		t.Fatalf("status = %s (%v)", res.Status, res.Reasons)
	}
	if res.Confidence != ConfidenceHigh {
		t.Errorf("confidence = %s (%v), want high", res.Confidence, res.Reasons)
	}
	if len(res.Reasons) != 0 {
		t.Errorf("reasons = %v, want none for a fully covered case", res.Reasons)
	}
	if res.ModelVersion != ModelVersion {
		t.Errorf("model_version = %s, want %s", res.ModelVersion, ModelVersion)
	}
	if res.AsOf != "2026-09-28" {
		t.Errorf("as_of = %s", res.AsOf)
	}
	// A1/D9 provenance must be echoed on the inputs.
	if res.Inputs.WACCUsed == nil || *res.Inputs.WACCUsed != 9.5 || res.Inputs.DiscountSource != WACCSourceCAPMIndividual || res.Inputs.DiscountLevel != 1 {
		t.Errorf("discount provenance = %+v", res.Inputs)
	}
	if res.Inputs.GrowthFallbackUsed {
		t.Error("growth fallback must not be reported when the normalised rate exists")
	}
	// Both methods have 3 scenarios and there is a 4-component uncertainty.
	for _, m := range []struct {
		name string
		m    Method
	}{{"graham", res.Graham}, {"dcf", res.DCF}} {
		if m.m.scenarioCount() != 3 {
			t.Errorf("%s scenarios = %d, want 3", m.name, m.m.scenarioCount())
		}
		if m.m.Bear == nil || m.m.Base == nil || m.m.Bull == nil {
			t.Fatalf("%s has a nil scenario", m.name)
		}
		if !(*m.m.Bear < *m.m.Base && *m.m.Base < *m.m.Bull) {
			t.Errorf("%s: expected bear < base < bull, got %v %v %v", m.name, *m.m.Bear, *m.m.Base, *m.m.Bull)
		}
	}
	if res.Uncertainty.Components != 4 {
		t.Errorf("uncertainty components = %d, want 4", res.Uncertainty.Components)
	}
	if res.Uncertainty.Mean == nil || res.Uncertainty.StdDev == nil || res.Uncertainty.Dispersion == nil {
		t.Fatalf("uncertainty incomplete: %+v", res.Uncertainty)
	}
	if res.MOS.GrahamBase == nil || res.MOS.DCFBase == nil || res.MOS.Target != 30 {
		t.Errorf("mos = %+v", res.MOS)
	}
	// §11: MOS = 100 × (I − P) / I.
	nearly(t, *res.MOS.GrahamBase, 100*(5*22.5-100)/(5*22.5), "graham base MOS")
	if res.MOS.GrahamBase == nil || *res.MOS.GrahamBase < 0 {
		t.Errorf("price above the intrinsic value must give a negative MOS, got %v", res.MOS.GrahamBase)
	}
	// §15 additive metrics.
	if res.PEG == nil {
		t.Error("PEG must be computable with a positive EPS and a positive growth")
	} else {
		nearly(t, *res.PEG, (100.0/5.0)/7.0, "PEG")
	}
	if res.PFcf == nil {
		t.Error("P/FCF must be computable")
	} else {
		nearly(t, *res.PFcf, (100.0*1e9)/100e6, "P/FCF")
	}
	// §8 grid.
	if len(res.Sensitivity) != 9 {
		t.Errorf("sensitivity points = %d, want 9", len(res.Sensitivity))
	}
}

func TestCalculateNoValuationPolicy(t *testing.T) {
	t.Run("no method computable is the ONLY unavailable case", func(t *testing.T) {
		in := Inputs{Ticker: "LOSS", AsOf: goodInputs().AsOf, Price: f64v(10)}
		res := Calculate(in, DefaultConfig())
		if res.Status != StatusUnavailable {
			t.Fatalf("status = %s, want unavailable", res.Status)
		}
		if res.Confidence != ConfidenceLow {
			t.Errorf("confidence = %s, want low", res.Confidence)
		}
		if !hasReason(res.Reasons, ReasonNoValuation) {
			t.Errorf("reasons = %v, want %s", res.Reasons, ReasonNoValuation)
		}
		if res.Uncertainty.Mean != nil || res.Uncertainty.StdDev != nil || res.Uncertainty.Dispersion != nil {
			t.Error("uncertainty must stay nil (not 0) without values")
		}
	})

	t.Run("one method survives", func(t *testing.T) {
		in := goodInputs()
		in.FreeCashFlow = nil // no DCF
		res := Calculate(in, DefaultConfig())
		if res.Status != StatusAvailable {
			t.Fatalf("status = %s, want available", res.Status)
		}
		if res.DCF.Status != StatusUnavailable || !hasReason(res.DCF.Reasons, ReasonFCFNonPositive) {
			t.Errorf("dcf = %s %v", res.DCF.Status, res.DCF.Reasons)
		}
		if res.Graham.Status != StatusAvailable || res.Graham.Base == nil {
			t.Errorf("graham = %s, want available", res.Graham.Status)
		}
		// A single uncertainty component ⇒ medium (plan rule 6).
		if res.Uncertainty.Components != 1 {
			t.Errorf("components = %d, want 1", res.Uncertainty.Components)
		}
		if res.Confidence != ConfidenceMedium || !hasReason(res.Reasons, ReasonInsufficientComps) {
			t.Errorf("confidence = %s reasons = %v, want medium/%s", res.Confidence, res.Reasons, ReasonInsufficientComps)
		}
	})
}

func TestCalculateGrowthFallbackA1(t *testing.T) {
	in := goodInputs()
	in.NormalizedGrowthRate = nil
	in.GrowthConfidence = ""
	res := Calculate(in, DefaultConfig())

	if res.Status != StatusAvailable {
		t.Fatalf("status = %s: a missing growth is NOT a valuation failure (A1)", res.Status)
	}
	if res.DCF.Status != StatusAvailable || res.Graham.Status != StatusAvailable {
		t.Fatalf("methods = %s/%s", res.Graham.Status, res.DCF.Status)
	}
	if !res.Inputs.GrowthFallbackUsed {
		t.Error("the snapshot must record that the fallback was used")
	}
	// Graham with g=7 is the legacy EPS × 22.5.
	nearly(t, *res.Graham.Base, 5*22.5, "graham base with the legacy 7")
	// DCF base uses the legacy 7 as the START of the §7 path (anchored at 7 by
	// the transition) and converges to the terminal growth. With the transition
	// disabled this is exactly the M3 value — see
	// TestDCFFlatPathReproducesM3BitForBit.
	wantDCF := dcfValue(f64v(100e6), growthTransition(7, 2.5, 5, 5, DefaultConfig().Transition), 9.5, 2.5, 5, f64v(1e9), f64v(0))
	nearly(t, *res.DCF.Base, *wantDCF, "dcf base with the legacy 7")
	flat := Calculate(in, flatConfig())
	nearly(t, *flat.DCF.Base, legacyDCF(100e6, 7, 9.5, 2.5, 5, 1e9, 0), "dcf base with the legacy 7 and a flat path")
	// Confidence is capped to low and the reason is reported at the valuation
	// level (never as a marker on the method).
	if res.Confidence != ConfidenceLow || !hasReason(res.Reasons, ReasonGrowthFallback) {
		t.Errorf("confidence = %s reasons = %v, want low/%s", res.Confidence, res.Reasons, ReasonGrowthFallback)
	}
	if res.Graham.Reasons != nil && hasReason(res.Graham.Reasons, ReasonGrowthFallback) {
		t.Error("the method must not carry a fallback marker (A1)")
	}
	// §15: an unreliable growth ⇒ PEG nil, but P/FCF still computable.
	if res.PEG != nil {
		t.Errorf("PEG = %v, want nil without a normalised growth", *res.PEG)
	}
	if res.PFcf == nil {
		t.Error("P/FCF does not depend on the growth and must stay available")
	}
}

func TestCalculateConfidenceLadder(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Inputs)
		cfg       func(Config) Config
		want      Confidence
		wantRsn   string
		wantMOSNS bool
	}{
		{
			name:   "observed wacc and high inputs",
			mutate: func(in *Inputs) {},
			want:   ConfidenceHigh,
		},
		{
			name: "cost of equity degrades to medium",
			mutate: func(in *Inputs) {
				in.WACC = nil
				in.CostOfEquity = f64v(11)
			},
			want:    ConfidenceMedium,
			wantRsn: ReasonWACCCostOfEquity,
		},
		{
			name:    "configured fallback degrades to low (no WACC value)",
			mutate:  func(in *Inputs) { in.WACC, in.CostOfEquity = nil, nil },
			want:    ConfidenceLow,
			wantRsn: ReasonWACCConfigured,
		},
		{
			name: "configured fallback with persisted WACC value is level 3 (B2)",
			mutate: func(in *Inputs) {
				in.WACC = f64v(9.0) // numeric fallback value from wacc_metrics
				in.WACCSource = WACCSourceConfiguredFB
				in.WACCConfidence = ConfidenceLow
				in.BetaObserved = false
				in.CostOfEquity = nil
			},
			want:    ConfidenceLow,
			wantRsn: ReasonWACCConfigured,
		},
		{
			name: "legacy env degrades to low",
			mutate: func(in *Inputs) {
				in.WACC, in.CostOfEquity = nil, nil
			},
			cfg:     func(c Config) Config { c.WACCFallback = 0; return c },
			want:    ConfidenceLow,
			wantRsn: ReasonWACCLegacyEnv,
		},
		{
			name:    "unknown net debt degrades to medium",
			mutate:  func(in *Inputs) { in.NetDebt = nil },
			want:    ConfidenceMedium,
			wantRsn: ReasonNetDebtUnknown,
		},
		{
			name:    "medium growth confidence caps the result",
			mutate:  func(in *Inputs) { in.GrowthConfidence = ConfidenceMedium },
			want:    ConfidenceMedium,
			wantRsn: ReasonInputConfidence,
		},
		{
			name: "high dispersion does NOT degrade (A4)",
			mutate: func(in *Inputs) {
				in.EPS = f64v(20) // a very high Graham value vs the DCF one
			},
			want: ConfidenceHigh,
		},
		{
			name: "hybrid wacc without an observed beta degrades",
			mutate: func(in *Inputs) {
				in.WACCSource = WACCSourceCAPMHybrid
				in.BetaObserved = false
			},
			want:    ConfidenceMedium,
			wantRsn: ReasonWACCConfigured,
		},
		{
			name: "missing price keeps the valuation and reports it",
			mutate: func(in *Inputs) {
				in.Price = nil
			},
			want:      ConfidenceHigh,
			wantMOSNS: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := goodInputs()
			tc.mutate(&in)
			cfg := DefaultConfig()
			if tc.cfg != nil {
				cfg = tc.cfg(cfg)
			}
			res := Calculate(in, cfg)
			if res.Confidence != tc.want {
				t.Errorf("confidence = %s (%v), want %s", res.Confidence, res.Reasons, tc.want)
			}
			if tc.wantRsn != "" && !hasReason(res.Reasons, tc.wantRsn) {
				t.Errorf("reasons = %v, want %s", res.Reasons, tc.wantRsn)
			}
			if tc.wantMOSNS {
				if res.MOS.HasValue() {
					t.Errorf("MOS without a price must stay nil: %+v", res.MOS)
				}
				if res.MOS.Reason != ReasonNoPrice {
					t.Errorf("MOS reason = %q, want %s", res.MOS.Reason, ReasonNoPrice)
				}
			}
			// §10: missing price never removes a valuation value.
			if res.Graham.Base == nil {
				t.Error("graham must survive without a price")
			}
		})
	}
}

func TestCalculateWACCBelowTerminalKeepsGraham(t *testing.T) {
	in := goodInputs()
	in.WACC = f64v(1)
	cfg := DefaultConfig()
	cfg.TerminalGrowth = 3 // kills base (1<=3), bear (2.5<=2.5) and bull (0<=3)
	res := Calculate(in, cfg)

	if res.DCF.Status != StatusUnavailable || !hasReason(res.DCF.Reasons, ReasonWACCBelowTerminal) {
		t.Errorf("dcf = %s %v", res.DCF.Status, res.DCF.Reasons)
	}
	if res.Graham.Status != StatusAvailable {
		t.Errorf("graham = %s, want available", res.Graham.Status)
	}
	if res.Status != StatusAvailable {
		t.Errorf("global status = %s, want available", res.Status)
	}
}

func TestCalculateMOS(t *testing.T) {
	in := goodInputs()
	in.Price = f64v(50)
	res := Calculate(in, DefaultConfig())
	if res.MOS.GrahamBase == nil {
		t.Fatal("graham MOS nil")
	}
	nearly(t, *res.MOS.GrahamBase, 100*(5*22.5-50)/(5*22.5), "graham MOS")
	for name, v := range map[string]*float64{
		"dcf bear": res.MOS.DCFBear, "dcf base": res.MOS.DCFBase, "dcf bull": res.MOS.DCFBull,
	} {
		if v == nil {
			t.Errorf("%s MOS nil", name)
		}
	}
	if !res.MOS.HasValue() {
		t.Error("HasValue must be true")
	}
	t.Run("negative price", func(t *testing.T) {
		bad := in
		bad.Price = f64v(-1)
		if m := CalcMOS(bad, Calculate(bad, DefaultConfig()).Graham, Calculate(bad, DefaultConfig()).DCF, DefaultConfig()); m.HasValue() {
			t.Error("a negative price must not produce a MOS")
		}
	})
}

func TestUncertainty(t *testing.T) {
	t.Run("known dispersion", func(t *testing.T) {
		g := Method{Status: StatusAvailable, Base: f64v(100)}
		d := Method{Status: StatusAvailable, Bear: f64v(80), Base: f64v(100), Bull: f64v(120)}
		u := CalcUncertainty(g, d)
		if u.Components != 4 || u.Mean == nil || u.StdDev == nil || u.Dispersion == nil {
			t.Fatalf("uncertainty = %+v", u)
		}
		nearly(t, *u.Mean, 100, "mean")
		nearly(t, *u.StdDev, math.Sqrt(200), "std (population)")
		nearly(t, *u.Dispersion, math.Sqrt(200)/100, "dispersion")
	})

	t.Run("identical values give dispersion 0, not nil", func(t *testing.T) {
		g := Method{Status: StatusAvailable, Base: f64v(50)}
		d := Method{Status: StatusAvailable, Bear: f64v(50), Base: f64v(50), Bull: f64v(50)}
		u := CalcUncertainty(g, d)
		if u.Dispersion == nil || *u.Dispersion != 0 {
			t.Errorf("dispersion = %v, want 0", u.Dispersion)
		}
	})

	t.Run("one component keeps the metrics nil", func(t *testing.T) {
		u := CalcUncertainty(Method{Status: StatusAvailable, Base: f64v(50)}, Method{Status: StatusUnavailable})
		if u.Components != 1 || u.Mean != nil || u.StdDev != nil || u.Dispersion != nil {
			t.Errorf("uncertainty = %+v", u)
		}
	})

	t.Run("non positive mean", func(t *testing.T) {
		g := Method{Status: StatusAvailable, Base: f64v(-1)}
		d := Method{Status: StatusAvailable, Base: f64v(-2)}
		u := CalcUncertainty(g, d)
		if u.Dispersion != nil {
			t.Errorf("dispersion = %v, want nil for a non-positive mean", *u.Dispersion)
		}
	})
}

func TestCalculateIsPureAndReproducible(t *testing.T) {
	in := goodInputs()
	cfg := DefaultConfig()
	first := Calculate(in, cfg)
	second := Calculate(in, cfg)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("Calculate is not deterministic for the same inputs and config")
	}
	b1, err := MarshalSnapshot(in, cfg, first)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := MarshalSnapshot(in, cfg, second)
	if string(b1) != string(b2) {
		t.Fatal("the snapshot is not byte-identical")
	}
	// The snapshot must carry the config and the resolved discount rate: a
	// persisted row has to be replayable without guessing.
	var snap Snapshot
	if err := json.Unmarshal(b1, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Config.HorizonYears != cfg.HorizonYears || snap.Inputs.WACCUsed == nil {
		t.Errorf("snapshot = %+v", snap)
	}
	if snap.ModelVersion != ModelVersion {
		t.Errorf("snapshot model_version = %s", snap.ModelVersion)
	}

	// A different as_of with identical data must not change the value.
	other := in
	other.AsOf = in.AsOf.AddDate(1, 0, 0)
	third := Calculate(other, cfg)
	if *third.Graham.Base != *first.Graham.Base {
		t.Error("the value must depend only on the data, not on the clock")
	}
	if third.AsOf == first.AsOf {
		t.Error("the provenance as_of must change")
	}
}

func TestResultJSONHasNoConsensusNorUpside(t *testing.T) {
	in := goodInputs()
	res := Calculate(in, DefaultConfig())
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, forbidden := range []string{"consensus", "upside", "fair_value", "target_price", "recommendation"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("the 2.0.0 contract must not contain %q: %s", forbidden, s)
		}
	}
	for _, required := range []string{"graham", "dcf", "bear", "base", "bull", "margin_of_safety", "uncertainty", "confidence", "model_version", "peg", "sensitivity"} {
		if !strings.Contains(s, required) {
			t.Errorf("the contract must contain %q: %s", required, s)
		}
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		cfg := ConfigFromEnv()
		want := DefaultConfig()
		if cfg != want {
			t.Errorf("config = %+v, want %+v", cfg, want)
		}
	})

	t.Run("valuation envs", func(t *testing.T) {
		t.Setenv("VALUATION_BEAR_GROWTH_DELTA", "-6")
		t.Setenv("VALUATION_BULL_GROWTH_DELTA", "5")
		t.Setenv("VALUATION_BEAR_WACC_DELTA", "2")
		t.Setenv("VALUATION_BULL_WACC_DELTA", "-1.5")
		t.Setenv("VALUATION_BEAR_TERMINAL_DELTA", "-1")
		t.Setenv("VALUATION_BULL_TERMINAL_DELTA", "1")
		t.Setenv("VALUATION_GROWTH_TRANSITION", "0")
		t.Setenv("VALUATION_GROWTH_TRANSITION_CAP", "3")
		t.Setenv("VALUATION_SENSITIVITY_STEPS", "5")
		cfg := ConfigFromEnv()
		if cfg.BearGrowthDelta != -6 || cfg.BullGrowthDelta != 5 {
			t.Errorf("growth deltas = %v/%v", cfg.BearGrowthDelta, cfg.BullGrowthDelta)
		}
		if cfg.BearWACCDelta != 2 || cfg.BullWACCDelta != -1.5 {
			t.Errorf("wacc deltas = %v/%v", cfg.BearWACCDelta, cfg.BullWACCDelta)
		}
		if cfg.BearTerminalDelta != -1 || cfg.BullTerminalDelta != 1 {
			t.Errorf("terminal deltas = %v/%v", cfg.BearTerminalDelta, cfg.BullTerminalDelta)
		}
		if cfg.Transition {
			t.Error("VALUATION_GROWTH_TRANSITION=0 must disable the transition")
		}
		if cfg.TransitionCapPP != 3 || cfg.SensitivitySteps != 5 {
			t.Errorf("cap/steps = %v/%v", cfg.TransitionCapPP, cfg.SensitivitySteps)
		}
	})

	t.Run("reuses the historical valuation envs", func(t *testing.T) {
		t.Setenv("MARGIN_OF_SAFETY", "45")
		t.Setenv("DCF_HORIZON_YEARS", "7")
		t.Setenv("DCF_TERMINAL_GROWTH", "1.5")
		t.Setenv("DCF_DISCOUNT_RATE", "12")
		t.Setenv("GROWTH_RATE_DEFAULT", "4")
		t.Setenv("WACC_FALLBACK", "8.25")
		cfg := ConfigFromEnv()
		if cfg.TargetMOS != 45 || cfg.HorizonYears != 7 || cfg.TerminalGrowth != 1.5 ||
			cfg.DiscountFallback != 12 || cfg.GrowthFallback != 4 || cfg.WACCFallback != 8.25 {
			t.Errorf("config = %+v", cfg)
		}
	})

	t.Run("invalid values fall back to the defaults", func(t *testing.T) {
		t.Setenv("VALUATION_BEAR_GROWTH_DELTA", "abc")
		t.Setenv("VALUATION_GROWTH_TRANSITION", "maybe")
		t.Setenv("DCF_HORIZON_YEARS", "0")
		t.Setenv("VALUATION_SENSITIVITY_STEPS", "-2")
		cfg := ConfigFromEnv()
		want := DefaultConfig()
		if cfg.BearGrowthDelta != want.BearGrowthDelta || cfg.Transition != want.Transition {
			t.Errorf("config = %+v", cfg)
		}
		if cfg.HorizonYears != DefaultHorizonYears {
			t.Errorf("horizon = %d, want %d", cfg.HorizonYears, DefaultHorizonYears)
		}
		if cfg.SensitivitySteps != 1 {
			t.Errorf("steps = %d, want 1", cfg.SensitivitySteps)
		}
	})
}

func TestConcepts(t *testing.T) {
	t.Run("EPS", func(t *testing.T) {
		if v := EPSFrom(f64v(10e9), f64v(1e9)); v == nil || *v != 10 {
			t.Errorf("EPS = %v, want 10", v)
		}
		if v := EPSFrom(nil, f64v(1e9)); v != nil {
			t.Error("a missing numerator must be nil, never 0")
		}
		if v := EPSFrom(f64v(10e9), nil); v != nil {
			t.Error("a missing share count must be nil, never 0")
		}
		if v := EPSFrom(f64v(-10e9), f64v(1e9)); v != nil {
			t.Error("a negative EPS must be nil")
		}
	})

	t.Run("FCF", func(t *testing.T) {
		if v := FCFFrom(f64v(100e6), f64v(-20e6)); v == nil || *v != 80e6 {
			t.Errorf("FCF = %v, want 80e6", v)
		}
		if v := FCFFrom(f64v(100e6), nil); v != nil {
			t.Error("a missing capex must be nil, never OCF alone")
		}
		if v := FCFFromEarnings(f64v(50e6), f64v(10e6), f64v(-20e6)); v == nil || *v != 40e6 {
			t.Errorf("FCF fallback = %v, want 40e6", v)
		}
	})

	t.Run("net debt", func(t *testing.T) {
		if v := NetDebtFrom(f64v(100e6), f64v(40e6)); v == nil || *v != 60e6 {
			t.Errorf("net debt = %v, want 60e6", v)
		}
		if v := NetDebtFrom(f64v(100e6), nil); v != nil {
			t.Error("unknown cash must be nil, never 0 (the DCF then records net_debt_unknown)")
		}
		if v := NetDebtFrom(nil, f64v(40e6)); v != nil {
			t.Error("unknown debt must be nil")
		}
	})
}

func TestRankAndCap(t *testing.T) {
	if got := worst(ConfidenceHigh, ConfidenceLow, ConfidenceMedium); got != ConfidenceLow {
		t.Errorf("worst = %s, want low", got)
	}
	if got := worst("", ""); got != "" {
		t.Errorf("worst of nothing = %q, want empty", got)
	}
	if got := cap(ConfidenceLow, ConfidenceHigh); got != ConfidenceLow {
		t.Errorf("cap = %s, want low (a cap can only lower)", got)
	}
	if got := cap(ConfidenceHigh, ConfidenceMedium); got != ConfidenceMedium {
		t.Errorf("cap = %s, want medium", got)
	}
	if got := cap("", ConfidenceMedium); got != ConfidenceMedium {
		t.Errorf("cap = %s, want medium", got)
	}
}

// TestParseSnapshotRoundTrip verifies that Snapshot() → ParseSnapshot() preserves
// Inputs, Config and Result with float tolerance (determinism §27).
func TestParseSnapshotRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	cfg.BearGrowthDelta = -5
	cfg.BullGrowthDelta = 4
	cfg.BearWACCDelta = 1.5
	cfg.BullWACCDelta = -1
	cfg.BearTerminalDelta = -0.5
	cfg.BullTerminalDelta = 0.5
	cfg.Transition = true
	cfg.TransitionCapPP = 2
	cfg.SensitivitySteps = 4
	cfg.TargetMOS = 35
	cfg.HorizonYears = 6
	cfg.TerminalGrowth = 2.0
	cfg.DiscountFallback = 11.0
	cfg.GrowthFallback = 5.0
	cfg.WACCFallback = 9.0

	in := goodInputs()
	in.Price = f64v(100)
	in.EPS = f64v(5.5)
	in.FreeCashFlow = f64v(120e6)
	in.SharesOutstanding = f64v(1.1e9)
	in.NetDebt = f64v(10e9)
	in.NormalizedGrowthRate = f64v(8.5)
	in.GrowthConfidence = ConfidenceHigh
	in.GrowthSource = "individual_normalized"
	in.GrowthModelVersion = "1.0.0"
	in.WACC = f64v(9.2)
	in.WACCSource = WACCSourceCAPMHybrid
	in.WACCConfidence = ConfidenceMedium
	in.WACCModelVersion = "1.0.0"
	in.BetaObserved = true

	res := Calculate(in, cfg)
	snap, err := MarshalSnapshot(in, cfg, res)
	if err != nil {
		t.Fatalf("MarshalSnapshot: %v", err)
	}

	parsedSnap, err := ParseSnapshot(snap)
	if err != nil {
		t.Fatalf("ParseSnapshot: %v", err)
	}

	// Verify Inputs
	if parsedSnap.Inputs.Ticker != in.Ticker {
		t.Errorf("Ticker: got %q, want %q", parsedSnap.Inputs.Ticker, in.Ticker)
	}
	if !parsedSnap.Inputs.AsOf.Equal(in.AsOf) {
		t.Errorf("AsOf: got %v, want %v", parsedSnap.Inputs.AsOf, in.AsOf)
	}
	assertFloatPtrEq(t, "Price", parsedSnap.Inputs.Price, in.Price, 1e-6)
	assertFloatPtrEq(t, "EPS", parsedSnap.Inputs.EPS, in.EPS, 1e-6)
	assertFloatPtrEq(t, "FreeCashFlow", parsedSnap.Inputs.FreeCashFlow, in.FreeCashFlow, 1e-6)
	assertFloatPtrEq(t, "SharesOutstanding", parsedSnap.Inputs.SharesOutstanding, in.SharesOutstanding, 1e-6)
	assertFloatPtrEq(t, "NetDebt", parsedSnap.Inputs.NetDebt, in.NetDebt, 1e-6)
	assertFloatPtrEq(t, "NormalizedGrowthRate", parsedSnap.Inputs.NormalizedGrowthRate, in.NormalizedGrowthRate, 1e-6)
	if parsedSnap.Inputs.GrowthConfidence != in.GrowthConfidence {
		t.Errorf("GrowthConfidence: got %q, want %q", parsedSnap.Inputs.GrowthConfidence, in.GrowthConfidence)
	}
	if parsedSnap.Inputs.GrowthSource != in.GrowthSource {
		t.Errorf("GrowthSource: got %q, want %q", parsedSnap.Inputs.GrowthSource, in.GrowthSource)
	}
	if parsedSnap.Inputs.GrowthModelVersion != in.GrowthModelVersion {
		t.Errorf("GrowthModelVersion: got %q, want %q", parsedSnap.Inputs.GrowthModelVersion, in.GrowthModelVersion)
	}
	assertFloatPtrEq(t, "WACC", parsedSnap.Inputs.WACC, in.WACC, 1e-6)
	if parsedSnap.Inputs.WACCSource != in.WACCSource {
		t.Errorf("WACCSource: got %q, want %q", parsedSnap.Inputs.WACCSource, in.WACCSource)
	}
	if parsedSnap.Inputs.WACCConfidence != in.WACCConfidence {
		t.Errorf("WACCConfidence: got %q, want %q", parsedSnap.Inputs.WACCConfidence, in.WACCConfidence)
	}
	if parsedSnap.Inputs.WACCModelVersion != in.WACCModelVersion {
		t.Errorf("WACCModelVersion: got %q, want %q", parsedSnap.Inputs.WACCModelVersion, in.WACCModelVersion)
	}
	if parsedSnap.Inputs.BetaObserved != in.BetaObserved {
		t.Errorf("BetaObserved: got %v, want %v", parsedSnap.Inputs.BetaObserved, in.BetaObserved)
	}

	// Verify Config
	if parsedSnap.Config.BearGrowthDelta != cfg.BearGrowthDelta {
		t.Errorf("BearGrowthDelta: got %v, want %v", parsedSnap.Config.BearGrowthDelta, cfg.BearGrowthDelta)
	}
	if parsedSnap.Config.BullGrowthDelta != cfg.BullGrowthDelta {
		t.Errorf("BullGrowthDelta: got %v, want %v", parsedSnap.Config.BullGrowthDelta, cfg.BullGrowthDelta)
	}
	if math.Abs(parsedSnap.Config.BearWACCDelta-cfg.BearWACCDelta) > 1e-9 {
		t.Errorf("BearWACCDelta: got %v, want %v", parsedSnap.Config.BearWACCDelta, cfg.BearWACCDelta)
	}
	if math.Abs(parsedSnap.Config.BullWACCDelta-cfg.BullWACCDelta) > 1e-9 {
		t.Errorf("BullWACCDelta: got %v, want %v", parsedSnap.Config.BullWACCDelta, cfg.BullWACCDelta)
	}
	if math.Abs(parsedSnap.Config.BearTerminalDelta-cfg.BearTerminalDelta) > 1e-9 {
		t.Errorf("BearTerminalDelta: got %v, want %v", parsedSnap.Config.BearTerminalDelta, cfg.BearTerminalDelta)
	}
	if math.Abs(parsedSnap.Config.BullTerminalDelta-cfg.BullTerminalDelta) > 1e-9 {
		t.Errorf("BullTerminalDelta: got %v, want %v", parsedSnap.Config.BullTerminalDelta, cfg.BullTerminalDelta)
	}
	if parsedSnap.Config.Transition != cfg.Transition {
		t.Errorf("Transition: got %v, want %v", parsedSnap.Config.Transition, cfg.Transition)
	}
	if parsedSnap.Config.TransitionCapPP != cfg.TransitionCapPP {
		t.Errorf("TransitionCapPP: got %v, want %v", parsedSnap.Config.TransitionCapPP, cfg.TransitionCapPP)
	}
	if parsedSnap.Config.SensitivitySteps != cfg.SensitivitySteps {
		t.Errorf("SensitivitySteps: got %v, want %v", parsedSnap.Config.SensitivitySteps, cfg.SensitivitySteps)
	}
	if parsedSnap.Config.TargetMOS != cfg.TargetMOS {
		t.Errorf("TargetMOS: got %v, want %v", parsedSnap.Config.TargetMOS, cfg.TargetMOS)
	}
	if parsedSnap.Config.HorizonYears != cfg.HorizonYears {
		t.Errorf("HorizonYears: got %v, want %v", parsedSnap.Config.HorizonYears, cfg.HorizonYears)
	}
	if math.Abs(parsedSnap.Config.TerminalGrowth-cfg.TerminalGrowth) > 1e-9 {
		t.Errorf("TerminalGrowth: got %v, want %v", parsedSnap.Config.TerminalGrowth, cfg.TerminalGrowth)
	}
	if math.Abs(parsedSnap.Config.DiscountFallback-cfg.DiscountFallback) > 1e-9 {
		t.Errorf("DiscountFallback: got %v, want %v", parsedSnap.Config.DiscountFallback, cfg.DiscountFallback)
	}
	if math.Abs(parsedSnap.Config.GrowthFallback-cfg.GrowthFallback) > 1e-9 {
		t.Errorf("GrowthFallback: got %v, want %v", parsedSnap.Config.GrowthFallback, cfg.GrowthFallback)
	}
	if math.Abs(parsedSnap.Config.WACCFallback-cfg.WACCFallback) > 1e-9 {
		t.Errorf("WACCFallback: got %v, want %v", parsedSnap.Config.WACCFallback, cfg.WACCFallback)
	}

	// Verify Result (key fields) - Snapshot has these fields directly
	if parsedSnap.ModelVersion != res.ModelVersion {
		t.Errorf("ModelVersion: got %q, want %q", parsedSnap.ModelVersion, res.ModelVersion)
	}
	if parsedSnap.Graham.Status != res.Graham.Status {
		t.Errorf("Graham.Status: got %q, want %q", parsedSnap.Graham.Status, res.Graham.Status)
	}
	if parsedSnap.DCF.Status != res.DCF.Status {
		t.Errorf("DCF.Status: got %q, want %q", parsedSnap.DCF.Status, res.DCF.Status)
	}
	if parsedSnap.Confidence != res.Confidence {
		t.Errorf("Confidence: got %q, want %q", parsedSnap.Confidence, res.Confidence)
	}
	assertFloatPtrEq(t, "Graham.Base", parsedSnap.Graham.Base, res.Graham.Base, 1e-4)
	assertFloatPtrEq(t, "DCF.Base", parsedSnap.DCF.Base, res.DCF.Base, 1e-4)
	assertFloatPtrEq(t, "MOS.GrahamBase", parsedSnap.MOS.GrahamBase, res.MOS.GrahamBase, 1e-4)
	assertFloatPtrEq(t, "MOS.DCFBase", parsedSnap.MOS.DCFBase, res.MOS.DCFBase, 1e-4)
}

// assertFloatPtrEq compares two *float64 with tolerance, handling nil.
func assertFloatPtrEq(t *testing.T, label string, got, want *float64, tol float64) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Errorf("%s: nil mismatch (got=%v, want=%v)", label, got, want)
		return
	}
	if got != nil && math.Abs(*got-*want) > tol {
		t.Errorf("%s: got %v, want %v (tol %v)", label, *got, *want, tol)
	}
}
