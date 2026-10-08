package api

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/quality"
	"github.com/miky/abys-invest/internal/relative"
	"github.com/miky/abys-invest/internal/score"
	"github.com/miky/abys-invest/internal/storage"
)

// B15: el contrato de /score es ADITIVO y GATEADO por model_version. Estos tests
// no necesitan base de datos: construyen el snapshot que el job habría
// persistido y verifican lo que la API devolvería.

func f(v float64) *float64 { return &v }

// trace21Fixture is a complete 2.1.0 trace (HISTORY: written before W6b,
// stamped with the then-current revision) with sub-blocks, relative with both
// sides, market context and the valuation bases.
func trace21Fixture(t *testing.T) []byte {
	t.Helper()
	return traceFixtureVersion(t, score.ModelVersion21)
}

// trace22Fixture is the SAME trace as the CURRENT engine writes it (2.2.0).
func trace22Fixture(t *testing.T) []byte {
	t.Helper()
	return traceFixtureVersion(t, score.ModelVersion22)
}

// traceFixtureVersion builds one trace stamped with an explicit revision. The
// trace SCHEMA is identical for 2.1.0 and 2.2.0 (W5 changed the entries, not
// the shape), so the only difference between a history row and a current row on
// disk is this field — which is exactly what stamping it simulates: a row
// persisted by 2.1.0 carries "2.1.0" inside its trace, a row persisted today
// carries "2.2.0".
func traceFixtureVersion(t *testing.T, modelVersion string) []byte {
	t.Helper()
	in := score.ScoreInput21{
		Ticker: "TEST", AsOf: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), Price: 100,
		GrahamBase: f(150), DCFBase: f(120),
		GrahamConfidence: "medium", DCFConfidence: "low",
		GrahamReasons: []string{"mos_below_target"}, DCFReasons: []string{"wacc_fallback"},
		Quality: score.QualityDetailFrom(&quality.Result{
			Score: f(84.1), Coverage: 16.0 / 17.0, Confidence: quality.ConfidenceMedium,
			TaxRateSource: quality.TaxRateSourceConfigured,
			Reasons:       []string{quality.ReasonInterestExpenseMissing},
			SubScores: map[string]*quality.SubScore{
				quality.SubProfitability: {Name: quality.SubProfitability, Score: f(90), Weight: 0.2, Coverage: 0.8},
				quality.SubGrowth:        {Name: quality.SubGrowth, Score: nil, Weight: 0.2, Coverage: 0},
			},
		}),
		Relative: score.RelativeDetailFrom(&relative.Result{
			Score: f(70), SectorScore: f(75), HistoricalScore: f(62),
			Coverage: 8.0 / 9.0, Confidence: relative.ConfidenceMedium,
		}),
		SMA50: f(110), SMA200: f(100), Momentum6m: f(0.05), Momentum12m: f(0.02),
	}
	res := score.CalculateScore21(in, modelcfg.DefaultModelConfig())
	trace := score.BuildTrace21(in, res)
	trace.ModelVersion = modelVersion
	raw, err := trace.Marshal()
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}
	return raw
}

