package valuation

import (
	"log/slog"
	"os"
	"strconv"
)

// Defaults of the valuation engine (SPEC v2 §6-§11, §19-§21, plan M6b D2/D6).
// EVERY rate is a PERCENTAGE (g = 1.47 means 1.47%/year) and every parameter
// lives in Config — not inside the formulas — so that M6c's Parameter Sets
// (§24/§25) and the §28 optimizer can move them without rewriting the math.
//
// Two defaults come from the historical envs of M3/M6a instead of a new
// VALUATION_* name, on purpose: a second parallel set of knobs for the same
// quantity is the classic source of "which one was used?" bugs.
//
//	TargetMOS          ← MARGIN_OF_SAFETY      (30)
//	HorizonYears       ← DCF_HORIZON_YEARS     (5)
//	TerminalGrowth     ← DCF_TERMINAL_GROWTH   (2.5)
//	DiscountFallback   ← DCF_DISCOUNT_RATE     (10, LAST resort of D9)
//	GrowthFallback     ← GROWTH_RATE_DEFAULT   (7, legacy; see A1 below)
//	WACCFallback       ← WACC_FALLBACK         (9, level 3 of D9)
const (
	DefaultBearGrowthDelta   = -4.0
	DefaultBullGrowthDelta   = 3.0
	DefaultBearWACCDelta     = 1.5
	DefaultBullWACCDelta     = -1.0
	DefaultBearTerminalDelta = -0.5
	DefaultBullTerminalDelta = 0.5
	DefaultTransitionCapPP   = 5.0
	DefaultTargetMOS         = 30.0
	DefaultHorizonYears      = 5
	DefaultTerminalGrowth    = 2.5
	DefaultDiscountFallback  = 10.0
	DefaultWACCFallback      = 9.0
	DefaultSensitivitySteps  = 3
)

// DefaultGrowthFallback is the LEGACY global growth (GROWTH_RATE_DEFAULT = 7)
// of the M3 formulas. M6b decision A1 (2026-09-28, overriding plan ADR D7):
// while the per-security Growth Engine has no row, the valuation engine keeps
// the current M3 behaviour and uses this fallback WITHOUT flagging it (no
// `unavailable`, no new reason on the method, no "fallback 7" marker). The
// mark-on-fallback variant is deferred to M6c. The normalised rate always wins
// when it exists.
const DefaultGrowthFallback = 7.0

// Config is the full parameter set of Calculate. It is the ONLY place where a
// valuation parameter may appear: the formulas read Config and never the
// environment (§27: inputs + config, nothing else).
type Config struct {
	// Scenario deltas in percentage points (ADR D6): the base scenario uses the
	// observed values untouched.
	BearGrowthDelta   float64 // -4.0
	BullGrowthDelta   float64 //  3.0
	BearWACCDelta     float64 //  1.5 (bear discounts MORE)
	BullWACCDelta     float64 // -1.0
	BearTerminalDelta float64 // -0.5
	BullTerminalDelta float64 //  0.5

	// Transition enables the §7 growth path growth_initial → growth_terminal
	// instead of a flat growth for the whole horizon (ADR D8).
	Transition      bool
	TransitionCapPP float64 // 5.0: caps how far the anchor can exceed terminal

	TargetMOS        float64 // 30.0 (MARGIN_OF_SAFETY), §11 threshold
	HorizonYears     int     // 5 (DCF_HORIZON_YEARS)
	TerminalGrowth   float64 // 2.5 (DCF_TERMINAL_GROWTH)
	DiscountFallback float64 // 10.0 (DCF_DISCOUNT_RATE) — last resort of D9
	WACCFallback     float64 //  9.0 (WACC_FALLBACK) — level 3 of D9
	GrowthFallback   float64 //  7.0 (GROWTH_RATE_DEFAULT) — legacy, only when
	// there is no normalised growth (decision A1; unmarked on purpose)

	SensitivitySteps int // 3: points per axis of the §8 WACC x growth grid
}

// DefaultConfig is the deterministic default configuration, the one used by
// the pipeline unless ConfigFromEnv overrides it.
func DefaultConfig() Config {
	return Config{
		BearGrowthDelta:   DefaultBearGrowthDelta,
		BullGrowthDelta:   DefaultBullGrowthDelta,
		BearWACCDelta:     DefaultBearWACCDelta,
		BullWACCDelta:     DefaultBullWACCDelta,
		BearTerminalDelta: DefaultBearTerminalDelta,
		BullTerminalDelta: DefaultBullTerminalDelta,
		Transition:        true,
		TransitionCapPP:   DefaultTransitionCapPP,
		TargetMOS:         DefaultTargetMOS,
		HorizonYears:      DefaultHorizonYears,
		TerminalGrowth:    DefaultTerminalGrowth,
		DiscountFallback:  DefaultDiscountFallback,
		WACCFallback:      DefaultWACCFallback,
		GrowthFallback:    DefaultGrowthFallback,
		SensitivitySteps:  DefaultSensitivitySteps,
	}
}

