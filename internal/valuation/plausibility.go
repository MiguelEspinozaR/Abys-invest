package valuation

// C1 — plausibility guard (SPEC abys-valuation-210).
//
// A method whose value (or whose inputs) are incoherent by orders of magnitude
// is the shape of a UNIT MISMATCH: a price of one share class compared with the
// EPS of another, a market cap computed with the wrong share count, a FCF in a
// different currency or scale. The guard NEVER fabricates a value: it withdraws
// the method (unavailable, scenarios nil) and reports `unit_mismatch`, and the
// score consumer then excludes the dimension and redistributes its weight into
// `weight_used` (§18 active_weight_sum).
//
// Thresholds (C1):
//
//	P/E < 1                     → the EPS cannot belong to that price
//	MOS > 95 %                  → the intrinsic is beyond any sane range
//	FCF yield > 100 %           → the FCF cannot belong to that market cap
//	intrinsic > 20 × price      → same signal as the MOS, direct and unitless
//
// The four overlap on purpose: each one is the readable form of the same
// incoherence for a different input (EPS, intrinsic, FCF). Any nil input is
// simply not evaluated — a missing datum is not a mismatch.
const (
	PlausibilityMinPE       = 1.0
	PlausibilityMaxMOS      = 95.0
	PlausibilityMaxFCFYield = 1.0 // >100% of the market cap per year
	PlausibilityMaxMultiple = 20.0
)

// checkPlausibility evaluates one method against the C1 thresholds. `value` is
// the method's base intrinsic; `mos` its margin of safety; `pe` and `fcfYield`
// are the ratios of the inputs the method consumes (pe for Graham, fcfYield for
// the DCF). It returns the reasons (today only `unit_mismatch`) or nil.
func checkPlausibility(price, value, mos, pe, fcfYield *float64) []string {
	var reasons []string
	if pe != nil && *pe < PlausibilityMinPE {
		reasons = dedupe(reasons, ReasonUnitMismatch)
	}
	if fcfYield != nil && *fcfYield > PlausibilityMaxFCFYield {
		reasons = dedupe(reasons, ReasonUnitMismatch)
	}
	if mos != nil && *mos > PlausibilityMaxMOS {
		reasons = dedupe(reasons, ReasonUnitMismatch)
	}
	if v := sane(value, true); price != nil && v != nil && isFinite(*price) && *price > 0 {
		if *v > PlausibilityMaxMultiple**price {
			reasons = dedupe(reasons, ReasonUnitMismatch)
		}
	}
	return reasons
}

// plausibilityRatios derives the two input ratios the guard needs from the
// INPUTS (not from the method): P/E for the Graham method and FCF yield for the
// DCF. They are unitless and nil when a required input is missing.
func plausibilityRatios(in Inputs) (pe, fcfYield *float64) {
	p := sane(in.Price, true)
	if p == nil {
		return nil, nil
	}
	if eps := sane(in.EPS, true); eps != nil {
		v := *p / *eps
		if isFinite(v) {
			pe = &v
		}
	}
	sh := sane(in.SharesOutstanding, true)
	fcf := sane(in.FreeCashFlow, true)
	if sh != nil && fcf != nil {
		capVal := *p * *sh
		if capVal > 0 {
			v := *fcf / capVal
			if isFinite(v) {
				fcfYield = &v
			}
		}
	}
	return pe, fcfYield
}

// applyPlausibility withdraws a method whose value or inputs fail the C1
// thresholds. `value` is the method's base scenario and `pe`/`fcfYield` are the
// method-specific input ratios (pass nil for the ones the method does not
// consume). The method keeps its computation reasons and gains `unit_mismatch`;
// the returned issue list lets Calculate surface the reason at the valuation
// level too, so the persisted row and the score justification can cite it.
func applyPlausibility(m Method, price, value *float64, pe, fcfYield *float64) (Method, []string) {
	mos := calcMOS(value, price)
	reasons := checkPlausibility(price, value, mos, pe, fcfYield)
	if len(reasons) == 0 {
		return m, nil
	}
	m.Status = StatusUnavailable
	m.Bear, m.Base, m.Bull = nil, nil, nil
	m.Confidence = ""
	m.Reasons = union(m.Reasons, reasons)
	return m, reasons
}
