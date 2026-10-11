package valuation

import (
	"sort"
	"time"
)

// FYPoint is one fiscal-year datum of a series (value in the concept's own
// unit, millions of dollars and dollars per share alike). PeriodEnd is the only
// key used to PAIR two series: the FCF margin is only meaningful when the FCF and
// the revenue come from the same fiscal year.
type FYPoint struct {
	PeriodEnd time.Time `json:"period_end"`
	Value     float64   `json:"value"`
}

// minMarginYears is the smallest number of paired FYs from which a MEDIAN FCF
// margin is considered meaningful. The method looks at up to 5 FYs (`maxMarginYears`);
// with fewer than 3 pairs the median is too thin and the normalisation simply
// does not apply (applied=false, never an error and never a fabricated value).
const (
	minMarginYears = 3
	maxMarginYears = 5
)

// normalizeFCF implements SPEC abys-valuation-210 C3:
//
//  1. pair the FCF and revenue series by PeriodEnd and take the most recent
//     (up to) 5 fiscal years;
//  2. median_margin = median(FCF_i / revenue_i);
//  3. current_margin = fcfNow / revenueNow;
//  4. if current_margin < 0.5 × median_margin → applied, and
//     FCF_norm = median_margin × revenueNow.
//
// raw is ALWAYS the caller's current FCF (finite), even when the method does not
// apply: the raw value and the normalised value are both exposed upstream. When
// the method does not apply, norm == raw and applied is false.
//
// It returns (nil, nil, false) only when fcfNow is not a finite number: without
// the raw there is nothing to normalise nor to echo. A non-positive revenue, an
// unusable series or a median margin <= 0 (a structurally loss-making FCF) all
// yield (raw, raw, false): the method corrects a MARGIN COLLAPSE, it does not
// invent cash flow for a business that never produced it (§21).
func normalizeFCF(fcfNow, revenueNow *float64, fcfSeries, revenueSeries []FYPoint) (raw, norm *float64, applied bool) {
	raw = sane(fcfNow, false)
	if raw == nil {
		return nil, nil, false
	}
	// A non-positive current FCF is NOT normalised back to health: §21 forbids
	// fabricating cash flow, and the DCF already reports fcf_non_positive. C3
	// corrects a MARGIN COLLAPSE of a still-positive FCF, it does not resurrect a
	// loss-making one.
	if *raw <= 0 {
		return raw, raw, false
	}
	rev := sane(revenueNow, true)
	if rev == nil {
		return raw, raw, false
	}

	margins := pairedMargins(fcfSeries, revenueSeries)
	if len(margins) < minMarginYears {
		return raw, raw, false
	}
	median := medianOf(margins)
	if !isFinite(median) || median <= 0 {
		return raw, raw, false
	}

	currentMargin := *raw / *rev
	if !isFinite(currentMargin) || currentMargin >= 0.5*median {
		return raw, raw, false
	}

	n := median * *rev
	if !isFinite(n) {
		return raw, raw, false
	}
	return raw, &n, true
}

// pairedMargins returns the FCF/revenue margin of every fiscal year present in
// BOTH series (paired by PeriodEnd), most recent LAST. A year with a
// non-positive revenue is skipped: it cannot yield a comparable margin. The
// result is capped to the most recent maxMarginYears years.
func pairedMargins(fcfSeries, revenueSeries []FYPoint) []float64 {
	if len(fcfSeries) == 0 || len(revenueSeries) == 0 {
		return nil
	}
	rev := make(map[int64]float64, len(revenueSeries))
	for _, p := range revenueSeries {
		if p.Value > 0 && isFinite(p.Value) {
			rev[p.PeriodEnd.Unix()] = p.Value
		}
	}
	paired := make([]FYPoint, 0, len(fcfSeries))
	for _, p := range fcfSeries {
		if !isFinite(p.Value) {
			continue
		}
		if _, ok := rev[p.PeriodEnd.Unix()]; ok {
			paired = append(paired, p)
		}
	}
	// Deterministic order regardless of the input order.
	sort.Slice(paired, func(i, j int) bool {
		return paired[i].PeriodEnd.Before(paired[j].PeriodEnd)
	})
	if len(paired) > maxMarginYears {
		paired = paired[len(paired)-maxMarginYears:]
	}
	out := make([]float64, 0, len(paired))
	for _, p := range paired {
		out = append(out, p.Value/rev[p.PeriodEnd.Unix()])
	}
	return out
}

// lastSeriesValue returns the value of the most recent point of a series
// (largest PeriodEnd), or nil for an empty series. It is the "now" of a series
// whose current value is not carried separately (revenue).
func lastSeriesValue(series []FYPoint) *float64 {
	if len(series) == 0 {
		return nil
	}
	idx := 0
	for i := 1; i < len(series); i++ {
		if series[i].PeriodEnd.After(series[idx].PeriodEnd) {
			idx = i
		}
	}
	v := series[idx].Value
	return &v
}

// medianOf returns the median of vs (vs is not mutated). Even counts average the
// two central values, which keeps the function deterministic and continuous.
func medianOf(vs []float64) float64 {
	if len(vs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), vs...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
