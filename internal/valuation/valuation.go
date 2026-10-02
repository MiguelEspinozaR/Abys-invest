// Package valuation implements the valuation engine of SPEC v2 §6-§11 and
// §19-§21 (plan M6b): Graham and DCF as INDEPENDENT methods with three
// scenarios each, Margin of Safety, Valuation Uncertainty, Valuation
// Confidence and the No Valuation policy.
//
// Contract (plan ADR D2/D3/D5, §26/§27):
//
//   - Calculate(Inputs, Config) Result is PURE: no network, no database, no
//     time.Now() (AsOf travels in Inputs), no global state. Same inputs+config
//     ⇒ byte-identical Result.
//   - ConfigFromEnv is the only reader of the environment.
//   - ModelVersion 2.0.0: there is NO consensus_intrinsic any more (SPEC §9,
//     the complete reversal of M4b) and no upside_pct. The divergence between
//     Graham and DCF is information, not something to average away.
//
// Conservative data rule, unchanged since M3 and reinforced by §21: a nil
// value means "insufficient data"; it is NEVER substituted by zero.
package valuation

import (
	"encoding/json"
	"math"
	"time"
)

// ModelVersion identifies the valuation formula revision (persisted as-is).
// 2.0.0 = SPEC v2 §6-§11/§19-§21: scenarios, MOS, uncertainty, confidence and
// the removal of the M4b consensus (a breaking contract change, ADR D3/D5).
const ModelVersion = "2.0.0"

// Status is the availability of a method (or of the valuation as a whole).
type Status string

// The only two statuses of §10/§21.
const (
	StatusAvailable   Status = "available"
	StatusUnavailable Status = "unavailable"
)

// Confidence is the §20 confidence level. Reused verbatim from growth/wacc
// (high|medium|low) — never renamed (handoff M6a).
type Confidence string

// Confidence levels of §20, in decreasing order of trust.
const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// LowConfidenceLabel is the literal string §20 requires to be SHOWN when the
// confidence is low. The API exposes it inside `reasons`/UI label so the
// wording cannot drift between backend and frontend.
const LowConfidenceLabel = "LOW CONFIDENCE"

// Reasons of the No Valuation policy (§21) and of the input degradations
// (D9). They are stable strings: they are persisted, filtered by clients and
// asserted by tests, so they are NOT free text.
const (
	ReasonEPSNonPositive      = "eps_non_positive"
	ReasonGrowthUnavailable   = "growth_unavailable"
	ReasonGrowthNonPositive   = "growth_non_positive"
	ReasonGrowthFallback      = "growth_fallback"
	ReasonFCFNonPositive      = "fcf_non_positive"
	ReasonSharesNonPositive   = "shares_non_positive"
	ReasonInvalidHorizon      = "invalid_horizon"
	ReasonWACCBelowTerminal   = "wacc_below_terminal_growth"
	ReasonNetDebtUnknown      = "net_debt_unknown"
	ReasonNonFinite           = "non_finite"
	ReasonNonPositiveValue    = "non_positive_value"
	ReasonNoPrice             = "no_price"
	ReasonNoValuation         = "no_valuation"
	ReasonIncompleteScenarios = "incomplete_scenarios"
	ReasonInsufficientComps   = "insufficient_components"
	ReasonInputConfidence     = "input_confidence"
	ReasonWACCCostOfEquity    = "wacc_cost_of_equity"
	ReasonWACCConfigured      = "wacc_configured"
	ReasonWACCLegacyEnv       = "wacc_legacy_env"
	ReasonGrowthNotReliable   = "growth_not_reliable"
)

// Discount-rate provenance of the DCF (plan D9). The first two are the M6a
// taxonomy (wacc_metrics.source verbatim); the last three are NEW and only
// exist for the discount rate of a valuation row.
const (
	WACCSourceCAPMIndividual  = "capm_individual"
	WACCSourceCAPMHybrid      = "capm_hybrid"
	WACCSourceCostOfEquity    = "cost_of_equity"
	WACCSourceConfiguredFB    = "configured_fallback"
	WACCSourceLegacyDiscountE = "legacy_discount_env"
)

