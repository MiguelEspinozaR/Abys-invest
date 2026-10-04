package growth

import (
	"log/slog"
	"os"
	"strconv"

	"github.com/miky/abys-invest/internal/modelcfg"
)

// Defaults of the Growth Engine (SPEC §5, plan D5/D14). All rates are
// PERCENTAGES and the annual window filter is expressed in DAYS, because the
// fiscal year of EDGAR is not exactly 365 days (52/53-week years).
const (
	DefaultWindowPrimaryYears  = 3
	DefaultWindowFallbackYears = 5
	DefaultAnnualMinDays       = 330
	DefaultAnnualMaxDays       = 400
	DefaultEPSWeight           = 0.5
	DefaultFCFWeight           = 0.5
	DefaultDiscrepancyPP       = 10.0
	DefaultMinRate             = -10.0
	DefaultMaxRate             = 25.0
	// DefaultFallbackRate is the LEGACY growth of the Graham/DCF/PEG formulas
	// (GROWTH_RATE_DEFAULT = 7). It is NOT the value of any formula: it is the
	// only fallback, and only through Result.EffectiveRate, which M6a does not
	// wire into anything (ADR D2, phase 1 of 3).
	DefaultFallbackRate = 7.0
)

// DefaultConfig is the deterministic default configuration: the only one used
// by the pipeline unless GROWTH_* env vars override it (ConfigFromEnv).
func DefaultConfig() Config {
	return Config{
		WindowPrimaryYears:  DefaultWindowPrimaryYears,
		WindowFallbackYears: DefaultWindowFallbackYears,
		AnnualMinDays:       DefaultAnnualMinDays,
		AnnualMaxDays:       DefaultAnnualMaxDays,
		EPSWeight:           DefaultEPSWeight,
		FCFWeight:           DefaultFCFWeight,
		DiscrepancyPP:       DefaultDiscrepancyPP,
		MinRate:             DefaultMinRate,
		MaxRate:             DefaultMaxRate,
		FallbackRate:        DefaultFallbackRate,
	}
}

