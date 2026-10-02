package valuation

// worst returns the least confident of the given levels. Empty strings (a level
// nobody reported) are ignored; with nothing to compare it returns "" so the
// caller can leave the field empty instead of inventing a level.
func worst(levels ...Confidence) Confidence {
	out := Confidence("")
	for _, l := range levels {
		if l == "" {
			continue
		}
		if out == "" || rank(l) > rank(out) {
			out = l
		}
	}
	return out
}

// rank orders the §20 levels (LOWER = MORE confident).
func rank(c Confidence) int {
	switch c {
	case ConfidenceHigh:
		return 0
	case ConfidenceMedium:
		return 1
	case ConfidenceLow:
		return 2
	default:
		return 3
	}
}

// cap returns the LESS confident of two levels (the "ceiling" of §20).
func cap(current, ceiling Confidence) Confidence {
	if ceiling == "" {
		return current
	}
	if current == "" {
		return ceiling
	}
	// A ceiling can only LOWER the level: the least confident of the two wins.
	if rank(ceiling) > rank(current) {
		return ceiling
	}
	return current
}

// IsNoValuation is the global No Valuation test (D10/§21): the valuation is
// unavailable ONLY when NEITHER method has a single value. A company with
// negative FCF but a usable EPS still gets its Graham valuation, with the DCF
// marked unavailable and the confidence degraded — throwing away a valid method
// because the other one cannot be computed would be hiding information (§9).
func IsNoValuation(g, d Method) bool {
	return !g.Available() && !d.Available()
}

// CalcConfidence derives the VALUATION-level confidence of §20 and returns the
// reasons behind it.
//
// User decision A4 (2026-09-28, amends plan ADR D13): the confidence comes from
// the COVERAGE/QUALITY OF THE INPUTS ONLY — valid methods, the source of growth
// and of the discount rate, an unobserved beta, an unknown net debt — never
// from the dispersion. The dispersion is exposed raw and its thresholds belong
// to M6c.
//
// The rules are evaluated in order and each one CAPS the level (the ceiling can
// only lower the result: a low is never resurrected by a high value later on).
//
//  1. no valuation at all (both methods unavailable) → LOW   no_valuation
//  2. legacy growth fallback in use                    → LOW   growth_fallback
//  3. discount rate from configuration / legacy env    → LOW   wacc_configured
//     3'. discount rate degraded to the cost of equity    → MED   wacc_cost_of_equity
//  4. net debt unknown                                → MED   net_debt_unknown
//  5. a method with fewer than 3 scenarios             → MED   incomplete_scenarios
//  6. a single uncertainty component                   → MED   insufficient_components
//  7. worst confidence of the inputs actually used     → min   input_confidence
//
// An unavailable valuation is ALWAYS LOW (§21).
func CalcConfidence(in Inputs, cfg Config, g, d Method, u Uncertainty, rate discountRate, growthFallback bool) (Confidence, []string) {
	var reasons []string
	confidence := ConfidenceHigh

	// 1) No valuation ⇒ LOW (the first rule already fixes the answer).
	if IsNoValuation(g, d) {
		return ConfidenceLow, []string{ReasonNoValuation}
	}

	// 2) The growth came from the legacy global fallback: a 7% that is not this
	// company's growth. The method still produced values (A1) but the
	// confidence cannot be high (§20 "se use fallback").
	if growthFallback {
		confidence = cap(confidence, ConfidenceLow)
		reasons = dedupe(reasons, ReasonGrowthFallback)
	}

	// 3/3') Discount-rate provenance (§7 "no fabricar WACC"; D9).
	switch rate.level {
	case 4:
		confidence = cap(confidence, ConfidenceLow)
		reasons = dedupe(reasons, ReasonWACCLegacyEnv)
	case 3, 0:
		confidence = cap(confidence, ConfidenceLow)
		reasons = dedupe(reasons, ReasonWACCConfigured)
	case 2:
		confidence = cap(confidence, ConfidenceMedium)
		reasons = dedupe(reasons, ReasonWACCCostOfEquity)
	}

	// An observed beta is the difference between an individual WACC and a
	// configuration constant: without it the cost of equity is assumed (§20
	// "WACC dependa de configuración").
	if rate.level == 1 && !in.BetaObserved && (in.WACCSource == WACCSourceCAPMHybrid) {
		confidence = cap(confidence, ConfidenceMedium)
		reasons = dedupe(reasons, ReasonWACCConfigured)
	}

	// 4) Equity after net debt unknown.
	if d.Reasons != nil {
		for _, r := range d.Reasons {
			if r == ReasonNetDebtUnknown {
				confidence = cap(confidence, ConfidenceMedium)
				reasons = dedupe(reasons, r)
			}
		}
	}

	// 5) A method without its three scenarios has no range to speak of.
	for _, m := range []Method{g, d} {
		if m.Available() && m.scenarioCount() < 3 {
			confidence = cap(confidence, ConfidenceMedium)
			reasons = dedupe(reasons, ReasonIncompleteScenarios)
			break
		}
	}

	// 6) A dispersion over a single value is not a dispersion.
	if u.Components < 2 {
		confidence = cap(confidence, ConfidenceMedium)
		reasons = dedupe(reasons, ReasonInsufficientComps)
	}

	// 7) Never above what the inputs that were really used support.
	if !growthFallback && in.GrowthConfidence != "" {
		confidence = cap(confidence, in.GrowthConfidence)
		if in.GrowthConfidence != ConfidenceHigh {
			reasons = dedupe(reasons, ReasonInputConfidence)
		}
	}
	if rate.level == 1 && in.WACCConfidence != "" {
		confidence = cap(confidence, in.WACCConfidence)
		if in.WACCConfidence != ConfidenceHigh {
			reasons = dedupe(reasons, ReasonInputConfidence)
		}
	}
	return confidence, reasons
}
