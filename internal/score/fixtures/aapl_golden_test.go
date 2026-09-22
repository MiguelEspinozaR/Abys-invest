//go:build integration

package fixtures_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/miky/abys-invest/internal/score"
)

// Golden test del plan T14: el fixture documentado aapl_score.json fija los
// valores de referencia del score engine (inputs → output deterministico).
// Ejecución: go test -p 1 ./internal/score/fixtures -tags=integration -count=1

func TestAAPLGoldenscore(t *testing.T) {
	data, err := os.ReadFile("aapl_score.json")
	if err != nil {
		t.Fatalf("leer fixture: %v", err)
	}
	var fx struct {
		Input  score.ScoreInput `json:"input"`
		Expect struct {
			Score        int    `json:"score"`
			Signal       string `json:"signal"`
			ModelVersion string `json:"model_version"`
		} `json:"expect"`
	}
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	got := score.CalculateScore(fx.Input)

	if got.Score != fx.Expect.Score {
		t.Errorf("golden: score esperado %d, got %d", fx.Expect.Score, got.Score)
	}
	if got.Signal != fx.Expect.Signal {
		t.Errorf("golden: señal esperada %q, got %q", fx.Expect.Signal, got.Signal)
	}
	if got.ModelVersion != fx.Expect.ModelVersion {
		t.Errorf("golden: model version esperada %q, got %q", fx.Expect.ModelVersion, got.ModelVersion)
	}
	if got.Justification == "" {
		t.Error("golden: justificación vacía")
	}
	if len(got.Dimensions) != 4 {
		t.Fatalf("golden: se esperaban 4 dimensiones, got %d", len(got.Dimensions))
	}
	var totalWeight float64
	for _, d := range got.Dimensions {
		totalWeight += d.Weight
		if d.Score < 0 || d.Score > 100 {
			t.Errorf("golden: dimensión %s fuera de rango: %v", d.Name, d.Score)
		}
	}
	if totalWeight < 0.99 || totalWeight > 1.01 {
		t.Errorf("golden: pesos no suman 1.0 (%.3f)", totalWeight)
	}
}
