package score

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/quality"
	"github.com/miky/abys-invest/internal/reason"
	"github.com/miky/abys-invest/internal/relative"
)

// This file is score 2.1.0: the revision that makes the five dimensions of SPEC
// §12/§18 the REAL taxonomy of the score, with the three M4b hybrids
// (fundamentals/comparables/trend) replaced by quality/relative/market_context.
//
// NON-REGRESSION (CA-M6c-14): score.go (2.0.0) and its fixtures are NOT touched.
// 2.1.0 lives beside them, ModelVersion21 is its own constant, and the API gate
// keeps 1.1.0/2.0.0 readable. Two coexisting revisions of one engine is the same
// contract M6b applied to valuation.
//
// THE RULE THIS FILE EXISTS TO ENFORCE (§16/§18, ADR D8): a dimension without a
// value is INVALID and leaves the average. It is never a 50. `CalculateScore21`
// has no `neutralDimension` at all — there is no code path that can invent one.

// ModelVersion21 is the score revision of M6c (SPEC §18).
const ModelVersion21 = "2.1.0"

// TraceVersion is the compatibility gate of the persisted trace (ADR D27): the
// backtest runner refuses a snapshot whose trace_version it cannot decode, instead
// of silently reading fields that moved.
const TraceVersion = "1"

// Dimensions of SPEC §12 and their weights (Az3 confirmed strict §18:
// graham 0.15, dcf 0.20, quality 0.35, relative 0.15, market_context 0.05).
// Sum = 0.90 (NOT 1.00). The missing 0.10 stays unallocated by design; the
// §18 renormalisation divides by active_weight_sum (0.90 when all 5 valid).
const (
	DimGrahamV21        = "graham"
	DimDCFV21           = "dcf"
	DimQualityV21       = "quality"
	DimRelativeV21      = "relative"
	DimMarketContextV21 = "market_context"
)

// QualitySubBlock weights: the five sub-blocks of §13, equal by default
// (0.20 each). They are exposed INSIDE the quality dimension, never as dimensions
// of their own: a sub-block with no data must not be able to move the score by
// pretending to be a fifth opinion.
const (
	SubProfitability = quality.SubProfitability
	SubGrowth        = quality.SubGrowth
	SubMargins       = quality.SubMargins
	SubStability     = quality.SubStability
	SubSolvency      = quality.SubSolvency
)

// QualityDetail is the projection of the quality engine inside the score: the
// score of §13 plus the five sub-blocks, so the UI can show WHY quality scored
// what it scored without a second request.
type QualityDetail struct {
	Score         *float64                     `json:"score,omitempty"`
	Coverage      float64                      `json:"coverage"`
	Confidence    string                       `json:"confidence"`
	TaxRateSource string                       `json:"tax_rate_source,omitempty"`
	SubScores     map[string]*quality.SubScore `json:"sub_scores,omitempty"`
	Reasons       []string                     `json:"reasons,omitempty"`
}

// RelativeDetail is the projection of the relative engine (§16).
type RelativeDetail struct {
	Score           *float64 `json:"score,omitempty"`
	SectorScore     *float64 `json:"sector_score,omitempty"`
	HistoricalScore *float64 `json:"historical_score,omitempty"`
	Coverage        float64  `json:"coverage"`
	Confidence      string   `json:"confidence"`
	Reasons         []string `json:"reasons,omitempty"`
}

