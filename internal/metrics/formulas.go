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

// DERatio is the SINGLE §13.4/§14 definition of the leverage ratio shared by
// every consumer (the metrics engine, quality's secondary de_ratio and any
// reader of derived_metrics):
//
//	de_ratio = total_liabilities / shareholders_equity
//
// When the issuer does not tag `Liabilities`, total_liabilities is DERIVED as
// `total_assets − shareholders_equity` (SPEC abys-valuation-210 C5). Equity is
// the denominator and must be strictly positive; a missing/derived numerator or
// a non-positive equity yields nil (never a fabricated 0, §21).
//
// It is the ONLY place the definition lives: a second copy is exactly how
// quality and the metrics engine drift into two different de_ratios for the
// same ticker/as_of (W5 acceptance).
func DERatio(totalLiabilities, totalAssets, shareholdersEquity *float64) *float64 {
	equity := sane(shareholdersEquity, true)
	if equity == nil {
		return nil
	}
	num := sane(totalLiabilities, false)
	if num == nil {
		assets := sane(totalAssets, false)
		if assets == nil {
			return nil
		}
		derived := *assets - *equity
		if !isFinite(derived) {
			return nil
		}
		num = &derived
	}
	return calcRatio(num, equity, false)
}

// isFinite reports whether v is a usable finite number.
func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// CalcDE is the LEGACY signature of the §13.4 ratio (Total Liabilities /
// Shareholders Equity). It now DELEGATES to DERatio with no total_assets, so
// the single definition is preserved; it is kept for callers that only carry a
// liabilities numerator (and for the historical tests).
func CalcDE(totalLiabilities, shareholdersEquity *float64) *float64 {
	return DERatio(totalLiabilities, nil, shareholdersEquity)
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
