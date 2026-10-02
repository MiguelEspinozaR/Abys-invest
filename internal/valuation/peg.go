package valuation

// CalcPEG computes the ADDITIVE §15 metrics with the INDIVIDUAL growth rate
// (user decision A7 of 2026-09-28, which includes PEG in M6b as a plain
// metric — no scoring dimension, that stays in M6c):
//
//	PEG   = PE / normalized_growth_rate_percentage
//	P/FCF = Market Cap / FCF
//
// §15 is explicit that a PEG with an unreliable growth is nil, NOT 0: when the
// normalised rate does not exist (legacy fallback, A1) both metrics return nil.
// A non-positive EPS/FCF/growth or a missing price also returns nil (§21).
//
// The two numerators are returned separately on purpose: §15 names both, and a
// company with volatile FCF has a meaningful P/FCF even when the PEG is not.
func CalcPEG(in Inputs, gPct float64, growthFallback bool) (peg, pFcf *float64) {
	// P/FCF does not need the growth: it is a pure price/cash-flow ratio.
	if price := sane(in.Price, true); price != nil {
		if shares := sane(in.SharesOutstanding, true); shares != nil {
			if fcf := sane(in.FreeCashFlow, true); fcf != nil {
				marketCap := *price * *shares
				v := marketCap / *fcf
				if isFinite(v) && v > 0 {
					pFcf = &v
				}
			}
		}
	}

	if growthFallback || !isFinite(gPct) || gPct <= 0 {
		return nil, pFcf // §15: unreliable growth ⇒ PEG = nil
	}
	price := sane(in.Price, true)
	eps := sane(in.EPS, true)
	if price == nil || eps == nil {
		return nil, pFcf
	}
	pe := *price / *eps
	if !isFinite(pe) || pe <= 0 {
		return nil, pFcf
	}
	v := pe / gPct
	if !isFinite(v) {
		return nil, pFcf
	}
	return &v, pFcf
}
