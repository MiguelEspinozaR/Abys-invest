// Package relative implements the Relative Valuation engine of SPEC v2 §16 (plan
// M6c §B, ADR D8/D9): how a company compares with its own sector AND with its
// own history, using nine metrics, blended 0.6/0.4.
//
// THE central rule of this package is the one the SPEC states twice and that M4b
// violated for years: **a missing datum is never a 50.** With fewer than
// COMPARABLES_MIN_SECURITIES peers, or with no usable metric on either side, the
// result is nil with reason `insufficient_comparables` and confidence low — and
// §18 then renormalises the score without this dimension. A fabricated 50 would
// be a number about companies the system has never seen.
//
// A 50 IS legitimate in one place only, and it is not this one: the per-metric
// sub-score of a value that sits exactly ON its median. There, 50 is the honest
// reading of "no different from the median", not a stand-in for missing data.
//
// Calculate(Inputs, Config) is pure: no clock, no database, no environment and
// no map iteration in the computation path (§27).
package relative

import (
	"fmt"
	"math"
	"time"

	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/reason"
)

// ModelVersion identifies the relative formula revision. 1.0.0 is the FIRST
// version of a NEW engine.
const ModelVersion = "1.0.0"

// DefaultComparablesMinSecurities is COMPARABLES_MIN_SECURITIES: below this many
// peers in the sector there is no sector relative score at all (ADR D8, Az4).
//
// 5 is not lowered for convenience: with the real catalogue (44 sectorised
// securities, 5 in Technology) `relative` is nil for the whole universe, and that
// is the correct answer rather than a comparison against four names.
const DefaultComparablesMinSecurities = 5

// Defaults of the engine (§16).
const (
	DefaultSectorWeight     = 0.6
	DefaultHistoricalWeight = 0.4
	DefaultHistoricalYears  = 5
	DefaultMinMetrics       = 3
	// DefaultAdvantagePct is the symmetric band of the per-metric sub-score: 20%
	// better than the median scores 100, 20% worse scores 0, and the median
	// itself scores 50. It is the M4b rule, kept because it is already what the
	// sector comparison has always meant.
	DefaultAdvantagePct = 0.20
)

// Metric names (§16). The same slugs that derived_metrics and valuation_results
// expose, so the trace of a score and the metrics tables speak one language.
const (
	MetricPE              = "pe_ratio"
	MetricPB              = "pb_ratio"
	MetricPFCF            = "p_fcf"
	MetricEVEBITDA        = "ev_ebitda"
	MetricEVEBIT          = "ev_ebit"
	MetricFCFYield        = "fcf_yield"
	MetricROE             = "roe"
	MetricROIC            = "roic"
	MetricNetDebtToEBITDA = "net_debt_to_ebitda"
)

// Metric directions (§16 + plan B1): for the six multiples a LOWER value is
// better; for the three returns a HIGHER value is better.
var directions = []struct {
	Name          string
	LowerIsBetter bool
}{
	{MetricPE, true},
	{MetricPB, true},
	{MetricPFCF, true},
	{MetricEVEBITDA, true},
	{MetricEVEBIT, true},
	{MetricFCFYield, false},
	{MetricROE, false},
	{MetricROIC, false},
	{MetricNetDebtToEBITDA, true},
}

// MetricNames returns the nine metrics in canonical order (no map iteration).
func MetricNames() []string {
	out := make([]string, 0, len(directions))
	for _, d := range directions {
		out = append(out, d.Name)
	}
	return out
}

// LowerIsBetter reports the direction of one metric.
func LowerIsBetter(name string) bool {
	for _, d := range directions {
		if d.Name == name {
			return d.LowerIsBetter
		}
	}
	return false
}

// Confidence levels (same vocabulary as growth/wacc/valuation/quality).
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Reason codes. Stable strings, persisted and asserted by tests.
const (
	ReasonInsufficientComparables = "insufficient_comparables"
	ReasonOnlySector              = "only_sector"
	ReasonOnlyHistorical          = "only_historical"
	ReasonNoSector                = "no_sector"
	ReasonNoHistoricalMedian      = "no_historical_median"
	ReasonInsufficientMetrics     = "insufficient_metrics"
)

