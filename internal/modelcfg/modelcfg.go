// Package modelcfg materialises the model configuration of §24/§25 (SPEC v2,
// plan M6c D1): the weights, thresholds and sub-weights that the engines read
// INSTEAD of the environment, plus the versionable Parameter Sets that can
// override them.
//
// Precedence — codes < env < parameter set — is the whole point of the package:
//
//  1. CODE defaults live in DefaultModelConfig. They are the contract of the
//     release and are asserted by the unit tests.
//  2. ENV overrides them (QUALITY_*, RELATIVE_*, MARKET_CONTEXT_WEIGHT,
//     MARGIN_OF_SAFETY, WACC_RISK_FREE...). Values are validated: NaN, ±Inf or
//     out-of-range values are refused with a warning (see env.go).
//  3. A PARAMETER SET (`parameter_sets.parameters`, JSONB) overrides both, and
//     is the ONLY layer that is versioned and persisted next to the results it
//     produced.
//
// A Parameter Set with `parameters = {}` therefore means "no overrides: behave
// exactly like this deployment's env" — which is what the `base` seed does.
// Missing keys never become zero: an override that is absent keeps the layer
// below (this is the "no silent zero-fill" rule of the plan's test matrix).
//
// ModelConfig is a pure value: nothing here reads the clock, the network or a
// global. The only impure functions are ModelConfigFromEnv (environment) and
// Resolve (parameter_sets table), and both are explicit about it.
package modelcfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ModelVersion is the score/metric formula revision these weights belong to. It
// is persisted in parameter_sets.model_version and must match the model_version
// of the rows produced with it.
const ModelVersion = "2.1.0"

// Defaults (codes layer). They are the literal SPEC §18 values, so a deployment
// with no env and the `base` set (parameters = {}) produces exactly the contract
// configuration.
const (
	DefaultGrahamWeight  = 0.15
	DefaultDCFWeight     = 0.20
	DefaultQualityWeight = 0.35
	// DefaultRelativeWeight and DefaultMarketContextWeight are the literal SPEC
	// §18 values 0.15 and 0.05.
	//
	// HISTORY (kept because it is a real, reverted decision): an amend of
	// 2026-10-02 had raised them to 0.20/0.10 on the argument that §18's table
	// prints "Total 100%" while its own list sums to 0.90. That amend is
	// REVERTED: the strict §18 reading is the approved contract (Az3, confirmed),
	// so the five weights are taken literally and the missing 0.10 stays
	// unallocated.
	//
	// The arithmetic, stated plainly so nobody has to rediscover it:
	// 0.15 + 0.20 + 0.35 + 0.15 + 0.05 = 0.90, NOT 1.00. A configured sum below
	// 1 is therefore NORMAL for a strict-§18 configuration, which is exactly what
	// Validate and the §18 renormalisation below are for: score21 divides by the
	// weight actually used, so an AAPL row with all five dimensions valid carries
	// weight_configured = weight_used = 0.90 and produces the same 45/100 as a
	// normalised 1.00 split. Rejecting sums < 1 would make the literal §18 values
	// unpersistable, so the barrier is "0 < sum <= 1" for the five top-level
	// weights only; the internal blends (quality sub-weights, relative mix) keep
	// the exact sum-to-1 invariant because there 1.0 is a real property, not a
	// renormalisation.
	DefaultRelativeWeight      = 0.15
	DefaultMarketContextWeight = 0.05

	DefaultTargetMarginOfSafety = 30.0
	DefaultDCFYears             = 5
	DefaultTerminalGrowth       = 2.5
	DefaultGrowthTransition     = true

	DefaultBuyThreshold  = 70.0
	DefaultHoldThreshold = 40.0

	DefaultQualityCoverageHigh   = 0.85
	DefaultQualityCoverageMedium = 0.60

	DefaultRelativeSectorWeight     = 0.6
	DefaultRelativeHistoricalWeight = 0.4

	// Az7(a): the explicit Rf / ERP / cost-of-debt SPREAD breakdown of the CAPM
	// discount. Their consequence is documented as M6c-D3: with all three
	// configured, the reachable WACC taxonomy ceiling is capm_hybrid/medium.
	DefaultRiskFree          = 4.5
	DefaultEquityRiskPremium = 5.5
	DefaultCostOfDebtSpread  = 1.5

	// Az1(a): the normalised tax rate for ROIC (QUALITY_TAX_RATE).
	DefaultQualityTaxRate = 21.0
)

// Quality sub-block names (§12 row "Quality 35%", ADR D3/Az3). They are the keys
// of QualitySubWeights, the keys of parameter_sets JSONB and the `name` of every
// SubScore exposed by GET /quality.
const (
	SubProfitability = "profitability"
	SubGrowth        = "growth"
	SubMargins       = "margins"
	SubStability     = "stability"
	SubSolvency      = "debt_solvency"
)

// SubBlockNames is the canonical, ORDERED list of the five quality sub-blocks.
// The order is the order of presentation in the API/UI and of the parameter set
// JSON, which keeps both reproducible (no map iteration).
func SubBlockNames() []string {
	return []string{SubProfitability, SubGrowth, SubMargins, SubStability, SubSolvency}
}

