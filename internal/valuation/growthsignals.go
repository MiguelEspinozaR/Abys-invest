package valuation

import "math"

// divergencePP is the minimum gap, in percentage points, between the EPS CAGR
// and the FCF CAGR for their opposite signs to be considered a MEANINGFUL
// divergence (SPEC abys-valuation-210 C4). Below it the two small rates could be
// noise around zero and the growth is not degraded.
const divergencePP = 20.0

// growthSignalsDiverge reports whether the two growth signals point in opposite
// directions AND differ by more than divergencePP percentage points.
//
// C4 (SPEC abys-valuation-210): EPS growing while FCF shrinks (or the reverse)
// means the normalised growth rate rests on two contradictory series. The
// valuation does NOT change the growth VALUE (that is growth's contract); it only
// lowers the confidence to low and records `growth_signals_diverge`.
//
// A nil rate, or either rate at exactly 0, cannot be "opposite in sign": the
// function returns false and the growth is left alone.
func growthSignalsDiverge(epsCAGR5y, fcfCAGR5y *float64) bool {
	eps := sane(epsCAGR5y, false)
	fcf := sane(fcfCAGR5y, false)
	if eps == nil || fcf == nil {
		return false
	}
	opposite := (*eps > 0 && *fcf < 0) || (*eps < 0 && *fcf > 0)
	if !opposite {
		return false
	}
	return math.Abs(*eps-*fcf) > divergencePP
}
