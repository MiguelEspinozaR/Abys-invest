package metrics

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/miky/abys-invest/internal/metricver"
	"github.com/miky/abys-invest/internal/storage"
)

// ModelVersion21 is the CURRENT formula revision of the 12 ADR D12 metrics.
// M6c-T1 W5 changed their inputs (interest and the observed tax rate from the
// same FY), hence 2.1.0.
const ModelVersion21 = "2.1.0"

// ModelVersion2 is the FIRST formula revision of the 12 ADR D12 metrics,
// superseded by 2.1.0; it is kept to address history (2.0.0 rows were already
// persisted, §26: never overwrite existing rows).
const ModelVersion2 = "2.0.0"

// DefiningVersion returns the revision in which a metric is DEFINED.
//
// This exists because of a tension the plan resolves implicitly and B5 needs
// explicitly (ADR D13, R-M6c-1). R-M6c-1 forbids mixing revisions of the SAME
// metric — a median over 1.0.0 rows and 2.0.0 rows of one metric is
// non-deterministic and wrong. But SPEC §16 needs nine metrics for `relative`,
// and only five of them (roic, ev_ebitda, ev_ebit, net_debt_to_ebitda and, via
// valuation_results, p_fcf) are 2.1.0: pe_ratio, pb_ratio, fcf_yield and roe are
// and remain 1.0.0 (ADR D12 does not touch them).
//
// So the comparables readers filter PER METRIC, by the version in which that
// metric is defined — not by one global version that would empty four of the
// nine. Filtering by this function is what makes R-M6c-1 close WITHOUT silently
// reducing relative to a quarter of its inputs.
func DefiningVersion(metric string) string {
	return metricver.DefiningVersion(metric)
}

// NewMetricNames returns the 12 slugs of 2.1.0 in canonical order.
func NewMetricNames() []string {
	return []string{
		MetricEVEBIT, MetricEVEBITDA, MetricEPSVolatility, MetricFCFMargin,
		MetricFCFToDebt, MetricFCFVolatility, MetricInterestCover,
		MetricNetDebtToEBITDA, MetricOperatingMargin, MetricPositiveEPSYrs,
		MetricPositiveFCFYrs, MetricROIC,
	}
}

// MetricSeriesPoint is one annual observation of a fundamental series.
type MetricSeriesPoint struct {
	PeriodEnd time.Time `json:"period_end"`
	Value     float64   `json:"value"`
}

// MetricInputV2 is every input of the 12 new metrics. Pointer fields are nil
// when the fact is absent — never zero.
//
// Price + SharesOutstanding give market_cap, which the EV metrics need; the
// series are the DEDUPLICATED annual histories for the stability metrics.
type MetricInputV2 struct {
	SecurityID int64
	Ticker     string
	AsOf       time.Time

	// Facts shared with 1.0.0.
	NetEarnings        *float64
	SharesOutstanding  *float64
	Price              float64
	ShareholdersEquity *float64
	TotalLiabilities   *float64
	FreeCashFlow       *float64

	// New facts of §13/§14.
	OperatingIncome *float64
	Revenue         *float64
	TotalDebt       *float64
	Cash            *float64
	EBITDA          *float64
	// InterestExpense is nil only when there is no aligned/fresh interest pair
	// for the fiscal year of the observed tax rate (M6c-T1 W5). When it is nil,
	// interest_coverage is emitted anyway with value NULL and a snapshot that
	// says ReasonInterestCoverageUnavailable.
	InterestExpense *float64
	// NormalizedTaxRate is a RATIO (0.21), per SPEC §13.
	NormalizedTaxRate *float64

	// Annual series, already deduplicated by period.
	EPSSeries []MetricSeriesPoint
	FCFSeries []MetricSeriesPoint
}

// ReasonInterestCoverageUnavailable is the snapshot reason that travels with the
// NULL interest_coverage row (M6c-T1). It is persisted, so it is a stable string.
const ReasonInterestCoverageUnavailable = "interest_expense_unavailable"

// interestCoverageAlwaysEmitted is the ONE metric of the 12 that is emitted even
// with a NULL value.
//
// The rule (ADR D12 + §14) is no-emission for missing data — a row nobody can
// use is noise — EXCEPT interest_coverage, whose absence would be
// indistinguishable from "the job did not run". A NULL value plus a snapshot
// that names the missing input turns a silent gap into a self-describing one.
const interestCoverageAlwaysEmitted = true