// JSON keys of parameter_sets.parameters. snake_case, flat where possible, so
// that an operator reading the JSONB row in psql recognises the env var it
// overrides.
const (
	KeyGrahamWeight             = "graham_weight"
	KeyDCFWeight                = "dcf_weight"
	KeyQualityWeight            = "quality_weight"
	KeyRelativeWeight           = "relative_weight"
	KeyMarketContextWeight      = "market_context_weight"
	KeyTargetMarginOfSafety     = "target_margin_of_safety"
	KeyDCFYears                 = "dcf_years"
	KeyTerminalGrowth           = "terminal_growth"
	KeyGrowthTransition         = "growth_transition"
	KeyBuyThreshold             = "buy_threshold"
	KeyHoldThreshold            = "hold_threshold"
	KeyQualitySubWeights        = "quality_sub_weights"
	KeyQualityCoverageHigh      = "quality_coverage_high"
	KeyQualityCoverageMedium    = "quality_coverage_medium"
	KeyRelativeSectorWeight     = "relative_sector_weight"
	KeyRelativeHistoricalWeight = "relative_historical_weight"
	KeyRelativeAdvantagePct     = "relative_advantage_pct"
	KeyComparablesMinSecurities = "comparables_min_securities"
	KeyRiskFree                 = "risk_free"
	KeyEquityRiskPremium        = "equity_risk_premium"
	KeyCostOfDebtSpread         = "cost_of_debt_spread"
	KeyQualityTaxRate           = "quality_tax_rate"
	KeyTaxRate                  = "tax_rate"
	KeyBetaAssumed              = "beta_assumed"
)

// weightSumTolerance is the tolerance of the "the weights sum to 1" barrier of
// the internal blends, and of the "<= 1" barrier of the top-level weights.
// The five binary weights of §12 sum to 0.9999999999999999, so the check must
// be a tolerance and not an equality.
const weightSumTolerance = 1e-9

// ModelConfig is the full parameter set of the model: everything the engines are
// allowed to read, and nothing they are not. There is no field for a value that
// the engines compute themselves (bands, medians, volatility windows of the
// price series): a Config field is a DECISION, not a datum.
type ModelConfig struct {
	// §12 top-level weights. They sum to 1 (Validate is the barrier).
	GrahamWeight        float64 `json:"graham_weight"`
	DCFWeight           float64 `json:"dcf_weight"`
	QualityWeight       float64 `json:"quality_weight"`
	RelativeWeight      float64 `json:"relative_weight"`
	MarketContextWeight float64 `json:"market_context_weight"`

	// §11/§18 valuation threshold and DCF horizon.
	TargetMarginOfSafety float64 `json:"target_margin_of_safety"`
	// RESERVED (§28): consumed in future optimization; today lives in ConfigFromEnv of valuation engine.
	DCFYears int `json:"dcf_years"`
	// RESERVED (§28): consumed in future optimization; today lives in ConfigFromEnv of valuation engine.
	TerminalGrowth float64 `json:"terminal_growth"` // percentage/year
	// RESERVED (§28): consumed in future optimization; today lives in ConfigFromEnv of valuation engine.
	GrowthTransition bool `json:"growth_transition"`

	// Signal thresholds (decision 2026-09-22 D5: >=70 comprar, >=40 mantener).
	// RESERVED (§28): consumed in future optimization; today lives in ConfigFromEnv of score engine.
	BuyThreshold float64 `json:"buy_threshold"`
	// RESERVED (§28): consumed in future optimization; today lives in ConfigFromEnv of score engine.
	HoldThreshold float64 `json:"hold_threshold"`

	// §23/ADR D3/D26: quality sub-weights and coverage gates.
	QualitySubWeights        map[string]float64 `json:"quality_sub_weights"`
	QualityCoverageHigh      float64            `json:"quality_coverage_high"`
	QualityCoverageMedium    float64            `json:"quality_coverage_medium"`
	QualityStabilityMinYears int                `json:"quality_stability_min_years"`
	QualityTaxRate           float64            `json:"quality_tax_rate"` // QUALITY_TAX_RATE (Az1a)

	// §16: sector/historical mix of relative valuation.
	RelativeSectorWeight     float64 `json:"relative_sector_weight"`
	RelativeHistoricalWeight float64 `json:"relative_historical_weight"`
	RelativeHistoricalYears  int     `json:"relative_historical_years"`
	RelativeMinMetrics       int     `json:"relative_min_metrics"`
	RelativeAdvantagePct     float64 `json:"relative_advantage_pct"`     // RELATIVE_ADVANTAGE_PCT
	ComparablesMinSecurities int     `json:"comparables_min_securities"` // COMPARABLES_MIN_SECURITIES

	// Az7(a): explicit CAPM breakdown (percentages). CostOfDebtSpread is ADDED to
	// RiskFree to obtain Kd; the engines own that arithmetic.
	// Canonical env names: WACC_RISK_FREE, WACC_EQUITY_RISK_PREMIUM, WACC_COST_OF_DEBT_SPREAD,
	// WACC_TAX_RATE (alias of QUALITY_TAX_RATE), WACC_BETA_ASSUMED.
	RiskFree          float64 `json:"risk_free"`
	EquityRiskPremium float64 `json:"equity_risk_premium"`
	CostOfDebtSpread  float64 `json:"cost_of_debt_spread"`
	TaxRate           float64 `json:"tax_rate"`     // WACC_TAX_RATE
	BetaAssumed       float64 `json:"beta_assumed"` // WACC_BETA_ASSUMED

	// Name/ID of the Parameter Set this config came from (provenance only: it is
	// persisted in scores.parameter_set_id, never read by a formula).
	ParameterSetName string `json:"parameter_set_name,omitempty"`
	ParameterSetID   int64  `json:"parameter_set_id,omitempty"`
}

