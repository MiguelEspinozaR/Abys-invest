// Package-normalized asset comparison: yields a base-100 index series for each
// asset and its risk statistics (annualized volatility, max drawdown, Sharpe).
// T6/D8 of the M3 plan: GET /compare?tickers=A,B,C&from=&to=.
package compare

import (
	"math"
	"sort"
	"time"

	"github.com/miky/abys-invest/internal/storage"
)

// DataPoint is one row of a normalized (base 100) price series.
type DataPoint struct {
	Date  string  `json:"date"`
	Index float64 `json:"index"`
}

// RiskMetrics summarizes the risk of an asset over the compared window.
type RiskMetrics struct {
	VolatilityAnnual float64 `json:"volatility_annual"`
	MaxDrawdown      float64 `json:"max_drawdown"`
	Sharpe           float64 `json:"sharpe"`
}

// ComparisonResult is the body of /compare (plan D8).
type ComparisonResult struct {
	NormalizedPerformance map[string][]DataPoint `json:"normalized_performance"`
	RiskMetrics           map[string]RiskMetrics `json:"risk_metrics"`
}

// tradingDaysPerYear anchors annualization to the standard 252-day convention.
const tradingDaysPerYear = 252.0

// NormalizePerformance turns a close series into a base-100 index. The first
// (oldest) bar is exactly 100.0; later bars are price/firstPrice*100.
// Series with fewer than 2 bars are not comparable and return nil.
func NormalizePerformance(prices []storage.DailyPrice) []DataPoint {
	if len(prices) < 2 {
		return nil
	}
	base := prices[0].AdjustedClose
	if base <= 0 {
		return nil
	}
	out := make([]DataPoint, len(prices))
	for i, p := range prices {
		out[i] = DataPoint{Date: p.Date.Format("2006-01-02"), Index: p.AdjustedClose / base * 100}
	}
	return out
}

// logReturns computes the inter-day log returns of the adjusted closes.
// Series with fewer than 2 bars yield an empty slice.
func logReturns(prices []storage.DailyPrice) []float64 {
	if len(prices) < 2 {
		return nil
	}
	out := make([]float64, 0, len(prices)-1)
	for i := 1; i < len(prices); i++ {
		prev, cur := prices[i-1].AdjustedClose, prices[i].AdjustedClose
		if prev <= 0 || cur <= 0 {
			continue
		}
		out = append(out, math.Log(cur/prev))
	}
	return out
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func stddev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := mean(xs)
	s := 0.0
	for _, x := range xs {
		d := x - m
		s += d * d
	}
	return math.Sqrt(s / float64(len(xs)-1))
}

// AnnualizedVolatility computes the annualized volatility (decimal, e.g. 0.28)
// of the daily log returns over the series (252 trading days per year).
func AnnualizedVolatility(prices []storage.DailyPrice) float64 {
	rets := logReturns(prices)
	if len(rets) < 2 {
		return 0
	}
	return stddev(rets) * math.Sqrt(tradingDaysPerYear)
}

// MaxDrawdown returns the maximum peak-to-trough decline of the series as a
// negative decimal (e.g. -0.18). A flat or degenerate series yields 0.
func MaxDrawdown(prices []storage.DailyPrice) float64 {
	peak := 0.0
	maxDD := 0.0
	for _, p := range prices {
		v := p.AdjustedClose
		if v <= 0 {
			continue
		}
		if peak == 0 || v > peak {
			peak = v
		}
		if peak > 0 {
			dd := v/peak - 1
			if dd < maxDD {
				maxDD = dd
			}
		}
	}
	return maxDD
}

// SharpeRatio computes the annualized Sharpe ratio of the daily log returns
// over the series using the given annual risk-free rate (decimal; default 0).
// A series without variance yields 0.
func SharpeRatio(prices []storage.DailyPrice, riskFreeRate float64) float64 {
	rets := logReturns(prices)
	if len(rets) < 2 {
		return 0
	}
	rfDaily := riskFreeRate / tradingDaysPerYear
	excess := make([]float64, len(rets))
	for i, r := range rets {
		excess[i] = r - rfDaily
	}
	sd := stddev(excess)
	if sd == 0 {
		return 0
	}
	return mean(excess) / sd * math.Sqrt(tradingDaysPerYear)
}

// filterByRange keeps bars within [from, to]; zero times mean "no limit".
func filterByRange(prices []storage.DailyPrice, from, to time.Time) []storage.DailyPrice {
	if from.IsZero() && to.IsZero() {
		return prices
	}
	out := make([]storage.DailyPrice, 0, len(prices))
	for _, p := range prices {
		if !from.IsZero() && p.Date.Before(from) {
			continue
		}
		if !to.IsZero() && p.Date.After(to) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// CompareAssets builds the normalized performance and risk metrics for every
// ticker present in allPrices over the window [from, to]. Tickers with fewer
// than 2 bars in the window are omitted (nothing meaningful to compare).
// The input map keys are returned sorted for determinism.
func CompareAssets(allPrices map[string][]storage.DailyPrice, from, to time.Time, riskFreeRate float64) ComparisonResult {
	res := ComparisonResult{
		NormalizedPerformance: map[string][]DataPoint{},
		RiskMetrics:           map[string]RiskMetrics{},
	}
	tickers := make([]string, 0, len(allPrices))
	for t := range allPrices {
		tickers = append(tickers, t)
	}
	sort.Strings(tickers)
	for _, t := range tickers {
		bars := filterByRange(allPrices[t], from, to)
		norm := NormalizePerformance(bars)
		if norm == nil {
			continue
		}
		res.NormalizedPerformance[t] = norm
		res.RiskMetrics[t] = RiskMetrics{
			VolatilityAnnual: AnnualizedVolatility(bars),
			MaxDrawdown:      MaxDrawdown(bars),
			Sharpe:           SharpeRatio(bars, riskFreeRate),
		}
	}
	return res
}
