package api

import (
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"strings"
	"testing"
)

// captureWarnings swaps the default slog logger for one writing into a buffer,
// and restores it. (These tests are in package api, so they run before/after
// whatever the suite configured; we always restore.)
func captureWarnings(t *testing.T) *strings.Builder {
	t.Helper()
	var sb strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&sb, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &sb
}

// TestLoadParametersNaNMarginOfSafety is the CA of (d): a `MARGIN_OF_SAFETY=NaN`
// must warn and fall back to the default, and the resulting struct must be
// JSON-encodable (encoding/json rejects NaN with "unsupported value: NaN").
//
// Before the fix this went through a local, NON-validating envFloat that parsed
// "NaN" into a float and returned it, so the NaN flowed into a valuation
// response and the handler failed to marshal it (500). Now it delegates to
// modelcfg.EnvFloat like the four engines.
func TestLoadParametersNaNMarginOfSafety(t *testing.T) {
	sb := captureWarnings(t)
	t.Setenv("MARGIN_OF_SAFETY", "NaN")

	p := LoadParameters()
	if math.IsNaN(p.MarginOfSafety) {
		t.Fatalf("MARGIN_OF_SAFETY=NaN must not reach Parameters (got NaN)")
	}
	if p.MarginOfSafety != defaultMarginSafety {
		t.Errorf("MARGIN_OF_SAFETY=NaN → got %v, want default %v", p.MarginOfSafety, defaultMarginSafety)
	}
	if !strings.Contains(sb.String(), "MARGIN_OF_SAFETY") {
		t.Errorf("expected a warning mentioning MARGIN_OF_SAFETY, log was: %q", sb.String())
	}

	// The regression itself: the struct must marshal. A NaN here would make
	// encoding/json fail, which is exactly the 500 the fix removes.
	if _, err := json.Marshal(p); err != nil {
		t.Fatalf("Parameters must be JSON-encodable with a NaN env knob, got: %v", err)
	}
}

// TestLoadParametersNonFiniteOtherKnobs covers the other numeric knobs that used
// the same local helper, so none of them can smuggle a non-finite value into a
// response body any more.
func TestLoadParametersNonFiniteOtherKnobs(t *testing.T) {
	captureWarnings(t)
	t.Setenv("GROWTH_RATE_DEFAULT", "NaN")
	t.Setenv("DCF_DISCOUNT_RATE", "+Inf")
	t.Setenv("DCF_TERMINAL_GROWTH", "not-a-number")

	p := LoadParameters()
	if p.GrowthRate != defaultGrowthRate {
		t.Errorf("GROWTH_RATE_DEFAULT=NaN → got %v, want default %v", p.GrowthRate, defaultGrowthRate)
	}
	if p.DiscountRate != defaultDiscountRate {
		t.Errorf("DCF_DISCOUNT_RATE=+Inf → got %v, want default %v", p.DiscountRate, defaultDiscountRate)
	}
	if p.TerminalGrowth != defaultTerminalGrowth {
		t.Errorf("DCF_TERMINAL_GROWTH=not-a-number → got %v, want default %v", p.TerminalGrowth, defaultTerminalGrowth)
	}
	if _, err := json.Marshal(p); err != nil {
		t.Fatalf("Parameters must be JSON-encodable, got: %v", err)
	}
}

// TestLoadParametersValidValuesStillWin guards the fix from over-rejecting: a
// finite value must still be honoured (the helper warns only on bad input).
func TestLoadParametersValidValuesStillWin(t *testing.T) {
	captureWarnings(t)
	t.Setenv("GROWTH_RATE_DEFAULT", "8.5")
	t.Setenv("DCF_DISCOUNT_RATE", "11.25")
	t.Setenv("DCF_HORIZON_YEARS", "7")
	t.Setenv("DCF_TERMINAL_GROWTH", "3")
	t.Setenv("MARGIN_OF_SAFETY", "25")
	t.Setenv("COMPARABLES_MIN_SECURITIES", "3")
	t.Setenv("COMPARABLES_HISTORY_YEARS", "4")

	p := LoadParameters()
	if p.GrowthRate != 8.5 || p.DiscountRate != 11.25 || p.Horizon != 7 ||
		p.TerminalGrowth != 3 || p.MarginOfSafety != 25 ||
		p.CompMinSecurities != 3 || p.CompHistoryYears != 4 {
		t.Errorf("valid env values not honoured: %+v", p)
	}
}

// TestLoadParametersEmptyEnvUsesDefaults: an unset var is silent (no warning) and
// uses the default, preserving the original contract.
func TestLoadParametersEmptyEnvUsesDefaults(t *testing.T) {
	for _, k := range []string{
		"GROWTH_RATE_DEFAULT", "DCF_DISCOUNT_RATE", "DCF_HORIZON_YEARS",
		"DCF_TERMINAL_GROWTH", "MARGIN_OF_SAFETY",
		"COMPARABLES_MIN_SECURITIES", "COMPARABLES_HISTORY_YEARS",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	sb := captureWarnings(t)

	p := LoadParameters()
	if p.GrowthRate != defaultGrowthRate || p.MarginOfSafety != defaultMarginSafety {
		t.Errorf("empty env → %+v, want defaults", p)
	}
	if sb.String() != "" {
		t.Errorf("empty env must not warn, log was: %q", sb.String())
	}
}
