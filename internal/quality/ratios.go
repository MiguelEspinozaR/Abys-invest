package quality

import "math"

// Financial concepts consumed from Inputs.Financials. `revenue`/`revenues` are
// the same magnitude under two XBRL tags and both are accepted (the loader asks
// the catalogue for both).
const (
	conceptOperatingIncome    = "operating_income"
	conceptShareholdersEquity = "shareholders_equity"
	conceptTotalDebt          = "total_debt"
	conceptCash               = "cash_and_equivalents"
	conceptNetDebt            = "net_debt"
	conceptEBITDA             = "ebitda"
	conceptRevenue            = "revenue"
	conceptRevenuePlural      = "revenues"
	conceptFreeCashFlow       = "free_cash_flow"
	conceptNetEarnings        = "net_earnings"
	conceptInterestExpense    = "interest_expense"
	conceptEPSDiluted         = "eps_diluted"
	conceptSharesOutstanding  = "shares_outstanding"
)

// calcROIC is §13 LITERAL (ADR D5):
//
//	nopat            = operating_income × (1 − tax_rate/100)
//	invested_capital = shareholders_equity + total_debt − cash
//	roic             = nopat / invested_capital
//
// AAPL FY2025: 133.050 × 0.79 = 105.1095 ; 73.733 + 98.657 − 35.934 = 136.456 ;
// roic = 0.7702.
//
// Two refusals, both honest:
//
//   - invested_capital <= 0 → nil. A company with negative equity funding is not
//     "a company with a return of −Inf": the denominator has no meaning.
//   - total_debt MISSING → nil + debt_unavailable. §13 is literal about the
//     formula and net_debt is NOT a substitute: net debt subtracts cash from debt
//     and says nothing about the equity the capital was raised with.
func calcROIC(in Inputs, cfg Config) metricValue {
	op := fin(in.Financials, conceptOperatingIncome)
	if op == nil {
		return mvNil(ReasonNoData)
	}
	debt := fin(in.Financials, conceptTotalDebt)
	if debt == nil {
		return mvNil(ReasonDebtUnavailable)
	}
	equity := fin(in.Financials, conceptShareholdersEquity)
	cash := fin(in.Financials, conceptCash)

	invested := 0.0
	if equity != nil {
		invested += *equity
	}
	invested += *debt
	if cash != nil {
		invested -= *cash
	}
	if !finiteVal(invested) {
		return mvNil(ReasonNonFiniteInput)
	}
	if invested <= 0 {
		return mvNil(ReasonNonPositiveInvestedCap)
	}
	nopat := *op * (1 - cfg.TaxRate/100)
	roic := nopat / invested
	if !finiteVal(roic) {
		return mvNil(ReasonNonFiniteInput)
	}
	return mv(&roic, cfg.Bands.ROIC.Score(roic))
}

// calcROE is §13: net_earnings / shareholders_equity. Refused on a
// non-positive denominator (the same reason as ROIC's invested capital).
func calcROE(in Inputs, cfg Config) metricValue {
	earnings := fin(in.Financials, conceptNetEarnings)
	if earnings == nil {
		return mvNil(ReasonNoData)
	}
	equity := fin(in.Financials, conceptShareholdersEquity)
	if equity == nil {
		return mvNil(ReasonNoData)
	}
	if *equity <= 0 {
		return mvNil(ReasonNonPositiveEquity)
	}
	roe := *earnings / *equity
	if !finiteVal(roe) {
		return mvNil(ReasonNonFiniteInput)
	}
	return mv(&roe, cfg.Bands.ROE.Score(roe))
}

// calcOperatingMargin is §13: operating_income / revenue.
func calcOperatingMargin(in Inputs, cfg Config) metricValue {
	op := fin(in.Financials, conceptOperatingIncome)
	if op == nil {
		return mvNil(ReasonNoData)
	}
	rev, ok := revenue(in)
	if !ok {
		return mvNil(ReasonNonPositiveRevenue)
	}
	m := *op / rev
	if !finiteVal(m) {
		return mvNil(ReasonNonFiniteInput)
	}
	return mv(&m, cfg.Bands.OperatingMargin.Score(m))
}

// calcFCFMargin is §13: free_cash_flow / revenue.
func calcFCFMargin(in Inputs, cfg Config) metricValue {
	fcf := fin(in.Financials, conceptFreeCashFlow)
	if fcf == nil {
		return mvNil(ReasonNoData)
	}
	rev, ok := revenue(in)
	if !ok {
		return mvNil(ReasonNonPositiveRevenue)
	}
	m := *fcf / rev
	if !finiteVal(m) {
		return mvNil(ReasonNonFiniteInput)
	}
	return mv(&m, cfg.Bands.FCFMargin.Score(m))
}

// calcFCFYield is §13/§15: free_cash_flow / market_cap, with market_cap =
// price × shares_outstanding of the as_of (A4). The result is a FRACTION
// (0.0196 = 1.96%), which is why derived_metrics keeps its own percentage
// convention untouched.
func calcFCFYield(in Inputs, mc *float64, cfg Config) metricValue {
	fcf := fin(in.Financials, conceptFreeCashFlow)
	if fcf == nil {
		return mvNil(ReasonNoData)
	}
	if mc == nil {
		return mvNil(ReasonNonPositiveMarketCap)
	}
	y := *fcf / *mc
	if !finiteVal(y) {
		return mvNil(ReasonNonFiniteInput)
	}
	return mv(&y, cfg.Bands.FCFYield.Score(y))
}

// revenue resolves the revenue magnitude and refuses a non-positive denominator:
// a negative revenue is not a margin, it is a data error.
func revenue(in Inputs) (float64, bool) {
	rev := fin(in.Financials, conceptRevenue, conceptRevenuePlural)
	if rev == nil || *rev <= 0 {
		return 0, false
	}
	return *rev, true
}

// mean returns the arithmetic mean of xs and whether it could be computed.
func mean(xs []float64) (float64, bool) {
	if len(xs) == 0 {
		return 0, false
	}
	sum := 0.0
	for _, x := range xs {
		if !finiteVal(x) {
			return 0, false
		}
		sum += x
	}
	m := sum / float64(len(xs))
	if !finiteVal(m) {
		return 0, false
	}
	return m, true
}

// stddev is the POPULATION standard deviation (divisor n), matching §13's
// "stddev" over the handful of annual points available. The sample divisor
// (n−1) would make a four-year series look 15% more volatile than it is.
func stddev(xs []float64, m float64) (float64, bool) {
	if len(xs) == 0 || !finiteVal(m) {
		return 0, false
	}
	sum := 0.0
	for _, x := range xs {
		d := x - m
		sum += d * d
	}
	v := math.Sqrt(sum / float64(len(xs)))
	if !finiteVal(v) {
		return 0, false
	}
	return v, true
}