// QualityInput carries the quality result into the score. It is the ENGINE result,
// not a re-computation: the score reads what the quality stage computed (§27
// determinism: one computation per as_of, one owner).
type ScoreInput21 struct {
	Ticker string    `json:"ticker"`
	AsOf   time.Time `json:"as_of"`
	Price  float64   `json:"price"`

	// GrahamBase/DCFBase and their confidence/reasons come from the persisted
	// valuation_results 2.0.0 row: the score NEVER recomputes a valuation (§9).
	GrahamBase       *float64 `json:"graham_base,omitempty"`
	DCFBase          *float64 `json:"dcf_base,omitempty"`
	GrahamConfidence string   `json:"graham_confidence,omitempty"`
	DCFConfidence    string   `json:"dcf_confidence,omitempty"`
	GrahamReasons    []string `json:"graham_reasons,omitempty"`
	DCFReasons       []string `json:"dcf_reasons,omitempty"`

	// Quality and Relative are the results of the two engines of M6c. A nil engine
	// result (or a nil score inside it) makes the dimension INVALID: the score
	// renormalises and the trace records why.
	Quality  *QualityDetail  `json:"quality,omitempty"`
	Relative *RelativeDetail `json:"relative,omitempty"`

	// Market context (§17): SMA50, SMA200 and the two momenta. Trend of M4b
	// RENAMED to market_context with the same inputs and the same scoring; the
	// rename is the taxonomy change, the formula is not.
	SMA50       *float64 `json:"sma50,omitempty"`
	SMA200      *float64 `json:"sma200,omitempty"`
	Momentum6m  *float64 `json:"momentum6m,omitempty"`
	Momentum12m *float64 `json:"momentum12m,omitempty"`

	MarginOfSafety float64 `json:"margin_of_safety,omitempty"`
}

// Weights21 returns the five weights of §12 in canonical order, taken from the
// resolved ModelConfig (precedence code < env < set, ADR D20).
//
// It reads the typed fields, never a free map: a weight that is not a field of
// the configuration cannot be forgotten by a typo, and every source of a weight
// has to go through ModelConfig.Validate before it gets here.
func Weights21(mc modelcfg.ModelConfig) [5]float64 {
	return [5]float64{
		mc.GrahamWeight,
		mc.DCFWeight,
		mc.QualityWeight,
		mc.RelativeWeight,
		mc.MarketContextWeight,
	}
}

// §12 weights of 2.1.0 as the CODES default (graham 0.15, dcf 0.20, quality 0.35,
// relative 0.20, market_context 0.10 — see the header note on the deviation).
// modelcfg.DefaultModelConfig is the authority; these constants exist so the test
// suite and the report can name the numbers without retyping the literals.
var (
	WeightGraham21        = modelcfg.DefaultGrahamWeight
	WeightDCF21           = modelcfg.DefaultDCFWeight
	WeightQuality21       = modelcfg.DefaultQualityWeight
	WeightRelative21      = modelcfg.DefaultRelativeWeight
	WeightMarketContext21 = modelcfg.DefaultMarketContextWeight
)

// DimensionOrder21 is the canonical order of the five dimensions. Persisted traces
// keep it, so a reader never depends on map iteration order.
func DimensionOrder21() []string {
	return []string{DimGrahamV21, DimDCFV21, DimQualityV21, DimRelativeV21, DimMarketContextV21}
}

// Dimension21 is one weighted dimension of 2.1.0. Same contract as
// DimensionScore: nil Score = invalid, and WeightConfigured is the configured
// weight while the applied divisor is the result's WeightUsed.
type Dimension21 struct {
	Name   string   `json:"name"`
	Score  *float64 `json:"score"`
	Valid  bool     `json:"valid"`
	Weight float64  `json:"weight_configured"`
}

// Result21 is the output of CalculateScore21.
type Result21 struct {
	Score            int           `json:"score"`
	Signal           string        `json:"signal"`
	Justification    string        `json:"justification"`
	Dimensions       []Dimension21 `json:"dimensions"`
	WeightConfigured float64       `json:"weight_configured"`
	WeightUsed       float64       `json:"weight_used"`
	ModelVersion     string        `json:"model_version"`
	// ParameterSetID is the identity of the configuration that produced this
	// score (§26). It is persisted and exposed, never inferred from the numbers.
	ParameterSetID int64  `json:"parameter_set_id,omitempty"`
	ParameterSet   string `json:"parameter_set,omitempty"`
}

// CalculateScore21 computes the score of SPEC §18 over the five dimensions.
//
// There is no neutral: a dimension with no computable score is Valid=false and is
// excluded from both the numerator and the divisor (active_weight_sum). The
// ONLY place a literal 50 can still appear is the "no dimension at all" case,
// which is the last-resort value the 2.0.0 engine already documents.
func CalculateScore21(in ScoreInput21, mc modelcfg.ModelConfig) Result21 {
	return calculateScore21(in, Weights21(mc), mc)
}