// DefaultModelConfig is the codes layer: a fresh value (including a fresh
// sub-weight map) with every M6a/M6b default.
func DefaultModelConfig() ModelConfig {
	return ModelConfig{
		GrahamWeight:        DefaultGrahamWeight,
		DCFWeight:           DefaultDCFWeight,
		QualityWeight:       DefaultQualityWeight,
		RelativeWeight:      DefaultRelativeWeight,
		MarketContextWeight: DefaultMarketContextWeight,

		TargetMarginOfSafety: DefaultTargetMarginOfSafety,
		DCFYears:             DefaultDCFYears,
		TerminalGrowth:       DefaultTerminalGrowth,
		GrowthTransition:     DefaultGrowthTransition,

		BuyThreshold:  DefaultBuyThreshold,
		HoldThreshold: DefaultHoldThreshold,

		QualitySubWeights:        DefaultQualitySubWeights(),
		QualityCoverageHigh:      DefaultQualityCoverageHigh,
		QualityCoverageMedium:    DefaultQualityCoverageMedium,
		QualityStabilityMinYears: DefaultQualityStabilityMinYears,
		QualityTaxRate:           DefaultQualityTaxRate,

		RelativeSectorWeight:     DefaultRelativeSectorWeight,
		RelativeHistoricalWeight: DefaultRelativeHistoricalWeight,
		RelativeHistoricalYears:  DefaultRelativeHistoricalYears,
		RelativeMinMetrics:       DefaultRelativeMinMetrics,
		RelativeAdvantagePct:     DefaultAdvantagePct,
		ComparablesMinSecurities: DefaultComparablesMinSecurities,

		RiskFree:          DefaultRiskFree,
		EquityRiskPremium: DefaultEquityRiskPremium,
		CostOfDebtSpread:  DefaultCostOfDebtSpread,
		TaxRate:           DefaultQualityTaxRate, // same default as quality tax rate
		BetaAssumed:       DefaultBetaAssumed,
	}
}

// Remaining M6c defaults that are integers of the model (documented here so the
// env table of the plan and the code share one source).
const (
	DefaultQualityStabilityMinYears = 4
	DefaultRelativeHistoricalYears  = 5
	DefaultRelativeMinMetrics       = 3
	DefaultAdvantagePct             = 0.20
	DefaultComparablesMinSecurities = 5
	DefaultBetaAssumed              = 1.0
)

// DefaultQualitySubWeights is the balanced §18 strict distribution (Az3: five
// sub-blocks at 0.20 each). A new map is returned on every call so that callers
// can never mutate a shared default.
func DefaultQualitySubWeights() map[string]float64 {
	return map[string]float64{
		SubProfitability: 0.20,
		SubGrowth:        0.20,
		SubMargins:       0.20,
		SubStability:     0.20,
		SubSolvency:      0.20,
	}
}

// Clone returns a deep copy: the sub-weight map is copied, so two configs never
// share mutable state (the replay resolves one set per run and must not be able
// to contaminate the next run).
func (mc ModelConfig) Clone() ModelConfig {
	out := mc
	out.QualitySubWeights = make(map[string]float64, len(mc.QualitySubWeights))
	for k, v := range mc.QualitySubWeights {
		out.QualitySubWeights[k] = v
	}
	return out
}

// Weights returns the five top-level weights keyed by dimension name, in the
// canonical order of §12. The score engine reads the weights from here instead
// of from its own constants, so a parameter set can move them.
func (mc ModelConfig) Weights() map[string]float64 {
	return map[string]float64{
		"graham":         mc.GrahamWeight,
		"dcf":            mc.DCFWeight,
		"quality":        mc.QualityWeight,
		"relative":       mc.RelativeWeight,
		"market_context": mc.MarketContextWeight,
	}
}

// SubWeight returns the configured weight of one quality sub-block, falling back
// to the balanced 0.20 when the key is absent (an absent key is not a zero).
func (mc ModelConfig) SubWeight(name string) float64 {
	if w, ok := mc.QualitySubWeights[name]; ok && w > 0 {
		return w
	}
	return 1.0 / 5.0
}

