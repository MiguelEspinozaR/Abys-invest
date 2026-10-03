package score

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/quality"
	"github.com/miky/abys-invest/internal/relative"
)

// These tests cover the two contracts 2.1.0 exists to guarantee: a dimension
// without data is INVALID and never a 50, and a persisted trace reproduces its
// own score with no access to any other table.

func f(v float64) *float64 { return &v }

func qd(score *float64) *QualityDetail {
	return &QualityDetail{Score: score, Coverage: 0.9, Confidence: quality.ConfidenceHigh}
}

func rd(score *float64) *RelativeDetail {
	return &RelativeDetail{Score: score, Coverage: 0.8, Confidence: relative.ConfidenceMedium}
}

func completeInput21() ScoreInput21 {
	return ScoreInput21{
		Ticker:         "TEST",
		AsOf:           time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Price:          100,
		GrahamBase:     f(150),
		DCFBase:        f(120),
		Quality:        qd(f(80)),
		Relative:       rd(f(60)),
		SMA50:          f(110),
		SMA200:         f(100),
		Momentum6m:     f(0.05),
		Momentum12m:    f(0.02),
		MarginOfSafety: 30,
	}
}

// CA-M6c-1: the five dimensions of §12, in canonical order, with the §12 weights.
func TestScore21CincoDimensionesEnOrdenCanonico(t *testing.T) {
	res := CalculateScore21(completeInput21(), modelcfg.DefaultModelConfig())
	want := []string{DimGrahamV21, DimDCFV21, DimQualityV21, DimRelativeV21, DimMarketContextV21}
	if len(res.Dimensions) != len(want) {
		t.Fatalf("dimensiones: esperado %d, got %d", len(want), len(res.Dimensions))
	}
	for i, name := range want {
		if res.Dimensions[i].Name != name {
			t.Fatalf("dimensión %d: esperado %q, got %q", i, name, res.Dimensions[i].Name)
		}
	}
	if res.ModelVersion != ModelVersion21 {
		t.Fatalf("model_version: esperado %q, got %q", ModelVersion21, res.ModelVersion)
	}
	// §18 estricto: los cinco pesos configurados suman 0.90. Con las cinco
	// dimensiones válidas, weight_used == weight_configured == 0.90 y el score
	// sale de dividir por ese total (§18 renormalización).
	if math.Abs(res.WeightConfigured-0.90) > 1e-9 {
		t.Fatalf("weight_configured: esperado 0.90 (§18), got %v", res.WeightConfigured)
	}
	if math.Abs(res.WeightUsed-res.WeightConfigured) > 1e-9 {
		t.Fatalf("con todo válido weight_used debe ser weight_configured: %v vs %v", res.WeightUsed, res.WeightConfigured)
	}
}

// CA-M6c-1: las dimensiones inválidas desaparecen del cálculo, NO valen 50.
func TestScore21DimensionInvalidaNoEsNeutral(t *testing.T) {
	in := completeInput21()
	in.Quality = nil
	in.Relative = nil
	in.SMA50, in.SMA200 = nil, nil
	in.Momentum6m, in.Momentum12m = nil, nil

	res := CalculateScore21(in, modelcfg.DefaultModelConfig())
	for _, d := range res.Dimensions {
		if d.Name == DimQualityV21 || d.Name == DimRelativeV21 || d.Name == DimMarketContextV21 {
			if d.Valid {
				t.Fatalf("%s sin datos debe ser inválida, no válida con score %v", d.Name, d.Score)
			}
			if d.Score != nil {
				t.Fatalf("%s inválida debe tener score nil, got %v", d.Name, *d.Score)
			}
		}
	}
	// weight_used = sólo graham (0.15) + dcf (0.20); quality/relative/market
	// quedan fuera por inválidas.
	wantUsed := WeightGraham21 + WeightDCF21
	if math.Abs(res.WeightUsed-wantUsed) > 1e-9 {
		t.Fatalf("weight_used: esperado %v, got %v", wantUsed, res.WeightUsed)
	}
	// Y el score final es el de esas dos, renormalizado — nunca el promedio
	// de cinco dimensiones donde tres valen 50.
	only := CalculateScore21(ScoreInput21{
		Ticker: in.Ticker, Price: in.Price,
		GrahamBase: in.GrahamBase, DCFBase: in.DCFBase, MarginOfSafety: in.MarginOfSafety,
	}, modelcfg.DefaultModelConfig())
	if res.Score != only.Score {
		t.Fatalf("score con 2 dimensiones %d != score solo-2 %d", res.Score, only.Score)
	}
	if res.Score == 50 && res.WeightUsed > 0 && res.WeightUsed < 1 {
		t.Fatalf("score 50 con weight_used %v: sospechoso de neutral reintroducido", res.WeightUsed)
	}
}