// Provenance of a Result (GET /relative marks `computed` when there is no
// persisted 2.1.0 row to read).
const (
	SourcePersisted = "persisted"
	SourceComputed  = "computed"
)

// Config is the full parameter set of Calculate.
type Config struct {
	SectorWeight     float64 `json:"sector_weight"`
	HistoricalWeight float64 `json:"historical_weight"`
	HistoricalYears  int     `json:"historical_years"`
	MinMetrics       int     `json:"min_metrics"`
	MinSecurities    int     `json:"min_securities"`
	AdvantagePct     float64 `json:"advantage_pct"`
}

// DefaultConfig is the deterministic default configuration.
func DefaultConfig() Config {
	return Config{
		SectorWeight:     DefaultSectorWeight,
		HistoricalWeight: DefaultHistoricalWeight,
		HistoricalYears:  DefaultHistoricalYears,
		MinMetrics:       DefaultMinMetrics,
		MinSecurities:    DefaultComparablesMinSecurities,
		AdvantagePct:     DefaultAdvantagePct,
	}
}

// ConfigFromEnv reads the RELATIVE_* / COMPARABLES_* parameters with the shared
// validating helpers (ADR D20). It is the only function that reads the env.
//
//	RELATIVE_SECTOR_WEIGHT        0.6
//	RELATIVE_HISTORICAL_WEIGHT    0.4
//	RELATIVE_HISTORICAL_YEARS     5
//	RELATIVE_MIN_METRICS          3
//	COMPARABLES_MIN_SECURITIES    5   (reused, not duplicated)
//	RELATIVE_ADVANTAGE_PCT        0.20
func ConfigFromEnv() Config {
	cfg := DefaultConfig()
	cfg.SectorWeight = envFloatRange("RELATIVE_SECTOR_WEIGHT", cfg.SectorWeight, 0, 1)
	cfg.HistoricalWeight = envFloatRange("RELATIVE_HISTORICAL_WEIGHT", cfg.HistoricalWeight, 0, 1)
	cfg.HistoricalYears = envIntRange("RELATIVE_HISTORICAL_YEARS", cfg.HistoricalYears, 1, 50)
	cfg.MinMetrics = envIntRange("RELATIVE_MIN_METRICS", cfg.MinMetrics, 1, 9)
	cfg.MinSecurities = envIntRange("COMPARABLES_MIN_SECURITIES", cfg.MinSecurities, 1, 1000)
	cfg.AdvantagePct = envFloatRange("RELATIVE_ADVANTAGE_PCT", cfg.AdvantagePct, 0.01, 1)
	return cfg
}

// Inputs is the full computational input set. Every pointer is nil when the
// datum is missing, and a missing median is nil too — never the value itself
// with an invented denominator.
type Inputs struct {
	Ticker string    `json:"ticker,omitempty"`
	AsOf   time.Time `json:"as_of"`

	// Metrics are the nine §16 values of THIS security at as_of, keyed by metric
	// name. p_fcf comes from valuation_results 2.0.0 (ADR D9: CalcPEG is reused,
	// not duplicated) and roic/ev_ebitda/ev_ebit/net_debt_to_ebitda from
	// derived_metrics 2.0.0.
	Metrics map[string]*float64 `json:"metrics,omitempty"`
	// SectorMedian is the median of the sector peers, FILTERED BY MODEL VERSION
	// (ADR D13/D8). Only usable when SectorCount >= MinSecurities.
	SectorMedian map[string]*float64 `json:"sector_median,omitempty"`
	// SectorCount is the number of active securities sharing the sector.
	SectorCount int `json:"sector_count"`
	// HistoricalMedian is the median of the security's own last
	// HistoricalYears as_of, also filtered by model version.
	HistoricalMedian map[string]*float64 `json:"historical_median,omitempty"`
	// HistoricalAsOfCount is how many distinct as_of the historical median was
	// computed over. Zero means "no history at all".
	HistoricalAsOfCount int `json:"historical_as_of_count"`
}

