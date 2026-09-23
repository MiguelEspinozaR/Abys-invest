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
// consumers when the snapshot is missing (nil → campo ausente).
type scoreResponse struct {
	storage.Score
	Dimensions []score.DimensionScore `json:"dimensions,omitempty"`
}

// dimensionsFromSnapshot recomputes the four score dimensions (valuation 35 /
// fundamentals 30 / comparables 20 / trend 15) from the exact inputs_snapshot
// persisted by the scores job (cmd/analytics: json.Marshal(score.ScoreInput)).
//
// Because score.CalculateScore is a pure, deterministic function of
// ScoreInput, the recomputed dimensions are EXACTLY the ones used to produce
// the persisted score/justification — no engine change, no new persistence.
//
// Degradation contract: an empty or unparseable snapshot returns nil (the
// persisted score/signal are still returned untouched; the API never fails
// because of a snapshot problem).
func dimensionsFromSnapshot(snapshot []byte) []score.DimensionScore {
	if len(snapshot) == 0 {
		return nil
	}
	var input score.ScoreInput
	if err := json.Unmarshal(snapshot, &input); err != nil {
		return nil
	}
	return score.CalculateScore(input).Dimensions
}
