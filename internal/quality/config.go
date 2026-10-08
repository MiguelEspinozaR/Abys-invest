package quality

import (
	"fmt"
	"log/slog"

	"github.com/miky/abys-invest/internal/modelcfg"
)

// Defaults of the Quality engine (SPEC v2 §13/§14/§23, plan M6c A1/A8).
//
// Every number is a PERCENTAGE where it is a rate (tax rate, CAGRs) and a
// FRACTION where it is a ratio (margins, yields, ROIC), and it lives in Config —
// never inside a formula — so that a Parameter Set can move it (§24/§25).
const (
	// DefaultTaxRate is QUALITY_TAX_RATE (Az1(a)): the NORMALISED tax rate used by
	// ROIC.
	DefaultTaxRate = 21.0

	DefaultCoverageHigh   = 0.85
	DefaultCoverageMedium = 0.60

	// DefaultMinStabilityYears is QUALITY_STABILITY_MIN_YEARS: the number of
	// annual points needed before a volatility or a count of positive years means
	// anything. §13's four stability metrics are nil below it.
	DefaultMinStabilityYears = 4

	// DefaultQualityFYMaxAgeDays is the default freshness window for FY observations (M6c-T1).
	DefaultQualityFYMaxAgeDays = 550
)

// Sub-block names of the Quality dimension (§12 "Quality 35%", ADR D3/Az3).
// They are the keys of Config.SubWeights, of the parameter_sets JSONB and the
// `name` of every SubScore that GET /quality exposes. The vocabulary lives in
// modelcfg so that the config, the engine, the API and the UI cannot drift.
const (
	SubProfitability = modelcfg.SubProfitability
	SubGrowth        = modelcfg.SubGrowth
	SubMargins       = modelcfg.SubMargins
	SubStability     = modelcfg.SubStability
	SubSolvency      = modelcfg.SubSolvency
)

// Config is the full parameter set of Calculate. Calculate(Inputs, Config) is
// pure: same inputs + same config ⇒ byte-identical Result (§27).
type Config struct {
	// TaxRate is the normalised tax rate of NOPAT (Az1(a)). Percentages.
	TaxRate float64 `json:"tax_rate"`
	// SubWeights are the five weights INSIDE the quality dimension (0.20 each by
	// default, ADR D3). They must sum to 1; the pipeline validates that with
	// modelcfg.Validate before it ever calls the engine.
	SubWeights map[string]float64 `json:"sub_weights"`
	// CoverageHigh / CoverageMedium are the §23 coverage gates.
	CoverageHigh   float64 `json:"coverage_high"`
	CoverageMedium float64 `json:"coverage_medium"`
	// MinStabilityYears is the minimum number of annual points for §13's
	// stability metrics.
	MinStabilityYears int `json:"min_stability_years"`
	// FYMaxAgeDays is the freshness window for FY observations (M6c-T1).
	FYMaxAgeDays int `json:"fy_max_age_days"`
	// Bands is the calibration of every metric (ADR U2: no cut point inside a
	// formula).
	Bands Bands `json:"bands"`
	// Reserved fields for future Parameter Sets (§28 preparation). These knobs
	// are exposed in ModelConfig so a set can carry them, but they are not yet
	// consumed by the quality engine. They are documented here as "reserved" so
	// reviewers can see they are intentional, not forgotten.
	//
	// ReservedROICBenchmark      float64 `json:"roic_benchmark,omitempty"`      // future: external ROIC benchmark
	// ReservedMarginBenchmark    float64 `json:"margin_benchmark,omitempty"`    // future: external margin benchmark
	// ReservedStabilityBenchmark float64 `json:"stability_benchmark,omitempty"` // future: external stability benchmark
}

// DefaultConfig is the deterministic default configuration.
func DefaultConfig() Config {
	return Config{
		TaxRate:           DefaultTaxRate,
		SubWeights:        modelcfg.DefaultQualitySubWeights(),
		CoverageHigh:      DefaultCoverageHigh,
		CoverageMedium:    DefaultCoverageMedium,
		MinStabilityYears: DefaultMinStabilityYears,
		FYMaxAgeDays:      DefaultQualityFYMaxAgeDays,
		Bands:             DefaultBands(),
	}
}