// MetricSubScore is one metric compared against one median.
type MetricSubScore struct {
	Name   string   `json:"name"`
	Value  *float64 `json:"value"`
	Median *float64 `json:"median"`
	Score  *float64 `json:"score"`
	Reason string   `json:"reason,omitempty"`
}

// SideScore is one of the two comparisons of §16.
type SideScore struct {
	// Name is "sector" or "historical".
	Name     string           `json:"name"`
	Score    *float64         `json:"score"`
	Coverage float64          `json:"coverage"`
	Metrics  []MetricSubScore `json:"metrics"`
}

// Result is the deterministic output of Calculate.
type Result struct {
	// Score is nil when the sector has too few peers AND the history has no usable
	// median — never 50 (ADR D8, CA-M6c-7).
	Score *float64 `json:"score"`
	// SectorScore and HistoricalScore are exposed separately because §16 asks for
	// them separated: the disagreement between "cheap for its sector" and "cheap
	// for itself" is information.
	SectorScore     *float64 `json:"sector_score"`
	HistoricalScore *float64 `json:"historical_score"`
	// Coverage is usable metrics over the nine (§23 style coverage, so that a low
	// coverage is visible next to the score it qualifies).
	Coverage   float64 `json:"coverage"`
	Confidence string  `json:"confidence"`
	// PeerCount is the sector evidence behind the sector side (nil side → 0).
	PeerCount    int              `json:"peer_count"`
	Metrics      []MetricSubScore `json:"metrics"`
	Sides        []SideScore      `json:"sides"`
	Reasons      []string         `json:"reasons,omitempty"`
	AvailableAt  time.Time        `json:"available_at"`
	ModelVersion string           `json:"model_version"`
	// Source is "computed" for an on-the-fly result (GET /relative without a
	// persisted score row).
	Source string `json:"source,omitempty"`
}

// Calculate blends the sector and historical comparisons of §16.
//
// Decision table (ADR D8/B3), in this order:
//
//  1. sector usable?      = SectorCount >= MinSecurities AND at least one metric
//     has BOTH a value and a median.
//  2. historical usable?  = HistoricalAsOfCount >= 1 AND at least one metric has
//     BOTH a value and a median.
//  3. both    → 0.6×sector + 0.4×historical, confidence medium/high by coverage.
//  4. one     → that one alone, confidence DROPS one level, reason only_sector /
//     only_historical.
//  5. neither → nil, confidence low, reason insufficient_comparables.
func Calculate(in Inputs, cfg Config) Result {
	res := Result{
		Confidence:   ConfidenceLow,
		AvailableAt:  in.AsOf,
		ModelVersion: ModelVersion,
		Source:       SourceComputed,
	}
	if cfg.MinSecurities <= 0 {
		cfg.MinSecurities = DefaultComparablesMinSecurities
	}
	if cfg.MinMetrics <= 0 {
		cfg.MinMetrics = DefaultMinMetrics
	}
	if cfg.AdvantagePct <= 0 || cfg.AdvantagePct > 1 {
		cfg.AdvantagePct = DefaultAdvantagePct
	}

	// Sector side: peer count first (ADR D8). Without the threshold the median of
	// four companies would be presented with the same authority as the median of
	// forty.
	var reasons []string
	var sectorMedian map[string]*float64
	if in.SectorCount < cfg.MinSecurities {
		// The median of four peers is DISCARDED, not merely flagged: exposing it
		// next to the value would let a consumer read "we know its sector" out of a
		// sample too small to support the claim (ADR D8).
		if in.SectorCount == 0 {
			reasons = append(reasons, ReasonNoSector)
		}
	} else {
		res.PeerCount = in.SectorCount
		sectorMedian = in.SectorMedian
	}

	sector := sideScore("sector", in.Metrics, sectorMedian, cfg)
	historical := sideScore("historical", in.Metrics, in.HistoricalMedian, cfg)
	if in.HistoricalAsOfCount == 0 {
		reasons = append(reasons, ReasonNoHistoricalMedian)
	}
	res.Sides = []SideScore{sector, historical}
	res.Metrics = mergedMetrics(in.Metrics, sectorMedian, in.HistoricalMedian, cfg)

	usable := 0
	for _, m := range res.Metrics {
		if m.Value != nil {
			usable++
		}
	}
	res.Coverage = float64(usable) / float64(len(directions))

	switch {
	case sector.Score != nil && historical.Score != nil:
		res.Score = valid(*sector.Score*cfg.SectorWeight + *historical.Score*cfg.HistoricalWeight)
		res.SectorScore, res.HistoricalScore = sector.Score, historical.Score
		res.Confidence = confidenceFor(res.Coverage)
	case sector.Score != nil:
		res.Score = sector.Score
		res.SectorScore = sector.Score
		res.Confidence = drop(confidenceFor(res.Coverage))
		reasons = append(reasons, ReasonOnlySector)
	case historical.Score != nil:
		res.Score = historical.Score
		res.HistoricalScore = historical.Score
		res.Confidence = drop(confidenceFor(res.Coverage))
		reasons = append(reasons, ReasonOnlyHistorical)
	default:
		// El caso que M4b convertía en 50 (scoreComparables → return 50).
		res.Score = nil
		res.Confidence = ConfidenceLow
		reasons = append(reasons, ReasonInsufficientComparables)
		if sector.Coverage > 0 && historical.Coverage == 0 {
			reasons = append(reasons, ReasonInsufficientMetrics)
		}
	}

	res.Reasons = reason.Normalize(reasons)
	return res
}