// calculateScore21 is the shared core: the weights are an argument, so a replay
// can pass the weights PERSISTED IN THE TRACE instead of today's configuration.
// Without that seam, a score reproduced after someone edits a parameter set would
// silently stop being a reproduction.
func calculateScore21(in ScoreInput21, w [5]float64, mc modelcfg.ModelConfig) Result21 {
	if in.MarginOfSafety <= 0 || in.MarginOfSafety > 100 {
		in.MarginOfSafety = 30
	}

	dims := []Dimension21{
		dimension21(DimGrahamV21, scoreGraham(in.Price, in.GrahamBase, in.MarginOfSafety), w[0]),
		dimension21(DimDCFV21, scoreDCF(in.Price, in.DCFBase, in.MarginOfSafety), w[1]),
		dimension21(DimQualityV21, qualityScore(in.Quality), w[2]),
		dimension21(DimRelativeV21, relativeScoreIn(in.Relative), w[3]),
		dimension21(DimMarketContextV21, scoreMarketContext21(in.SMA50, in.SMA200, in.Momentum6m, in.Momentum12m), w[4]),
	}

	total, weightConfigured, weightUsed := 0.0, 0.0, 0.0
	for _, d := range dims {
		weightConfigured += d.Weight
		if !d.Valid {
			continue
		}
		total += *d.Score * d.Weight
		weightUsed += d.Weight
	}
	final := 50 // ninguna dimensión válida: neutral de último resort (documentado)
	if weightUsed > 0 {
		final = int(total/weightUsed + 0.5)
	}
	if final < 0 {
		final = 0
	}
	if final > 100 {
		final = 100
	}
	signal := SignalForScore(final)

	return Result21{
		Score:            final,
		Signal:           signal,
		Justification:    generateJustification21(in.Ticker, final, signal, dims),
		Dimensions:       dims,
		WeightConfigured: round6(weightConfigured),
		WeightUsed:       round6(weightUsed),
		ModelVersion:     ModelVersion21,
		ParameterSetID:   mc.ParameterSetID,
		ParameterSet:     mc.ParameterSetName,
	}
}

func dimension21(name string, score *float64, weight float64) Dimension21 {
	return Dimension21{Name: name, Score: score, Valid: score != nil, Weight: weight}
}

// qualityScore reads the quality engine result. A missing result AND a nil score
// are both invalid: there is no third reading.
func qualityScore(q *QualityDetail) *float64 {
	if q == nil || q.Score == nil {
		return nil
	}
	if math.IsNaN(*q.Score) || math.IsInf(*q.Score, 0) {
		return nil
	}
	s := *q.Score
	if s < 0 {
		s = 0
	}
	if s > 100 {
		s = 100
	}
	return &s
}

func relativeScoreIn(r *RelativeDetail) *float64 {
	if r == nil || r.Score == nil {
		return nil
	}
	if math.IsNaN(*r.Score) || math.IsInf(*r.Score, 0) {
		return nil
	}
	s := *r.Score
	if s < 0 {
		s = 0
	}
	if s > 100 {
		s = 100
	}
	return &s
}

// scoreMarketContext21 is §17 with the M4b neutral REMOVED.
//
// Same ladder as scoreTrend (2.0.0) — the taxonomy changed, the formula did not —
// with the one difference §18 demands: with no SMA and no momentum there is
// NOTHING to score, so it returns nil and the dimension leaves the average
// instead of contributing the 50 that scoreTrend starts from. The formula is
// shared with 2.0.0 via scoreTrend; the test asserts equality for all usable
// inputs so they cannot drift apart silently.
func scoreMarketContext21(sma50, sma200, momentum6m, momentum12m *float64) *float64 {
	hasSMAs := sma50 != nil && sma200 != nil && *sma50 > 0 && *sma200 > 0
	if !hasSMAs && momentum6m == nil && momentum12m == nil {
		return nil
	}
	v := scoreTrend(sma50, sma200, momentum6m, momentum12m)
	return &v
}