// B15: 2.1.0 expone las CINCO dimensiones de §18 con el orden canónico.
func TestScoreResponse21CincoDimensionesDeSec18(t *testing.T) {
	resp := newScoreResponse(storage.Score{
		SecurityID: 1, AsOf: time.Now(), Score: 72, Signal: "comprar",
		InputsSnapshot: trace21Fixture(t), ModelVersion: score.ModelVersion21,
	})
	want := []string{"graham", "dcf", "quality", "relative", "market_context"}
	if len(resp.Dimensions) != 5 {
		t.Fatalf("se esperaban 5 dimensiones, got %d (%+v)", len(resp.Dimensions), resp.Dimensions)
	}
	for i, w := range want {
		if resp.Dimensions[i].Name != w {
			t.Fatalf("dimensión %d: esperado %q, got %q", i, w, resp.Dimensions[i].Name)
		}
	}
	// §18 estricto: los cinco pesos suman 0.90, no 1.00. El JSON lo expone tal
	// cual para que el 0.10 sin asignar sea auditable desde el cliente.
	if resp.WeightConfigured == nil || math.Abs(*resp.WeightConfigured-0.90) > 1e-6 {
		t.Fatalf("weight_configured: esperado 0.90 (§18), got %v", resp.WeightConfigured)
	}
	if resp.WeightUsed == nil || math.Abs(*resp.WeightUsed-0.90) > 1e-6 {
		t.Fatalf("con las cinco dimensiones válidas weight_used: esperado 0.90, got %v", resp.WeightUsed)
	}
	if resp.TraceVersion != score.TraceVersion {
		t.Fatalf("trace_version ausente: %q", resp.TraceVersion)
	}
}