// CA-M6c-1: sin datos de mercado la dimensión es inválida. 2.0.0 devolvía 50 aquí.
func TestScore21MarketContextSinDatosEsInvalida(t *testing.T) {
	in := completeInput21()
	in.SMA50, in.SMA200, in.Momentum6m, in.Momentum12m = nil, nil, nil, nil
	res := CalculateScore21(in, modelcfg.DefaultModelConfig())

	var mc21 Dimension21
	for _, d := range res.Dimensions {
		if d.Name == DimMarketContextV21 {
			mc21 = d
		}
	}
	if mc21.Valid || mc21.Score != nil {
		t.Fatalf("market_context sin datos debe ser inválida/nil, got valid=%v score=%v", mc21.Valid, mc21.Score)
	}
	// El 2.0.0 sí devolvía 50 en este caso: la diferencia es exactamente la que §18 pide.
	if legacy := scoreTrend(nil, nil, nil, nil); legacy != 50 {
		t.Fatalf("precondición: scoreTrend 2.0.0 debería seguir en 50, cambió a %v", legacy)
	}
}

// El ladder de mercado contexto no puede divergir del 2.0.0 en los casos USABLES.
func TestScore21MarketContextCoincideConLadder2_0_0(t *testing.T) {
	cases := []struct {
		s50, s200, m6, m12 *float64
	}{
		{f(110), f(100), f(0.05), f(0.02)},
		{f(90), f(100), f(0.05), f(0.02)},
		{f(100.5), f(100), nil, f(-0.3)},
		{f(120), f(100), f(0.5), nil},
	}
	for i, c := range cases {
		want := scoreTrend(c.s50, c.s200, c.m6, c.m12)
		got := scoreMarketContext21(c.s50, c.s200, c.m6, c.m12)
		if got == nil {
			t.Fatalf("caso %d con datos: no debe ser nil", i)
		}
		if math.Abs(*got-want) > 1e-9 {
			t.Fatalf("caso %d: ladder divergió: 2.0.0=%v 2.1.0=%v", i, want, *got)
		}
	}
}

// NaN/Inf en una entrada de motor no puede contaminar el score (ADR D22).
func TestScore21RechazaNaNInfDeMotores(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		in := completeInput21()
		in.Quality = qd(f(bad))
		res := CalculateScore21(in, modelcfg.DefaultModelConfig())
		for _, d := range res.Dimensions {
			if d.Name == DimQualityV21 && d.Valid {
				t.Fatalf("quality con %v debe ser inválida", bad)
			}
			if d.Score != nil && (math.IsNaN(*d.Score) || math.IsInf(*d.Score, 0)) {
				t.Fatalf("dimensión %s filtró %v al resultado", d.Name, bad)
			}
		}
		if res.Score < 0 || res.Score > 100 {
			t.Fatalf("score fuera de rango con %v: %d", bad, res.Score)
		}
	}
}

// Un score de motor fuera de [0,100] se recorta: la entrada es del motor, pero
// el scorepublished no puede dejar que un 140 suba el promedio.
func TestScore21RecortaScoreDeMotorFueraDeRango(t *testing.T) {
	in := completeInput21()
	in.Quality = qd(f(140))
	res := CalculateScore21(in, modelcfg.DefaultModelConfig())
	for _, d := range res.Dimensions {
		if d.Name == DimQualityV21 {
			if d.Score == nil || math.Abs(*d.Score-100) > 1e-9 {
				t.Fatalf("quality 140 debe recortarse a 100, got %v", d.Score)
			}
		}
	}
}