// Inputs is the full computational input set. Every pointer field is nil when
// the datum is missing; a nil value NEVER means zero (§1/§21 conservative
// rule). Ticker/AsOf are provenance only: the formulas never read them, which
// is what makes the engine reproducible by as_of (§26).
type Inputs struct {
	Ticker string    `json:"ticker,omitempty"`
	AsOf   time.Time `json:"as_of"`

	Price             *float64 `json:"price,omitempty"`          // market close = valuation_price (§22)
	EPS               *float64 `json:"eps,omitempty"`            // FY diluted EPS
	FreeCashFlow      *float64 `json:"free_cash_flow,omitempty"` // FY canonical FCF (OCF+capex fallback)
	SharesOutstanding *float64 `json:"shares_outstanding,omitempty"`
	NetDebt           *float64 `json:"net_debt,omitempty"` // total debt − cash; nil = unknown

	// Growth and WACC come ALREADY PERSISTED from growth_metrics / wacc_metrics
	// (M6a); M6b wires them, it never recomputes them.
	NormalizedGrowthRate *float64   `json:"normalized_growth_rate,omitempty"`
	GrowthConfidence     Confidence `json:"growth_confidence,omitempty"`
	GrowthSource         string     `json:"growth_source,omitempty"`
	GrowthModelVersion   string     `json:"growth_model_version,omitempty"`

	WACC             *float64   `json:"wacc,omitempty"`           // wacc_metrics.wacc
	CostOfEquity     *float64   `json:"cost_of_equity,omitempty"` // wacc_metrics.cost_of_equity
	WACCSource       string     `json:"wacc_source,omitempty"`    // wacc_metrics.source (M6a taxonomy)
	WACCConfidence   Confidence `json:"wacc_confidence,omitempty"`
	WACCModelVersion string     `json:"wacc_model_version,omitempty"`
	BetaObserved     bool       `json:"beta_observed,omitempty"`

	// Enriched by Calculate with the discount rate it REALLY used (D9: "el
	// discount rate nunca es invisible") and its provenance.
	WACCUsed           *float64 `json:"wacc_used,omitempty"`
	DiscountSource     string   `json:"discount_source,omitempty"`      // taxonomy of §WACCSource*
	DiscountReason     string   `json:"discount_reason,omitempty"`      // degradation reason, empty at level 1
	DiscountLevel      int      `json:"discount_level,omitempty"`       // 1..4 of D9
	GrowthFallbackUsed bool     `json:"growth_fallback_used,omitempty"` // legacy 7% (A1)
}

// Method is one valuation method (Graham or DCF) with its three scenarios and
// its OWN status/confidence/reasons (§10, §20). The two methods are fully
// independent: one can be unavailable while the other has values (D10).
type Method struct {
	Status     Status     `json:"status"`
	Bear       *float64   `json:"bear,omitempty"`
	Base       *float64   `json:"base,omitempty"`
	Bull       *float64   `json:"bull,omitempty"`
	Confidence Confidence `json:"confidence,omitempty"`
	Reasons    []string   `json:"reasons,omitempty"`
}

// Available reports whether the method produced at least one value.
func (m Method) Available() bool { return m.Status == StatusAvailable }

// scenarioCount counts the non-nil scenarios of the method.
func (m Method) scenarioCount() int {
	n := 0
	for _, v := range []*float64{m.Bear, m.Base, m.Bull} {
		if v != nil {
			n++
		}
	}
	return n
}

// MarginOfSafety is §11: MOS = 100 × (I − Price) / I for the four valuations
// §11 names. It NEVER changes the intrinsic value, only the score (§11).
type MarginOfSafety struct {
	GrahamBase *float64 `json:"graham_base,omitempty"`
	DCFBear    *float64 `json:"dcf_bear,omitempty"`
	DCFBase    *float64 `json:"dcf_base,omitempty"`
	DCFBull    *float64 `json:"dcf_bull,omitempty"`
	Target     float64  `json:"target_margin_of_safety"` // % from MARGIN_OF_SAFETY
	// Reason is set when the margins could not be computed at all (today only
	// `no_price`). Empty ⇒ the block above is meaningful.
	Reason string `json:"reason,omitempty"`
}

