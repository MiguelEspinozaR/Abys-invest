// Package wacc implements the per-security CAPM WACC of SPEC v2 §7.
//
// Determinism (§27): Calculate is a pure function of (Inputs, Config).
//
// Conservative rule (§7): a WACC is never fabricated. Every parameter records
// its provenance. Per-company inputs (E = price x shares, D = total_debt and
// beta from Yahoo defaultKeyStatistics, plan D17) are "observed"; the
// macro/discount inputs (Rf, ERP, Kd, tax) come from Config in M6a because no
// provider exists for them. The wacc_source taxonomy makes that mix explicit:
//
//	capm_individual    / high    → everything observed (ceiling of the taxonomy)
//	capm_hybrid        / medium  → beta and E/D observed, Rf/ERP/Kd/tax configured
//	                                 (the M6a reality for almost every company)
//	configured_fallback/ low     → no observed beta or no E/D → Config.Fallback
//
// All rates are PERCENTAGES (TaxRate: 21 means 21%), like DCF_DISCOUNT_RATE and
// growth.Config.
package wacc

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/reason"
)

// ModelVersion is the revision of the WACC formula persisted in
// wacc_metrics.model_version (1.0.0 in M6a; the 2.0.0 bump is M6b/M6c, ADR D3).
const ModelVersion = "1.0.0"

// WACC sources (SPEC §7, plan D7).
const (
	SourceCAPMIndividual     = "capm_individual"     // beta, E/D and Rf/ERP/Kd/tax observed
	SourceCAPMHybrid         = "capm_hybrid"         // beta and E/D observed; Rf/ERP/Kd/tax from Config
	SourceConfiguredFallback = "configured_fallback" // no observed beta or no E/D
)

// Confidence levels of the WACC (same shape as wacc_source, strict rule §7).
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// MaxTaxRate is the ceiling of the tax rate used in Kd_after_tax = Kd x
// (1 - tax): a 80% "tax" in a raw XBRL fact would be a unit error, not a rate.
const MaxTaxRate = 50.0

// Config holds every parameter of the engine (§28: they must be variables
// afterwards). Rates are PERCENTAGES.
type Config struct {
	RiskFreeRate      float64 `json:"risk_free_rate"`      // Rf %
	EquityRiskPremium float64 `json:"equity_risk_premium"` // ERP % (sin proveedor: siempre config)
	BetaAssumed       float64 `json:"beta_assumed"`        // β usada SOLO si no hay beta observada
	CostOfDebt        float64 `json:"cost_of_debt"`        // Kd pre-tax %
	TaxRate           float64 `json:"tax_rate"`            // % en [0, MaxTaxRate]
	Fallback          float64 `json:"fallback"`            // WACC global, % (<= 0 -> nil)
}

// Inputs are the per-company inputs of the CAPM. A nil pointer means "not
// observed": the value comes from Config (and the source is downgraded
// accordingly). Ticker/AsOf are traceability only.
type Inputs struct {
	Ticker      string    `json:"ticker"`
	AsOf        time.Time `json:"as_of"`
	EquityValue *float64  `json:"equity_value"` // observado: E = valuation_price x shares
	DebtValue   *float64  `json:"debt_value"`   // observado: D = total_debt
	Beta        *float64  `json:"beta"`         // observado: beta_history (ADR D29)
	// BetaAsOf is the date of the beta OBSERVATION (not the date it was fetched).
	// BetaSource is "history" when it came from beta_history and "configured" when
	// Inputs.Beta is nil. Both travel to the result so the taxonomy can tell an
	// observed beta from an assumed one without re-reading the database.
	BetaAsOf          *time.Time `json:"beta_as_of,omitempty"`
	BetaSource        string     `json:"beta_source,omitempty"` // history|configured
	RiskFreeRate      *float64   `json:"risk_free_rate"`        // nil -> Config (hoy siempre nil)
	EquityRiskPremium *float64   `json:"equity_risk_premium"`   // nil -> Config
	CostOfDebt        *float64   `json:"cost_of_debt"`          // nil -> Config
	TaxRate           *float64   `json:"tax_rate"`              // nil -> Config

	// Reasons are the reasons the CALLER already knows about the inputs (the beta
	// reader reports `beta_missing` / `beta_stale`, ADR D29). The engine does not
	// invent them: it normalises and propagates them into the result and the
	// snapshot, so a reason travels with the number it explains.
	Reasons []string `json:"reasons,omitempty"`
}

// Beta provenance values (ADR D29).
const (
	BetaSourceHistory    = "history"
	BetaSourceConfigured = "configured"
)