// Trace21 is the persisted snapshot of 2.1.0 (ADR D27): everything needed to
// REPRODUCE the score and to explain it, without reading any other table.
//
// It is a versioned structure on purpose: TraceVersion gates its decoding, so a
// future field change is a new version instead of a silent reinterpretation of an
// old row.
type Trace21 struct {
	TraceVersion string        `json:"trace_version"`
	Ticker       string        `json:"ticker"`
	AsOf         time.Time     `json:"as_of"`
	Price        float64       `json:"price"`
	ModelVersion string        `json:"model_version"`
	Dimensions   []Dimension21 `json:"dimensions"`

	WeightConfigured float64 `json:"weight_configured"`
	WeightUsed       float64 `json:"weight_used"`

	ParameterSetID   int64  `json:"parameter_set_id,omitempty"`
	ParameterSetName string `json:"parameter_set,omitempty"`

	Quality  *QualityDetail  `json:"quality,omitempty"`
	Relative *RelativeDetail `json:"relative,omitempty"`

	MarketContext *MarketContextDetail `json:"market_context,omitempty"`

	// The valuation blocks are COPIED from the 2.0.0 row that was read, never
	// recomputed (§9): the 2.1.0 score does not own a valuation.
	GrahamBase       *float64 `json:"graham_base,omitempty"`
	DCFBase          *float64 `json:"dcf_base,omitempty"`
	GrahamConfidence string   `json:"graham_confidence,omitempty"`
	DCFConfidence    string   `json:"dcf_confidence,omitempty"`
	// GrahamMOS and DCFMOS are the per-method margins of safety from the 2.0.0
	// valuation row (INFORMATIONAL ONLY). The 2.1.0 score uses a single
	// MarginOfSafety (field below) for both dimensions. These fields are kept
	// for auditability and are not used in score calculation (P2).
	GrahamMOS   *float64 `json:"graham_margin_of_safety,omitempty"`
	DCFMOS      *float64 `json:"dcf_margin_of_safety,omitempty"`
	Uncertainty *float64 `json:"uncertainty,omitempty"`
	Confidence  string   `json:"valuation_confidence,omitempty"`
	Reasons     []string `json:"reasons,omitempty"`

	// MarginOfSafety is the effective MOS used for the Graham/DCF dimensions.
	// It is persisted so that a replay with a different parameter set can show
	// the MOS that was actually applied (P0-2: H-2 root cause).
	//
	// FALLBACK: when the trace is unmarshaled, a missing or zero MarginOfSafety
	// defaults to 30 (the SPEC §11 default) in calculateScore21. This field is
	// therefore always effective for replay even if the original row was written
	// before this field existed or with an empty value.
	MarginOfSafety float64 `json:"margin_of_safety,omitempty"`
}

// MarketContextDetail is the §17 block of the trace (SMA50/SMA200/momentums).
type MarketContextDetail struct {
	SMA50       *float64 `json:"sma50,omitempty"`
	SMA200      *float64 `json:"sma200,omitempty"`
	Momentum6m  *float64 `json:"momentum6m,omitempty"`
	Momentum12m *float64 `json:"momentum12m,omitempty"`
}

// BuildTrace21 assembles the trace of a computed 2.1.0 result.
func BuildTrace21(in ScoreInput21, res Result21) *Trace21 {
	t := &Trace21{
		TraceVersion:     TraceVersion,
		Ticker:           in.Ticker,
		AsOf:             in.AsOf,
		Price:            in.Price,
		ModelVersion:     res.ModelVersion,
		Dimensions:       res.Dimensions,
		WeightConfigured: res.WeightConfigured,
		WeightUsed:       res.WeightUsed,
		ParameterSetID:   res.ParameterSetID,
		ParameterSetName: res.ParameterSet,
		Quality:          in.Quality,
		Relative:         in.Relative,
		GrahamBase:       in.GrahamBase,
		DCFBase:          in.DCFBase,
		GrahamConfidence: in.GrahamConfidence,
		DCFConfidence:    in.DCFConfidence,
		MarginOfSafety:   in.MarginOfSafety,
	}
	if in.SMA50 != nil || in.SMA200 != nil || in.Momentum6m != nil || in.Momentum12m != nil {
		t.MarketContext = &MarketContextDetail{
			SMA50: in.SMA50, SMA200: in.SMA200,
			Momentum6m: in.Momentum6m, Momentum12m: in.Momentum12m,
		}
	}
	var reasons []string
	reasons = append(reasons, in.GrahamReasons...)
	reasons = append(reasons, in.DCFReasons...)
	if in.Quality != nil {
		reasons = append(reasons, in.Quality.Reasons...)
	}
	if in.Relative != nil {
		reasons = append(reasons, in.Relative.Reasons...)
	}
	t.Reasons = reason.Normalize(reasons)
	return t
}

