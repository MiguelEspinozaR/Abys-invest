package score

import (
	"encoding/json"
	"testing"

	"github.com/miky/abys-invest/internal/modelcfg"
)

// TestTrace21MarginOfSafetyZeroRoundTrips is the CA of (h): with `omitempty`
// removed, a row written with MOS=0 persists the value and ParseTrace returns 0,
// not the invented 30.
//
// Why the field matters: the trace exists so a replay shows the MOS that was
// ACTUALLY applied (P0-2). With omitempty a 0 vanished from the JSON, and the
// reader could no longer tell "MOS desactivado" from "campo ausente" — so the
// only observable behaviour was indistinguishable from the default. Recording 0
// explicitly is what turns the trace into an audit record.
func TestTrace21MarginOfSafetyZeroRoundTrips(t *testing.T) {
	in := completeInput21()
	in.MarginOfSafety = 0
	res := CalculateScore21(in, modelcfg.DefaultModelConfig())
	trace := BuildTrace21(in, res)

	if trace.MarginOfSafety != 0 {
		t.Fatalf("el trace construido debe llevar el MOS aplicado, got %v", trace.MarginOfSafety)
	}

	raw, err := trace.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// The field must be PRESENT in the JSON with value 0 (not omitted).
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal del trace: %v", err)
	}
	mosRaw, ok := probe["margin_of_safety"]
	if !ok {
		t.Fatal("margin_of_safety debe persistirse siempre (sin omitempty)")
	}
	if string(mosRaw) != "0" {
		t.Fatalf("margin_of_safety debe persistir 0, got %s", mosRaw)
	}

	parsed, err := ParseTrace(raw)
	if err != nil {
		t.Fatalf("ParseTrace: %v", err)
	}
	if parsed.MarginOfSafety != 0 {
		t.Fatalf("el round-trip debe preservar MOS=0, got %v (30 sería inventado)", parsed.MarginOfSafety)
	}

	// ToScoreInput is the reader the replay uses; it must carry the same 0.
	back, err := parsed.ToScoreInput()
	if err != nil {
		t.Fatalf("ToScoreInput: %v", err)
	}
	if back.MarginOfSafety != 0 {
		t.Fatalf("ToScoreInput debe preservar MOS=0, got %v", back.MarginOfSafety)
	}
}

// TestTrace21MarginOfSafetyNonZeroRoundTrips keeps the obvious case honest: 30
// must still survive (the field is not accidentally dropped for everyone).
func TestTrace21MarginOfSafetyNonZeroRoundTrips(t *testing.T) {
	in := completeInput21()
	in.MarginOfSafety = 42.5
	res := CalculateScore21(in, modelcfg.DefaultModelConfig())
	raw, err := BuildTrace21(in, res).Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseTrace(raw)
	if err != nil {
		t.Fatalf("ParseTrace: %v", err)
	}
	if parsed.MarginOfSafety != 42.5 {
		t.Fatalf("MOS=42.5 no debe alterarse en el round-trip, got %v", parsed.MarginOfSafety)
	}
}

// TestTrace21LegacyRowWithoutMOSLoadsThirty covers the OTHER side of the
// migration: a row written BEFORE the field existed has no key, and the reader
// must keep applying the SPEC §11 default of 30 rather than treating the
// absence as "MOS 0 desactivado".
//
// This is the behaviour migration 017 makes EXPLICIT in the database: every
// existing 2.1.0 row gets margin_of_safety = 30 written into its trace, which is
// the number the parse was already using implicitly.
func TestTrace21LegacyRowWithoutMOSLoadsThirty(t *testing.T) {
	in := completeInput21()
	in.MarginOfSafety = 30
	res := CalculateScore21(in, modelcfg.DefaultModelConfig())

	// Reproduce a LEGACY payload: a trace from before the field existed.
	raw, err := BuildTrace21(in, res).Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	delete(legacy, "margin_of_safety")
	legacyRaw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}

	parsed, err := ParseTrace(legacyRaw)
	if err != nil {
		t.Fatalf("ParseTrace de una fila antigua: %v", err)
	}
	if parsed.MarginOfSafety != 0 {
		t.Fatalf("una fila sin el campo se lee como 0 y el motor aplica el default, got %v", parsed.MarginOfSafety)
	}

	// The DEFAULT lives in the calculation, so the replay of a legacy row
	// reproduces the same score as the row that did carry 30.
	again, err := RecomputeFromTrace(parsed, modelcfg.DefaultModelConfig())
	if err != nil {
		t.Fatalf("RecomputeFromTrace de una fila antigua: %v", err)
	}
	if again.Score != res.Score {
		t.Fatalf("una fila antigua sin margin_of_safety debe reproducir el score (default 30): %d vs %d",
			again.Score, res.Score)
	}
}
