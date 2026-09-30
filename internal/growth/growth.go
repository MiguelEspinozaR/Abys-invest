// Package growth implements the per-security Growth Engine of SPEC v2 §5.
//
// Determinism (§27): Calculate is a pure function of (Inputs, Config) — no DB,
// no network, no clock, no env, no package state. The conservative rule (§1/§5)
// is the one that matters most here: a nil normalized_growth_rate means
// "insufficient data", never a fabricated 7%.
//
// The annual FY series is filtered in SQL (storage.GetFYAnnualSeries): 'FY' in
// EDGAR also carries quarterly periods, so the engine receives only annual
// points (duration 330-400 days), already cut at filing_date <= as_of and
// deduplicated per (concept, period_end). That is why Calculate never has to
// re-label a point.
package growth

import (
	"encoding/json"
	"math"
	"time"
)

// ModelVersion is the revision of the growth formula persisted in
// growth_metrics.model_version. M6a ships 1.0.0; the coordinated bump to
// 2.0.0 belongs to M6b/M6c (ADR D3) and the UNIQUE (security_id, as_of,
// model_version) key guarantees the new version does not overwrite these rows.
const ModelVersion = "1.0.0"

// Confidence of normalized_growth_rate.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Growth sources: which inputs and which window produced the rate.
const (
	SourceEPSFCF3y         = "eps_fcf_3y"
	SourceEPS3y            = "eps_3y"
	SourceFCF3y            = "fcf_3y"
	SourceEPSFCF5y         = "eps_fcf_5y"
	SourceEPS5y            = "eps_5y"
	SourceFCF5y            = "fcf_5y"
	SourceRevenue3y        = "revenue_3y"
	SourceRevenue5y        = "revenue_5y"
	SourceInsufficientData = "insufficient_data"
)

// Point is one already-filtered annual FY fact.
type Point struct {
	PeriodEnd   time.Time `json:"period_end"`   // end of the fiscal year
	Value       float64   `json:"value"`        // revenue / EPS / FCF
	AvailableAt time.Time `json:"available_at"` // filing_date (traceability §4)
}

// Series holds the three annual series. A nil/empty slice = no usable data
// (conservative rule: a missing series degrades, it does not break).
type Series struct {
	Revenue []Point `json:"revenue,omitempty"`
	EPS     []Point `json:"eps,omitempty"`
	FCF     []Point `json:"fcf,omitempty"`
}

// Inputs is the full computational input of Calculate. Ticker/AsOf are
// traceability only (AsOf is the price date, already applied as the cut-off by
// the loader).
type Inputs struct {
	Ticker string    `json:"ticker"`
	AsOf   time.Time `json:"as_of"`
	Series Series    `json:"series"`
}

// Config holds every parameter of the engine (§28: they must be variables
// afterwards). All rates are PERCENTAGES (12.3 = 12.3%), as in the rest of the
// model; the annual window is expressed in DAYS because a fiscal year is not
// exactly 365 days long.
type Config struct {
	WindowPrimaryYears  int     `json:"window_primary_years"`  // 3
	WindowFallbackYears int     `json:"window_fallback_years"` // 5
	AnnualMinDays       int     `json:"annual_min_days"`       // 330 (filtro anual, D2)
	AnnualMaxDays       int     `json:"annual_max_days"`       // 400
	EPSWeight           float64 `json:"eps_weight"`            // 0.5
	FCFWeight           float64 `json:"fcf_weight"`            // 0.5
	DiscrepancyPP       float64 `json:"discrepancy_pp"`        // 10 (puntos porcentuales)
	MinRate             float64 `json:"min_rate"`              // -10
	MaxRate             float64 `json:"max_rate"`              // 25
	FallbackRate        float64 `json:"fallback_rate"`         // 7 (legacy; solo EffectiveRate)
}

// Result is the deterministic output of Calculate. Every CAGR is nil when the
// data does not allow computing it (§5). NormalizedGrowthRate is nil ONLY when
// there is not enough information, and then Confidence = low and
// Source = SourceInsufficientData.
//
// All rates are PERCENTAGES (12.3 means 12.3%), like metrics.GrowthRate and
// DCF_DISCOUNT_RATE.
type Result struct {
	RevenueCAGR3y        *float64 `json:"revenue_cagr_3y,omitempty"`
	RevenueCAGR5y        *float64 `json:"revenue_cagr_5y,omitempty"`
	EPSCAGR3y            *float64 `json:"eps_cagr_3y,omitempty"`
	EPSCAGR5y            *float64 `json:"eps_cagr_5y,omitempty"`
	FCFCAGR3y            *float64 `json:"fcf_cagr_3y,omitempty"`
	FCFCAGR5y            *float64 `json:"fcf_cagr_5y,omitempty"`
	NormalizedGrowthRate *float64 `json:"normalized_growth_rate,omitempty"` // PORCENT
	Confidence           string   `json:"growth_confidence"`
	Source               string   `json:"growth_source"`
	Clamped              bool     `json:"growth_clamped"`
	RevenueDiscrepancy   bool     `json:"revenue_discrepancy"`
	ModelVersion         string   `json:"model_version"`
}