// ModelConfigFromEnv returns the codes layer with the environment applied. It is
// the only function of the package that reads os.Getenv, and it never fails: an
// invalid value falls back to the default with a warning (env.go).
//
//	MARGIN_OF_SAFETY               30    §11 margin of safety
//	DCF_HORIZON_YEARS              5     §7 horizon
//	DCF_TERMINAL_GROWTH            2.5   terminal growth %/year
//	VALUATION_GROWTH_TRANSITION     1     §7 growth transition
//	MARKET_CONTEXT_WEIGHT          0.10  §17 (0.05 per §12, see the DEVIATION
//	                                    note on DefaultMarketContextWeight)
//	QUALITY_TAX_RATE               21    Az1(a) normalised tax rate %
//	QUALITY_WEIGHT_<SUBBLOCK>      0.20  ADR D3 (five sub-blocks)
//	QUALITY_COVERAGE_HIGH          0.85  §23
//	QUALITY_COVERAGE_MEDIUM        0.60  §23
//	QUALITY_STABILITY_MIN_YEARS    4     §13 stability
//	RELATIVE_SECTOR_WEIGHT         0.6   §16
//	RELATIVE_HISTORICAL_WEIGHT     0.4   §16
//	RELATIVE_HISTORICAL_YEARS      5     §16
//	RELATIVE_MIN_METRICS           3     §16
//	WACC_RISK_FREE                 4.5   Az7(a)
//	WACC_EQUITY_RISK_PREMIUM       5.5   Az7(a)
//	WACC_COST_OF_DEBT_SPREAD       1.5   Az7(a)
//
// The score signal thresholds (BUY/HOLD) have no env of their own: they are the
// user decision of 2026-09-22 (D5) and are only movable from a parameter set.
func ModelConfigFromEnv() ModelConfig {
	mc := DefaultModelConfig()

	mc.TargetMarginOfSafety = EnvFloatRange("MARGIN_OF_SAFETY", mc.TargetMarginOfSafety, 0, 100)
	mc.DCFYears = EnvIntRange("DCF_HORIZON_YEARS", mc.DCFYears, 1, 50)
	mc.TerminalGrowth = EnvFloatRange("DCF_TERMINAL_GROWTH", mc.TerminalGrowth, -100, 100)
	mc.GrowthTransition = EnvBool("VALUATION_GROWTH_TRANSITION", mc.GrowthTransition)

	mc.MarketContextWeight = EnvFloatRange("MARKET_CONTEXT_WEIGHT", mc.MarketContextWeight, 0, 1)
	mc.QualityCoverageHigh = EnvFloatRange("QUALITY_COVERAGE_HIGH", mc.QualityCoverageHigh, 0, 1)
	mc.QualityCoverageMedium = EnvFloatRange("QUALITY_COVERAGE_MEDIUM", mc.QualityCoverageMedium, 0, 1)
	mc.QualityStabilityMinYears = EnvIntRange("QUALITY_STABILITY_MIN_YEARS", mc.QualityStabilityMinYears, 1, 20)
	mc.QualityTaxRate = EnvFloatRange("QUALITY_TAX_RATE", mc.QualityTaxRate, 0, 50)

	mc.RelativeSectorWeight = EnvFloatRange("RELATIVE_SECTOR_WEIGHT", mc.RelativeSectorWeight, 0, 1)
	mc.RelativeHistoricalWeight = EnvFloatRange("RELATIVE_HISTORICAL_WEIGHT", mc.RelativeHistoricalWeight, 0, 1)
	mc.RelativeHistoricalYears = EnvIntRange("RELATIVE_HISTORICAL_YEARS", mc.RelativeHistoricalYears, 1, 50)
	mc.RelativeMinMetrics = EnvIntRange("RELATIVE_MIN_METRICS", mc.RelativeMinMetrics, 1, 9)
	mc.RelativeAdvantagePct = EnvFloatRange("RELATIVE_ADVANTAGE_PCT", mc.RelativeAdvantagePct, 0.01, 1)
	mc.ComparablesMinSecurities = EnvIntRange("COMPARABLES_MIN_SECURITIES", mc.ComparablesMinSecurities, 1, 1000)

	mc.RiskFree = EnvFloatRange("WACC_RISK_FREE", mc.RiskFree, -100, 100)
	mc.EquityRiskPremium = EnvFloatRange("WACC_EQUITY_RISK_PREMIUM", mc.EquityRiskPremium, 0, 100)
	mc.CostOfDebtSpread = EnvFloatRange("WACC_COST_OF_DEBT_SPREAD", mc.CostOfDebtSpread, 0, 100)
	// WACC_TAX_RATE is DEPRECATED (alias of QUALITY_TAX_RATE). QUALITY_TAX_RATE is the
	// single canonical tax rate for both NOPAT (quality/ROIC) and Kd after-tax (WACC).
	if v := os.Getenv("WACC_TAX_RATE"); v != "" {
		slog.Warn("WACC_TAX_RATE deprecated, use QUALITY_TAX_RATE")
	}
	mc.TaxRate = mc.QualityTaxRate // single source of truth
	mc.BetaAssumed = EnvFloatRange("WACC_BETA_ASSUMED", mc.BetaAssumed, 0, 10)

	sub := mc.QualitySubWeights
	sub[SubProfitability] = EnvFloatRange("QUALITY_WEIGHT_PROFITABILITY", sub[SubProfitability], 0, 1)
	sub[SubGrowth] = EnvFloatRange("QUALITY_WEIGHT_GROWTH", sub[SubGrowth], 0, 1)
	sub[SubMargins] = EnvFloatRange("QUALITY_WEIGHT_MARGINS", sub[SubMargins], 0, 1)
	sub[SubStability] = EnvFloatRange("QUALITY_WEIGHT_STABILITY", sub[SubStability], 0, 1)
	sub[SubSolvency] = EnvFloatRange("QUALITY_WEIGHT_SOLVENCY", sub[SubSolvency], 0, 1)

	return mc
}