// sideScore averages the metrics that have BOTH a value and a median on that
// side. A side with fewer than MinMetrics usable metrics is nil: one lucky metric
// is not a comparison.
func sideScore(name string, values, medians map[string]*float64, cfg Config) SideScore {
	out := SideScore{Name: name}
	var sum float64
	n := 0
	for _, d := range directions {
		v := lookup(values, d.Name)
		m := lookup(medians, d.Name)
		s := relativeScore(v, m, d.LowerIsBetter, cfg.AdvantagePct)
		if s != nil {
			sum += *s
			n++
		}
		out.Metrics = append(out.Metrics, MetricSubScore{Name: d.Name, Value: v, Median: m, Score: s})
	}
	out.Coverage = float64(n) / float64(len(directions))
	if n >= cfg.MinMetrics {
		out.Score = valid(sum / float64(n))
	}
	return out
}

// mergedMetrics is the per-metric view exposed by GET /relative: the value and
// BOTH medians side by side, with the sector sub-score as the comparable one
// (the historical one is in Sides).
func mergedMetrics(values, sectorMedian, historicalMedian map[string]*float64, cfg Config) []MetricSubScore {
	out := make([]MetricSubScore, 0, len(directions))
	for _, d := range directions {
		v := lookup(values, d.Name)
		m := lookup(sectorMedian, d.Name)
		out = append(out, MetricSubScore{
			Name: d.Name, Value: v, Median: m,
			Score: relativeScore(v, m, d.LowerIsBetter, cfg.AdvantagePct),
		})
	}
	return out
}

// relativeScore is the M4b rule, kept verbatim in spirit: a value 20% better
// than the median scores 100, the median itself scores 50, a value 20% worse
// scores 0, and the rest is linear and clamped.
//
// The 50 here is the median, NOT a missing datum: a nil value, a non-positive
// value, a nil median or a non-positive median all return nil. That distinction
// is the whole difference between §16 and the M4b neutral.
func relativeScore(value, median *float64, lowerIsBetter bool, advantage float64) *float64 {
	if !ok(value) || !ok(median) {
		return nil
	}
	if *value <= 0 || *median <= 0 {
		return nil
	}
	diff := (*value - *median) / math.Abs(*median)
	slope := 50 / advantage // 250 with advantage = 0.20
	var out float64
	if lowerIsBetter {
		out = 50 - slope*diff
	} else {
		out = 50 + slope*diff
	}
	if out < 0 {
		out = 0
	}
	if out > 100 {
		out = 100
	}
	return &out
}

