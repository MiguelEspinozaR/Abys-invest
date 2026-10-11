package valuation

// grahamValue implements SPEC §6 with the individual growth:
//
//	multiple  = (2 × g) + 8.5
//	intrinsic = EPS × multiple
//
// g is the NORMALISED growth rate in percent (§6: g = normalized_growth_rate
// instead of 7%), which is what makes the valuation independent from a single
// global number. EPS must be > 0 (§6/§21); the caller guarantees it. Returns
// nil when the result is not a finite positive number: never a 0.
func grahamValue(eps, growthPct float64) *float64 {
	multiple := (2*growthPct + 8.5)
	out := multiple * eps
	if !isFinite(out) || out <= 0 {
		return nil
	}
	return &out
}

// CalcGrahamScenarios implements §6 + §8 for Graham: the base scenario uses the
// observed (normalised) growth and the bear/bull scenarios shift g by the
// configured deltas with the SAME EPS.
//
// The §6 no-calculation conditions, each with its own reason (never a zero):
//
//	EPS nil or <= 0                     → unavailable, eps_non_positive
//	no normalised growth (legacy 7, A1) → still computed with cfg.GrowthFallback
//	2g + 8.5 <= 0 in a scenario         → that scenario is nil
//	                                      (graham_growth_out_of_domain)
//	g <= 0 but 2g+8.5 > 0               → computed; growth_non_positive is
//	                                      INFORMATIONAL only (C2)
//	non-finite growth / non-positive    → nil (non_finite / non_positive_value)
//
// A degenerate scenario does NOT invalidate the method: the other two survive
// (D10: per-method availability).
func CalcGrahamScenarios(in Inputs, cfg Config, gBase float64, fallbackUsed bool) Method {
	m := Method{Status: StatusUnavailable}

	if in.EPS == nil || *in.EPS <= 0 || !isFinite(*in.EPS) {
		m.Reasons = []string{ReasonEPSNonPositive}
		return m
	}
	if !isFinite(gBase) {
		m.Reasons = []string{ReasonGrowthUnavailable}
		return m
	}

	scenarios := []struct {
		delta float64
		ptr   **float64
	}{
		{cfg.BearGrowthDelta, &m.Bear},
		{0, &m.Base},
		{cfg.BullGrowthDelta, &m.Bull},
	}
	for _, s := range scenarios {
		g := gBase + s.delta
		if !isFinite(g) {
			m.Reasons = dedupe(m.Reasons, ReasonNonFinite)
			continue
		}
		if g <= 0 {
			// C2: a non-positive growth is no longer a reason to drop the
			// scenario. It is recorded as INFORMATION; the scenario is dropped
			// only when the Graham multiple leaves its domain below.
			m.Reasons = dedupe(m.Reasons, ReasonGrowthNonPositive)
		}
		if 2*g+8.5 <= 0 {
			// The multiple 2g+8.5 is non-positive: the scenario has no economic
			// meaning, so it is dropped (never clipped to an invented value)
			// with its own reason. This is the ONLY Graham domain condition C2
			// keeps.
			m.Reasons = dedupe(m.Reasons, ReasonGrahamGrowthOutOfDomain)
			continue
		}
		v := grahamValue(*in.EPS, g)
		if v == nil {
			m.Reasons = dedupe(m.Reasons, ReasonNonPositiveValue)
			continue
		}
		*s.ptr = v
	}

	if m.scenarioCount() == 0 {
		m.Status = StatusUnavailable
		return m
	}
	m.Status = StatusAvailable
	// Method confidence = confidence of the growth actually used. With the
	// legacy fallback (A1) the growth has NO confidence of its own, so the
	// method reports none instead of inventing one.
	if !fallbackUsed {
		m.Confidence = in.GrowthConfidence
	}
	return m
}
