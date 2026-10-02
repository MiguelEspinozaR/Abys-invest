package api

import (
	"encoding/json"

	"github.com/miky/abys-invest/internal/score"
	"github.com/miky/abys-invest/internal/storage"
)

// scoreResponse is the enriched payload of GET /score/{ticker} (and each item
// of GET /scores): every persisted field of storage.Score plus the dimension
// breakdown recomputed from the inputs_snapshot (plan M4, CA M4-1).
//
// Additive and backwards compatible: the embed flattens the exact previous
// JSON contract and `dimensions` is a new sibling field; clients that ignore
// it keep working unchanged. `dimensions,omitempty` avoids surprising
// consumers when the snapshot is missing or belongs to another model version
// (nil → campo ausente, ver dimensionsFromSnapshot).
type scoreResponse struct {
	storage.Score
	Dimensions []score.DimensionScore `json:"dimensions,omitempty"`
	// §18 auditability: the configured weights always sum to 1, and
	// WeightUsed is the sum of the weights of the dimensions that were really
	// computable. A renormalised score is then explicable ("DCF no disponible →
	// se repartió su 20%") instead of mysteriously different. Both are additive
	// and omitted when there are no dimensions at all.
	WeightConfigured *float64 `json:"weight_configured,omitempty"`
	WeightUsed       *float64 `json:"weight_used,omitempty"`
}

// newScoreResponse builds the enriched payload of one score row.
func newScoreResponse(s storage.Score) scoreResponse {
	resp := scoreResponse{
		Score:      s,
		Dimensions: dimensionsFromSnapshot(s.InputsSnapshot, s.ModelVersion),
	}
	configured, used := weightSums(resp.Dimensions)
	resp.WeightConfigured, resp.WeightUsed = configured, used
	return resp
}

// weightSums returns the configured weight sum (always 1 in 2.0.0) and §18's
// active_weight_sum. nil when there are no dimensions (nothing to sum).
func weightSums(dims []score.DimensionScore) (configured, used *float64) {
	if len(dims) == 0 {
		return nil, nil
	}
	c, u := 0.0, 0.0
	for _, d := range dims {
		c += d.Weight
		if d.Valid {
			u += d.Weight
		}
	}
	return &c, &u
}

// dimensionsFromSnapshot recomputes the score dimensions from the exact
// inputs_snapshot persisted by the scores job (json.Marshal(score.ScoreInput)).
//
// Because score.CalculateScore is a pure, deterministic function of ScoreInput,
// the recomputed dimensions are EXACTLY the ones used to produce the persisted
// score/justification — no engine change, no new persistence (plan M4, CA M4-1).
//
// Two guards, both from M6b:
//
//   - VERSION ISOLATION (D3). M6b splits the 35% "valuation" dimension into
//     Graham 15% + DCF 20%. Recomputing a 1.x snapshot with the 2.0.0 engine
//     would return five dimensions with weights the persisted score never used,
//     so the endpoint would describe a score it did not produce. A row whose
//     model_version is not the current one therefore gets NO dimensions
//     (omitempty ⇒ the key disappears) instead of wrong ones.
//   - Degradation. An empty or unparseable snapshot returns nil; the persisted
//     score/signal are still returned untouched and the API never fails because
//     of a snapshot problem.
func dimensionsFromSnapshot(snapshot []byte, modelVersion string) []score.DimensionScore {
	if len(snapshot) == 0 {
		return nil
	}
	if modelVersion != "" && modelVersion != score.ModelVersion {
		// Persisted by another model version: its dimensions are not ours.
		return nil
	}
	var input score.ScoreInput
	if err := json.Unmarshal(snapshot, &input); err != nil {
		return nil
	}
	return score.CalculateScore(input).Dimensions
}
