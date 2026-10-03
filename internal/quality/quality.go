// Package quality implements the Quality engine of SPEC v2 §13/§14/§23 (plan
// M6c §A): ONE dimension of the investment score (weight 0.35, ADR D3) built
// from FIVE visible sub-blocks of 0.20 each — profitability, growth, margins,
// stability and debt_solvency.
//
// Three rules shape the whole package:
//
//  1. A missing datum is NEVER a number. A metric that cannot be computed is
//     nil with a stable reason; it is excluded from its sub-block and counted as
//     NOT VALID in quality_coverage (§23). Nothing is substituted by zero, by a
//     50 or by a median.
//  2. The sub-blocks are part of the CONTRACT, not an internal detail (Az3,
//     ADR D26): GET /quality and the UI show every sub-block with its coverage,
//     and the aggregate is a weighted mean over the sub-blocks that could be
//     computed — never over the ones we would like to have.
//  3. Calculate(Inputs, Config) is PURE: no clock (AsOf travels in Inputs), no
//     database, no environment, no map iteration in the computation path. The
//     same inputs and config produce a byte-identical Result (§27).
//
// §32 forbids filling missing information with new indicators; it does not
// forbid honest bands, so every metric is mapped to 0-100 with two documented
// cut points that live in Config (see bands.go).
package quality

import (
	"math"
	"time"

	"github.com/miky/abys-invest/internal/reason"
)

// ModelVersion identifies the quality formula revision. 1.0.0 is the FIRST
// version of a NEW engine: nothing persisted before M6c carries this string.
const ModelVersion = "1.0.0"

// Metric names (§13/§14). They are the keys of Result.Metrics, the `name` of
// every Metric of a SubScore, and the same slugs that derived_metrics 2.0.0
// persists (ADR D12), so the trace of a score and the metrics table speak the
// same language.
const (
	MetricROIC            = "roic"
	MetricROE             = "roe"
	MetricOperatingMargin = "operating_margin"
	MetricFCFMargin       = "fcf_margin"
	MetricFCFYield        = "fcf_yield"

	MetricFCFCAGR3y     = "fcf_cagr_3y"
	MetricFCFCAGR5y     = "fcf_cagr_5y"
	MetricEPSCAGR3y     = "eps_cagr_3y"
	MetricEPSCAGR5y     = "eps_cagr_5y"
	MetricRevenueCAGR3y = "revenue_cagr_3y"

	MetricPositiveEPSYears = "positive_eps_years"
	MetricPositiveFCFYears = "positive_fcf_years"
	MetricEPSVolatility    = "eps_volatility"
	MetricFCFVolatility    = "fcf_volatility"

	MetricNetDebtToEBITDA  = "net_debt_to_ebitda"
	MetricFCFToDebt        = "fcf_to_debt"
	MetricInterestCoverage = "interest_coverage"

	// MetricDERatio is SECONDARY (§14: "mantener D/E como métrica SECUNDARIA").
	// It is exposed inside its sub-block and it does NOT count towards
	// applicable_metrics nor quality_coverage: a metric that does not move the
	// score must not inflate the coverage either.
	MetricDERatio = "de_ratio"
)

// Reason codes. Stable strings: they are persisted, shown by the UI and asserted
// by tests, so they are not free text.
const (
	ReasonDebtUnavailable        = "debt_unavailable"
	ReasonNonPositiveInvestedCap = "non_positive_invested_capital"
	ReasonNonPositiveEquity      = "non_positive_equity"
	ReasonNonPositiveRevenue     = "non_positive_revenue"
	ReasonNonPositiveMarketCap   = "non_positive_market_cap"
	ReasonNonPositiveEBITDA      = "non_positive_ebitda"
	ReasonNonPositiveTotalDebt   = "non_positive_total_debt"
	ReasonGrowthUnreliable       = "growth_unreliable"
	ReasonInsufficientHistory    = "insufficient_history"
	ReasonZeroMeanBase           = "zero_mean_base"
	ReasonInterestExpenseMissing = "interest_expense_unavailable"
	ReasonTaxRateConfigured      = "tax_rate_configured"
	ReasonNoValidMetric          = "no_valid_metric"
	ReasonNoData                 = "no_data"
	ReasonNonFiniteInput         = "non_finite_input"
)