func ok(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0)
}

func lookup(m map[string]*float64, name string) *float64 {
	if m == nil {
		return nil
	}
	return m[name]
}

func valid(v float64) *float64 { return &v }

// confidenceFor maps the coverage of usable metrics to a confidence. It starts
// at MEDIUM on purpose: relative valuation is a comparison against a median, and
// a median is a much weaker claim than an observed accounting fact.
func confidenceFor(coverage float64) string {
	switch {
	case coverage >= 0.8:
		return ConfidenceHigh
	case coverage >= 1.0/3.0:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

// drop lowers one confidence level (high→medium→low) for a result built on ONE
// side only (§16 "si solo existe uno, utilizarlo y reducir confidence").
func drop(c string) string {
	switch c {
	case ConfidenceHigh:
		return ConfidenceMedium
	case ConfidenceMedium:
		return ConfidenceLow
	default:
		return ConfidenceLow
	}
}

// ConfigFromModelConfig builds a relative.Config from a resolved ModelConfig.
// This is the path used by the pipeline so that parameter set overrides (e.g.
// relative_sector_weight, COMPARABLES_MIN_SECURITIES, RELATIVE_ADVANTAGE_PCT
// from the parameter set) actually reach the engine. Precedence: code defaults < env < parameter set (via ModelConfig).
func ConfigFromModelConfig(mc modelcfg.ModelConfig) Config {
	// Start from env-applied config (code < env), then apply parameter set overrides from mc
	cfg := ConfigFromEnv()
	cfg.SectorWeight = mc.RelativeSectorWeight
	cfg.HistoricalWeight = mc.RelativeHistoricalWeight
	cfg.HistoricalYears = mc.RelativeHistoricalYears
	cfg.MinMetrics = mc.RelativeMinMetrics
	cfg.MinSecurities = mc.ComparablesMinSecurities // COMPARABLES_MIN_SECURITIES from ModelConfig (P1-3)
	cfg.AdvantagePct = mc.RelativeAdvantagePct      // RELATIVE_ADVANTAGE_PCT from ModelConfig (P1-3)
	return cfg
}

// Validate is the local barrier of the engine.
func (c Config) Validate() error {
	if c.SectorWeight < 0 || c.HistoricalWeight < 0 {
		return fmt.Errorf("relative: pesos negativos (%v/%v)", c.SectorWeight, c.HistoricalWeight)
	}
	if math.Abs(c.SectorWeight+c.HistoricalWeight-1) > 1e-9 {
		return fmt.Errorf("relative: la mezcla suma %.12f, debe sumar 1 (§16)", c.SectorWeight+c.HistoricalWeight)
	}
	if c.HistoricalYears < 1 || c.HistoricalYears > 50 {
		return fmt.Errorf("relative: historical_years %d fuera de [1,50]", c.HistoricalYears)
	}
	if c.MinMetrics < 1 || c.MinMetrics > len(directions) {
		return fmt.Errorf("relative: min_metrics %d fuera de [1,%d]", c.MinMetrics, len(directions))
	}
	if c.MinSecurities < 1 {
		return fmt.Errorf("relative: min_securities %d debe ser >= 1", c.MinSecurities)
	}
	if c.AdvantagePct <= 0 || c.AdvantagePct > 1 {
		return fmt.Errorf("relative: advantage_pct %v fuera de (0,1]", c.AdvantagePct)
	}
	return nil
}

// env helpers delegate to the shared validating ones (ADR D20) so that the env
// contract of this engine is identical to the rest of the project.
func envFloatRange(key string, def, lo, hi float64) float64 {
	return modelcfg.EnvFloatRange(key, def, lo, hi)
}

func envIntRange(key string, def, lo, hi int) int {
	return modelcfg.EnvIntRange(key, def, lo, hi)
}