// ADR D20: los pesos vienen de la ModelConfig resuelta (env/set), no del código.
func TestScore21UsaPesosDeModelConfig(t *testing.T) {
	mc := modelcfg.DefaultModelConfig()
	mc.GrahamWeight, mc.DCFWeight = 0.30, 0.30
	mc.QualityWeight, mc.RelativeWeight, mc.MarketContextWeight = 0.20, 0.10, 0.10
	mc.ParameterSetName = "conservative"
	mc.ParameterSetID = 7

	res := CalculateScore21(completeInput21(), mc)
	if res.Dimensions[0].Weight != 0.30 {
		t.Fatalf("graham debe usar el peso del set, got %v", res.Dimensions[0].Weight)
	}
	if res.ParameterSet != "conservative" || res.ParameterSetID != 7 {
		t.Fatalf("provenance del set perdida: %q/%d", res.ParameterSet, res.ParameterSetID)
	}
	if math.Abs(res.WeightConfigured-1) > 1e-9 {
		t.Fatalf("weight_configured: esperado 1.0, got %v", res.WeightConfigured)
	}
	// Y el score cambia respecto al default (los pesos sí mandan).
	def := CalculateScore21(completeInput21(), modelcfg.DefaultModelConfig())
	if def.Score == res.Score {
		t.Logf("aviso: mismo score con otros pesos (posible con estos datos)")
	}
}

// ADR D27: el trace se reconstruye desde sí mismo y reproduce el MISMO score.
func TestScore21TraceReproduceElScore(t *testing.T) {
	in := completeInput21()
	mc := modelcfg.DefaultModelConfig()
	mc.ParameterSetName = "base"
	mc.ParameterSetID = 1
	res := CalculateScore21(in, mc)
	trace := BuildTrace21(in, res)

	raw, err := trace.Marshal()
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}
	parsed, err := ParseTrace(raw)
	if err != nil {
		t.Fatalf("ParseTrace: %v", err)
	}
	// Replay con OTRA configuración a propósito: la reproducción debe usar los
	// pesos del trace, no los de hoy.
	other := modelcfg.DefaultModelConfig()
	other.GrahamWeight, other.DCFWeight = 0.4, 0.4
	other.QualityWeight, other.RelativeWeight, other.MarketContextWeight = 0.05, 0.05, 0.10

	again, err := RecomputeFromTrace(parsed, other)
	if err != nil {
		t.Fatalf("RecomputeFromTrace: %v", err)
	}
	if again.Score != res.Score {
		t.Fatalf("score no reproducido: original %d, replay %d", res.Score, again.Score)
	}
	if math.Abs(again.WeightUsed-res.WeightUsed) > 1e-9 {
		t.Fatalf("weight_used no reproducido: %v vs %v", again.WeightUsed, res.WeightUsed)
	}
	for i := range res.Dimensions {
		a, b := res.Dimensions[i], again.Dimensions[i]
		if a.Name != b.Name || a.Valid != b.Valid {
			t.Fatalf("dimensión %d difiere: %s/%v vs %s/%v", i, a.Name, a.Valid, b.Name, b.Valid)
		}
		if (a.Score == nil) != (b.Score == nil) {
			t.Fatalf("dimensión %s: nil/no-nil difiere en el replay", a.Name)
		}
		if a.Score != nil && math.Abs(*a.Score-*b.Score) > 1e-9 {
			t.Fatalf("dimensión %s: score %v vs %v en el replay", a.Name, *a.Score, *b.Score)
		}
	}
}

// Un trace con dimensiones inválidas también se reproduce: nil es un dato válido.
func TestScore21TraceReproduceConDimensionesInvalidas(t *testing.T) {
	in := completeInput21()
	in.Quality = nil
	in.Relative = nil
	in.SMA50, in.SMA200, in.Momentum6m, in.Momentum12m = nil, nil, nil, nil
	res := CalculateScore21(in, modelcfg.DefaultModelConfig())
	raw, err := BuildTrace21(in, res).Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseTrace(raw)
	if err != nil {
		t.Fatalf("ParseTrace: %v", err)
	}
	again, err := RecomputeFromTrace(parsed, modelcfg.DefaultModelConfig())
	if err != nil {
		t.Fatalf("RecomputeFromTrace: %v", err)
	}
	if again.Score != res.Score {
		t.Fatalf("score no reproducido con dimensiones inválidas: %d vs %d", res.Score, again.Score)
	}
}