// SeriesPoint is one annual fiscal-year datum of the stability series: the
// value and the fiscal year it belongs to. The loader (storage.GetFYAnnualSeries)
// already deduplicates by period_end, so a restatement of the same fiscal year
// arrives once — otherwise FY2024 would be counted twice and the volatility of a
// five-year series would be computed over six points.
type SeriesPoint struct {
	PeriodEnd time.Time `json:"period_end"`
	Value     float64   `json:"value"`
}

// Series keys of Inputs.Series.
const (
	SeriesEPSDiluted   = "eps_diluted"
	SeriesFreeCashFlow = "free_cash_flow"
)

// GrowthInputs are the CAGRs ALREADY PERSISTED by the Growth engine in
// growth_metrics (ADR D7): M6c reads them and never recomputes a CAGR, because
// recomputing it here would mean two definitions of the same number.
//
// A nil CAGR, or a Confidence of "low", is not a valid metric (ADR D7) — with the
// single reason `growth_unreliable`. A NEGATIVE CAGR is perfectly valid
// (AAPL fcf_cagr_3y = -3.9451): shrinking cash flow is information, not a bug.
type GrowthInputs struct {
	FCFCAGR3y     *float64 `json:"fcf_cagr_3y,omitempty"`
	FCFCAGR5y     *float64 `json:"fcf_cagr_5y,omitempty"`
	EPSCAGR3y     *float64 `json:"eps_cagr_3y,omitempty"`
	EPSCAGR5y     *float64 `json:"eps_cagr_5y,omitempty"`
	RevenueCAGR3y *float64 `json:"revenue_cagr_3y,omitempty"`
	// Confidence is growth_metrics.growth_confidence (high|medium|low).
	Confidence string `json:"confidence,omitempty"`
}

// Inputs is the full computational input set of Calculate. Every pointer/map
// entry is nil/absent when the datum is missing: a nil never means zero (§1).
type Inputs struct {
	Ticker string    `json:"ticker,omitempty"`
	AsOf   time.Time `json:"as_of"`

	// Financials are the FY instants of the fiscal year used, keyed by EDGAR
	// concept: operating_income, shareholders_equity, total_debt,
	// cash_and_equivalents, net_debt, ebitda, revenue(s), free_cash_flow,
	// net_earnings, interest_expense.
	Financials map[string]*float64 `json:"financials,omitempty"`
	// Series are the annual FY series (deduplicated by period_end) used by §13's
	// stability metrics, keyed by SeriesEPSDiluted / SeriesFreeCashFlow.
	Series map[string][]SeriesPoint `json:"series,omitempty"`
	// Growth are the CAGRs read from growth_metrics.
	Growth GrowthInputs `json:"growth,omitempty"`

	// Price is the market close of AsOf and SharesOutstanding the share count of
	// the same date: market_cap = price × shares (A4). An explicit MarketCap
	// overrides the product when the caller already has it.
	Price             float64  `json:"price,omitempty"`
	SharesOutstanding *float64 `json:"shares_outstanding,omitempty"`
	MarketCap         *float64 `json:"market_cap,omitempty"`

	// TaxRateSource is "configured" in M6c (Az1(a)) — the only value it can have
	// until M6c-T1 lands the XBRL plumbing for income_tax_expense. It caps the
	// confidence at medium (ADR D26).
	TaxRateSource string `json:"tax_rate_source,omitempty"`
}

// Tax rate provenance (ADR D26/D5).
const TaxRateSourceConfigured = "configured"

// Confidence levels of §23, in decreasing order of trust.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Metric is one §13 metric with its value, its 0-100 score and the reason it has
// no value. Exactly one of Score/Reason is meaningful: Score != nil implies
// Reason == "".
type Metric struct {
	Name   string   `json:"name"`
	Value  *float64 `json:"value"`
	Score  *float64 `json:"score"`
	Reason string   `json:"reason,omitempty"`
}