// ConfigFromEnv reads the VALUATION_* parameters on top of DefaultConfig and
// REUSES the historical valuation envs. A missing or unparseable value falls
// back to the default with a slog.Warn: a bad env var must never break the
// pipeline (never panic, never an error return). It is the only function of the
// package that touches the environment; Calculate stays pure (§27).
//
//	VALUATION_BEAR_GROWTH_DELTA    -4     pp subtracted from g in bear
//	VALUATION_BULL_GROWTH_DELTA    +3     pp added to g in bull
//	VALUATION_BEAR_WACC_DELTA      +1.5   pp added to the discount rate in bear
//	VALUATION_BULL_WACC_DELTA      -1.0   pp subtracted in bull
//	VALUATION_BEAR_TERMINAL_DELTA  -0.5   pp on the terminal growth in bear
//	VALUATION_BULL_TERMINAL_DELTA  +0.5   pp on the terminal growth in bull
//	VALUATION_GROWTH_TRANSITION    1      §7 growth transition (0 = flat, A/B)
//	VALUATION_GROWTH_TRANSITION_CAP 5     cap of the anchor over terminal (pp)
//	VALUATION_SENSITIVITY_STEPS    3      points per axis of the WACC x growth grid
//
// Reused (NOT duplicated): MARGIN_OF_SAFETY, DCF_HORIZON_YEARS,
// DCF_TERMINAL_GROWTH, DCF_DISCOUNT_RATE, GROWTH_RATE_DEFAULT, WACC_FALLBACK.
//
// NOT read from the env on purpose (user decisions of 2026-09-28):
//   - VALUATION_DISPERSION_HIGH / VALUATION_DISPERSION_CRITICAL (A4): the
//     valuation uncertainty is exposed RAW (value + ratio) and never degrades
//     the confidence in M6b; the thresholds belong to M6c.
//   - VALUATION_ALLOW_GROWTH_FALLBACK (A1): the legacy growth fallback is
//     unconditional and unmarked, exactly as M3/M6a behave today.
func ConfigFromEnv() Config {
	cfg := DefaultConfig()
	cfg.BearGrowthDelta = envFloat("VALUATION_BEAR_GROWTH_DELTA", cfg.BearGrowthDelta)
	cfg.BullGrowthDelta = envFloat("VALUATION_BULL_GROWTH_DELTA", cfg.BullGrowthDelta)
	cfg.BearWACCDelta = envFloat("VALUATION_BEAR_WACC_DELTA", cfg.BearWACCDelta)
	cfg.BullWACCDelta = envFloat("VALUATION_BULL_WACC_DELTA", cfg.BullWACCDelta)
	cfg.BearTerminalDelta = envFloat("VALUATION_BEAR_TERMINAL_DELTA", cfg.BearTerminalDelta)
	cfg.BullTerminalDelta = envFloat("VALUATION_BULL_TERMINAL_DELTA", cfg.BullTerminalDelta)
	cfg.Transition = envBool("VALUATION_GROWTH_TRANSITION", cfg.Transition)
	cfg.TransitionCapPP = envFloat("VALUATION_GROWTH_TRANSITION_CAP", cfg.TransitionCapPP)
	cfg.SensitivitySteps = envInt("VALUATION_SENSITIVITY_STEPS", cfg.SensitivitySteps)

	cfg.TargetMOS = envFloat("MARGIN_OF_SAFETY", cfg.TargetMOS)
	cfg.HorizonYears = envInt("DCF_HORIZON_YEARS", cfg.HorizonYears)
	cfg.TerminalGrowth = envFloat("DCF_TERMINAL_GROWTH", cfg.TerminalGrowth)
	cfg.DiscountFallback = envFloat("DCF_DISCOUNT_RATE", cfg.DiscountFallback)
	cfg.GrowthFallback = envFloat("GROWTH_RATE_DEFAULT", cfg.GrowthFallback)
	cfg.WACCFallback = envFloat("WACC_FALLBACK", cfg.WACCFallback)

	if cfg.HorizonYears <= 0 {
		cfg.HorizonYears = DefaultHorizonYears
	}
	if cfg.SensitivitySteps < 1 {
		cfg.SensitivitySteps = 1
	}
	return cfg
}

// envFloat reads a float env var; unparseable → default + warning (§27: the
// engine never fails because of configuration).
func envFloat(key string, def float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		slog.Warn("valoración: env inválido, usando default", "env", key, "value", v, "default", def)
		return def
	}
	return f
}

// envInt reads an int env var; unparseable → default + warning.
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		slog.Warn("valoración: env inválido, usando default", "env", key, "value", v, "default", def)
		return def
	}
	return i
}

// envBool reads a 1/true/yes/on flag; anything else → default + warning.
func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	case "0", "false", "FALSE", "no", "off":
		return false
	}
	slog.Warn("valoración: env booleano inválido, usando default", "env", key, "value", v, "default", def)
	return def
}