// Result is the deterministic output of Calculate. WACC is nil when the
// conservative rule forbids inventing it (no observed beta/E-D AND no positive
// configured fallback). The other components are kept even when the result is
// the fallback: they are the evidence of WHY it degraded (§7, plan D8).
type Result struct {
	Ke         *float64 `json:"cost_of_equity,omitempty"`         // Rf + beta*ERP
	KdAfterTax *float64 `json:"cost_of_debt_after_tax,omitempty"` // Kd x (1 - tax/100)
	WACC       *float64 `json:"wacc,omitempty"`

	WeightEquity *float64 `json:"weight_equity,omitempty"`
	WeightDebt   *float64 `json:"weight_debt,omitempty"`

	Beta         *float64 `json:"beta,omitempty"`  // beta efectivamente usada
	BetaObserved bool     `json:"beta_observed"`   // true = observada en Yahoo, no de Config
	Source       string   `json:"wacc_source"`     // capm_individual|capm_hybrid|configured_fallback
	Confidence   string   `json:"wacc_confidence"` // high|medium|low
	ModelVersion string   `json:"model_version"`

	// BetaAsOf and BetaSource are the provenance of the beta used: the observation
	// date and where it came from. They are reported even when the WACC degraded
	// to the fallback, because "why is this a fallback" is the answer a reader of
	// wacc_metrics needs (ADR D29: beta_stale / beta_missing).
	BetaAsOf   *time.Time `json:"beta_as_of,omitempty"`
	BetaSource string     `json:"beta_source,omitempty"`

	// Reasons carry `beta_missing` / `beta_stale` (ADR D29) through the engine.
	Reasons []string `json:"reasons,omitempty"`

	// Resolved (observed OR configured) values of the four macro/discount
	// parameters, so the persistence layer can record the provenance of each one
	// without re-deriving the resolution rule outside the engine.
	RiskFreeRate      *float64 `json:"resolved_risk_free_rate,omitempty"`
	EquityRiskPremium *float64 `json:"resolved_equity_risk_premium,omitempty"`
	CostOfDebt        *float64 `json:"resolved_cost_of_debt,omitempty"`
	TaxRate           *float64 `json:"resolved_tax_rate,omitempty"`
}

// Calculate is the CAPM engine: Ke = Rf + beta x ERP, Kd_after_tax = Kd x
// (1 - tax) and WACC = wE x Ke + wD x Kd_after_tax, with E = equity value and
// D = debt value of the security.
//
// The provenance is resolved first (nil in Inputs ⇒ value from Config), then
// the CAPM is validated; anything that is not calculable degrades to the
// configured fallback instead of producing a NaN.
func Calculate(in Inputs, cfg Config) Result {
	res := Result{ModelVersion: ModelVersion, Reasons: reason.Normalize(in.Reasons)}

	betaObserved := usableBeta(in.Beta)
	beta := cfg.BetaAssumed
	if betaObserved {
		beta = *in.Beta
	}
	res.Beta, res.BetaObserved = ptr(beta), betaObserved
	// Provenance of the beta. An assumed beta is reported as "configured" with no
	// date: there is no observation to date.
	res.BetaSource = BetaSourceConfigured
	if betaObserved {
		res.BetaSource = BetaSourceHistory
		res.BetaAsOf = in.BetaAsOf
	}

	rf, rfObserved := observedOr(in.RiskFreeRate, cfg.RiskFreeRate)
	erp, erpObserved := observedOr(in.EquityRiskPremium, cfg.EquityRiskPremium)
	kd, kdObserved := observedOr(in.CostOfDebt, cfg.CostOfDebt)
	tax, taxObserved := observedOr(in.TaxRate, cfg.TaxRate)
	tax = clamp(tax, 0, MaxTaxRate)
	res.RiskFreeRate, res.EquityRiskPremium = ptr(rf), ptr(erp)
	res.CostOfDebt, res.TaxRate = ptr(kd), ptr(tax)

	// 1) CAPM validation: a non-finite input or a non-positive cost of equity
	// makes the WACC uncomputable, never NaN.
	ke := rf + beta*erp
	if !finite(rf) || !finite(erp) || !finite(beta) || !finite(kd) || !finite(tax) || !finite(ke) || ke <= 0 {
		return fallback(res, cfg)
	}
	res.Ke = ptr(ke)
	kdAfterTax := kd * (1 - tax/100)
	if !finite(kdAfterTax) {
		return fallback(res, cfg)
	}
	res.KdAfterTax = ptr(kdAfterTax)

	// 2) Capital structure: both E and D observed and E+D > 0. Debt <= 0 means
	// all-equity (wD = 0, WACC = Ke) and is NOT a degradation: it is an
	// observed structure.
	//
	// The observed beta is a REQUIREMENT of the CAPM path (plan D17 / CA-3): a
	// CAPM over Config.BetaAssumed would be a per-company-looking number built
	// on an invented input, which is exactly what the taxonomy must not do. With
	// no observed beta the security degrades to the marked configured_fallback
	// (res.Beta still reports the assumed beta as evidence of why).
	e, eUsable := usableEquity(in.EquityValue)
	d, dObserved := observedDebt(in.DebtValue)
	if eUsable && dObserved && e+d > 0 && betaObserved {
		debt := math.Max(d, 0) // negative debt is not a capital structure
		wE := e / (e + debt)
		wD := debt / (e + debt)
		res.WeightEquity, res.WeightDebt = ptr(wE), ptr(wD)
		res.WACC = ptr(wE*ke + wD*kdAfterTax)

		// 3) Taxonomy: the ceiling requires EVERY parameter observed.
		if rfObserved && erpObserved && kdObserved && taxObserved {
			res.Source, res.Confidence = SourceCAPMIndividual, ConfidenceHigh
		} else {
			res.Source, res.Confidence = SourceCAPMHybrid, ConfidenceMedium
		}
		return res
	}

	// No observed beta or no observed capital structure: the WACC would be a
	// global number pretending to be per-company, so it degrades to the marked
	// fallback.
	return fallback(res, cfg)
}