// SubScore is one of the five visible sub-blocks (Az3): its score, the weight it
// carries INSIDE quality, its own coverage (valid/own applicable metrics) and
// every metric it is built from.
type SubScore struct {
	Name     string   `json:"name"`
	Score    *float64 `json:"score"`
	Weight   float64  `json:"weight"`
	Coverage float64  `json:"coverage"`
	Metrics  []Metric `json:"metrics"`
}

// Result is the deterministic output of Calculate.
type Result struct {
	// Score is nil ONLY when every sub-block is nil, i.e. when there is no usable
	// datum at all: §18 then renormalises the score without this dimension. A
	// partially covered quality is a NUMBER with a coverage below 1, never nil.
	Score *float64 `json:"score"`
	// Coverage is valid/applicable (§23). It is always a float when Score != nil,
	// and Coverage == 0 implies Score == nil (invariant tested in quality_test.go).
	Coverage float64 `json:"coverage"`
	// Confidence is high|medium|low (§23), capped at medium while the tax rate is
	// configured (ADR D26). It is never RESURRECTED by a high score.
	Confidence string               `json:"confidence"`
	SubScores  map[string]*SubScore `json:"sub_scores"`
	Metrics    map[string]*float64  `json:"metrics"`
	// TaxRateSource and Reasons explain the confidence and the degradations.
	TaxRateSource string   `json:"tax_rate_source"`
	Reasons       []string `json:"reasons,omitempty"`
	// AvailableAt is Inputs.AsOf echoed: the date this quality is reproducible
	// FROM (§4/§26).
	AvailableAt time.Time `json:"available_at"`
	// ModelVersion is the quality formula revision that produced this result.
	ModelVersion string `json:"model_version"`
}

// Metric names in canonical order: the order of presentation, of the coverage
// denominator and of the trace. No map iteration ever decides it.
func MetricNames() []string {
	return []string{
		MetricROIC, MetricROE,
		MetricOperatingMargin, MetricFCFMargin, MetricFCFYield,
		MetricFCFCAGR3y, MetricFCFCAGR5y, MetricEPSCAGR3y, MetricEPSCAGR5y, MetricRevenueCAGR3y,
		MetricPositiveEPSYears, MetricPositiveFCFYears, MetricEPSVolatility, MetricFCFVolatility,
		MetricNetDebtToEBITDA, MetricFCFToDebt, MetricInterestCoverage,
	}
}

// ApplicableMetrics is the CONSTANT denominator of quality_coverage: §13/§14
// define seventeen metrics, they do not define "whatever happened to be there
// today". With a constant denominator a company cannot raise its coverage by
// being less analysed, and the 16/17 of the smoke (only interest_coverage
// missing) is a comparable number across tickers and across time.
func ApplicableMetrics() []string { return MetricNames() }

// SubBlockNames is the canonical order of the five sub-blocks.
func SubBlockNames() []string {
	return []string{SubProfitability, SubGrowth, SubMargins, SubStability, SubSolvency}
}

// subBlockDef ties a sub-block to the applicable metrics it owns. de_ratio is
// deliberately absent from every list: it is secondary (§14) and must not count
// towards applicable_metrics.
var subBlockDefs = []struct {
	Name    string
	Metrics []string
}{
	{SubProfitability, []string{MetricROIC, MetricROE}},
	{SubGrowth, []string{MetricFCFCAGR3y, MetricFCFCAGR5y, MetricEPSCAGR3y, MetricEPSCAGR5y, MetricRevenueCAGR3y}},
	{SubMargins, []string{MetricOperatingMargin, MetricFCFMargin, MetricFCFYield}},
	{SubStability, []string{MetricPositiveEPSYears, MetricPositiveFCFYears, MetricEPSVolatility, MetricFCFVolatility}},
	{SubSolvency, []string{MetricNetDebtToEBITDA, MetricFCFToDebt, MetricInterestCoverage}},
}

// secondaryOfSubBlock is where de_ratio is displayed: inside debt_solvency, as a
// context metric with no weight and no coverage.
func secondaryOfSubBlock(name string) []string {
	if name == SubSolvency {
		return []string{MetricDERatio}
	}
	return nil
}

// metricValue is the internal per-metric outcome before it is exposed.
type metricValue struct {
	Value  *float64
	Score  *float64
	Reason string
}