// Marshal serialises the trace for the JSONB column.
func (t *Trace21) Marshal() ([]byte, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("score: marshal trace %s: %w", t.Ticker, err)
	}
	return b, nil
}

// ErrUnsupportedTraceVersion is returned for a trace_version this build cannot
// decode. Refusing is the point: an unknown layout read with today's assumptions
// is a wrong number presented as a reproduced one.
var ErrUnsupportedTraceVersion = fmt.Errorf("score: trace_version no soportada")

// ParseTrace decodes a persisted trace and validates that it is complete enough
// to replay (ADR D27).
func ParseTrace(raw []byte) (*Trace21, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: snapshot vacío", modelcfg.ErrSnapshotIncomplete)
	}
	var t Trace21
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("score: parse trace: %w", err)
	}
	if t.TraceVersion == "" {
		return nil, fmt.Errorf("%w: falta trace_version", modelcfg.ErrSnapshotIncomplete)
	}
	if t.TraceVersion != TraceVersion {
		return nil, fmt.Errorf("%w: %q (soportada: %q)", ErrUnsupportedTraceVersion, t.TraceVersion, TraceVersion)
	}
	if t.Ticker == "" {
		return nil, fmt.Errorf("%w: falta ticker", modelcfg.ErrSnapshotIncomplete)
	}
	if len(t.Dimensions) == 0 {
		return nil, fmt.Errorf("%w: el trace no tiene dimensiones", modelcfg.ErrSnapshotIncomplete)
	}
	// Unknown fields are ignored on purpose: a future writer may add fields, and a
	// reader that failed on them could not read its own history.
	t.Reasons = reason.Normalize(t.Reasons)
	return &t, nil
}

// ToScoreInput rebuilds the engine input FROM THE TRACE ONLY.
//
// This is what makes the replay independent of the database: given the trace, the
// same score comes out. It returns ErrSnapshotIncomplete when a field the
// calculation needed is absent, so a truncated snapshot fails loudly instead of
// producing a different number from the original one.
func (t *Trace21) ToScoreInput() (ScoreInput21, error) {
	in := ScoreInput21{
		Ticker:           t.Ticker,
		AsOf:             t.AsOf,
		Price:            t.Price,
		GrahamBase:       t.GrahamBase,
		DCFBase:          t.DCFBase,
		GrahamConfidence: t.GrahamConfidence,
		DCFConfidence:    t.DCFConfidence,
		Quality:          t.Quality,
		Relative:         t.Relative,
		MarginOfSafety:   t.MarginOfSafety,
	}
	if t.MarketContext != nil {
		in.SMA50, in.SMA200 = t.MarketContext.SMA50, t.MarketContext.SMA200
		in.Momentum6m, in.Momentum12m = t.MarketContext.Momentum6m, t.MarketContext.Momentum12m
	}
	if t.Ticker == "" || len(t.Dimensions) == 0 {
		return in, fmt.Errorf("%w: trace sin ticker o dimensiones", modelcfg.ErrSnapshotIncomplete)
	}
	return in, nil
}

// RecomputeFromTrace reproduces the score from the trace ALONE (ADR D27).
//
// The weights come from the trace's own dimensions, not from the current
// ModelConfig: a parameter set edited after the row was written must not change
// what that row reproduces. mc is used ONLY for the provenance echoed in the
// result, never for the arithmetic.
func RecomputeFromTrace(t *Trace21, mc modelcfg.ModelConfig) (Result21, error) {
	in, err := t.ToScoreInput()
	if err != nil {
		return Result21{}, err
	}
	var w [5]float64
	seen := 0
	for _, d := range t.Dimensions {
		switch d.Name {
		case DimGrahamV21:
			w[0] = d.Weight
		case DimDCFV21:
			w[1] = d.Weight
		case DimQualityV21:
			w[2] = d.Weight
		case DimRelativeV21:
			w[3] = d.Weight
		case DimMarketContextV21:
			w[4] = d.Weight
		default:
			return Result21{}, fmt.Errorf("%w: dimensión desconocida %q en el trace", modelcfg.ErrSnapshotIncomplete, d.Name)
		}
		seen++
	}
	if seen != 5 {
		return Result21{}, fmt.Errorf("%w: el trace declara %d de 5 dimensiones", modelcfg.ErrSnapshotIncomplete, seen)
	}
	return calculateScore21(in, w, mc), nil
}