// Uncertainty is §19: mean, POPULATION standard deviation and dispersion
// (std/mean) of the §19 component set. High dispersion communicates
// uncertainty; it is exposed RAW and does NOT degrade the confidence in M6b
// (user decision A4: thresholds belong to M6c).
type Uncertainty struct {
	Dispersion *float64 `json:"dispersion,omitempty"` // std/mean, unitless
	Mean       *float64 `json:"mean,omitempty"`
	StdDev     *float64 `json:"std_dev,omitempty"`
	Components int      `json:"components"` // 0..4
}

// SensitivityPoint is one cell of the §8 WACC × growth grid.
type SensitivityPoint struct {
	Growth  float64 `json:"growth"`   // %
	WACC    float64 `json:"wacc"`     // %
	DCFBase float64 `json:"dcf_base"` // 0 when the cell is not computable
}

// Result IS the §10 grouped valuation block returned by the API as `value`.
// There is no "consensus", no "fair value", no "target price" and no
// recommendation (§9 + ADR D5).
type Result struct {
	Ticker string `json:"ticker,omitempty"`
	AsOf   string `json:"as_of,omitempty"` // YYYY-MM-DD (provenance)
	Status Status `json:"status"`          // global (D10)

	Graham      Method         `json:"graham"`
	DCF         Method         `json:"dcf"`
	MOS         MarginOfSafety `json:"margin_of_safety"`
	Uncertainty Uncertainty    `json:"uncertainty"`
	// Confidence is the VALUATION-level confidence (§20); each method also
	// carries its own. Unavailable ⇒ LOW (A5/§21).
	Confidence  Confidence         `json:"confidence"`
	Reasons     []string           `json:"reasons,omitempty"`
	Sensitivity []SensitivityPoint `json:"sensitivity,omitempty"`

	// PEG and P/FCF are ADDITIVE §15 metrics on the individual growth rate
	// (user decision A7). They are NOT scored here (that is M6c) and never
	// replace a valuation value: nil means "not computable" (§15).
	PEG  *float64 `json:"peg,omitempty"`
	PFcf *float64 `json:"p_fcf,omitempty"`

	Inputs       Inputs `json:"inputs"`
	ModelVersion string `json:"model_version"`
}

// Snapshot is the reproducible record of a calculation (§26): the exact
// inputs, the resolved discount rate and the FULL Config. Without the config a
// persisted row could not be replayed, and §28/§25 need the parameters to be
// visible to move them.
type Snapshot struct {
	ModelVersion string         `json:"model_version"`
	Ticker       string         `json:"ticker,omitempty"`
	AsOf         string         `json:"as_of,omitempty"`
	Inputs       Inputs         `json:"inputs"`
	Config       Config         `json:"config"`
	Graham       Method         `json:"graham"`
	DCF          Method         `json:"dcf"`
	MOS          MarginOfSafety `json:"margin_of_safety"`
	Uncertainty  Uncertainty    `json:"uncertainty"`
	Confidence   Confidence     `json:"confidence"`
	Reasons      []string       `json:"reasons,omitempty"`
}

// SnapshotOf builds the reproducibility record of a calculation from the exact
// inputs and config that produced it.
func SnapshotOf(in Inputs, cfg Config, res Result) Snapshot {
	return Snapshot{
		ModelVersion: ModelVersion,
		Ticker:       in.Ticker,
		AsOf:         in.AsOf.Format("2006-01-02"),
		Inputs:       res.Inputs,
		Config:       cfg,
		Graham:       res.Graham,
		DCF:          res.DCF,
		MOS:          res.MOS,
		Uncertainty:  res.Uncertainty,
		Confidence:   res.Confidence,
		Reasons:      res.Reasons,
	}
}

// MarshalSnapshot is the JSON of SnapshotOf, for inputs_snapshot of
// valuation_results. The Config is embedded, not referenced (§28).
func MarshalSnapshot(in Inputs, cfg Config, res Result) ([]byte, error) {
	return json.Marshal(SnapshotOf(in, cfg, res))
}

