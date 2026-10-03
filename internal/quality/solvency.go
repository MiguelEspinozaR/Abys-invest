package quality

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
//	de_ratio           = total_debt / shareholders_equity   [SECONDARY]
//
// AAPL FY2025: 62.723 / 144.748 = 0.4333 ; 98.767 / 98.657 = 1.0011.
//
// interest_coverage is NOT CALCULABLE in M6c (Az2b / ADR D6): the XBRL catalogue
// has no `interest_expense` (nor `income_tax_expense`), and re-ingesting the
// facts with new concepts is the separate task M6c-T1. The metric is therefore
// emitted as nil + interest_expense_unavailable — NEVER as 50 and NEVER as a
// fabricated value — which leaves debt_solvency computable with 2 of its 3
// applicable metrics (coverage 2/3) and the quality coverage at 16/17.
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

	if debt != nil {
		if equity := fin(in.Financials, conceptShareholdersEquity); equity != nil && *equity > 0 {
			r := *debt / *equity
			if finiteVal(r) {
				out.deRatio = metricValue{Value: &r} // sin score: es secundaria
			} else {
				out.deRatio = mvNil(ReasonNonFiniteInput)
			}
		} else {
			out.deRatio = mvNil(ReasonNonPositiveEquity)
		}
	} else {
		out.deRatio = mvNil(ReasonDebtUnavailable)
	}
	return out
}

// calcInterestCoverage is deliberately the ONLY branch of the whole engine that
// returns a value computed from a datum the database does not have: it returns
// the reason instead, and that reason travels to the API, to the UI and to the
// score trace (CA-M6c-4, M6c-T1).
func calcInterestCoverage(in Inputs, cfg Config) metricValue {
	if fin(in.Financials, conceptInterestExpense) == nil {
		return mvNil(ReasonInterestExpenseMissing)
	}
	// Defence in depth for the day M6c-T1 lands the concept: the ratio exists
	// here so the formula does not have to be rewritten, and it is only reachable
	// with a positive denominator.
	interest := fin(in.Financials, conceptInterestExpense)
	op := fin(in.Financials, conceptOperatingIncome)
	if op == nil {
		return mvNil(ReasonNoData)
	}
	if *interest <= 0 {
		return mvNil(ReasonNonFiniteInput)
	}
	r := *op / *interest
	if !finiteVal(r) {
		return mvNil(ReasonNonFiniteInput)
	}
	return mv(&r, cfg.Bands.InterestCoverage.Score(r))
}