// generateJustification21 renders the Spanish justification of 2.1.0, naming the
// dimensions that were MISSING instead of quietly scoring them.
//
// A justification that omits an absent dimension is how a score of 72 with
// weight_used 0.55 reads as a verdict on a company when it is a verdict on the
// data available.
func generateJustification21(ticker string, score int, signal string, dims []Dimension21) string {
	var b []string
	b = append(b, fmt.Sprintf("%s: %d/100 (%s).", ticker, score, signal))
	var parts, missing []string
	for _, d := range dims {
		if d.Valid && d.Score != nil {
			parts = append(parts, fmt.Sprintf("%s %.0f (peso %.2f)", d.Name, *d.Score, d.Weight))
		} else {
			missing = append(missing, d.Name)
		}
	}
	// Cada frase se une con ESPACIOS, no con joinES: la conjunción "y" sólo
	// pertenece a la lista interna de una frase. Aplicarla entre frases producía
	// "AAPL: 45/100 (mantener). y Dimensiones: ..." — una y suelta que delata que
	// el texto se armó con el helper equivocado.
	if len(parts) > 0 {
		b = append(b, "Dimensiones: "+joinES(parts)+".")
	}
	if len(missing) > 0 {
		b = append(b, "Sin datos (excluidas del cálculo): "+joinES(missing)+".")
	}
	return strings.Join(b, " ")
}

func joinES(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			if i == len(parts)-1 {
				out += " y "
			} else {
				out += ", "
			}
		}
		out += p
	}
	return out
}

// QualityDetailFrom projects a quality engine result into the score input.
//
// It is the ONLY sanctioned way to build it, so the score can never receive a
// quality block assembled from a different as_of than the one the score is for.
func QualityDetailFrom(r *quality.Result) *QualityDetail {
	if r == nil {
		return nil
	}
	return &QualityDetail{
		Score:         r.Score,
		Coverage:      r.Coverage,
		Confidence:    r.Confidence,
		TaxRateSource: r.TaxRateSource,
		SubScores:     r.SubScores,
		Reasons:       reason.Normalize(r.Reasons),
	}
}

// RelativeDetailFrom projects a relative engine result into the score input.
func RelativeDetailFrom(r *relative.Result) *RelativeDetail {
	if r == nil {
		return nil
	}
	return &RelativeDetail{
		Score:           r.Score,
		SectorScore:     r.SectorScore,
		HistoricalScore: r.HistoricalScore,
		Coverage:        r.Coverage,
		Confidence:      r.Confidence,
		Reasons:         reason.Normalize(r.Reasons),
	}
}

// RecomputeWithWeights re-scores the trace with the weights of a DIFFERENT
// configuration (B13: "replay with another parameter set").
//
// It exists as a separate entry point from RecomputeFromTrace on purpose. The
// default replay must use the weights the row was written with — that is what
// "deterministic" means here — while this one deliberately changes them. Mixing
// the two would mean a verification run could disagree with a what-if run for
// reasons nobody could see in the output.
//
// SEMANTICS: reproduces what WOULD HAVE BEEN scored with the new weights.
// The trace's MarginOfSafety (the one actually applied at write time) is used,
// NOT mc.TargetMarginOfSafety. The new config's weights are applied to the
// same dimension scores; only the weighted average changes.
func RecomputeWithWeights(t *Trace21, mc modelcfg.ModelConfig) (Result21, error) {
	in, err := t.ToScoreInput()
	if err != nil {
		return Result21{}, err
	}
	return calculateScore21(in, Weights21(mc), mc), nil
}
