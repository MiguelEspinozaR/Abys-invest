package api

import (
	"encoding/json"

	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/score"
	"github.com/miky/abys-invest/internal/storage"
)

// dimensionResponse is ONE dimension in the API contract, for BOTH revisions.
//
// It is a projection and not score.DimensionScore / score.Dimension21 on purpose:
// the wire format must not change shape when the engine does, and it must not
// require the API to know which engine produced a row. `weight` is kept
// alongside `weight_configured` because M4b clients read `weight`, and dropping
// it would break them for no reason: §18's applied divisor is `weight_used`, not
// this field.
type dimensionResponse struct {
	Name             string   `json:"name"`
	Score            *float64 `json:"score"`
	Valid            bool     `json:"valid"`
	Weight           float64  `json:"weight"` // alias de 2.0.0 (compat M4b)
	WeightConfigured float64  `json:"weight_configured"`
}

func fromDimension20(d score.DimensionScore) dimensionResponse {
	return dimensionResponse{Name: d.Name, Score: d.Score, Valid: d.Valid, Weight: d.Weight, WeightConfigured: d.Weight}
}

func fromDimension21(d score.Dimension21) dimensionResponse {
	return dimensionResponse{Name: d.Name, Score: d.Score, Valid: d.Valid, Weight: d.Weight, WeightConfigured: d.Weight}
}

// qualitySubBlockResponse is one of the five §13 sub-blocks, with its metrics.
type qualitySubBlockResponse struct {
	Name     string              `json:"name"`
	Score    *float64            `json:"score"`
	Weight   float64             `json:"weight"`
	Coverage float64             `json:"coverage"`
	Metrics  map[string]*float64 `json:"metrics,omitempty"`
}

// qualityResponse is the §13 block of 2.1.0: score, coverage, confidence, the
// tax rate provenance and the five sub-blocks (D26/Az3: they are what the UI has
// to show, not just a number).
type qualityResponse struct {
	Score         *float64                           `json:"score,omitempty"`
	Coverage      float64                            `json:"coverage"`
	Confidence    string                             `json:"confidence"`
	TaxRateSource string                             `json:"tax_rate_source,omitempty"`
	SubScores     map[string]qualitySubBlockResponse `json:"sub_scores,omitempty"`
	Reasons       []string                           `json:"reasons,omitempty"`
}

// relativeSideResponse is one side of §16 (sector o historical).
type relativeSideResponse struct {
	Score    *float64 `json:"score"`
	Coverage float64  `json:"coverage"`
}

// relativeResponse is the §16 block of 2.1.0. The two sides are SEPARATE fields
// because §16 asks for them separated: "cheap for its sector" and "cheap for
// itself" disagreeing is information.
type relativeResponse struct {
	Score           *float64 `json:"score,omitempty"`
	SectorScore     *float64 `json:"sector_score,omitempty"`
	HistoricalScore *float64 `json:"historical_score,omitempty"`
	Coverage        float64  `json:"coverage"`
	Confidence      string   `json:"confidence"`
	Reasons         []string `json:"reasons,omitempty"`
}

// scoreResponse is the enriched payload of GET /score/{ticker} (and each item
// of GET /scores): every persisted field of storage.Score plus the breakdown of
// the revision that produced the row.
//
// ADDITIVE, and gated by model_version (B15):
//
//	1.1.0 → no `dimensions` (the revision had none) and no quality/relative.
//	2.0.0 → its five M4b dimensions (graham/dcf/fundamentals/comparables/trend).
//	2.1.0 → the five §18 dimensions (graham/dcf/quality/relative/market_context)
//	         plus `quality` (with sub-blocks) and `relative` (sides separated).
//
// A row of a revision this build does not know gets NO dimensions rather than
// wrong ones: describing a score you did not produce is worse than omitting it.
type scoreResponse struct {
	storage.Score
	Dimensions []dimensionResponse `json:"dimensions,omitempty"`
	// §18 auditability: the configured weights sum to 1, and WeightUsed is the sum
	// of the weights of the dimensions that were really computable. A
	// renormalised score is then explicable instead of mysteriously different.
	WeightConfigured *float64          `json:"weight_configured,omitempty"`
	WeightUsed       *float64          `json:"weight_used,omitempty"`
	Quality          *qualityResponse  `json:"quality,omitempty"`
	Relative         *relativeResponse `json:"relative,omitempty"`
	// TraceVersion says which trace layout the `dimensions`/`quality`/`relative`
	// above were decoded from (ADR D27). It lets a client detect a future layout
	// instead of silently misreading the fields it already knows.
	TraceVersion string `json:"trace_version,omitempty"`
	// ParameterSet is the name of the parameter set that produced this score
	// (P2: visible in API for smoke verification).
	ParameterSet string `json:"parameter_set,omitempty"`
}

// newScoreResponse builds the enriched payload of one score row.
func newScoreResponse(s storage.Score) scoreResponse {
	resp := scoreResponse{Score: s}
	switch s.ModelVersion {
	case score.ModelVersion21:
		resp.Dimensions, resp.Quality, resp.Relative, resp.TraceVersion, resp.ParameterSet = blocksFromTrace21(s.InputsSnapshot)
	default:
		resp.Dimensions = dimensionsFromSnapshot(s.InputsSnapshot, s.ModelVersion)
	}
	configured, used := weightSums(resp.Dimensions)
	resp.WeightConfigured, resp.WeightUsed = configured, used
	return resp
}

