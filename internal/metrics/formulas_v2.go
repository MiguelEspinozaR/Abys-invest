package metrics

import "math"

// ptrOf is a local helper: taking the address of a literal expression is legal
// Go but unreadable, and these formulas return computed values.
func ptrOf(v float64) *float64 { return &v }

// This file holds the 12 formulas of ADR D12 — the metrics revision 2.0.0 that
// SPEC §13 (Quality/Fundamentals) and §14 (Debt & solvency) need and that M2
// never had.
//
// NON-REGRESSION (CA-M6c-14): nothing here modifies the 8 formulas of 1.0.0.
// CalcEPS/CalcPE/CalcPB/CalcPCF/CalcPEG/CalcROE/CalcDE/CalcFCFYield keep their
// exact behaviour, and `peg = 30.654` / `p_fcf = 51.027` for AAPL must stay
// byte-identical. 2.0.0 ADDS metrics under new slugs; it does not re-emit the
// old ones under a new version, because a re-emission under a new version would
// make `derived_metrics` claim two formula revisions for the same number.
//
// UNITS are explicit and inconsistent ON PURPOSE (inherited, not invented):
// roe/fcf_yield of 1.0.0 are already persisted with their own conventions and
// are not touched; every NEW metric here is a RATIO (0.15 = 15%), except
// positive_*_years which is a COUNT. Each row carries its unit in
// inputs_snapshot so a reader never has to guess.

// New metric slugs of 2.0.0 (ADR D12). All are <= 30 chars, the width of
// derived_metrics.metric.
const (
	MetricROIC            = "roic"
	MetricOperatingMargin = "operating_margin"
	MetricFCFMargin       = "fcf_margin"
	MetricNetDebtToEBITDA = "net_debt_to_ebitda"
	MetricInterestCover   = "interest_coverage"
	MetricFCFToDebt       = "fcf_to_debt"
	MetricPositiveEPSYrs  = "positive_eps_years"
	MetricPositiveFCFYrs  = "positive_fcf_years"
	MetricEPSVolatility   = "eps_volatility"
	MetricFCFVolatility   = "fcf_volatility"
	MetricEVEBITDA        = "ev_ebitda"
	MetricEVEBIT          = "ev_ebit"
)

// CalcROIC implements SPEC §13 ROIC:
//
//	NOPAT           = OperatingIncome x (1 - normalized_tax_rate)
//	InvestedCapital = Equity + Debt - Cash
//	ROIC            = NOPAT / InvestedCapital
//
// InvestedCapital <= 0 is refused: a company whose book capital is destroyed or
// negative (banks routinely, and any company that has bought back more than it
// has earned) has no denominator here, and dividing by it would produce a
// spectacular and meaningless number.
//
// taxRate is the NORMALIZED rate as a RATIO (0.21), matching the SPEC. A nil or
// out-of-range tax rate means NOPAT is unknown, so ROIC is nil — the tax rate is
// a first-class input of the formula, not a detail to default away.
func CalcROIC(operatingIncome, equity, totalDebt, cash, taxRate *float64) *float64 {
	oi := sane(operatingIncome, false)
	eq := sane(equity, false)
	debt := sane(totalDebt, false)
	c := sane(cash, false)
	tax := sane(taxRate, false)
	if oi == nil || eq == nil || debt == nil || c == nil || tax == nil {
		return nil
	}
	if *tax < 0 || *tax >= 1 {
		return nil
	}
	invested := *eq + *debt - *c
	if invested <= 0 {
		return nil
	}
	nopat := *oi * (1 - *tax)
	return ptrOf(nopat / invested)
}

// CalcOperatingMargin implements §13: operating_margin = operating_income / revenue.
func CalcOperatingMargin(operatingIncome, revenue *float64) *float64 {
	return calcRatio(operatingIncome, revenue, false)
}

// CalcFCFMargin implements §13: fcf_margin = fcf / revenue.
func CalcFCFMargin(freeCashFlow, revenue *float64) *float64 {
	return calcRatio(freeCashFlow, revenue, false)
}