func valid(v float64) *float64 { return &v }

func mv(value *float64, score float64) metricValue {
	return metricValue{Value: value, Score: valid(score)}
}

func mvNil(reason string) metricValue { return metricValue{Reason: reason} }

// finite reports whether v is usable as an input of a ratio.
func finite(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0)
}

// fin looks a concept up in the financials map, accepting the aliases that the
// EDGAR catalogue uses for the same magnitude. It returns nil when absent or
// non-finite (never zero).
func fin(m map[string]*float64, keys ...string) *float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok && finite(v) {
			return v
		}
	}
	return nil
}

// Calculate runs the whole engine: five sub-blocks of metrics, their bands, the
// weighted aggregate, the §23 coverage and the confidence.
//
// It never returns an error and never panics: a bad input degrades a metric with
// a reason, which is the contract of every engine of this project.
func Calculate(in Inputs, cfg Config) Result {
	res := Result{
		Confidence:    ConfidenceLow,
		SubScores:     map[string]*SubScore{},
		Metrics:       map[string]*float64{},
		TaxRateSource: taxRateSource(in),
		AvailableAt:   in.AsOf,
		ModelVersion:  ModelVersion,
	}

	values := computeAll(in, cfg)

	// Metrics map: every applicable metric that has a value, plus de_ratio as a
	// secondary. Written in canonical order (the map is unordered, but the TRACE
	// that consumes it is not).
	for _, name := range MetricNames() {
		if values[name].Value != nil {
			res.Metrics[name] = values[name].Value
		}
	}
	if values[MetricDERatio].Value != nil {
		res.Metrics[MetricDERatio] = values[MetricDERatio].Value
	}

	var reasons []string

	// Sub-blocks, in canonical order.
	totalWeight, weighted, validCount := 0.0, 0.0, 0
	for _, def := range subBlockDefs {
		metrics := make([]Metric, 0, len(def.Metrics))
		var sum float64
		var n int
		for _, name := range def.Metrics {
			mv := values[name]
			sub := Metric{Name: name, Value: mv.Value, Score: mv.Score, Reason: mv.Reason}
			metrics = append(metrics, sub)
			if mv.Score != nil {
				sum += *mv.Score
				n++
				validCount++
			}
		}
		// Secondary metrics are displayed inside the sub-block but never counted.
		for _, name := range secondaryOfSubBlock(def.Name) {
			mv := values[name]
			metrics = append(metrics, Metric{Name: name, Value: mv.Value, Score: nil, Reason: mv.Reason})
		}

		sub := &SubScore{
			Name:     def.Name,
			Score:    nil,
			Weight:   cfg.SubWeight(def.Name),
			Coverage: coverage(n, len(def.Metrics)),
			Metrics:  metrics,
		}
		if n > 0 {
			sub.Score = valid(sum / float64(n))
			weighted += *sub.Score * sub.Weight
			totalWeight += sub.Weight
		} else {
			reasons = append(reasons, subBlockNilReason(def.Name))
		}
		res.SubScores[def.Name] = sub
	}

	applicable := len(ApplicableMetrics())
	res.Coverage = coverage(validCount, applicable)

	if totalWeight > 0 {
		// Weighted mean over the sub-blocks that COULD be computed, renormalised by
		// their own weights (§18 applied inside the dimension). A sub-block that is
		// nil therefore lowers the weight used, never pulls the average towards a
		// neutral value it does not have.
		res.Score = valid(weighted / totalWeight)
		res.Confidence = confidenceFor(res.Coverage, cfg, res.TaxRateSource)
	} else {
		res.Coverage = coverage(validCount, applicable)
		reasons = append(reasons, ReasonNoValidMetric)
	}

	if res.TaxRateSource == TaxRateSourceConfigured && res.Score != nil {
		reasons = append(reasons, ReasonTaxRateConfigured)
	}

	res.Reasons = reason.Normalize(reasons)
	return res
}

func subBlockNilReason(name string) string {
	switch name {
	case SubProfitability:
		return "profitability_unavailable"
	case SubGrowth:
		return "growth_metrics_unavailable"
	case SubMargins:
		return "margins_unavailable"
	case SubStability:
		return "stability_unavailable"
	case SubSolvency:
		return "solvency_unavailable"
	default:
		return ReasonNoData
	}
}

