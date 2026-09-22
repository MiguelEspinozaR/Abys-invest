package metrics

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/miky/abys-invest/internal/storage"
)

// Metric names (slugs) persisted in derived_metrics.metric.
const (
	MetricEPS      = "eps"
	MetricPE       = "pe_ratio"
	MetricPB       = "pb_ratio"
	MetricPCF      = "pcf_ratio"
	MetricPEG      = "peg_ratio"
	MetricROE      = "roe"
	MetricDE       = "de_ratio"
	MetricFCFYield = "fcf_yield"
)

// DefaultModelVersion is the formula revision used when persisting metrics.
const DefaultModelVersion = "1.0.0"

// MetricInput encapsulates every input needed to compute the 8 MVP metrics.
// Pointer fields are NULL when the underlying data was missing/not usable.
type MetricInput struct {
	SecurityID         int64
	Ticker             string
	AsOf               time.Time
	NetEarnings        *float64
	SharesOutstanding  *float64
	Price              float64
	ShareholdersEquity *float64
	TotalLiabilities   *float64
	FreeCashFlow       *float64
	GrowthRate         float64 // g, in percent (default 7.0)
}

// MetricResult is the outcome of a single metric with its input snapshot.
type MetricResult struct {
	Metric         string
	Value          *float64
	InputsSnapshot map[string]any
}

// CalculateMetrics computes the 8 MVP metrics (§13) for one security. It is
// deterministic: same input -> same output, and results are sorted by metric
// name. marketCap = Price x SharesOutstanding is derived internally.
func CalculateMetrics(input MetricInput) []MetricResult {
	if input.GrowthRate <= 0 {
		input.GrowthRate = 7 // GROWTH_RATE_DEFAULT: g = 7%
	}

	var marketCap float64
	if so := sane(input.SharesOutstanding, true); so != nil {
		if price := sane(&input.Price, true); price != nil {
			marketCap = input.Price * *so
		}
	}

	snapshot := func(extra map[string]any) map[string]any {
		m := map[string]any{
			"ticker":              input.Ticker,
			"net_earnings":        sanitize(input.NetEarnings),
			"shares_outstanding":  sanitize(input.SharesOutstanding),
			"price":               input.Price,
			"market_cap":          marketCap,
			"shareholders_equity": sanitize(input.ShareholdersEquity),
			"total_liabilities":   sanitize(input.TotalLiabilities),
			"free_cash_flow":      sanitize(input.FreeCashFlow),
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	eps := CalcEPS(input.NetEarnings, input.SharesOutstanding)
	pe := CalcPE(&input.Price, eps)
	pb := CalcPB(marketCap, input.ShareholdersEquity)
	pcf := CalcPCF(marketCap, input.FreeCashFlow)
	peg := CalcPEG(pe, &input.GrowthRate)
	roe := CalcROE(input.NetEarnings, input.ShareholdersEquity)
	de := CalcDE(input.TotalLiabilities, input.ShareholdersEquity)
	fcfy := CalcFCFYield(input.FreeCashFlow, marketCap)

	results := []MetricResult{
		{Metric: MetricEPS, Value: eps, InputsSnapshot: snapshot(map[string]any{"formula": "net_earnings / shares_outstanding"})},
		{Metric: MetricPE, Value: pe, InputsSnapshot: snapshot(map[string]any{"formula": "price / eps"})},
		{Metric: MetricPB, Value: pb, InputsSnapshot: snapshot(map[string]any{"formula": "market_cap / shareholders_equity"})},
		{Metric: MetricPCF, Value: pcf, InputsSnapshot: snapshot(map[string]any{"formula": "market_cap / free_cash_flow"})},
		{Metric: MetricPEG, Value: peg, InputsSnapshot: snapshot(map[string]any{"growth_rate": input.GrowthRate, "formula": "pe_ratio / g"})},
		{Metric: MetricROE, Value: roe, InputsSnapshot: snapshot(map[string]any{"formula": "net_earnings / shareholders_equity"})},
		{Metric: MetricDE, Value: de, InputsSnapshot: snapshot(map[string]any{"formula": "total_liabilities / shareholders_equity"})},
		{Metric: MetricFCFYield, Value: fcfy, InputsSnapshot: snapshot(map[string]any{"formula": "free_cash_flow / market_cap (x100)"})},
	}

	// Deterministic ordering by metric name.
	sort.Slice(results, func(i, j int) bool { return results[i].Metric < results[j].Metric })
	return results
}

// BuildDerivedMetrics converts metric results into persistable rows
// (inputs_snapshot serialized as JSON).
func BuildDerivedMetrics(input MetricInput, modelVersion string) ([]storage.DerivedMetric, error) {
	if modelVersion == "" {
		modelVersion = DefaultModelVersion
	}
	results := CalculateMetrics(input)
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

// sanitize converts a *float64 into a JSON-serializable value: nil becomes
// nil, valid numbers are passed through.
func sanitize(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}