// fallback applies the configured global WACC (plan D8): with a positive
// Fallback it is used and source/confidence are FORCED to
// configured_fallback/low; with Fallback <= 0 the result is nil rather than
// invented. The already computed Ke, weights and beta are preserved as
// evidence of why it degraded.
func fallback(res Result, cfg Config) Result {
	res.Source, res.Confidence = SourceConfiguredFallback, ConfidenceLow
	if cfg.Fallback > 0 {
		res.WACC = ptr(cfg.Fallback)
	}
	return res
}

// Snapshot serializes inputs + config + result into the JSONB persisted in
// wacc_metrics.inputs_snapshot (§26/§30).
func (r Result) Snapshot(in Inputs, cfg Config) ([]byte, error) {
	raw, err := json.Marshal(struct {
		Inputs       Inputs `json:"inputs"`
		Config       Config `json:"config"`
		Result       Result `json:"result"`
		ModelVersion string `json:"model_version"`
	}{Inputs: in, Config: cfg, Result: r, ModelVersion: ModelVersion})
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// SnapshotData is the structure used for JSON unmarshaling of a WACC snapshot.
type SnapshotData struct {
	Inputs       Inputs `json:"inputs"`
	Config       Config `json:"config"`
	Result       Result `json:"result"`
	ModelVersion string `json:"model_version"`
}

// ParseSnapshot decodes a WACC snapshot and validates it has the required
// fields for replay (ADR D27, B12). It is tolerant of unknown fields but
// strict on required fields.
func ParseSnapshot(raw []byte) (Inputs, Config, Result, error) {
	if len(raw) == 0 {
		return Inputs{}, Config{}, Result{}, fmt.Errorf("%w: snapshot vacío", modelcfg.ErrSnapshotIncomplete)
	}
	var probe struct {
		ModelVersion string `json:"model_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return Inputs{}, Config{}, Result{}, fmt.Errorf("wacc: parse snapshot: %w", err)
	}
	if strings.TrimSpace(probe.ModelVersion) == "" {
		return Inputs{}, Config{}, Result{}, fmt.Errorf("%w: wacc: falta model_version", modelcfg.ErrSnapshotIncomplete)
	}
	if probe.ModelVersion != ModelVersion {
		return Inputs{}, Config{}, Result{}, fmt.Errorf("%w: %q (soportada: %q)", modelcfg.ErrUnsupportedModelVersion, probe.ModelVersion, ModelVersion)
	}
	var data SnapshotData
	if err := json.Unmarshal(raw, &data); err != nil {
		return Inputs{}, Config{}, Result{}, fmt.Errorf("wacc: parse snapshot: %w", err)
	}
	if data.Inputs.Ticker == "" {
		return Inputs{}, Config{}, Result{}, fmt.Errorf("%w: wacc: falta ticker", modelcfg.ErrSnapshotIncomplete)
	}
	return data.Inputs, data.Config, data.Result, nil
}

// usableBeta reports whether the security has an OBSERVED, credible beta
// (plan D17): present, finite and positive. 0, NaN, Inf and negatives are
// treated as "not observed", so Config.BetaAssumed is used and the row is
// marked configured_fallback instead of hiding a missing input.
func usableBeta(beta *float64) bool {
	return beta != nil && finite(*beta) && *beta > 0
}

// usableEquity reports whether the market value of equity is observed and
// positive. E <= 0 (or absent) means "not observed": a zero equity value is not
// a capital structure, and turning it into a 100% debt weight would silently
// replace the WACC with Kd_after_tax.
func usableEquity(v *float64) (float64, bool) {
	if v == nil || !finite(*v) || *v <= 0 {
		return 0, false
	}
	return *v, true
}

// observedDebt reports whether the debt amount is observed. D = 0 is a VALID
// observation (all-equity company, WACC = Ke) and a negative value is clamped
// to 0 downstream: only an absent or non-finite debt is "not observed".
func observedDebt(v *float64) (float64, bool) {
	if v == nil || !finite(*v) {
		return 0, false
	}
	return *v, true
}

// observedOr resolves the provenance of one parameter: a non-nil, finite value
// in Inputs is observed, otherwise the configured value is used and the second
// return is false.
func observedOr(observed *float64, configured float64) (float64, bool) {
	if observed != nil && finite(*observed) {
		return *observed, true
	}
	return configured, false
}

func clamp(v, lo, hi float64) float64 {
	if !finite(v) {
		return lo
	}
	return math.Max(lo, math.Min(hi, v))
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func ptr(v float64) *float64 { return &v }
