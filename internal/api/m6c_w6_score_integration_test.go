//go:build integration

package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/api"
	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/quality"
	"github.com/miky/abys-invest/internal/relative"
	"github.com/miky/abys-invest/internal/score"
	"github.com/miky/abys-invest/internal/storage"
)

// M6c-T1 W6b — NO-REGRESIÓN DE §26 para la gate de score:
//
// dos filas `scores` del MISMO (security, as_of) — 2.1.0 (historia) y 2.2.0
// (vigente) — COEXISTEN (nada se sobrescribe), el "latest" devuelve 2.2.0 y la
// 2.1.0 sigue consultable por `?model_version=2.1.0`, con sus dimensiones y su
// bloque quality decodificados en los DOS casos.
//
// El prefijo de ticker W6SC no colisiona con los fixtures M3/M3TST/T4B.

const w6scTick = "W6SC"

// w6scTrace construye el trace que el job scores persistiría para `modelVersion`
// (2.1.0 = cómo se escribió ANTES del bump; 2.2.0 = lo que escribe el motor
// hoy). El esquema es idéntico: sólo cambia el campo que lo identifica.
func w6scTrace(t *testing.T, ticker string, asOf time.Time, modelVersion string) ([]byte, score.Result21) {
	t.Helper()
	in := score.ScoreInput21{
		Ticker: ticker, AsOf: asOf, Price: 230,
		GrahamBase: fp(144.45), DCFBase: fp(121.16),
		GrahamConfidence: "medium", DCFConfidence: "medium",
		Quality: score.QualityDetailFrom(&quality.Result{
			Score: fp(84.1), Coverage: 16.0 / 17.0, Confidence: quality.ConfidenceMedium,
			TaxRateSource: quality.TaxRateSourceConfigured,
			SubScores: map[string]*quality.SubScore{
				"profitability": {
					Name: "profitability", Score: fp(88.4), Weight: 0.3, Coverage: 0.94,
					Metrics: []quality.Metric{{Name: "ROIC", Value: fp(21.5), Score: fp(88.4), Reason: "ROIC > WACC"}},
				},
			},
		}),
		Relative: score.RelativeDetailFrom(&relative.Result{
			Score: fp(70), SectorScore: fp(75), HistoricalScore: fp(62),
			Coverage: 8.0 / 9.0, Confidence: relative.ConfidenceMedium,
		}),
		SMA50: fp(250), SMA200: fp(200), Momentum6m: fp(0.2), Momentum12m: fp(0.35),
		MarginOfSafety: 30,
	}
	res := score.CalculateScore21(in, modelcfg.DefaultModelConfig())
	trace := score.BuildTrace21(in, res)
	trace.ModelVersion = modelVersion
	raw, err := trace.Marshal()
	if err != nil {
		t.Fatalf("marshal trace %s: %v", modelVersion, err)
	}
	return raw, res
}

// seedW6scScores siembra las DOS revisiones para el MISMO (security, as_of),
// como haría un pipeline que corre con 2.2.0 sobre una BD que ya tenía 2.1.0.
//
// M6c-T1 review P2-2: la fila VIGENTE (2.2.0) se inserta PRIMERO (id MENOR) y
// la historia (2.1.0) DESPUÉS (id MAYOR), con el mismo as_of. Así, sin el
// ORDER BY ... model_version DESC del lector, el ganador del latest sería la
// 2.1.0 (id DESC tras as_of empatado) y el subtest "latest-es-la-vigente"
// fallaría: el orden de seed deja de enmascarar el desempate.
func seedW6scScores(t *testing.T, asOf time.Time) {
	t.Helper()
	ctx := context.Background()
	sector := "Technology"
	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: w6scTick, CIK: "900000099", Name: "W6 Score Co", Type: "stock",
		Currency: "USD", Status: "active", Sector: &sector,
	})
	if err != nil {
		t.Fatalf("upsert security %s: %v", w6scTick, err)
	}
	// 2.2.0 primero (id menor) y 2.1.0 después (id mayor).
	for _, mv := range []string{score.ModelVersion22, score.ModelVersion21} {
		raw, res := w6scTrace(t, w6scTick, asOf, mv)
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := storage.UpsertScore(ctx, tx, &storage.Score{
			SecurityID: sec.ID, AsOf: asOf, Score: res.Score, Signal: res.Signal,
			Justification: res.Justification, InputsSnapshot: raw, ModelVersion: mv,
		}); err != nil {
			tx.Rollback(ctx) //nolint:errcheck
			t.Fatalf("upsert score %s: %v", mv, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit score %s: %v", mv, err)
		}
	}
}

