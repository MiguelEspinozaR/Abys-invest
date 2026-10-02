// Package score implements the deterministic 0-100 investment score engine
// (SPEC §12/§18, plan M6b D1): FIVE weighted dimensions — Graham (15%), DCF
// (20%), fundamentals (30%), comparables (20%) and price trend (15%) — plus the
// Spanish signal comprar/mantener/vender and a template-based justification.
//
// M6b change (ADR D16, §12): the single `valuation` dimension of M4b, which
// averaged Graham and DCF into one number, is SPLIT into two. The average hid
// the disagreement between the methods, which is exactly the information §9
// wants to show.
//
// Two more M6b facts:
//   - A dimension that cannot be computed is INVALID (`Score = nil`,
//     `Valid = false`), never a neutral 50: §18 renormalises by
//     active_weight_sum instead of inventing a score.
//   - Quality (35%) and Market Context (5%) of §18 arrive in M6c; until then the
//     weights of fundamentals/comparables/trend stay at their M4b values and sum
//     to 1.0 together with Graham and DCF.
//
// Determinism guarantee: CalculateScore is a pure function of ScoreInput;
// the same input always produces the same output (CA-4/CA-6).
package score

import "math"

// Dimension names and weights (§12, ADR D16). Graham and DCF replace the M4b
// `valuation` dimension (0.35) and keep its two halves separated; the remaining
// three are unchanged from M4b because the Quality/Market Context split is M6c.
const (
	DimGraham         = "graham"
	DimDCF            = "dcf"
	DimFundamentals   = "fundamentals"
	DimComparables    = "comparables"
	DimTrend          = "trend"
	WeightGraham      = 0.15
	WeightDCF         = 0.20
	WeightFundaments  = 0.30
	WeightComparables = 0.20
	WeightTrend       = 0.15
)

// ModelVersion identifies the score formula revision (persisted in scores).
// 2.0.0 = the Graham/DCF split of §12 (ADR D16): a BREAKING change, which is why
// the 1.1.0 rows stay readable and the readers order by model_version (D17).
const ModelVersion = "2.0.0"

// DefaultComparablesMinSecurities is COMPARABLES_MIN_SECURITIES (plan D6).
const DefaultComparablesMinSecurities = 5

