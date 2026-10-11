package quality

import "github.com/miky/abys-invest/internal/metrics"

// solvencyResult groups §14's debt metrics: the three applicable ones and the
// secondary de_ratio.
type solvencyResult struct {
	netDebtToEBITDA  metricValue
	fcfToDebt        metricValue
	interestCoverage metricValue
	deRatio          metricValue // SECONDARY: no score, no coverage (§14)
}

// calcSolvency implements §14 LITERAL:
//
//	net_debt_to_ebitda = net_debt / ebitda
//	fcf_to_debt        = free_cash_flow / total_debt
//	interest_coverage  = operating_income / interest_expense
//	de_ratio           = total_liabilities / shareholders_equity   [SECONDARY]
//
// AAPL FY2025: 62.723 / 144.748 = 0.4333 ; 98.767 / 98.657 = 1.0011.
// interest_coverage uses aligned FY pair when provided (M6c-T1).
//
// de_ratio is NOT recomputed here any more (SPEC abys-valuation-210 C5, W5): it
// is read through the SINGLE definition shared by every consumer,
// metrics.DERatio (total_liabilities / equity, deriving total_liabilities from
// total_assets − equity when the issuer does not tag it). This is what makes
// quality's de_ratio identical to the derived_metrics one for the same
// ticker/as_of instead of a second, diverging ratio.
func calcSolvency(in Inputs, cfg Config) solvencyResult {
	var out solvencyResult

	ebitda := fin(in.Financials, conceptEBITDA)
	if ebitda == nil {
		out.netDebtToEBITDA = mvNil(ReasonNoData)
	} else if *ebitda <= 0 {
		out.netDebtToEBITDA = mvNil(ReasonNonPositiveEBITDA)
	} else {
		netDebt := fin(in.Financials, conceptNetDebt)
		if netDebt == nil {
			// net_debt is not a derived value here on purpose: a company that
			// reports debt but no cash still HAS a net debt position, and §14
			// takes the reported magnitude.
			out.netDebtToEBITDA = mvNil(ReasonNoData)
		} else {
			r := *netDebt / *ebitda
			if !finiteVal(r) {
				out.netDebtToEBITDA = mvNil(ReasonNonFiniteInput)
			} else {
				out.netDebtToEBITDA = mv(&r, cfg.Bands.NetDebtToEBITDA.Score(r))
			}
		}
	}

	debt := fin(in.Financials, conceptTotalDebt)
	fcf := fin(in.Financials, conceptFreeCashFlow)
	switch {
	case debt == nil:
		out.fcfToDebt = mvNil(ReasonDebtUnavailable)
	case *debt <= 0:
		out.fcfToDebt = mvNil(ReasonNonPositiveTotalDebt)
	case fcf == nil:
		out.fcfToDebt = mvNil(ReasonNoData)
	default:
		r := *fcf / *debt
		if !finiteVal(r) {
			out.fcfToDebt = mvNil(ReasonNonFiniteInput)
		} else {
			out.fcfToDebt = mv(&r, cfg.Bands.FCFToDebt.Score(r))
		}
	}

	out.interestCoverage = calcInterestCoverage(in, cfg)

	// W5: de_ratio reads the unified definition (metrics.DERatio), it does not
	// recompute its own. The reason taxonomy of the legacy block is preserved:
	// a non-positive/missing equity is `non_positive_equity`, a numerator that
	// cannot be resolved (no total_liabilities AND no total_assets) is
	// `debt_unavailable`.
	equity := fin(in.Financials, conceptShareholdersEquity)
	switch {
	case equity == nil || *equity <= 0:
		out.deRatio = mvNil(ReasonNonPositiveEquity)
	default:
		if r := metrics.DERatio(fin(in.Financials, conceptTotalLiabilities), fin(in.Financials, conceptTotalAssets), equity); r != nil {
			out.deRatio = metricValue{Value: r} // sin score: es secundaria
		} else {
			out.deRatio = mvNil(ReasonDebtUnavailable)
		}
	}
	return out
}

// calcInterestCoverage calculates interest_coverage. With aligned inputs (M6c-T1),
// use the aligned pair if provided; otherwise fall back to legacy lookup.
func calcInterestCoverage(in Inputs, cfg Config) metricValue {
	interest := in.AlignedInterestExpense
	op := in.AlignedOperatingIncome
	if in.InterestReason != "" {
		return mvNil(in.InterestReason)
	}
	if interest == nil || op == nil {
		// fallback to legacy behavior for compatibility
		interest = fin(in.Financials, conceptInterestExpense)
		op = fin(in.Financials, conceptOperatingIncome)
		if interest == nil {
			return mvNil(ReasonInterestExpenseMissing)
		}
		if op == nil {
			return mvNil(ReasonNoData)
		}
	}
	if *interest <= 0 || !finiteVal(*interest) || !finiteVal(*op) {
		return mvNil(ReasonNonFiniteInput)
	}
	r := *op / *interest
	if !finiteVal(r) {
		return mvNil(ReasonNonFiniteInput)
	}
	return mv(&r, cfg.Bands.InterestCoverage.Score(r))
}