// ADR D27: un trace que no se puede decodificar falla ruidosamente, con errores
// DISTINTOS para versión desconocida y snapshot incompleto.
func TestScore21ParseTraceRechazaEntradasInvalidas(t *testing.T) {
	if _, err := ParseTrace(nil); !errors.Is(err, modelcfg.ErrSnapshotIncomplete) {
		t.Fatalf("snapshot vacío: esperado ErrSnapshotIncomplete, got %v", err)
	}
	if _, err := ParseTrace([]byte(`{"ticker":"X"}`)); !errors.Is(err, modelcfg.ErrSnapshotIncomplete) {
		t.Fatalf("sin trace_version: esperado ErrSnapshotIncomplete, got %v", err)
	}
	var unknown Trace21
	if err := json.Unmarshal([]byte(`{"trace_version":"99","ticker":"X","dimensions":[{"name":"graham"}]}`), &unknown); err != nil {
		t.Fatalf("setup: %v", err)
	}
	raw, _ := json.Marshal(unknown)
	if _, err := ParseTrace(raw); !errors.Is(err, ErrUnsupportedTraceVersion) {
		t.Fatalf("versión desconocida: esperado ErrUnsupportedTraceVersion, got %v", err)
	}
	// Sin dimensiones no hay nada que reproducir.
	if _, err := ParseTrace([]byte(`{"trace_version":"1","ticker":"X"}`)); !errors.Is(err, modelcfg.ErrSnapshotIncomplete) {
		t.Fatalf("sin dimensiones: esperado ErrSnapshotIncomplete, got %v", err)
	}
	// Una dimensión desconocida en el replay es un fallo, no un score distinto.
	bad := &Trace21{TraceVersion: TraceVersion, Ticker: "X", Dimensions: []Dimension21{
		{Name: DimGrahamV21, Weight: 0.15}, {Name: "inventada", Weight: 0.2},
	}}
	if _, err := RecomputeFromTrace(bad, modelcfg.DefaultModelConfig()); !errors.Is(err, modelcfg.ErrSnapshotIncomplete) {
		t.Fatalf("dimensión desconocida: esperado ErrSnapshotIncomplete, got %v", err)
	}
	// Y un trace con 4 de 5 dimensiones también.
	partial := &Trace21{TraceVersion: TraceVersion, Ticker: "X", Dimensions: []Dimension21{
		{Name: DimGrahamV21, Weight: 0.15}, {Name: DimDCFV21, Weight: 0.2},
	}}
	if _, err := RecomputeFromTrace(partial, modelcfg.DefaultModelConfig()); !errors.Is(err, modelcfg.ErrSnapshotIncomplete) {
		t.Fatalf("trace parcial: esperado ErrSnapshotIncomplete, got %v", err)
	}
}

// La justificación tiene que NOMBRAR lo que faltó: un 72 sin quality no puede
// leerse como un veredicto sobre la empresa.
func TestScore21JustificacionNombraLasDimensionesAusentes(t *testing.T) {
	in := completeInput21()
	in.Quality = nil
	res := CalculateScore21(in, modelcfg.DefaultModelConfig())
	j := res.Justification
	if !strings.Contains(j, DimQualityV21) {
		t.Fatalf("la justificación debe nombrar %s ausente: %q", DimQualityV21, j)
	}
	if !strings.Contains(j, "Sin datos") {
		t.Fatalf("la justificación debe marcar las dimensiones sin datos: %q", j)
	}
	for _, d := range res.Dimensions {
		if d.Valid && !strings.Contains(j, d.Name) {
			t.Fatalf("la justificación no nombra la dimensión válida %s: %q", d.Name, j)
		}
	}
}