// `weight` sigue ahí por compatibilidad M4b, junto a `weight_configured`.
func TestScoreResponse21MantieneElAliasWeight(t *testing.T) {
	resp := newScoreResponse(storage.Score{ModelVersion: score.ModelVersion21, InputsSnapshot: trace21Fixture(t)})
	for _, d := range resp.Dimensions {
		if d.Weight != d.WeightConfigured {
			t.Fatalf("dimensión %s: weight %v != weight_configured %v", d.Name, d.Weight, d.WeightConfigured)
		}
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	dims, ok := m["dimensions"].([]any)
	if !ok || len(dims) != 5 {
		t.Fatalf("dimensions en el JSON: %v", m["dimensions"])
	}
	first := dims[0].(map[string]any)
	if _, ok := first["weight"]; !ok {
		t.Fatalf("el JSON debe conservar `weight` para clientes M4b: %v", first)
	}
	if _, ok := first["weight_configured"]; !ok {
		t.Fatalf("el JSON debe exponer `weight_configured`: %v", first)
	}
}

// B15: quality llega con sus sub-bloques (D26/Az3) y relative con los dos lados.
func TestScoreResponse21QualityYRelative(t *testing.T) {
	resp := newScoreResponse(storage.Score{ModelVersion: score.ModelVersion21, InputsSnapshot: trace21Fixture(t)})
	if resp.Quality == nil {
		t.Fatalf("falta el bloque quality de 2.1.0")
	}
	if resp.Quality.Score == nil || *resp.Quality.Score != 84.1 {
		t.Fatalf("quality.score: %v", resp.Quality.Score)
	}
	if resp.Quality.Confidence != quality.ConfidenceMedium {
		t.Fatalf("quality.confidence: %q", resp.Quality.Confidence)
	}
	if resp.Quality.TaxRateSource != quality.TaxRateSourceConfigured {
		t.Fatalf("quality.tax_rate_source: %q", resp.Quality.TaxRateSource)
	}
	if len(resp.Quality.SubScores) != 2 {
		t.Fatalf("sub-bloques: esperado 2, got %d", len(resp.Quality.SubScores))
	}
	if resp.Quality.SubScores[quality.SubProfitability].Score == nil {
		t.Fatalf("el sub-bloque profitability debe traer su score")
	}
	if resp.Quality.SubScores[quality.SubGrowth].Score != nil {
		t.Fatalf("el sub-bloque growth sin datos debe llegar nil, no 0 ni 50")
	}
	if resp.Relative == nil || resp.Relative.SectorScore == nil || resp.Relative.HistoricalScore == nil {
		t.Fatalf("relative debe traer los dos lados separados: %+v", resp.Relative)
	}
	if *resp.Relative.SectorScore != 75 || *resp.Relative.HistoricalScore != 62 {
		t.Fatalf("lados de relative: %v / %v", *resp.Relative.SectorScore, *resp.Relative.HistoricalScore)
	}
}

// B15: una fila 2.0.0 conserva sus CINCO dimensiones viejas y NO recibe quality.
func TestScoreResponse20ConservaSusDimensionesViejas(t *testing.T) {
	in := score.ScoreInput{Ticker: "TEST", Price: 100, GrahamBase: f(150), DCFBase: f(120)}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp := newScoreResponse(storage.Score{ModelVersion: score.ModelVersion, InputsSnapshot: raw})
	want := []string{"graham", "dcf", "fundamentals", "comparables", "trend"}
	if len(resp.Dimensions) != len(want) {
		t.Fatalf("2.0.0 debe servir sus %d dimensiones, got %d", len(want), len(resp.Dimensions))
	}
	for i, w := range want {
		if resp.Dimensions[i].Name != w {
			t.Fatalf("dimensión %d de 2.0.0: esperado %q, got %q", i, w, resp.Dimensions[i].Name)
		}
	}
	if resp.Quality != nil || resp.Relative != nil {
		t.Fatalf("2.0.0 no tiene bloques de M6c: %+v %+v", resp.Quality, resp.Relative)
	}
	if resp.TraceVersion != "" {
		t.Fatalf("2.0.0 no usa trace: %q", resp.TraceVersion)
	}
}

// B15: 1.1.0 NO recibe dimensiones (la revisión no las tenía). Describir un score
// con una taxonomía que nunca produjo sería inventar su explicación.
func TestScoreResponse11SinDimensiones(t *testing.T) {
	resp := newScoreResponse(storage.Score{ModelVersion: "1.1.0", InputsSnapshot: []byte(`{"graham_intrinsic":150}`)})
	if len(resp.Dimensions) != 0 {
		t.Fatalf("1.1.0 no debe tener dimensiones: %+v", resp.Dimensions)
	}
	if resp.WeightConfigured != nil || resp.WeightUsed != nil {
		t.Fatalf("1.1.0 no debe tener sumas de pesos: %v / %v", resp.WeightConfigured, resp.WeightUsed)
	}
	raw, _ := json.Marshal(resp)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if _, present := m["dimensions"]; present {
		t.Fatalf("la clave dimensions debe AUSENTARSE (omitempty), got %v", m["dimensions"])
	}
}

// Un trace 2.1.0 ilegible degrada a "sin dimensiones" y NO rompe el endpoint: el
// score persistido sigue saliendo.
func TestScoreResponse21TraceIlegibleDegradaSinRomper(t *testing.T) {
	resp := newScoreResponse(storage.Score{
		Score: 61, Signal: "mantener", ModelVersion: score.ModelVersion21,
		InputsSnapshot: []byte(`{"trace_version":"42","ticker":"X"}`),
	})
	if len(resp.Dimensions) != 0 || resp.Quality != nil || resp.Relative != nil {
		t.Fatalf("un trace de otra versión no debe producir bloques: %+v", resp.Dimensions)
	}
	if resp.Score.Score != 61 || resp.Score.Signal != "mantener" {
		t.Fatalf("el score persistido debe seguir saliendo: %d %s", resp.Score.Score, resp.Score.Signal)
	}
}

// La renormalización se ve en el JSON: weight_used < weight_configured. La fila
// lleva la revisión que el MOTOR declara en su trace (hoy 2.2.0): el camino de
// decode es el mismo para 2.1.0 y 2.2.0, así que el dato no se inventa.
func TestScoreResponse21RenormalizacionVisible(t *testing.T) {
	in := score.ScoreInput21{Ticker: "X", AsOf: time.Now(), Price: 100, GrahamBase: f(150), DCFBase: f(120)}
	res := score.CalculateScore21(in, modelcfg.DefaultModelConfig())
	raw, err := score.BuildTrace21(in, res).Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp := newScoreResponse(storage.Score{Score: res.Score, ModelVersion: res.ModelVersion, InputsSnapshot: raw})
	if resp.WeightUsed == nil || resp.WeightConfigured == nil {
		t.Fatalf("faltan las sumas de pesos")
	}
	if *resp.WeightUsed >= *resp.WeightConfigured {
		t.Fatalf("con una sola dimensión válido weight_used (%v) debe ser menor que weight_configured (%v)",
			*resp.WeightUsed, *resp.WeightConfigured)
	}
}

// M6c-T1 W6b: la fila VIGENTE (2.2.0) decodifica por el mismo camino que la
// 2.1.0 — misma forma de trace, mismas cinco dimensiones de §18, mismo bloque
// quality con sub-bloques. Si el gate de newScoreResponse no la incluyera,
// tendría score persistido SIN desglose.
func TestScoreResponse22VigenteDecodificaIgualQue210(t *testing.T) {
	resp := newScoreResponse(storage.Score{
		Score: 72, Signal: "comprar",
		InputsSnapshot: trace22Fixture(t), ModelVersion: score.ModelVersion22,
	})
	want := []string{"graham", "dcf", "quality", "relative", "market_context"}
	if len(resp.Dimensions) != 5 {
		t.Fatalf("2.2.0 debe exponer las 5 dimensiones de §18, got %d (%+v)", len(resp.Dimensions), resp.Dimensions)
	}
	for i, w := range want {
		if resp.Dimensions[i].Name != w {
			t.Fatalf("dimensión %d: esperado %q, got %q", i, w, resp.Dimensions[i].Name)
		}
	}
	if resp.Quality == nil || resp.Quality.Score == nil || *resp.Quality.Score != 84.1 {
		t.Fatalf("2.2.0 debe traer el bloque quality: %+v", resp.Quality)
	}
	if len(resp.Quality.SubScores) != 2 {
		t.Fatalf("sub-bloques de quality: esperado 2, got %d", len(resp.Quality.SubScores))
	}
	if resp.Relative == nil || resp.Relative.SectorScore == nil {
		t.Fatalf("2.2.0 debe traer relative con sus dos lados: %+v", resp.Relative)
	}
	if resp.TraceVersion != score.TraceVersion {
		t.Fatalf("trace_version de la fila vigente: %q (el esquema NO cambió)", resp.TraceVersion)
	}
	if resp.WeightConfigured == nil || math.Abs(*resp.WeightConfigured-0.90) > 1e-6 {
		t.Fatalf("weight_configured: esperado 0.90 (§18), got %v", resp.WeightConfigured)
	}
}

// M6c-T1 W6b (§26): la 2.1.0 es HISTORIA y sigue siendo legible. Su fila, su
// trace y su bloque quality no se tocaron al subir a 2.2.0.
func TestScoreResponse21HistoriaSigueLegible(t *testing.T) {
	resp := newScoreResponse(storage.Score{
		Score: 72, Signal: "mantener",
		InputsSnapshot: trace21Fixture(t), ModelVersion: score.ModelVersion21,
	})
	if len(resp.Dimensions) != 5 {
		t.Fatalf("la historia 2.1.0 debe seguir exponiendo sus 5 dimensiones, got %d", len(resp.Dimensions))
	}
	if resp.Quality == nil || resp.Quality.Score == nil {
		t.Fatalf("la historia 2.1.0 debe seguir exponiendo su bloque quality: %+v", resp.Quality)
	}
	if resp.Relative == nil {
		t.Fatalf("la historia 2.1.0 debe seguir exponiendo su bloque relative")
	}
	if resp.TraceVersion != score.TraceVersion {
		t.Fatalf("trace_version de la historia: %q", resp.TraceVersion)
	}
	// Y las DOS revisiones coexisten sin pisarse: mismo payload, distinta fila.
	actual := newScoreResponse(storage.Score{InputsSnapshot: trace22Fixture(t), ModelVersion: score.ModelVersion22})
	if len(actual.Dimensions) != len(resp.Dimensions) {
		t.Fatalf("2.1.0 (%d) y 2.2.0 (%d) deben describir las mismas 5 dimensiones",
			len(resp.Dimensions), len(actual.Dimensions))
	}
}
