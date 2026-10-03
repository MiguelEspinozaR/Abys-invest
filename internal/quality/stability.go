package quality

import "sort"

// stabilityResult groups §13's four stability metrics.
type stabilityResult struct {
	positiveEPSYears metricValue
	positiveFCFYears metricValue
	epsVolatility    metricValue
	fcfVolatility    metricValue
}

// calcStability computes §13's stability block over the annual FY series, which
// the loader already DEDUPLICATED by period_end (AAPL has three filings for some
// fiscal years). Counting a restatement twice would inflate the number of
// "positive years" and deflate the volatility of a five-point series.
//
//	positive_eps_years = #{period_end : eps_diluted > 0}
//	positive_fcf_years = #{period_end : free_cash_flow > 0}
//	eps_volatility     = stddev(eps_diluted) / |mean(eps_diluted)|
//	fcf_volatility     = stddev(free_cash_flow) / |mean(free_cash_flow)|
//
// With fewer than MinStabilityYears points ALL FOUR are nil +
// insufficient_history: four metrics computed over two annual reports would be a
// claim, not a measurement.
//
// The two COUNTS are reported as counts (§13) but SCORED on their ratio over the
// observed points, because a raw count rewards a longer history instead of a
// steadier one (see bands.go). The two VOLATILITIES are refused on a zero mean
// with zero_mean_base — never an Inf, which would travel into the score as a NaN.
func calcStability(in Inputs, cfg Config) stabilityResult {
	var out stabilityResult
	eps := values(in.Series[SeriesEPSDiluted])
	fcf := values(in.Series[SeriesFreeCashFlow])

	epsYears, okEps := countPositive(eps, cfg.MinStabilityYears, cfg.Bands.PositiveEPSYears)
	fcfYears, okFCF := countPositive(fcf, cfg.MinStabilityYears, cfg.Bands.PositiveFCFYears)
	out.positiveEPSYears = epsYears
	out.positiveFCFYears = fcfYears

	if okEps {
		out.epsVolatility = coefficientOfVariation(eps, cfg.Bands.EPSVolatility)
	} else {
		out.epsVolatility = mvNil(ReasonInsufficientHistory)
	}
	if okFCF {
		out.fcfVolatility = coefficientOfVariation(fcf, cfg.Bands.FCFVolatility)
	} else {
		out.fcfVolatility = mvNil(ReasonInsufficientHistory)
	}
	return out
}

// values extracts the numeric series, sorted by period_end ascending. The sort
// makes the engine independent of the order the loader returned (and of any
// accidental duplicate): a series is a series.
func values(points []SeriesPoint) []float64 {
	out := make([]SeriesPoint, len(points))
	copy(out, points)
	sort.Slice(out, func(i, j int) bool { return out[i].PeriodEnd.Before(out[j].PeriodEnd) })
	xs := make([]float64, 0, len(out))
	for _, p := range out {
		if !finiteVal(p.Value) {
			continue
		}
		xs = append(xs, p.Value)
	}
	return xs
}

// countPositive returns the number of positive observations as the METRIC
// (§13: a count), scored on the RATIO over the observed points with the
// configured band. It reports ok=false below MinStabilityYears, which is also how
// Calculate knows the volatilities must be nil too.
func countPositive(xs []float64, minYears int, band Band) (metricValue, bool) {
	if len(xs) < minYears {
		return mvNil(ReasonInsufficientHistory), false
	}
	n := 0
	for _, x := range xs {
		if x > 0 {
			n++
		}
	}
	count := float64(n)
	ratio := count / float64(len(xs))
	return metricValue{Value: &count, Score: valid(band.Score(ratio))}, true
}

// coefficientOfVariation is stddev/|mean|, refused on a zero (or non-finite)
// mean.
func coefficientOfVariation(xs []float64, band Band) metricValue {
	m, ok := mean(xs)
	if !ok {
		return mvNil(ReasonNoData)
	}
	if m == 0 {
		return mvNil(ReasonZeroMeanBase)
	}
	sd, ok := stddev(xs, m)
	if !ok {
		return mvNil(ReasonNonFiniteInput)
	}
	cv := sd / abs(m)
	if !finiteVal(cv) {
		return mvNil(ReasonNonFiniteInput)
	}
	return mv(&cv, band.Score(cv))
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
