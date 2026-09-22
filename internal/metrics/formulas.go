// Package metrics implements the deterministic valuation metrics engine
// (ADR-0004) with the exact formulas of SPEC §13.
//
// Every formula is a pure function: same inputs -> same output. A nil result
// means "insufficient data" (conservative rule: never fabricate a zero).
package metrics

import "math"

// sane returns the pointer if the value is a valid finite number > 0, else nil.
// Denominators must be strictly positive; numerators must be finite.
func sane(v *float64, positiveOnly bool) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return nil
	}
	if positiveOnly && *v <= 0 {
		return nil
	}
	return v
}

// calcRatio returns num/den (as float64) or nil when num is nil/non-finite or
// den is nil, non-finite, or <= 0. percent scales the result by 100 when true.
func calcRatio(num *float64, den *float64, percent bool) *float64 {
	n := sane(num, false)
	d := sane(den, true)
	if n == nil || d == nil {
		return nil
	}
	out := *n / *d
	if percent {
		out *= 100
	}
	return &out
}

// CalcEPS implements §13.2: EPS = Net Earnings / Shares Outstanding.
func CalcEPS(netEarnings, sharesOutstanding *float64) *float64 {
	// Shares must be strictly positive (denominator).
	return calcRatio(netEarnings, sharesOutstanding, false)
}

// CalcPE implements §13.2: P/E = Price / EPS. EPS and Price must both be
// strictly positive (a zero price means "no market data", never a valid 0).
func CalcPE(price, eps *float64) *float64 {
	p := sane(price, true)
	e := sane(eps, true)
	if p == nil || e == nil {
		return nil
	}
	out := *p / *e
	return &out
}

// CalcPB implements §13.2: P/B = Market Cap / Shareholders Equity.
func CalcPB(marketCap float64, shareholdersEquity *float64) *float64 {
	mc := &marketCap
	if sane(mc, true) == nil {
		return nil
	}
	return calcRatio(mc, shareholdersEquity, false)
}

// CalcPCF implements §13.2: P/FCF = Market Cap / Free Cash Flow.
func CalcPCF(marketCap float64, freeCashFlow *float64) *float64 {
	mc := &marketCap
	if sane(mc, true) == nil {
		return nil
	}
	return calcRatio(mc, freeCashFlow, false)
}

// CalcPEG implements §13.2: PEG = P/E / g, with g in percent (e.g. 7 = 7%).
func CalcPEG(peRatio, growthRate *float64) *float64 {
	// g must be strictly positive (denominator); PE must be > 0 too.
	g := sane(growthRate, true)
	pe := sane(peRatio, true)
	if g == nil || pe == nil {
		return nil
	}
	out := *pe / *g
	return &out
}

// CalcROE implements §13.3: ROE = Net Earnings / Shareholders Equity.
func CalcROE(netEarnings, shareholdersEquity *float64) *float64 {
	return calcRatio(netEarnings, shareholdersEquity, false)
}

// CalcDE implements §13.4: D/E = Total Liabilities / Shareholders Equity.
func CalcDE(totalLiabilities, shareholdersEquity *float64) *float64 {
	return calcRatio(totalLiabilities, shareholdersEquity, false)
}

// CalcFCFYield implements §13.2 (as yield): FCF Yield = FCF / Market Cap,
// expressed as a percentage (x100).
func CalcFCFYield(freeCashFlow *float64, marketCap float64) *float64 {
	mc := &marketCap
	if sane(mc, true) == nil {
		return nil
	}
	return calcRatio(freeCashFlow, mc, true)
}
