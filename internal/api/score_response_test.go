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
		GrahamBase: fptr(144.45), DCFBase: fptr(121.16),
		GrahamConfidence: "medium", DCFConfidence: "medium",
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
// cmd/analytics: json.Marshal(ScoreInput)) debe devolver las 5 dimensiones del
// contrato 2.0.0 (Graham 15 / DCF 20 / fundamentals 30 / comparables 20 /
// trend 15), y ser EXACTAMENTE igual a lo que el motor produce con el mismo
// input.
func TestDimensionsFromSnapshot(t *testing.T) {
	input := knownInput()
	snapshot, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	got := dimensionsFromSnapshot(snapshot, score.ModelVersion)
	if len(got) != 5 {
		t.Fatalf("se esperaban 5 dimensiones, got %d (%+v)", len(got), got)
	}

	wantNames := []string{score.DimGraham, score.DimDCF, score.DimFundamentals, score.DimComparables, score.DimTrend}
	wantWeights := []float64{score.WeightGraham, score.WeightDCF, score.WeightFundaments, score.WeightComparables, score.WeightTrend}
	wantWeightsDesc := []string{"15%", "20%", "30%", "20%", "15%"}
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
		if got[i].Name != expected[i].Name || !equalScore(got[i].Score, expected[i].Score) || got[i].Weight != expected[i].Weight {
			t.Fatalf("dimensión %d: recompute %+v != motor %+v", i, got[i], expected[i])
		}
	}

	// Los pesos suman exactamente 15+20+30+20+15 = 100%.
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
			if got := dimensionsFromSnapshot(snap, score.ModelVersion); got != nil {
				t.Fatalf("snapshot %q: se esperaba nil, got %+v", snap, got)
			}
		})
	}

	t.Run("json-minimo-no-falla", func(t *testing.T) {
		got := dimensionsFromSnapshot([]byte(`{"price":230}`), score.ModelVersion)
		if len(got) != 5 {
			t.Fatalf("json mínimamente parseable debe devolver las 5 dimensiones (neutras), got %+v", got)
		}
	})
}

// TestDimensionsFromSnapshotVersionIsolation — D3: una fila persistida por otra
// versión del modelo NO puede describirse con las dimensiones de la versión
// actual (en M6b el bloque "valuation" 35% son dos dimensiones 15+20). El
// endpoint omite `dimensions` en vez de devolver un desglose que el score
// persistido nunca usó.
func TestDimensionsFromSnapshotVersionIsolation(t *testing.T) {
	snapshot, err := json.Marshal(knownInput())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	for _, v := range []string{"1.0.0", "1.2.0", "0.9.0", "2.0.1"} {
		t.Run(v, func(t *testing.T) {
			if got := dimensionsFromSnapshot(snapshot, v); got != nil {
				t.Fatalf("model_version %s: se esperaba nil (aislamiento de versión), got %+v", v, got)
			}
		})
	}
}

// equalScore compara el puntero de §2.0.0 (nil = dimensión inválida) sin
// desreferenciar un nil.
func equalScore(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// M6c-T1 W6b: la lista CERRADA de `?model_version=` conoce la revisión vigente
// 2.2.0 y conserva legibles las cuatro revisiones históricas (§26: se lee la
// historia, no se reescribe). Una cadena malformada NO pasa: el gate existe
// para poder responder "esa revisión no existe" en vez de un 404 confuso.
func TestIsKnownModelVersionIncluyeLaVigenteYLasHistoricas(t *testing.T) {
	for _, v := range []string{"1.1.0", "2.0.0", "2.1.0", "2.2.0"} {
		if !isKnownModelVersion(v) {
			t.Errorf("isKnownModelVersion(%q) = false, esperado true", v)
		}
	}
	for _, v := range []string{"3.0.0", "2.2.0 ", " 2.2.0", "2.2", "2.10.0", "1.1.0/2.0.0", ""} {
		if isKnownModelVersion(v) {
			t.Errorf("isKnownModelVersion(%q) = true, esperado false (lista cerrada)", v)
		}
	}
	// Los literales de arriba deben ser LAS CONSTANTES de los motores: si el
	// score sube a 2.3.0 sin tocar esta lista, el gate no lo deja fuera en
	// silencio.
	if !isKnownModelVersion(score.ModelVersion22) {
		t.Errorf("la revisión vigente del motor (%q) debe estar en la gate", score.ModelVersion22)
	}
	if score.ModelVersion22 != "2.2.0" {
		t.Errorf("score.ModelVersion22: esperado \"2.2.0\", got %q", score.ModelVersion22)
	}
	if !isKnownModelVersion(score.ModelVersion21) || score.ModelVersion21 != "2.1.0" {
		t.Errorf("la historia 2.1.0 debe seguir legible: %q", score.ModelVersion21)
	}
	if !isKnownModelVersion(score.ModelVersion) {
		t.Errorf("2.0.0 (%q) debe seguir legible", score.ModelVersion)
	}
}
