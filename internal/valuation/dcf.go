package valuation

import "math"

// CalcDCFIntrinsic implements the simplified DCF (plan D3):
//
//  1. Project FCF for 'horizon' years at 'growthRate' (yearly growth).
//  2. Terminal value via the growing perpetuity:
//     FCF_final × (1 + g_terminal) / (WACC − g_terminal).
//  3. Discount every cash flow (explicit + terminal) to present at 'wacc'.
//  4. Subtract net debt.
//  5. Divide by shares outstanding → intrinsic value per share.
//
// growthRate, wacc and terminalGrowth are in percent (e.g. 10 = 10%).
// Conservative rule: returns nil when FCF is nil/<= 0, shares is nil/<= 0,
// netDebt is missing (nil), WACC <= terminalGrowth, or any parameter is not a
// valid finite number.
func CalcDCFIntrinsic(fcf *float64, growthRate, wacc, terminalGrowth float64, horizon int, shares, netDebt *float64) *float64 {
	if growthRate <= 0 {
		growthRate = defaultGrowth
	}
	if wacc <= 0 {
		wacc = 10 // DCF_DISCOUNT_RATE default
	}
	if stride(&growthRate) || stride(&wacc) || stride(&terminalGrowth) {
		return nil
	}
	if horizon <= 0 {
		horizon = 5 // DCF_HORIZON_YEARS default
	}
	if wacc <= terminalGrowth {
		return nil // perpetuity degenerada
	}

	f := sane(fcf, true)
	sh := sane(shares, true)
	nd := sane(netDebt, false)
	if f == nil || sh == nil || nd == nil {
		return nil
	}

	g := growthRate / 100
	dr := wacc / 100
	gt := terminalGrowth / 100

	// 1) Explicit projection + 3) discounting.
	pv := 0.0
	fcfFinal := *f
	for t := 1; t <= horizon; t++ {
		fcfFinal = *f * math.Pow(1+g, float64(t))
		pv += fcfFinal / math.Pow(1+dr, float64(t))
	}

	// 2) Growing perpetuity at the end of the horizon, discounted back.
	terminal := fcfFinal * (1 + gt) / (dr - gt)
	pv += terminal / math.Pow(1+dr, float64(horizon))

	// 4) − net debt; 5) per share.
	equity := pv - *nd
	if equity <= 0 {
		return nil // conservador: valor no positivo no es un "valor"
	}
	out := equity / *sh
	return &out
}

// stride reports whether the pointer value is NaN/Inf.
func stride(v *float64) bool {
	return v == nil || math.IsNaN(*v) || math.IsInf(*v, 0)
}