// CAGR is the compound annual growth rate of final over initial for `years`
// years, in PERCENT. It returns nil (never a NaN or a panic) when years <= 0,
// initial <= 0, final <= 0 or the result is not finite: a CAGR over
// non-positive values is not defined and must not be invented (SPEC §5).
func CAGR(initial, final float64, years int) *float64 {
	if years <= 0 || initial <= 0 || final <= 0 {
		return nil
	}
	ratio := final / initial
	if !finite(ratio) || ratio <= 0 {
		return nil
	}
	rate := (math.Pow(ratio, 1/float64(years)) - 1) * 100
	if !finite(rate) {
		return nil
	}
	return &rate
}

// Calculate is the Growth Engine: six annual CAGRs plus the normalized growth
// rate with its confidence, source and flags (SPEC §5).
//
// Selection order (deterministic, no map iteration):
//  1. EPS and/or FCF over the primary window (3y) → weighted blend with the
//     weights RENORMALIZED over the available series.
//  2. EPS and/or FCF over the fallback window (5y) → same blend.
//  3. Revenue (3y, else 5y) → last resort, confidence forced to low.
//  4. Nothing → nil rate, low confidence, insufficient_data.
func Calculate(in Inputs, cfg Config) Result {
	res := Result{ModelVersion: ModelVersion, Confidence: ConfidenceLow, Source: SourceInsufficientData}

	primary, fallback := cfg.WindowPrimaryYears, cfg.WindowFallbackYears
	epsPrimary, fcfPrimary := pick(in.Series.EPS, primary), pick(in.Series.FCF, primary)
	epsFallback, fcfFallback := pick(in.Series.EPS, fallback), pick(in.Series.FCF, fallback)
	revPrimary, revFallback := pick(in.Series.Revenue, primary), pick(in.Series.Revenue, fallback)

	res.EPSCAGR3y, res.FCFCAGR3y = epsPrimary, fcfPrimary
	res.EPSCAGR5y, res.FCFCAGR5y = epsFallback, fcfFallback
	res.RevenueCAGR3y, res.RevenueCAGR5y = revPrimary, revFallback

	// 1) primary window (3y) with EPS and/or FCF: the strongest evidence of the
	// SPEC (both series in the preferred window) is the only "high" case.
	if epsPrimary != nil || fcfPrimary != nil {
		res.Confidence, res.Source = ConfidenceHigh, SourceEPSFCF3y
		if epsPrimary == nil {
			res.Confidence, res.Source = ConfidenceMedium, singleSource(false, primary)
		} else if fcfPrimary == nil {
			res.Confidence, res.Source = ConfidenceMedium, singleSource(true, primary)
		}
		applyBlend(&res, blendOf(epsPrimary, fcfPrimary, cfg), revPrimary, cfg)
		return res
	}

	// 2) fallback window (5y) with EPS and/or FCF. Only one of the two series in
	// the fallback window is never "high": it is the same evidence as one series
	// in the primary window, taken from an older slice.
	if epsFallback != nil || fcfFallback != nil {
		res.Confidence, res.Source = ConfidenceMedium, SourceEPSFCF5y
		if epsFallback == nil {
			res.Source = singleSource(false, fallback)
		} else if fcfFallback == nil {
			res.Source = singleSource(true, fallback)
		}
		applyBlend(&res, blendOf(epsFallback, fcfFallback, cfg), revFallback, cfg)
		return res
	}

	// 3) revenue as the last resort (never a 7% invented): low confidence, and
	// no discrepancy check (revenue is the source here, not a control).
	if revPrimary != nil {
		res.NormalizedGrowthRate = revPrimary
		res.Source = SourceRevenue3y
		res.Confidence = ConfidenceLow
		return res
	}
	if revFallback != nil {
		res.NormalizedGrowthRate = revFallback
		res.Source = SourceRevenue5y
		res.Confidence = ConfidenceLow
		return res
	}

	// 4) no usable information: nil + low + insufficient_data.
	return res
}