// Calculate is the ONLY entry point of the engine: it orchestrates Graham →
// DCF → Margin of Safety → Uncertainty → Confidence and returns the grouped
// result of §10.
//
// It is pure (§27): no time.Now, no network, no database. `in` is taken by
// value, so the enrichment of Inputs (wacc_used/discount_source) stays local to
// the result.
func Calculate(in Inputs, cfg Config) Result {
	// (0) Growth: the normalised rate when it exists, the legacy global one
	// when it does not (decision A1: current M3 behaviour, unmarked).
	gBase, gFallback := resolveGrowth(in, cfg)

	// (1) Discount rate with the 4 precedence levels of D9 (never fabricated).
	rate := resolveDiscountRate(in, cfg)

	// (2) Methods, independent of each other (§9/§10).
	graham := CalcGrahamScenarios(in, cfg, gBase, gFallback)
	dcf := CalcDCFScenarios(in, cfg, gBase, rate)

	// (3) §11 margin of safety for the four valuations §11 names.
	mos := CalcMOS(in, graham, dcf, cfg)

	// (4) §19 uncertainty, exposed raw (A4: no thresholds in M6b).
	unc := CalcUncertainty(graham, dcf)

	// (5) §20/§21 confidence from input coverage/quality ONLY (A4) and the
	// global status of D10.
	conf, reasons := CalcConfidence(in, cfg, graham, dcf, unc, rate, gFallback)
	status := StatusAvailable
	if IsNoValuation(graham, dcf) {
		status = StatusUnavailable
	}

	// (6) §8 sensitivity grid (persisted, reproducible by as_of).
	sens := calcSensitivity(in, cfg, gBase, rate)

	// (7) §15 additive metrics with the individual growth (A7).
	peg, pFcf := CalcPEG(in, gBase, gFallback)

	// (8) Enrich the echoed inputs with the provenance of what was used. A
	// non-resolved rate stays nil (never NaN, never 0): the persistence layer
	// must be able to distinguish "no rate" from "rate = 0".
	out := in
	out.WACCUsed = nil
	if isFinite(rate.rate) {
		used := rate.rate
		out.WACCUsed = &used
	}
	out.DiscountSource = rate.source
	out.DiscountReason = rate.reason
	out.DiscountLevel = rate.level
	out.GrowthFallbackUsed = gFallback

	res := Result{
		Ticker:       in.Ticker,
		AsOf:         in.AsOf.Format("2006-01-02"),
		Status:       status,
		Graham:       graham,
		DCF:          dcf,
		MOS:          mos,
		Uncertainty:  unc,
		Confidence:   conf,
		Reasons:      reasons,
		Sensitivity:  sens,
		PEG:          peg,
		PFcf:         pFcf,
		Inputs:       out,
		ModelVersion: ModelVersion,
	}
	return res
}

// resolveGrowth returns the growth rate in percent to use as g_initial and
// whether the LEGACY fallback was used.
//
// A1 (2026-09-28, amends D7): a missing normalised rate is NOT a
// `unavailable`: the engine falls back to GROWTH_RATE_DEFAULT exactly as M3
// and M6a do, and the fallback is NOT flagged on the method. The valuation
// level still reports it in its reasons because the confidence derives from the
// coverage of the inputs (A4/§20 "se use fallback").
func resolveGrowth(in Inputs, cfg Config) (rate float64, fallbackUsed bool) {
	if in.NormalizedGrowthRate != nil && isFinite(*in.NormalizedGrowthRate) {
		return *in.NormalizedGrowthRate, false
	}
	return cfg.GrowthFallback, true
}

// sane returns v when it is a finite number (and > 0 when positiveOnly), nil
// otherwise. Every formula funnels its result through it: a non-finite result
// is a nil + reason, NEVER a 0 (§21).
func sane(v *float64, positiveOnly bool) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	if positiveOnly && *v <= 0 {
		return nil
	}
	return v
}

// isFinite reports whether v is a usable finite number.
func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// ptr returns a pointer to v (helper for the pure formulas).
func ptr(v float64) *float64 { return &v }

// dedupe appends reason to the slice when it is not already present, keeping
// the first occurrence (stable order → deterministic snapshots).
func dedupe(reasons []string, reason string) []string {
	if reason == "" {
		return reasons
	}
	for _, r := range reasons {
		if r == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

// union merges reason slices in order without duplicates.
func union(dst []string, srcs ...[]string) []string {
	for _, src := range srcs {
		for _, r := range src {
			dst = dedupe(dst, r)
		}
	}
	return dst
}
