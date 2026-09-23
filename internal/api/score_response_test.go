package api

import (
	"encoding/json"
	"testing"

	"github.com/miky/abys-invest/internal/score"
)

func fptr(v float64) *float64 { return &v }

// knownInput es un ScoreInput determinista (mismas métricas que el fixture
// internal/score/fixtures/aapl_score.json) con unidades de la capa
// persistida: roe como fracción, fcf_yield en %.
func knownInput() score.ScoreInput {
	return score.ScoreInput{
		Ticker: "AAPL", Price: 230,
		GrahamIntrinsic: fptr(144.45), DCFIntrinsic: fptr(121.16),
		Metrics: map[string]*float64{
			"pe_ratio": fptr(26.2), "pb_ratio": fptr(43.0), "fcf_yield": fptr(4.1),
			"roe": fptr(1.61), "de_ratio": fptr(4.7),
		},
		SectorCount: 2,
		SMA50:       fptr(250), SMA200: fptr(200),
		Momentum6m: fptr(0.2), Momentum12m: fptr(0.35),
		MarginOfSafety: 30,
	}
}

// TestDimensionsFromSnapshot — un snapshot válido (tal como lo persiste
// cmd/analytics: json.Marshal(ScoreInput)) debe devolver las 4 dimensiones con
// nombres y pesos del contrato D5 (35/30/20/15), y ser EXACTAMENTE igual a lo
// que el motor produce con el mismo input.
func TestDimensionsFromSnapshot(t *testing.T) {
	input := knownInput()
	snapshot, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	got := dimensionsFromSnapshot(snapshot)
	if len(got) != 4 {
		t.Fatalf("se esperaban 4 dimensiones, got %d (%+v)", len(got), got)
	}

	wantNames := []string{score.DimValuation, score.DimFundamentals, score.DimComparables, score.DimTrend}
	wantWeights := []float64{score.WeightValuation, score.WeightFundaments, score.WeightComparables, score.WeightTrend}
	wantWeightsDesc := []string{"35%", "30%", "20%", "15%"}
	for i := range wantNames {
		if got[i].Name != wantNames[i] {
			t.Fatalf("dimensión %d: nombre esperado %q, got %q", i, wantNames[i], got[i].Name)
		}
		if got[i].Weight != wantWeights[i] {
			t.Fatalf("dimensión %s: peso esperado %s (%v), got %v", wantNames[i], wantWeightsDesc[i], wantWeights[i], got[i].Weight)
		}
	}

	// Coherencia con el motor: las dimensiones recomputadas deben coincidir
	// 1:1 con CalculateScore(input).Dimensions (determinismo puro).
	expected := score.CalculateScore(input).Dimensions
	for i := range expected {
		if got[i].Name != expected[i].Name || got[i].Score != expected[i].Score || got[i].Weight != expected[i].Weight {
			t.Fatalf("dimensión %d: recompute %+v != motor %+v", i, got[i], expected[i])
		}
	}

	// Los pesos suman exactamente 35+30+20+15 = 100%.
	total := 0.0
	for _, d := range got {
		total += d.Weight
	}
	if total < 0.9999 || total > 1.0001 {
		t.Fatalf("los pesos deben sumar 1, got %v", total)
	}
}

// TestDimensionsFromSnapshotDegrades — snapshot ausente o corrupto degrada a
// nil sin error (el endpoint sigue devolviendo score/signal persistidos).
// Un JSON mínimamente parseable (p. ej. `{"price":230}`) NO es error: el
// snapshot "real" del job persiste el ScoreInput completo y el motor degrada
// con elegancia a neutros.
func TestDimensionsFromSnapshotDegrades(t *testing.T) {
	for name, snap := range map[string][]byte{
		"nil":    nil,
		"vacio":  {},
		"basura": []byte("definitivamente no json"),
	} {
		t.Run(name, func(t *testing.T) {
			if got := dimensionsFromSnapshot(snap); got != nil {
				t.Fatalf("snapshot %q: se esperaba nil, got %+v", snap, got)
			}
		})
	}

	t.Run("json-minimo-no-falla", func(t *testing.T) {
		got := dimensionsFromSnapshot([]byte(`{"price":230}`))
		if len(got) != 4 {
			t.Fatalf("json mínimamente parseable debe devolver las 4 dimensiones (neutras), got %+v", got)
		}
	})
}