// weightSums returns the configured weight sum and §18's active_weight_sum.
// With strict §18 weights (graham .15, dcf .20, quality .35, relative .15,
// market_context .05) the CONFIGURED sum is 0.90, not 1: the tenth point is
// deliberately unassigned and the score divides by the weight actually used.
// So this must be computed from the dimensions and never hardcoded to 1.
// nil when there are no dimensions (nothing to sum).
func weightSums(dims []dimensionResponse) (configured, used *float64) {
	if len(dims) == 0 {
		return nil, nil
	}
	c, u := 0.0, 0.0
	for _, d := range dims {
		c += d.WeightConfigured
		if d.Valid {
			u += d.WeightConfigured
		}
	}
	return &c, &u
}

// blocksFromTrace21 decodes the trace of a 2.1.0 row.
//
// The DIMENSIONS are recomputed from the trace (not read from the trace's own
// dimension list) so that what the API shows is what the engine produces from the
// stored inputs — the same guarantee M4b's dimensionsFromSnapshot gave for 2.0.0.
// An unreadable trace degrades to no dimensions/blocks, and the persisted
// score/signal still come back untouched: a snapshot problem never fails the
// endpoint.
func blocksFromTrace21(snapshot []byte) (dims []dimensionResponse, q *qualityResponse, r *relativeResponse, traceVersion string, parameterSet string) {
	if len(snapshot) == 0 {
		return nil, nil, nil, "", ""
	}
	trace, err := score.ParseTrace(snapshot)
	if err != nil {
		return nil, nil, nil, "", ""
	}
	res, err := score.RecomputeFromTrace(trace, modelcfg.ModelConfigFromEnv())
	if err != nil {
		return nil, nil, nil, trace.TraceVersion, trace.ParameterSetName
	}
	out := make([]dimensionResponse, 0, len(res.Dimensions))
	for _, d := range res.Dimensions {
		out = append(out, fromDimension21(d))
	}
	return out, qualityFromTrace(trace), relativeFromTrace(trace), trace.TraceVersion, trace.ParameterSetName
}

func qualityFromTrace(t *score.Trace21) *qualityResponse {
	if t.Quality == nil {
		return nil
	}
	out := &qualityResponse{
		Score: t.Quality.Score, Coverage: t.Quality.Coverage,
		Confidence: t.Quality.Confidence, TaxRateSource: t.Quality.TaxRateSource,
		Reasons: t.Quality.Reasons,
	}
	for name, sub := range t.Quality.SubScores {
		if sub == nil {
			continue
		}
		block := qualitySubBlockResponse{Name: sub.Name, Score: sub.Score, Weight: sub.Weight, Coverage: sub.Coverage}
		if len(sub.Metrics) > 0 {
			block.Metrics = map[string]*float64{}
			for _, m := range sub.Metrics {
				block.Metrics[m.Name] = m.Value
			}
		}
		if out.SubScores == nil {
			out.SubScores = map[string]qualitySubBlockResponse{}
		}
		out.SubScores[name] = block
	}
	return out
}

func relativeFromTrace(t *score.Trace21) *relativeResponse {
	if t.Relative == nil {
		return nil
	}
	return &relativeResponse{
		Score: t.Relative.Score, SectorScore: t.Relative.SectorScore,
		HistoricalScore: t.Relative.HistoricalScore, Coverage: t.Relative.Coverage,
		Confidence: t.Relative.Confidence, Reasons: t.Relative.Reasons,
	}
}

// dimensionsFromSnapshot recomputes the 2.0.0 dimensions from the exact
// inputs_snapshot persisted by the M4b/M6b scores job.
//
// VERSION ISOLATION (D3): recomputing a 1.x snapshot with the 2.0.0 engine would
// return five dimensions with weights the persisted score never used, so a row
// whose model_version is not 2.0.0 gets NO dimensions. 2.1.0 is handled by
// blocksFromTrace21 because its snapshot is a TRACE, not a ScoreInput.
func dimensionsFromSnapshot(snapshot []byte, modelVersion string) []dimensionResponse {
	if len(snapshot) == 0 {
		return nil
	}
	if modelVersion != "" && modelVersion != score.ModelVersion {
		return nil
	}
	var input score.ScoreInput
	if err := json.Unmarshal(snapshot, &input); err != nil {
		return nil
	}
	res := score.CalculateScore(input)
	out := make([]dimensionResponse, 0, len(res.Dimensions))
	for _, d := range res.Dimensions {
		out = append(out, fromDimension20(d))
	}
	return out
}

// isKnownModelVersion gates `?model_version=` (B15).
//
// A closed list, not a pass-through: un cliente podría pedir "2.1.0 " con un
// espacio o "3.0.0" y obtendría un 404 genérico que no distingue "no existe esa
// revisión" de "no hay score de AAPL para esa fecha". Con la lista cerrada el 400
// dice exactamente qué versiones puede pedir.
func isKnownModelVersion(v string) bool {
	switch v {
	case "1.1.0", score.ModelVersion, score.ModelVersion21:
		return true
	default:
		return false
	}
}