// ScoreInput aggregates every datum needed by CalculateScore. Pointer fields
// are nil when the datum is missing (the engine degrades to neutral).
type ScoreInput struct {
	Ticker string  `json:"ticker"`
	Price  float64 `json:"price"`
	// GrahamBase and DCFBase are the BASE scenarios of valuation_results 2.0.0
	// (M6b D2): the scores READ the persisted row, they never recompute a
	// valuation. The M4b keys `graham_intrinsic`/`dcf_intrinsic` are GONE: the
	// 1.1.0 inputs_snapshot keeps them as history, and a new reader of an old
	// snapshot simply finds no base (and renormalises) instead of pretending
	// the 1.1.0 average is still meaningful.
	GrahamBase *float64 `json:"graham_base"`
	DCFBase    *float64 `json:"dcf_base"`
	// GrahamConfidence/DCFConfidence/GrahamReasons/DCFReasons come from the same
	// persisted row: the justification cites them so a 0 scored by a LOW
	// confidence DCF is not read as a verdict.
	GrahamConfidence string              `json:"graham_confidence,omitempty"`
	DCFConfidence    string              `json:"dcf_confidence,omitempty"`
	GrahamReasons    []string            `json:"graham_reasons,omitempty"`
	DCFReasons       []string            `json:"dcf_reasons,omitempty"`
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
//
// Score is a POINTER and Valid is explicit: a nil score means "this dimension
// could not be computed" (§18), which is a different fact from a 50 or a 0. The
// M4b `return 50` neutral of the valuation dimension is gone: with no value
// there is nothing to score, and §18 renormalises the rest.
type DimensionScore struct {
	Name  string   `json:"name"`
	Score *float64 `json:"score"` // nil = inválida
	Valid bool     `json:"valid"`
	// Weight is the CONFIGURED weight of the dimension (§12). The applied one is
	// ScoreResult.WeightUsed (the active_weight_sum divisor of §18).
	Weight float64 `json:"weight"`
}

// neutralDimension lifts a float score into a pointer. In M6b only the VALUATION
// dimensions can be invalid (plan D1 / CA-M6b-D2): removing the neutral 50 of
// comparables belongs to §16 Relative Valuation, i.e. M6c, and doing it here
// would change a score for a reason nobody asked for.
func neutralDimension(v float64) *float64 { return &v }

// newDimension builds a VALID dimension from a computed score.
func newDimension(name string, score *float64, weight float64) DimensionScore {
	return DimensionScore{Name: name, Score: score, Valid: score != nil, Weight: weight}
}

// ScoreResult is the deterministic output of CalculateScore.
type ScoreResult struct {
	Score         int              `json:"score"`
	Signal        string           `json:"signal"`
	Justification string           `json:"justification"`
	Dimensions    []DimensionScore `json:"dimensions"`
	// WeightConfigured is the sum of the configured weights (1.0 in 2.0.0) and
	// WeightUsed is §18's active_weight_sum: the sum of the weights of the
	// dimensions that were actually computable. Persisting both makes a
	// renormalised score auditable instead of mysterious.
	WeightConfigured float64 `json:"weight_configured"`
	WeightUsed       float64 `json:"weight_used"`
	ModelVersion     string  `json:"model_version"`
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
		newDimension(DimGraham, scoreGraham(input.Price, input.GrahamBase, input.MarginOfSafety), WeightGraham),
		newDimension(DimDCF, scoreDCF(input.Price, input.DCFBase, input.MarginOfSafety), WeightDCF),
		newDimension(DimFundamentals, neutralDimension(scoreFundamentals(input.Metrics)), WeightFundaments),
		newDimension(DimComparables, neutralDimension(scoreComparables(input.Metrics, input.SectorMedian, input.HistoricalMedian, input.SectorCount, input.ComparablesMinSecurities)), WeightComparables),
		newDimension(DimTrend, neutralDimension(scoreTrend(input.SMA50, input.SMA200, input.Momentum6m, input.Momentum12m)), WeightTrend),
	}

	// §18: only the valid dimensions enter the sum, and the total is divided by
	// the sum of THEIR weights (active_weight_sum). A missing valuation must
	// lower the confidence of the score by removing coverage, never by being
	// silently replaced by a 50.
	total := 0.0
	weightConfigured := 0.0
	weightUsed := 0.0
	for _, d := range dimensions {
		weightConfigured += d.Weight
		if !d.Valid {
			continue
		}
		total += *d.Score * d.Weight
		weightUsed += d.Weight
	}
	final := 50 // ninguna dimensión válida: neutral de último recurso, documentado
	if weightUsed > 0 {
		final = int(total/weightUsed + 0.5) // redondeo a entero
	}
	// The reported sums are rounded to 6 decimals: the exact sum of the five
	// binary weights is 0.9999999999999999, and a persisted "weight_used"
	// showing seventeen nines would be noise (the division above uses the raw
	// value, so the score does not change).
	weightConfigured = round6(weightConfigured)
	weightUsed = round6(weightUsed)
	if final < 0 {
		final = 0
	}
	if final > 100 {
		final = 100
	}
	signal := SignalForScore(final)

	return ScoreResult{
		Score:            final,
		Signal:           signal,
		Justification:    generateJustification(input.Ticker, final, signal, dimensions, input),
		Dimensions:       dimensions,
		WeightConfigured: weightConfigured,
		WeightUsed:       weightUsed,
		ModelVersion:     ModelVersion,
	}
}

// round6 rounds v to 6 decimals (see CalculateScore).
func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

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