// TestScoreGateVigente220YHistoria210Coexisten es la prueba de no-regresión de
// §26 en el endpoint: coexistencia, latest = vigente e historia consultable.
func TestScoreGateVigente220YHistoria210Coexisten(t *testing.T) {
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	asOf := time.Date(2025, 11, 3, 0, 0, 0, 0, time.UTC)
	seedW6scScores(t, asOf)
	router := api.NewRouter(pool)

	// (1) Las DOS filas siguen en la tabla: nada se sobrescribió (§26).
	ctx := context.Background()
	all, err := storage.ListScores(ctx, pool, storage.ScoresFilter{Ticker: w6scTick})
	if err != nil {
		t.Fatalf("list scores: %v", err)
	}
	versions := map[string]bool{}
	for _, s := range all {
		versions[s.ModelVersion] = true
	}
	if !versions[score.ModelVersion21] || !versions[score.ModelVersion22] {
		t.Fatalf("las dos revisiones deben coexistir para el mismo (security, as_of), got %v", versions)
	}
	if len(all) != 2 {
		t.Fatalf("se esperaban 2 filas (2.1.0 + 2.2.0), got %d", len(all))
	}

	t.Run("latest-es-la-vigente-2.2.0", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/score/"+w6scTick)
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		if mv, _ := body["model_version"].(string); mv != score.ModelVersion22 {
			t.Fatalf("el latest debe devolver la revisión vigente %q, got %q", score.ModelVersion22, mv)
		}
		assertW6ScoreBlocks(t, body, "latest 2.2.0")
	})

	t.Run("vigente-2.2.0-por-query", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/score/"+w6scTick+"?model_version=2.2.0")
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		if mv, _ := body["model_version"].(string); mv != score.ModelVersion22 {
			t.Fatalf("model_version: esperado 2.2.0, got %q", mv)
		}
		assertW6ScoreBlocks(t, body, "2.2.0")
	})

	t.Run("historia-2.1.0-legible", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/score/"+w6scTick+"?model_version=2.1.0")
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		if mv, _ := body["model_version"].(string); mv != score.ModelVersion21 {
			t.Fatalf("model_version: esperado 2.1.0, got %q", mv)
		}
		assertW6ScoreBlocks(t, body, "2.1.0 (historia)")
	})

	t.Run("historia-2.1.0-con-as-of", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/score/"+w6scTick+"?as_of=2025-11-03&model_version=2.1.0")
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		if mv, _ := body["model_version"].(string); mv != score.ModelVersion21 {
			t.Fatalf("model_version: esperado 2.1.0, got %q", mv)
		}
		assertW6ScoreBlocks(t, body, "2.1.0 con as_of")
	})

	t.Run("revision-desconocida-rechazada", func(t *testing.T) {
		rec, _ := do(t, router, http.MethodGet, "/score/"+w6scTick+"?model_version=3.0.0")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("3.0.0 debe dar 400 (lista cerrada), got %d (%s)", rec.Code, rec.Body.String())
		}
	})
}

// assertW6ScoreBlocks comprueba que la fila describa su desglose: 5 dimensiones
// de §18 en orden canónico, bloque quality y trace_version.
func assertW6ScoreBlocks(t *testing.T, body map[string]any, label string) {
	t.Helper()
	dims, _ := body["dimensions"].([]any)
	want := []string{"graham", "dcf", "quality", "relative", "market_context"}
	if len(dims) != len(want) {
		t.Fatalf("%s: esperadas %d dimensiones, got %d (%v)", label, len(want), len(dims), body["dimensions"])
	}
	for i, name := range want {
		d, _ := dims[i].(map[string]any)
		if got, _ := d["name"].(string); got != name {
			t.Fatalf("%s: dimensión %d: esperado %q, got %q", label, i, name, got)
		}
	}
	q, _ := body["quality"].(map[string]any)
	if q == nil {
		t.Fatalf("%s: falta el bloque quality (%v)", label, body["quality"])
	}
	if _, ok := q["sub_scores"]; !ok {
		t.Fatalf("%s: quality sin sub_scores: %v", label, q)
	}
	if tv, _ := body["trace_version"].(string); tv != score.TraceVersion {
		t.Fatalf("%s: trace_version: esperado %q, got %q", label, score.TraceVersion, tv)
	}
}
