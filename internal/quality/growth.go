package quality

// cagrMetric maps one persisted CAGR to a §13 metric (ADR D7).
//
// Three rules, all of them about honesty rather than convenience:
//
//   - a CAGR is READ, never recomputed (the Growth engine owns it);
//   - a nil CAGR, or a growth_confidence of "low", is NOT a valid metric: it is
//     excluded from its sub-block and counted as not valid in quality_coverage,
//     with the single reason growth_unreliable;
//   - a NEGATIVE CAGR is a valid value (AAPL fcf_cagr_3y = −3.9451): shrinking
//     cash generation is a fact about the company, not a missing datum.
func cagrMetric(v *float64, g GrowthInputs, band Band) metricValue {
	if v == nil {
		return mvNil(ReasonGrowthUnreliable)
	}
	if !finite(v) {
		return mvNil(ReasonNonFiniteInput)
	}
	if g.Confidence == "low" {
		return mvNil(ReasonGrowthUnreliable)
	}
	return mv(v, band.Score(*v))
}