// Los cinco sub-bloques de quality viajan en el trace con los nombres del motor,
// en la cobertura que el motor calculó.
func TestScore21TraceIncluyeSubBloquesDeQuality(t *testing.T) {
	in := completeInput21()
	in.Quality = &QualityDetail{
		Score: f(80), Coverage: 0.94, Confidence: quality.ConfidenceMedium,
		TaxRateSource: quality.TaxRateSourceConfigured,
		SubScores: map[string]*quality.SubScore{
			quality.SubProfitability: {Name: quality.SubProfitability, Score: f(90), Weight: 0.2},
			quality.SubGrowth:        {Name: quality.SubGrowth, Score: nil, Weight: 0.2},
		},
		Reasons: []string{quality.ReasonGrowthUnreliable},
	}
	res := CalculateScore21(in, modelcfg.DefaultModelConfig())
	raw, err := BuildTrace21(in, res).Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseTrace(raw)
	if err != nil {
		t.Fatalf("ParseTrace: %v", err)
	}
	if parsed.Quality == nil || len(parsed.Quality.SubScores) != 2 {
		t.Fatalf("sub-bloques perdidos en el trace: %+v", parsed.Quality)
	}
	if _, ok := parsed.Quality.SubScores[quality.SubProfitability]; !ok {
		t.Fatalf("sub-bloque profitability ausente")
	}
	if parsed.Quality.TaxRateSource != quality.TaxRateSourceConfigured {
		t.Fatalf("tax_rate_source perdido: %q", parsed.Quality.TaxRateSource)
	}
	if !strings.Contains(strings.Join(parsed.Reasons, ","), quality.ReasonGrowthUnreliable) {
		t.Fatalf("razones perdidas en el trace: %v", parsed.Reasons)
	}
	// Un sub-bloque nil NO invalida la dimensión quality (sólo el score total nil lo hace).
	if !parsed.Dimensions[2].Valid {
		t.Fatalf("quality con un sub-bloque nil debe seguir válida")
	}
}

// Los conversores son la única vía de construcción: nil entra, nil sale.
func TestScore21ConvertersDesdeMotores(t *testing.T) {
	if QualityDetailFrom(nil) != nil {
		t.Fatalf("QualityDetailFrom(nil) debe ser nil")
	}
	if RelativeDetailFrom(nil) != nil {
		t.Fatalf("RelativeDetailFrom(nil) debe ser nil")
	}
	q := QualityDetailFrom(&quality.Result{
		Score: f(84.1), Coverage: 16.0 / 17.0, Confidence: quality.ConfidenceMedium,
		TaxRateSource: quality.TaxRateSourceConfigured,
		Reasons:       []string{quality.ReasonInterestExpenseMissing},
		SubScores:     map[string]*quality.SubScore{quality.SubMargins: {Name: quality.SubMargins, Score: f(70)}},
	})
	if q.Score == nil || math.Abs(*q.Score-84.1) > 1e-9 || q.TaxRateSource == "" {
		t.Fatalf("conversor de quality pierde datos: %+v", q)
	}
	r := RelativeDetailFrom(&relative.Result{
		Score: f(70), SectorScore: f(75), HistoricalScore: f(62),
		Coverage: 8.0 / 9.0, Confidence: relative.ConfidenceMedium,
	})
	if r.SectorScore == nil || r.HistoricalScore == nil {
		t.Fatalf("conversor de relative pierde los lados separados: %+v", r)
	}
}

// El orden canónico es el de §12 y no depende de un mapa.
func TestScore21DimensionOrder21EsEstable(t *testing.T) {
	want := []string{"graham", "dcf", "quality", "relative", "market_context"}
	got := DimensionOrder21()
	if len(got) != len(want) {
		t.Fatalf("orden: esperado %d, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("orden[%d]: esperado %q, got %q", i, want[i], got[i])
		}
	}
}

// El motor 2.1.0 no altera el 2.0.0 publicado (CA-M6c-14).
func TestScore21NoAfectaScore2_0_0(t *testing.T) {
	in := ScoreInput{
		Ticker: "TEST", Price: 100,
		GrahamBase: f(150), DCFBase: f(120),
		Metrics: map[string]*float64{"roe": f(0.2)},
	}
	legacy := CalculateScore(in)
	if legacy.ModelVersion != "2.0.0" {
		t.Fatalf("2.0.0 debe seguir identificado como tal, got %q", legacy.ModelVersion)
	}
	if legacy.Score <= 0 || legacy.Score > 100 {
		t.Fatalf("2.0.0 devolvió un score imposible: %d", legacy.Score)
	}
}