// confidenceFor implements §23 plus the ADR D26 cap: the coverage sets the band
// and a CONFIGURED tax rate caps it at medium, because a normalised tax rate is
// an assumption of the deployment and not an observation of the company.
//
// The cap reads the tax rate PROVENANCE (Inputs.TaxRateSource, "configured" in
// M6c) and not the value: the moment M6c-T1 derives the rate from
// income_tax_expense, the same numbers will legitimately reach `high`.
func confidenceFor(cov float64, cfg Config, taxSource string) string {
	base := ConfidenceLow
	switch {
	case cov >= cfg.CoverageHigh:
		base = ConfidenceHigh
	case cov >= cfg.CoverageMedium:
		base = ConfidenceMedium
	}
	if taxSource == TaxRateSourceConfigured && base == ConfidenceHigh && cfg.TaxRateConfigured() {
		return ConfidenceMedium
	}
	return base
}

func coverage(valid, applicable int) float64 {
	if applicable <= 0 {
		return 0
	}
	return float64(valid) / float64(applicable)
}

func taxRateSource(in Inputs) string {
	if in.TaxRateSource == "" {
		return TaxRateSourceConfigured
	}
	return in.TaxRateSource
}

// computeAll evaluates the seventeen applicable metrics (plus the secondary
// de_ratio) in the canonical order of MetricNames. It is the only place that
// decides WHICH number each metric is, which keeps the trace and the score
// reproducible from a single review.
func computeAll(in Inputs, cfg Config) map[string]metricValue {
	mc := marketCap(in)
	out := map[string]metricValue{
		MetricROIC:     calcROIC(in, cfg),
		MetricROE:      calcROE(in, cfg),
		MetricFCFYield: calcFCFYield(in, mc, cfg),

		MetricFCFCAGR3y:     cagrMetric(in.Growth.FCFCAGR3y, in.Growth, cfg.Bands.FCFCAGR3y),
		MetricFCFCAGR5y:     cagrMetric(in.Growth.FCFCAGR5y, in.Growth, cfg.Bands.FCFCAGR5y),
		MetricEPSCAGR3y:     cagrMetric(in.Growth.EPSCAGR3y, in.Growth, cfg.Bands.EPSCAGR3y),
		MetricEPSCAGR5y:     cagrMetric(in.Growth.EPSCAGR5y, in.Growth, cfg.Bands.EPSCAGR5y),
		MetricRevenueCAGR3y: cagrMetric(in.Growth.RevenueCAGR3y, in.Growth, cfg.Bands.RevenueCAGR3y),
	}
	out[MetricOperatingMargin] = calcOperatingMargin(in, cfg)
	out[MetricFCFMargin] = calcFCFMargin(in, cfg)

	st := calcStability(in, cfg)
	out[MetricPositiveEPSYears] = st.positiveEPSYears
	out[MetricPositiveFCFYears] = st.positiveFCFYears
	out[MetricEPSVolatility] = st.epsVolatility
	out[MetricFCFVolatility] = st.fcfVolatility

	sv := calcSolvency(in, cfg)
	out[MetricNetDebtToEBITDA] = sv.netDebtToEBITDA
	out[MetricFCFToDebt] = sv.fcfToDebt
	out[MetricInterestCoverage] = sv.interestCoverage
	out[MetricDERatio] = sv.deRatio
	return out
}

// marketCap resolves market_cap = price × shares (A4). An explicit MarketCap
// wins; otherwise the product of the two inputs of the as_of is used. Without
// both, fcf_yield has no denominator and is nil with a reason.
func marketCap(in Inputs) *float64 {
	if finite(in.MarketCap) && *in.MarketCap > 0 {
		return in.MarketCap
	}
	if !finite(in.SharesOutstanding) || *in.SharesOutstanding <= 0 {
		return nil
	}
	if !finiteVal(in.Price) || in.Price <= 0 {
		return nil
	}
	mc := in.Price * *in.SharesOutstanding
	if !finiteVal(mc) || mc <= 0 {
		return nil
	}
	return &mc
}

func finiteVal(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
