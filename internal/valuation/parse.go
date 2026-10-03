package valuation

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/miky/abys-invest/internal/modelcfg"
)

// B12: decoders of the persisted inputs_snapshot of §26.
//
// WHY A SNAPSHOT NEEDS A PARSER AND NOT JUST json.Unmarshal
// ----------------------------------------------------------
// A snapshot is the promise of §28: "the same inputs give the same result". An
// Unmarshal into Snapshot satisfies the letter of that and breaks its spirit in
// two ways that matter in production:
//
//  1. It cannot tell "the field was absent" from "the field was present and
//     null". For a snapshot those are different facts: null means "not
//     computable" (legitimate, explainable), absent means "this snapshot was
//     written by a different revision and cannot be replayed".
//  2. It cannot tell "the snapshot is the one this build knows" from "the
//     snapshot is from a future revision that happens to use the same field
//     names". A silently ignored field is a silently wrong replay.
//
// So ParseSnapshot validates the identity of the document (model_version) and
// refuses the ambiguous ones, returning a typed error the caller can distinguish.

// ErrUnsupportedModelVersion is returned for a snapshot written by a revision
// this build cannot read.
var ErrUnsupportedModelVersion = fmt.Errorf("valuation: model_version no soportada")

// ParseSnapshot decodes and validates an inputs_snapshot of valuation 2.0.0.
func ParseSnapshot(raw []byte) (*Snapshot, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: snapshot vacío", modelcfg.ErrSnapshotIncomplete)
	}
	var probe struct {
		ModelVersion string `json:"model_version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("valuation: parse snapshot: %w", err)
	}
	if strings.TrimSpace(probe.ModelVersion) == "" {
		return nil, fmt.Errorf("%w: falta model_version", modelcfg.ErrSnapshotIncomplete)
	}
	if probe.ModelVersion != ModelVersion {
		return nil, fmt.Errorf("%w: %q (soportada: %q)", ErrUnsupportedModelVersion, probe.ModelVersion, ModelVersion)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("valuation: parse snapshot: %w", err)
	}
	if snap.Inputs.AsOf.IsZero() {
		return nil, fmt.Errorf("%w: falta inputs.as_of", modelcfg.ErrSnapshotIncomplete)
	}
	return &snap, nil
}

// PFcfFromSnapshot returns the P/FCF of §15 for the snapshot.
//
// ADR D9 says the metric is REUSED, not recomputed: the relative engine of §16
// must not define a second P/FCF. valuation_results does not persist a p_fcf
// column, so the only honest source is the snapshot the row already carries.
// Returns nil when the inputs cannot produce it, which is the correct reading of
// "not computable" and not an error.
func PFcfFromSnapshot(raw []byte) (*float64, error) {
	snap, err := ParseSnapshot(raw)
	if err != nil {
		return nil, err
	}
	if snap.Inputs.FreeCashFlow == nil || *snap.Inputs.FreeCashFlow == 0 {
		return nil, nil
	}
	if snap.Inputs.Price == nil || *snap.Inputs.Price <= 0 {
		return nil, nil
	}
	p := *snap.Inputs.Price / *snap.Inputs.FreeCashFlow
	return &p, nil
}

// AsOf returns the snapshot's as_of, or the zero time when unparseable.
func AsOf(snap *Snapshot) time.Time {
	if snap == nil {
		return time.Time{}
	}
	return snap.Inputs.AsOf
}