// CalculateMetricsV2 computes the 12 metrics of 2.1.0 for one security.
//
// Emission policy, and it is the part worth stating out loud:
//
//   - a metric whose REQUIRED INPUTS are missing is NOT emitted at all;
//   - interest_coverage IS emitted with value NULL and a reason snapshot
//     (see interestCoverageAlwaysEmitted);
//   - results are sorted by metric name, so the output is deterministic and
//     diffable against a previous run.
func CalculateMetricsV2(input MetricInputV2) []MetricResult {
	var marketCap float64
	if so := sane(input.SharesOutstanding, true); so != nil {
		if price := sane(&input.Price, true); price != nil {
			marketCap = input.Price * *so
		}
	}
	mc := &marketCap
	if sane(mc, true) == nil {
		mc = nil
	}

	epsValues := seriesValues(input.EPSSeries)
	fcfValues := seriesValues(input.FCFSeries)
	netDebt := CalcNetDebt(input.TotalDebt, input.Cash)

	snapshot := func(extra map[string]any) map[string]any {
		m := map[string]any{
			"ticker":              input.Ticker,
			"price":               input.Price,
			"market_cap":          sanitize(mc),
			"net_earnings":        sanitize(input.NetEarnings),
			"shares_outstanding":  sanitize(input.SharesOutstanding),
			"free_cash_flow":      sanitize(input.FreeCashFlow),
			"revenue":             sanitize(input.Revenue),
			"operating_income":    sanitize(input.OperatingIncome),
			"shareholders_equity": sanitize(input.ShareholdersEquity),
			"total_debt":          sanitize(input.TotalDebt),
			"cash":                sanitize(input.Cash),
			"ebitda":              sanitize(input.EBITDA),
			"interest_expense":    sanitize(input.InterestExpense),
			"normalized_tax_rate": sanitize(input.NormalizedTaxRate),
			"net_debt":            sanitize(netDebt),
			"model_version":       ModelVersion21,
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	type candidate struct {
		metric   string
		value    *float64
		snapshot map[string]any
	}
	var out []candidate
	add := func(name string, value *float64, extra map[string]any) {
		if value == nil {
			return // no-emission: an unusable metric produces no row (ADR D12).
		}
		out = append(out, candidate{metric: name, value: value, snapshot: snapshot(extra)})
	}

	add(MetricROIC, CalcROIC(input.OperatingIncome, input.ShareholdersEquity, input.TotalDebt, input.Cash, input.NormalizedTaxRate),
		map[string]any{"formula": "operating_income * (1 - normalized_tax_rate) / (equity + total_debt - cash)", "units": "ratio"})
	add(MetricOperatingMargin, CalcOperatingMargin(input.OperatingIncome, input.Revenue),
		map[string]any{"formula": "operating_income / revenue", "units": "ratio"})
	add(MetricFCFMargin, CalcFCFMargin(input.FreeCashFlow, input.Revenue),
		map[string]any{"formula": "free_cash_flow / revenue", "units": "ratio"})
	add(MetricNetDebtToEBITDA, CalcNetDebtToEBITDA(netDebt, input.EBITDA),
		map[string]any{"formula": "(total_debt - cash) / ebitda", "units": "ratio"})
	add(MetricFCFToDebt, CalcFCFToDebt(input.FreeCashFlow, input.TotalDebt),
		map[string]any{"formula": "free_cash_flow / total_debt", "units": "ratio"})
	add(MetricEVEBITDA, CalcEVEBITDA(mc, netDebt, input.EBITDA),
		map[string]any{"formula": "(market_cap + total_debt - cash) / ebitda", "units": "ratio"})
	add(MetricEVEBIT, CalcEVEBIT(mc, netDebt, input.OperatingIncome),
		map[string]any{"formula": "(market_cap + total_debt - cash) / operating_income", "units": "ratio"})
	add(MetricPositiveEPSYrs, CalcPositiveYears(epsValues),
		map[string]any{"formula": "count(eps > 0)", "units": "count", "years_observed": len(epsValues)})
	add(MetricPositiveFCFYrs, CalcPositiveYears(fcfValues),
		map[string]any{"formula": "count(free_cash_flow > 0)", "units": "count", "years_observed": len(fcfValues)})
	add(MetricEPSVolatility, CalcCoefficientOfVariation(epsValues),
		map[string]any{"formula": "stdev(eps, sample) / abs(mean(eps))", "units": "ratio", "years_observed": len(epsValues)})
	add(MetricFCFVolatility, CalcCoefficientOfVariation(fcfValues),
		map[string]any{"formula": "stdev(free_cash_flow, sample) / abs(mean(free_cash_flow))", "units": "ratio", "years_observed": len(fcfValues)})

	// interest_coverage: emitted even when NULL, with the reason in the snapshot.
	ic := CalcInterestCoverage(input.OperatingIncome, input.InterestExpense)
	icExtra := map[string]any{"formula": "operating_income / interest_expense", "units": "ratio"}
	if ic == nil {
		icExtra["reason"] = ReasonInterestCoverageUnavailable
		icExtra["missing_input"] = "interest_expense"
		icExtra["planned_task"] = "M6c-T1"
	}
	if ic != nil || interestCoverageAlwaysEmitted {
		out = append(out, candidate{metric: MetricInterestCover, value: ic, snapshot: snapshot(icExtra)})
	}

	results := make([]MetricResult, 0, len(out))
	for _, c := range out {
		results = append(results, MetricResult{Metric: c.metric, Value: c.value, InputsSnapshot: c.snapshot})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Metric < results[j].Metric })
	return results
}

// seriesValues extracts the values of a series, dropping non-finite points so a
// corrupted observation cannot poison a mean or a count.
func seriesValues(pts []MetricSeriesPoint) []float64 {
	out := make([]float64, 0, len(pts))
	for _, p := range pts {
		out = append(out, p.Value)
	}
	return out
}

// BuildDerivedMetricsV2 converts 2.1.0 results into persistable rows.
func BuildDerivedMetricsV2(input MetricInputV2, modelVersion string) ([]storage.DerivedMetric, error) {
	if modelVersion == "" {
		modelVersion = ModelVersion21
	}
	results := CalculateMetricsV2(input)
	out := make([]storage.DerivedMetric, 0, len(results))
	for _, r := range results {
		snap, err := json.Marshal(r.InputsSnapshot)
		if err != nil {
			return nil, fmt.Errorf("metrics: marshal inputs_snapshot for %s: %w", r.Metric, err)
		}
		out = append(out, storage.DerivedMetric{
			SecurityID:     input.SecurityID,
			AsOf:           input.AsOf,
			Metric:         r.Metric,
			Value:          r.Value,
			InputsSnapshot: snap,
			ModelVersion:   modelVersion,
		})
	}
	return out, nil
}