// ConfigFromEnv reads the QUALITY_* parameters on top of DefaultConfig, using
// the shared validating helpers (ADR D20, closes M6b-H2): an invalid value falls
// back to the default with a warning instead of poisoning a ratio with NaN.
//
//	QUALITY_TAX_RATE               21    normalised tax rate %
//	QUALITY_WEIGHT_PROFITABILITY   0.20  sub-weight
//	QUALITY_WEIGHT_GROWTH          0.20
//	QUALITY_WEIGHT_MARGINS         0.20
//	QUALITY_WEIGHT_STABILITY       0.20
//	QUALITY_WEIGHT_SOLVENCY        0.20
//	QUALITY_COVERAGE_HIGH          0.85
//	QUALITY_COVERAGE_MEDIUM        0.60
//	QUALITY_STABILITY_MIN_YEARS    4
//
// It is the only function of the package that reads the environment.
func ConfigFromEnv() Config {
	cfg := DefaultConfig()
	cfg.TaxRate = modelcfg.EnvFloatRange("QUALITY_TAX_RATE", cfg.TaxRate, 0, 50)
	cfg.CoverageHigh = modelcfg.EnvFloatRange("QUALITY_COVERAGE_HIGH", cfg.CoverageHigh, 0, 1)
	cfg.CoverageMedium = modelcfg.EnvFloatRange("QUALITY_COVERAGE_MEDIUM", cfg.CoverageMedium, 0, 1)
	cfg.MinStabilityYears = modelcfg.EnvIntRange("QUALITY_STABILITY_MIN_YEARS", cfg.MinStabilityYears, 1, 20)
	fyMax := modelcfg.EnvFloatRange("QUALITY_FY_MAX_AGE_DAYS", float64(cfg.FYMaxAgeDays), 1, 3650)
	cfg.FYMaxAgeDays = int(fyMax)

	w := cfg.SubWeights
	w[SubProfitability] = modelcfg.EnvFloatRange("QUALITY_WEIGHT_PROFITABILITY", w[SubProfitability], 0, 1)
	w[SubGrowth] = modelcfg.EnvFloatRange("QUALITY_WEIGHT_GROWTH", w[SubGrowth], 0, 1)
	w[SubMargins] = modelcfg.EnvFloatRange("QUALITY_WEIGHT_MARGINS", w[SubMargins], 0, 1)
	w[SubStability] = modelcfg.EnvFloatRange("QUALITY_WEIGHT_STABILITY", w[SubStability], 0, 1)
	w[SubSolvency] = modelcfg.EnvFloatRange("QUALITY_WEIGHT_SOLVENCY", w[SubSolvency], 0, 1)

	// The tax rate is ALWAYS configured in M6c: there is no observed tax rate to
	// derive it from until M6c-T1 lands the XBRL plumbing. Making it explicit here
	// keeps ADR D26 (confidence capped at medium) impossible to forget.
	if !cfg.TaxRateConfigured() {
		slog.Warn("quality: QUALITY_TAX_RATE fuera de rango, se usa el default (tax_rate_source=configured)",
			"usado", cfg.TaxRate)
		cfg.TaxRate = DefaultTaxRate
	}
	return cfg
}

// ConfigFromModelConfig builds a quality.Config from a resolved ModelConfig.
// This is the path used by the pipeline so that parameter set overrides (e.g.
// quality_sub_weights from the `conservative` set) actually reach the engine.
// Precedence: code defaults < env < parameter set (via ModelConfig).
func ConfigFromModelConfig(mc modelcfg.ModelConfig) Config {
	// Start from env-applied config (code < env), then apply parameter set overrides from mc
	cfg := ConfigFromEnv()
	cfg.TaxRate = mc.QualityTaxRate
	cfg.CoverageHigh = mc.QualityCoverageHigh
	cfg.CoverageMedium = mc.QualityCoverageMedium
	cfg.MinStabilityYears = mc.QualityStabilityMinYears
	// FYMaxAgeDays keeps the value applied from env (QUALITY_FY_MAX_AGE_DAYS) in
	// ConfigFromEnv: mc (parameter set) does not override it.

	w := cfg.SubWeights
	for _, name := range SubBlockNames() {
		if v, ok := mc.QualitySubWeights[name]; ok && v > 0 {
			w[name] = v
		}
	}

	return cfg
}

// TaxRateConfigured reports whether the tax rate came from configuration. It is
// always true in M6c, and the field exists so that the ADR D26 confidence cap is
// a property of the Config rather than a literal in the aggregation.
func (c Config) TaxRateConfigured() bool { return c.TaxRate > 0 && c.TaxRate <= 50 }

// Validate is the local barrier (the global one is modelcfg.Validate, which the
// pipeline runs before persisting). It refuses a config whose bands or coverage
// gates would produce a meaningless score.
func (c Config) Validate() error {
	if err := c.Bands.Validate(); err != nil {
		return err
	}
	if c.CoverageMedium <= 0 || c.CoverageMedium >= c.CoverageHigh || c.CoverageHigh > 1 {
		return fmt.Errorf("quality: umbrales de coverage incoherentes (medium %.4f, high %.4f): 0 < medium < high <= 1",
			c.CoverageMedium, c.CoverageHigh)
	}
	if c.MinStabilityYears < 1 || c.MinStabilityYears > 20 {
		return fmt.Errorf("quality: min_stability_years %d fuera de [1,20]", c.MinStabilityYears)
	}
	if c.TaxRate <= 0 || c.TaxRate > 100 {
		return fmt.Errorf("quality: tax_rate %.4f fuera de (0,100]", c.TaxRate)
	}
	for _, name := range SubBlockNames() {
		w, ok := c.SubWeights[name]
		if !ok || w <= 0 {
			return fmt.Errorf("quality: sub-peso de %q ausente o no positivo (%v)", name, w)
		}
	}
	return nil
}

// SubWeight returns the weight of one sub-block, falling back to the balanced
// 0.20 when the key is absent (an absent sub-block is not a zero weight).
func (c Config) SubWeight(name string) float64 {
	if w, ok := c.SubWeights[name]; ok && w > 0 {
		return w
	}
	return 1.0 / float64(len(SubBlockNames()))
}