// ConfigFromEnv reads the GROWTH_* parameters on top of DefaultConfig. Values
// that are missing or unparseable fall back to the default (never an error: a
// bad env var must not break the pipeline). It is the only function of the
// package that touches the environment; Calculate stays pure (§27).
//
//	GROWTH_WINDOW_PRIMARY_YEARS  3      ventana preferida del CAGR (fija, ver abajo)
//	GROWTH_WINDOW_FALLBACK_YEARS 5      ventana de reserva (fija, ver abajo)
//	GROWTH_ANNUAL_MIN_DAYS       330    duración mínima de un hecho FY anual
//	GROWTH_ANNUAL_MAX_DAYS       400    duración máxima de un hecho FY anual
//	GROWTH_EPS_WEIGHT            0.5    peso de EPS en el blend (0..1)
//	GROWTH_FCF_WEIGHT            0.5    peso de FCF en el blend (0..1)
//	GROWTH_DISCREPANCY_PP        10     umbral de discrepancia con revenue (pp) (0..100)
//	GROWTH_MIN_RATE              -10    suelo de la tasa normalizada (%) (-100..100)
//	GROWTH_MAX_RATE              25     techo de crecimiento extraordinario (%) (-100..100)
func ConfigFromEnv() Config {
	cfg := DefaultConfig()
	cfg.WindowPrimaryYears = envInt("GROWTH_WINDOW_PRIMARY_YEARS", cfg.WindowPrimaryYears)
	cfg.WindowFallbackYears = envInt("GROWTH_WINDOW_FALLBACK_YEARS", cfg.WindowFallbackYears)
	cfg.AnnualMinDays = envInt("GROWTH_ANNUAL_MIN_DAYS", cfg.AnnualMinDays)
	cfg.AnnualMaxDays = envInt("GROWTH_ANNUAL_MAX_DAYS", cfg.AnnualMaxDays)
	cfg.EPSWeight = modelcfg.EnvFloatRange("GROWTH_EPS_WEIGHT", cfg.EPSWeight, 0, 1)
	cfg.FCFWeight = modelcfg.EnvFloatRange("GROWTH_FCF_WEIGHT", cfg.FCFWeight, 0, 1)
	cfg.DiscrepancyPP = modelcfg.EnvFloatRange("GROWTH_DISCREPANCY_PP", cfg.DiscrepancyPP, 0, 100)
	cfg.MinRate = modelcfg.EnvFloatRange("GROWTH_MIN_RATE", cfg.MinRate, -100, 100)
	cfg.MaxRate = modelcfg.EnvFloatRange("GROWTH_MAX_RATE", cfg.MaxRate, -100, 100)
	return cfg.canonicalWindows()
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

// ConfigFromModelConfig builds a growth Config tied to a RESOLVED ModelConfig,
// the way wacc.ConfigFromModelConfig already does.
//
// It exists so the full-chain replay (internal/backtest.scorereplay) does not
// read the environment for ONE step while every other step of the same chain
// uses the parameter set: a chain replayed with `--parameter-set conservative`
// recomputed growth from whatever GROWTH_* the process happened to have, so the
// step that "recomputes" was in fact reading something else. That is the shape
// of a reproducibility bug waiting for the day growth knobs move into the set
// (§28 RESERVED).
//
// Today ModelConfig carries NO growth knob, so this is deliberately equivalent
// to ConfigFromEnv: it is the ANCHOR, not new behaviour. When a growth knob is
// added to ModelConfig, it is applied HERE and every consumer of a resolved
// config (pipeline and replay) inherits it at once, instead of one of them
// being forgotten. No functional change today, which is what keeps the replay
// fixtures (B13) green.
func ConfigFromModelConfig(mc modelcfg.ModelConfig) Config {
	cfg := ConfigFromEnv()
	// Kept explicit (and currently empty) so the derivation from mc is visible
	// where it belongs. Any ModelConfig field that becomes a growth knob is
	// applied here with the same precedence the other engines use:
	// code defaults < env < parameter set.
	_ = mc
	return cfg
}

// canonicalWindows enforces the ONLY two windows the persisted contract can
// represent (deuda M6b F2).
//
// growth_metrics has exactly four window columns — eps_cagr_3y, eps_cagr_5y,
// fcf_cagr_3y, fcf_cagr_5y (and revenue_cagr_3y/5y) — and the `source` labels
// carry the same suffix (`eps_fcf_3y`, `fcf_5y`, ...). So a 4-year fallback is
// not a slower version of the same thing: measuring four years and writing it
// into `eps_cagr_5y` makes a COLUMN lie about its own window, and every
// consumer (API included) would repeat the lie. There is no `_4y` column and no
// window dimension in the contract.
//
// A non-canonical window is therefore REFUSED here — with a warning, because a
// silently ignored env var is worse than a refused one — and the canonical
// window is used instead. Supporting other windows is a schema change
// (columns + labels + consumers), not a config tweak.
func (c Config) canonicalWindows() Config {
	if c.WindowPrimaryYears != DefaultWindowPrimaryYears {
		slog.Warn("GROWTH_WINDOW_PRIMARY_YEARS ignorado: el contrato persistido solo admite la ventana de 3 años (columnas *_3y y etiquetas *_3y); medir con otra ventana escribiría datos de una ventana en columnas de otra",
			"valor", c.WindowPrimaryYears, "usado", DefaultWindowPrimaryYears)
		c.WindowPrimaryYears = DefaultWindowPrimaryYears
	}
	if c.WindowFallbackYears != DefaultWindowFallbackYears {
		slog.Warn("GROWTH_WINDOW_FALLBACK_YEARS ignorado: el contrato persistido solo admite la ventana de 5 años (columnas *_5y y etiquetas *_5y); medir con otra ventana escribiría datos de una ventana en columnas de otra",
			"valor", c.WindowFallbackYears, "usado", DefaultWindowFallbackYears)
		c.WindowFallbackYears = DefaultWindowFallbackYears
	}
	return c
}