// Validate is the barrier §24 demands: an invalid configuration must fail
// BEFORE any result is persisted, never produce a score with weights that do not
// add up. It is pure and returns a descriptive error.
//
// The checks, in order of how badly a wrong value would corrupt a result:
//
//  1. the five weights are finite, non-negative and sum to 1 (§18);
//  2. the sub-weights cover exactly the five sub-blocks, are positive and sum
//     to 1 (a sub-block with weight 0 could never influence the score, which is
//     a silent decision, not a parameterisation);
//  3. the relative mix sums to 1 (§16);
//  4. the coverage gates are ordered (medium < high, both in (0,1]);
//  5. the thresholds are inside (0,100] and BUY > HOLD (an inverted pair would
//     emit "mantener" for a score that is above the buy threshold);
//  6. MOS, horizon, stability years, historical years and the CAPM breakdown are
//     inside their acceptance ranges.
func (mc ModelConfig) Validate() error {
	for _, w := range []struct {
		name  string
		value float64
	}{
		{"graham_weight", mc.GrahamWeight},
		{"dcf_weight", mc.DCFWeight},
		{"quality_weight", mc.QualityWeight},
		{"relative_weight", mc.RelativeWeight},
		{"market_context_weight", mc.MarketContextWeight},
	} {
		if math.IsNaN(w.value) || math.IsInf(w.value, 0) || w.value < 0 {
			return fmt.Errorf("modelcfg: peso %s inválido (%v): debe ser finito y >= 0", w.name, w.value)
		}
	}
	sum := mc.GrahamWeight + mc.DCFWeight + mc.QualityWeight + mc.RelativeWeight + mc.MarketContextWeight
	// §18 renormalisation: the five top-level weights express RELATIVE importance
	// and must fit in the unit interval. A strict-§18 default set sums to 0.90 by
	// construction, so demanding exactly 1 here would reject the approved values;
	// score21 divides by the weight used and the row reports weight_configured /
	// weight_used / active_weight_sum so the missing 0.10 is always visible.
	if sum <= 0 || sum > 1+weightSumTolerance {
		return fmt.Errorf("modelcfg: los pesos suman %.12f, deben sumar >0 y <=1 (§18)", sum)
	}

	for _, name := range SubBlockNames() {
		w, ok := mc.QualitySubWeights[name]
		if !ok {
			return fmt.Errorf("modelcfg: falta el sub-peso de quality %q", name)
		}
		if math.IsNaN(w) || math.IsInf(w, 0) || w <= 0 {
			return fmt.Errorf("modelcfg: sub-peso de quality %q inválido (%v): debe ser > 0", name, w)
		}
	}
	subSum := 0.0
	for name, w := range mc.QualitySubWeights {
		if !knownSubBlock(name) {
			return fmt.Errorf("modelcfg: sub-bloque de quality desconocido %q (válidos: %s)",
				name, strings.Join(SubBlockNames(), ", "))
		}
		subSum += w
	}
	if math.Abs(subSum-1) > weightSumTolerance {
		return fmt.Errorf("modelcfg: los sub-pesos de quality suman %.12f, deben sumar 1 (§18)", subSum)
	}

	if mc.RelativeSectorWeight < 0 || mc.RelativeHistoricalWeight < 0 {
		return fmt.Errorf("modelcfg: la mezcla de relative no puede ser negativa (%v/%v)",
			mc.RelativeSectorWeight, mc.RelativeHistoricalWeight)
	}
	if math.Abs(mc.RelativeSectorWeight+mc.RelativeHistoricalWeight-1) > weightSumTolerance {
		return fmt.Errorf("modelcfg: la mezcla de relative suma %.12f, debe sumar 1 (§16)",
			mc.RelativeSectorWeight+mc.RelativeHistoricalWeight)
	}

	if mc.QualityCoverageMedium <= 0 || mc.QualityCoverageHigh > 1 ||
		mc.QualityCoverageMedium >= mc.QualityCoverageHigh {
		return fmt.Errorf("modelcfg: umbrales de coverage de quality incoherentes (medium %.4f, high %.4f): 0 < medium < high <= 1",
			mc.QualityCoverageMedium, mc.QualityCoverageHigh)
	}

	if err := inRange("target_margin_of_safety", mc.TargetMarginOfSafety, 0, 100, false); err != nil {
		return err
	}
	if mc.BuyThreshold <= 0 || mc.BuyThreshold > 100 {
		return fmt.Errorf("modelcfg: buy_threshold %.4f fuera de (0,100]", mc.BuyThreshold)
	}
	if mc.HoldThreshold < 0 || mc.HoldThreshold > 100 {
		return fmt.Errorf("modelcfg: hold_threshold %.4f fuera de [0,100]", mc.HoldThreshold)
	}
	if mc.BuyThreshold <= mc.HoldThreshold {
		return fmt.Errorf("modelcfg: buy_threshold %.4f debe ser mayor que hold_threshold %.4f",
			mc.BuyThreshold, mc.HoldThreshold)
	}
	if mc.DCFYears < 1 || mc.DCFYears > 50 {
		return fmt.Errorf("modelcfg: dcf_years %d fuera de [1,50]", mc.DCFYears)
	}
	if err := inRange("terminal_growth", mc.TerminalGrowth, -100, 100, false); err != nil {
		return err
	}
	if mc.QualityStabilityMinYears < 1 || mc.QualityStabilityMinYears > 20 {
		return fmt.Errorf("modelcfg: quality_stability_min_years %d fuera de [1,20]", mc.QualityStabilityMinYears)
	}
	if err := inRange("quality_tax_rate", mc.QualityTaxRate, 0, 50, false); err != nil {
		return err
	}
	if mc.RelativeHistoricalYears < 1 || mc.RelativeHistoricalYears > 50 {
		return fmt.Errorf("modelcfg: relative_historical_years %d fuera de [1,50]", mc.RelativeHistoricalYears)
	}
	if mc.RelativeMinMetrics < 1 || mc.RelativeMinMetrics > 9 {
		return fmt.Errorf("modelcfg: relative_min_metrics %d fuera de [1,9]", mc.RelativeMinMetrics)
	}
	if err := inRange("relative_advantage_pct", mc.RelativeAdvantagePct, 0.01, 1, false); err != nil {
		return err
	}
	if mc.ComparablesMinSecurities < 1 {
		return fmt.Errorf("modelcfg: comparables_min_securities %d debe ser >= 1", mc.ComparablesMinSecurities)
	}
	if err := inRange("risk_free", mc.RiskFree, -100, 100, false); err != nil {
		return err
	}
	if err := inRange("equity_risk_premium", mc.EquityRiskPremium, 0, 100, false); err != nil {
		return err
	}
	if err := inRange("cost_of_debt_spread", mc.CostOfDebtSpread, 0, 100, false); err != nil {
		return err
	}
	if err := inRange("tax_rate", mc.TaxRate, 0, 50, false); err != nil {
		return err
	}
	if mc.BetaAssumed < 0 || mc.BetaAssumed > 10 {
		return fmt.Errorf("modelcfg: beta_assumed %.4f fuera de [0,10]", mc.BetaAssumed)
	}
	return nil
}