// CalcNetDebt implements net_debt = total_debt - cash. A missing component is a
// missing net debt: reporting 0 for a company whose cash is unknown would make
// it look debt-free.
func CalcNetDebt(totalDebt, cash *float64) *float64 {
	d := sane(totalDebt, false)
	c := sane(cash, false)
	if d == nil || c == nil {
		return nil
	}
	out := *d - *c
	return &out
}

// CalcNetDebtToEBITDA implements §14:
// net_debt_to_ebitda = net_debt / EBITDA. Negative net debt (net cash) is a
// legitimate value and is kept: the metric is a leverage reading, not a debt
// amount.
func CalcNetDebtToEBITDA(netDebt, ebitda *float64) *float64 {
	n := sane(netDebt, false)
	e := sane(ebitda, true)
	if n == nil || e == nil {
		return nil
	}
	out := *n / *e
	return &out
}

// CalcInterestCoverage implements §14: interest_coverage = EBIT / interest_expense.
//
// EBIT is operating income, as SPEC §14 names it. Both sides must be strictly
// positive: a company with no interest expense does not have infinite coverage
// that scores well, it has an unmeasurable ratio, and §14 is explicit that
// missing data must not become 50.
func CalcInterestCoverage(operatingIncome, interestExpense *float64) *float64 {
	oi := sane(operatingIncome, true)
	ie := sane(interestExpense, true)
	if oi == nil || ie == nil {
		return nil
	}
	out := *oi / *ie
	return &out
}

// CalcFCFToDebt implements §14: fcf_to_debt = FCF / total_debt.
func CalcFCFToDebt(freeCashFlow, totalDebt *float64) *float64 {
	return calcRatio(freeCashFlow, totalDebt, false)
}

// CalcPositiveYears counts the points of a series that are strictly > 0.
//
// It returns nil for an empty series (nothing observed) and never a 0 that looks
// like "zero good years" when the truth is "no data". The count is accompanied by
// yearsObserved in the snapshot, because 3 out of 3 is not evidence of stability
// and the consumer (quality.StabilityMinYears) is the one that decides how many
// years are enough.
func CalcPositiveYears(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	positive := 0
	for _, v := range values {
		if !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 {
			positive++
		}
	}
	out := float64(positive)
	return &out
}

// CalcCoefficientOfVariation implements the volatility of §13 as the coefficient
// of variation: stdev(sample) / |mean|.
//
// Why a ratio and not a stdev: EPS of 2 and FCF of 2,000,000,000 are not
// comparable in absolute terms, so an absolute stdev would rank a large company
// as wildly unstable and a small one as perfectly steady. Normalising by the mean
// makes the metric dimensionless.
//
// mean == 0 is refused (division by zero, and a series that oscillates around
// zero has no meaningful level to normalise against); fewer than 2 points has no
// sample stdev. This is the SAME function quality.Stability uses, so a metric row
// and the quality sub-score can never disagree.
func CalcCoefficientOfVariation(values []float64) *float64 {
	if len(values) < 2 {
		return nil
	}
	var sum float64
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil
		}
		sum += v
	}
	mean := sum / float64(len(values))
	if mean == 0 {
		return nil
	}
	var ss float64
	for _, v := range values {
		d := v - mean
		ss += d * d
	}
	stdev := math.Sqrt(ss / float64(len(values)-1))
	return ptrOf(stdev / math.Abs(mean))
}

// CalcEVEBITDA implements §16/§13: EV/EBITDA = (market_cap + net_debt) / EBITDA.
func CalcEVEBITDA(marketCap, netDebt, ebitda *float64) *float64 {
	mc := sane(marketCap, true)
	nd := sane(netDebt, false)
	e := sane(ebitda, true)
	if mc == nil || nd == nil || e == nil {
		return nil
	}
	out := (*mc + *nd) / *e
	return &out
}

// CalcEVEBIT implements §16: EV/EBIT = (market_cap + net_debt) / EBIT.
// EBIT is operating income (SPEC §14 naming).
func CalcEVEBIT(marketCap, netDebt, operatingIncome *float64) *float64 {
	mc := sane(marketCap, true)
	nd := sane(netDebt, false)
	oi := sane(operatingIncome, true)
	if mc == nil || nd == nil || oi == nil {
		return nil
	}
	out := (*mc + *nd) / *oi
	return &out
}