// applyBlend clamps the blend to [MinRate, MaxRate] (marking Clamped) and then
// checks the revenue control on the CLAMPED value, so a cap created by the
// clamp cannot itself raise a discrepancy. Finally it degrades the confidence
// one level per active flag (floor: low). The reason of the degradation is
// visible in the flags and in the snapshot.
func applyBlend(res *Result, blend *float64, revenue *float64, cfg Config) {
	if blend == nil {
		return
	}
	rate := *blend
	if rate < cfg.MinRate {
		rate = cfg.MinRate
		res.Clamped = true
	} else if rate > cfg.MaxRate {
		rate = cfg.MaxRate
		res.Clamped = true
	}
	if revenue != nil && math.Abs(*revenue-rate) > cfg.DiscrepancyPP {
		res.RevenueDiscrepancy = true
	}
	res.NormalizedGrowthRate = &rate
	if res.Clamped || res.RevenueDiscrepancy {
		res.Confidence = degrade(res.Confidence)
	}
}

// blendOf returns the weighted mean of the available CAGRs, RENORMALIZING the
// weights over the series actually present (a company with FCF but no EPS
// series is not punished with half its weight missing). Both nil → nil.
func blendOf(eps, fcf *float64, cfg Config) *float64 {
	var sumWeighted, sumWeights float64
	if eps != nil {
		sumWeighted += *eps * cfg.EPSWeight
		sumWeights += cfg.EPSWeight
	}
	if fcf != nil {
		sumWeighted += *fcf * cfg.FCFWeight
		sumWeights += cfg.FCFWeight
	}
	if sumWeights <= 0 || !finite(sumWeighted) {
		return nil
	}
	return ptr(sumWeighted / sumWeights)
}

// singleSource names the source when only ONE of EPS/FCF is available in the
// given window (`epsAvailable` picks which one).
func singleSource(epsAvailable bool, years int) string {
	fallbackWindow := years == fallbackWindowName
	switch {
	case epsAvailable && fallbackWindow:
		return SourceEPS5y
	case epsAvailable:
		return SourceEPS3y
	case fallbackWindow:
		return SourceFCF5y
	}
	return SourceFCF3y
}

// fallbackWindowName is the window whose persisted source label carries the
// "5y" suffix (Config.WindowFallbackYears, 5 by default). The labels are part
// of the growth_metrics contract, so they are tied to the window, not to the
// order of evaluation.
const fallbackWindowName = 5

// degrade drops one confidence level, with low as the floor.
func degrade(c string) string {
	switch c {
	case ConfidenceHigh:
		return ConfidenceMedium
	case ConfidenceMedium:
		return ConfidenceLow
	}
	return ConfidenceLow
}

// pick returns the CAGR of the last `years+1` points of the series (the exact
// window, never the whole series) or nil when there are not enough points.
func pick(series []Point, years int) *float64 {
	if years <= 0 || len(series) < years+1 {
		return nil
	}
	window := series[len(series)-(years+1):]
	return CAGR(window[0].Value, window[len(window)-1].Value, years)
}

// EffectiveRate is the SINGLE place where the legacy fallback is decided: the
// normalized rate when there is one, otherwise cfg.FallbackRate (the legacy 7%
// of Graham/DCF/PEG). NOT wired into any formula in M6a (ADR D2, phase 1 of 3):
// Graham, DCF, PEG and the score keep using 7% until M6b.
func (r Result) EffectiveRate(cfg Config) float64 {
	if r.NormalizedGrowthRate == nil {
		return cfg.FallbackRate
	}
	return *r.NormalizedGrowthRate
}

// Snapshot serializes inputs + config + result: the JSONB persisted in
// growth_metrics.inputs_snapshot (§26/§30). It contains the annual points
// actually used and every parameter, so a row can be reproduced and audited.
func (r Result) Snapshot(in Inputs, cfg Config) ([]byte, error) {
	raw, err := json.Marshal(struct {
		Ticker       string    `json:"ticker"`
		AsOf         time.Time `json:"as_of"`
		Series       Series    `json:"series"`
		Config       Config    `json:"config"`
		Result       Result    `json:"result"`
		ModelVersion string    `json:"model_version"`
	}{in.Ticker, in.AsOf, in.Series, cfg, r, ModelVersion})
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func ptr(v float64) *float64 { return &v }