func inRange(name string, v, lo, hi float64, inclusiveLo bool) error {
	okLo := v >= lo
	if !inclusiveLo {
		okLo = v > lo
	}
	if !okLo || v > hi || math.IsNaN(v) || math.IsInf(v, 0) {
		return fmt.Errorf("modelcfg: %s %.4f fuera de rango", name, v)
	}
	return nil
}

func knownSubBlock(name string) bool {
	for _, n := range SubBlockNames() {
		if n == name {
			return true
		}
	}
	return false
}

// ParameterSet is a row of the `parameter_sets` table (§25): a NAME, the model
// version it was written for, and the JSONB overrides.
type ParameterSet struct {
	ID           int64          `json:"id"`
	Name         string         `json:"name"`
	ModelVersion string         `json:"model_version"`
	Parameters   map[string]any `json:"parameters"`
	CreatedAt    time.Time      `json:"created_at"`
}

// ErrParameterSetNotFound is returned by Resolve when the requested set does not
// exist. It is an EXPLICIT failure on purpose (ADR D15): silently falling back
// to `base` would persist a row labelled with a set that never ran.
var ErrParameterSetNotFound = errors.New("modelcfg: parameter set no encontrado")

// DBTX is the minimal read surface Resolve needs. *pgxpool.Pool and
// *pgxpool.Conn both satisfy it, and so does a fake in the unit tests.
type DBTX interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// DefaultParameterSetName is PARAMETER_SET, or "base" when unset. "base" means
// "no overrides: use env/defaults" (ADR D28), which is why it is a safe default.
func DefaultParameterSetName() string {
	if v := strings.TrimSpace(EnvString("PARAMETER_SET", DefaultParameterSet)); v != "" {
		return v
	}
	return DefaultParameterSet
}

// DefaultParameterSet is the seeded set that applies no overrides.
const DefaultParameterSet = "base"

