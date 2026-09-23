// Package score implements the deterministic 0-100 investment score engine
// (plan D5): four weighted dimensions — valuation (35%), fundamentals (30%),
// comparables (20%) and price trend (15%) — plus the Spanish signal
// comprar/mantener/vender and a template-based textual justification.
//
// Determinism guarantee: CalculateScore is a pure function of ScoreInput;
// the same input always produces the same output (CA-4/CA-6).
package score

// Dimension names and weights (decision del usuario 2026-09-22: D5).
const (
	DimValuation      = "valuation"
	DimFundamentals   = "fundamentals"
	DimComparables    = "comparables"
	DimTrend          = "trend"
	WeightValuation   = 0.35
	WeightFundaments  = 0.30
	WeightComparables = 0.20
	WeightTrend       = 0.15
)

// ModelVersion identifies the score formula revision (persisted in scores).
const ModelVersion = "1.1.0"

// DefaultComparablesMinSecurities is COMPARABLES_MIN_SECURITIES (plan D6).
const DefaultComparablesMinSecurities = 5

// ScoreInput aggregates every datum needed by CalculateScore. Pointer fields
// are nil when the datum is missing (the engine degrades to neutral).
type ScoreInput struct {
	Ticker           string              `json:"ticker"`
	Price            float64             `json:"price"`
	GrahamIntrinsic  *float64            `json:"graham_intrinsic"`
	DCFIntrinsic     *float64            `json:"dcf_intrinsic"`
	Metrics          map[string]*float64 `json:"metrics"` // pe_ratio, pb_ratio, fcf_yield, roe, de_ratio, peg_ratio
	SectorMedian     map[string]*float64 `json:"sector_median"`
	HistoricalMedian map[string]*float64 `json:"historical_median"`
	SectorCount      int                 `json:"sector_count"` // empresas activas del mismo sector (comparables)
	// ComparablesMinSecurities overrides DefaultComparablesMinSecurities; <=0
	// usa el default (env COMPARABLES_MIN_SECURITIES).
	ComparablesMinSecurities int      `json:"comparables_min_securities"`
	SMA50                    *float64 `json:"sma50"`
	SMA200                   *float64 `json:"sma200"`
	Momentum6m               *float64 `json:"momentum6m"`       // retorno fraccional 6m (0.15 = +15%)
	Momentum12m              *float64 `json:"momentum12m"`      // retorno fraccional 12m
	MarginOfSafety           float64  `json:"margin_of_safety"` // 0-100; default 30
}

// DimensionScore is one weighted dimension of the result.
type DimensionScore struct {
	Name   string  `json:"name"`
	Score  float64 `json:"score"`
	Weight float64 `json:"weight"`
}

// ScoreResult is the deterministic output of CalculateScore.
type ScoreResult struct {
	Score         int              `json:"score"`
	Signal        string           `json:"signal"`
	Justification string           `json:"justification"`
	Dimensions    []DimensionScore `json:"dimensions"`
	ModelVersion  string           `json:"model_version"`
}

// Signal thresholds (decision 2026-09-22, D5).
const (
	signalCompra   = "comprar"
	signalMantener = "mantener"
	signalVender   = "vender"
)

// SignalForScore maps a score to the Spanish signal: >=70 comprar,
// 40-69 mantener, <40 vender.
func SignalForScore(score int) string {
	switch {
	case score >= 70:
		return signalCompra
	case score >= 40:
		return signalMantener
	default:
		return signalVender
	}
}

// CalculateScore computes the weighted 0-100 score, signal and justification.
// Fully deterministic. MarginOfSafety defaults to 30 when <=0 or >100.
func CalculateScore(input ScoreInput) ScoreResult {
	if input.MarginOfSafety <= 0 || input.MarginOfSafety > 100 {
		input.MarginOfSafety = 30
	}
	dimensions := []DimensionScore{
		{Name: DimValuation, Score: scoreValuation(input.Price, input.GrahamIntrinsic, input.DCFIntrinsic, input.MarginOfSafety), Weight: WeightValuation},
		{Name: DimFundamentals, Score: scoreFundamentals(input.Metrics), Weight: WeightFundaments},
		{Name: DimComparables, Score: scoreComparables(input.Metrics, input.SectorMedian, input.HistoricalMedian, input.SectorCount, input.ComparablesMinSecurities), Weight: WeightComparables},
		{Name: DimTrend, Score: scoreTrend(input.SMA50, input.SMA200, input.Momentum6m, input.Momentum12m), Weight: WeightTrend},
	}

	total := 0.0
	for _, d := range dimensions {
		total += d.Score * d.Weight
	}
	final := int(total + 0.5) // redondeo a entero
	if final < 0 {
		final = 0
	}
	if final > 100 {
		final = 100
	}
	signal := SignalForScore(final)

	return ScoreResult{
		Score:         final,
		Signal:        signal,
		Justification: generateJustification(input.Ticker, final, signal, dimensions, input),
		Dimensions:    dimensions,
		ModelVersion:  ModelVersion,
	}
}

// clamp keeps v within [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