// EnvString reads a non-empty trimmed string env var (the only string helper:
// parameter set names and file paths are not numbers, so they need no range).
func EnvString(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// GetParameterSetByName reads one parameter set. A missing row is
// ErrParameterSetNotFound; the raw pgx error is never leaked to the caller as a
// nil result.
func GetParameterSetByName(ctx context.Context, q DBTX, name string) (*ParameterSet, error) {
	if q == nil {
		return nil, fmt.Errorf("modelcfg: DBTX nil al leer el parameter set %q", name)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("modelcfg: nombre de parameter set vacío")
	}
	var (
		ps  ParameterSet
		raw []byte
	)
	err := q.QueryRow(ctx,
		`SELECT id, name, model_version, parameters, created_at FROM parameter_sets WHERE name = $1`,
		name).Scan(&ps.ID, &ps.Name, &ps.ModelVersion, &raw, &ps.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: %q", ErrParameterSetNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("modelcfg: leer parameter set %q: %w", name, err)
	}
	ps.Parameters, err = decodeParameters(raw)
	if err != nil {
		return nil, fmt.Errorf("modelcfg: parameter set %q: %w", name, err)
	}
	if ps.Parameters == nil {
		ps.Parameters = map[string]any{}
	}
	return &ps, nil
}

// decodeParameters turns the JSONB payload into a map. NULL, `null` and `{}` all
// mean "no overrides", never an error.
func decodeParameters(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parameters ilegibles: %w", err)
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

// Resolve returns the Parameter Set and the ModelConfig it implies, applying the
// three layers in order (codes < env < set). The configuration is validated
// before it is returned: an invalid set is an error, and the caller (the scores
// job) must NOT persist a result with it.
func Resolve(ctx context.Context, q DBTX, name string) (ParameterSet, ModelConfig, error) {
	if name == "" {
		name = DefaultParameterSetName()
	}
	cfg := ModelConfigFromEnv()

	ps, err := GetParameterSetByName(ctx, q, name)
	if err != nil {
		return ParameterSet{}, cfg, err
	}

	cfg = cfg.WithParameters(ps.Parameters)
	cfg.ParameterSetID = ps.ID
	cfg.ParameterSetName = ps.Name

	if err := cfg.Validate(); err != nil {
		return *ps, cfg, fmt.Errorf("parameter set %q: %w", ps.Name, err)
	}
	return *ps, cfg, nil
}

// WithParameters returns a COPY of mc with the JSONB overrides applied. It is
// pure (no database, no clock) and forgiving by design:
//
//   - an unknown key, a non-numeric value or an out-of-range value is warned
//     about and IGNORED, keeping the layer below. A parameter set written for a
//     newer model version must not be able to zero a parameter of the current
//     one (the "no silent zero-fill" rule);
//   - an ABSENT key never becomes zero: it keeps env/default.
func (mc ModelConfig) WithParameters(params map[string]any) ModelConfig {
	out := mc.Clone()
	if len(params) == 0 {
		return out // `{}` = no overrides (the `base` seed)
	}

	// Deterministic order: the canonical key list first (so two runs log the
	// same warnings in the same order), then any unknown key sorted.
	for _, k := range canonicalParameterKeys() {
		v, ok := params[k]
		if !ok {
			continue
		}
		applyParameter(&out, k, v)
	}
	for _, k := range sortedKeys(params) {
		if _, known := parameterKeyOrder()[k]; known {
			continue
		}
		slog.Warn("modelcfg: clave desconocida en el parameter set, se ignora",
			"clave", k, "origen", out.ParameterSetName)
	}
	return out
}

// applyParameter applies one override in place, refusing anything that is not a
// finite number inside the documented acceptance range (or a bool for
// growth_transition).
func applyParameter(mc *ModelConfig, key string, raw any) {
	switch key {
	case KeyGrahamWeight:
		mc.GrahamWeight = paramFloat(mc, key, raw, mc.GrahamWeight, 0, 1)
	case KeyDCFWeight:
		mc.DCFWeight = paramFloat(mc, key, raw, mc.DCFWeight, 0, 1)
	case KeyQualityWeight:
		mc.QualityWeight = paramFloat(mc, key, raw, mc.QualityWeight, 0, 1)
	case KeyRelativeWeight:
		mc.RelativeWeight = paramFloat(mc, key, raw, mc.RelativeWeight, 0, 1)
	case KeyMarketContextWeight:
		mc.MarketContextWeight = paramFloat(mc, key, raw, mc.MarketContextWeight, 0, 1)
	case KeyTargetMarginOfSafety:
		mc.TargetMarginOfSafety = paramFloat(mc, key, raw, mc.TargetMarginOfSafety, 0, 100)
	case KeyDCFYears:
		mc.DCFYears = paramInt(mc, key, raw, mc.DCFYears, 1, 50)
	case KeyTerminalGrowth:
		mc.TerminalGrowth = paramFloat(mc, key, raw, mc.TerminalGrowth, -100, 100)
	case KeyGrowthTransition:
		mc.GrowthTransition = paramBool(mc, key, raw, mc.GrowthTransition)
	case KeyBuyThreshold:
		mc.BuyThreshold = paramFloat(mc, key, raw, mc.BuyThreshold, 0, 100)
	case KeyHoldThreshold:
		mc.HoldThreshold = paramFloat(mc, key, raw, mc.HoldThreshold, 0, 100)
	case KeyQualityCoverageHigh:
		mc.QualityCoverageHigh = paramFloat(mc, key, raw, mc.QualityCoverageHigh, 0, 1)
	case KeyQualityCoverageMedium:
		mc.QualityCoverageMedium = paramFloat(mc, key, raw, mc.QualityCoverageMedium, 0, 1)
	case KeyRelativeSectorWeight:
		mc.RelativeSectorWeight = paramFloat(mc, key, raw, mc.RelativeSectorWeight, 0, 1)
	case KeyRelativeHistoricalWeight:
		mc.RelativeHistoricalWeight = paramFloat(mc, key, raw, mc.RelativeHistoricalWeight, 0, 1)
	case KeyRiskFree:
		mc.RiskFree = paramFloat(mc, key, raw, mc.RiskFree, -100, 100)
	case KeyEquityRiskPremium:
		mc.EquityRiskPremium = paramFloat(mc, key, raw, mc.EquityRiskPremium, 0, 100)
	case KeyCostOfDebtSpread:
		mc.CostOfDebtSpread = paramFloat(mc, key, raw, mc.CostOfDebtSpread, 0, 100)
	case KeyQualityTaxRate:
		mc.QualityTaxRate = paramFloat(mc, key, raw, mc.QualityTaxRate, 0, 50)
	case KeyRelativeAdvantagePct:
		mc.RelativeAdvantagePct = paramFloat(mc, key, raw, mc.RelativeAdvantagePct, 0.01, 1)
	case KeyComparablesMinSecurities:
		mc.ComparablesMinSecurities = paramInt(mc, key, raw, mc.ComparablesMinSecurities, 1, 1000)
	case KeyTaxRate:
		mc.TaxRate = paramFloat(mc, key, raw, mc.TaxRate, 0, 50)
	case KeyBetaAssumed:
		mc.BetaAssumed = paramFloat(mc, key, raw, mc.BetaAssumed, 0, 10)
	case KeyQualitySubWeights:
		applySubWeights(mc, raw)
	}
}

// applySubWeights merges the `quality_sub_weights` object one sub-block at a
// time, so an object that only overrides `growth` keeps the other four from the
// layer below (the `conservative` seed does exactly that).
func applySubWeights(mc *ModelConfig, raw any) {
	obj, ok := raw.(map[string]any)
	if !ok {
		slog.Warn("modelcfg: quality_sub_weights no es un objeto, se ignoran los overrides",
			"tipo", fmt.Sprintf("%T", raw))
		return
	}
	for _, name := range SubBlockNames() {
		v, ok := obj[name]
		if !ok {
			continue
		}
		current := mc.QualitySubWeights[name]
		mc.QualitySubWeights[name] = paramFloat(mc, KeyQualitySubWeights+"."+name, v, current, 0, 1)
	}
	for _, k := range sortedKeys(obj) {
		if !knownSubBlock(k) {
			slog.Warn("modelcfg: sub-bloque de quality desconocido en el parameter set, se ignora",
				"clave", k)
		}
	}
}

func paramFloat(mc *ModelConfig, key string, raw any, current, lo, hi float64) float64 {
	f, ok := toFloat(raw)
	if !ok {
		slog.Warn("modelcfg: valor no numérico en el parameter set, se conserva el actual",
			"clave", key, "tipo", fmt.Sprintf("%T", raw), "actual", current)
		return current
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f < lo || f > hi {
		slog.Warn("modelcfg: valor fuera de rango en el parameter set, se conserva el actual",
			"clave", key, "valor", f, "min", lo, "max", hi, "actual", current)
		return current
	}
	return f
}

func paramInt(mc *ModelConfig, key string, raw any, current, lo, hi int) int {
	f, ok := toFloat(raw)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
		slog.Warn("modelcfg: valor no entero en el parameter set, se conserva el actual",
			"clave", key, "valor", fmt.Sprintf("%v", raw), "actual", current)
		return current
	}
	i := int(f)
	if i < lo || i > hi {
		slog.Warn("modelcfg: valor fuera de rango en el parameter set, se conserva el actual",
			"clave", key, "valor", i, "min", lo, "max", hi, "actual", current)
		return current
	}
	return i
}

func paramBool(mc *ModelConfig, key string, raw any, current bool) bool {
	switch v := raw.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	slog.Warn("modelcfg: valor no booleano en el parameter set, se conserva el actual",
		"clave", key, "tipo", fmt.Sprintf("%T", raw), "actual", current)
	return current
}

// toFloat accepts the numeric shapes JSONB can produce. encoding/json decodes
// JSON numbers as float64; a set written by hand and scanned through a different
// driver may arrive as json.Number or int64, so those are accepted too.
func toFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// canonicalParameterKeys is the ORDERED list of the known keys: two runs over the
// same set must apply (and warn about) the overrides in the same order.
func canonicalParameterKeys() []string {
	order := parameterKeyOrder()
	out := make([]string, 0, len(order))
	for _, k := range canonicalOrder {
		out = append(out, k)
	}
	return out
}

// ErrSnapshotIncomplete is the shared error for snapshot decoders across all
// engines (growth, wacc, valuation, score). It signals that a required field
// is missing from the snapshot, making replay impossible. It is distinct from
// "field present but nil" (which means the engine couldn't compute it).
var ErrSnapshotIncomplete = errors.New("snapshot incompleto")

// ErrUnsupportedModelVersion is returned by snapshot decoders (growth, wacc,
// valuation) when the snapshot's model_version is not supported by this build.
// It is a SHARED error so callers can distinguish "version not supported" from
// "version missing" or "snapshot malformed".
var ErrUnsupportedModelVersion = errors.New("model_version no soportada")

var canonicalOrder = []string{
	KeyGrahamWeight, KeyDCFWeight, KeyQualityWeight, KeyRelativeWeight, KeyMarketContextWeight,
	KeyTargetMarginOfSafety, KeyDCFYears, KeyTerminalGrowth, KeyGrowthTransition,
	KeyBuyThreshold, KeyHoldThreshold,
	KeyQualityCoverageHigh, KeyQualityCoverageMedium, KeyQualitySubWeights, KeyQualityTaxRate,
	KeyRelativeSectorWeight, KeyRelativeHistoricalWeight,
	KeyRelativeAdvantagePct, KeyComparablesMinSecurities,
	KeyRiskFree, KeyEquityRiskPremium, KeyCostOfDebtSpread,
	KeyTaxRate, KeyBetaAssumed,
}

func parameterKeyOrder() map[string]int {
	m := make(map[string]int, len(canonicalOrder))
	for i, k := range canonicalOrder {
		m[k] = i
	}
	return m
}

// sortedKeys returns the map keys sorted: no map iteration may leak into a log
// line or into a persisted result (§27 determinism).
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
