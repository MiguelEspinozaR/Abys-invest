//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/api"
	"github.com/miky/abys-invest/internal/score"
	"github.com/miky/abys-invest/internal/storage"
)

// Test M4 (T4b): GET /score/{ticker} y GET /scores?ticker= deben exponer las
// dimensiones del score (CA M4-1) recomputadas desde el inputs_snapshot
// persistido. Reutiliza el pool y TestMain de m3_integration_test.go
// (misma package api_test); no duplica infraestructura.
//
// Ejecución: DATABASE_URL=... go test ./internal/api -p 1 -tags=integration -count=1
//
// El ticker usa el prefijo T4B para no colisionar con los fixtures M3/M3TST
// (el suite storage trunca las tablas de datos solo al final, no aquí).

const t4bTick = "T4BSC"

func scoreFP(v float64) *float64 { return &v }

// t4bInput es el ScoreInput idéntico al que el job scores persiste como
// inputs_snapshot (json.Marshal(input)) y del que el score persistido se
// deriva. Devuelve también el resultado esperado del motor (determinista).
func t4bInput() (score.ScoreInput, score.ScoreResult) {
	input := score.ScoreInput{
		Ticker: t4bTick, Price: 230,
		GrahamIntrinsic: scoreFP(144.45), DCFIntrinsic: scoreFP(121.16),
		Metrics: map[string]*float64{
			"pe_ratio": scoreFP(26.2), "pb_ratio": scoreFP(43.0), "fcf_yield": scoreFP(4.1),
			"roe": scoreFP(1.61), "de_ratio": scoreFP(4.7),
		},
		SectorCount: 2,
		SMA50:       scoreFP(250), SMA200: scoreFP(200),
		Momentum6m: scoreFP(0.2), Momentum12m: scoreFP(0.35),
		MarginOfSafety: 30,
	}
	return input, score.CalculateScore(input)
}

// seedScoreFixture crea un security con un score persistido cuyo
// inputs_snapshot es un ScoreInput real marshaleado (igual que el job
// cmd/analytics).
func seedScoreFixture(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	sector := "Technology"
	exch := "XNAS"
	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: t4bTick, CIK: "900000091", Name: "T4B Score Co", Type: "stock",
		Currency: "USD", Status: "active", Sector: &sector, Exchange: &exch,
	})
	if err != nil {
		t.Fatalf("upsert security %s: %v", t4bTick, err)
	}

	input, res := t4bInput()
	snapshot, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	asOf := time.Date(2025, 9, 28, 0, 0, 0, 0, time.UTC)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	err = storage.UpsertScore(ctx, tx, &storage.Score{
		SecurityID:     sec.ID,
		AsOf:           asOf,
		Score:          res.Score,
		Signal:         res.Signal,
		Justification:  res.Justification,
		InputsSnapshot: snapshot,
		ModelVersion:   res.ModelVersion,
	})
	if err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("upsert score: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// assertDimensions valida que el objeto de score (detalle o ítem del
// histórico) exponga el contrato D5: nombres en orden, pesos 35/30/20/15 y
// el score/signal persistidos sin recalcular.
func assertDimensions(t *testing.T, body map[string]any) {
	t.Helper()
	raw, _ := body["dimensions"].([]any)
	wantNames := []string{score.DimValuation, score.DimFundamentals, score.DimComparables, score.DimTrend}
	if len(raw) != len(wantNames) {
		t.Fatalf("dimensiones esperadas %v, got %+v (body: %v)", wantNames, raw, body["dimensions"])
	}
	wantWeights := []float64{score.WeightValuation, score.WeightFundaments, score.WeightComparables, score.WeightTrend}
	for i, name := range wantNames {
		it, _ := raw[i].(map[string]any)
		gotName, _ := it["name"].(string)
		if gotName != name {
			t.Fatalf("dimensión %d: nombre esperado %q, got %q", i, name, gotName)
		}
		gotWeight, _ := it["weight"].(float64)
		if gotWeight != wantWeights[i] {
			t.Fatalf("dimensión %s: peso esperado %v, got %v", name, wantWeights[i], gotWeight)
		}
	}
	// El score/signal son los persistidos (enriquecimiento, no recálculo).
	_, res := t4bInput()
	if s, _ := body["score"].(float64); int(s) != res.Score {
		t.Fatalf("score persistido esperado %d, got %v (no recalcular)", res.Score, body["score"])
	}
	if sig, _ := body["signal"].(string); sig != res.Signal {
		t.Fatalf("signal persistido esperado %q, got %q (no recalcular)", res.Signal, sig)
	}
}

// TestScoreDimensionsIntegration — /score/{ticker} y /scores?ticker=
// devuelven `dimensions` con nombres/orden/pesos del contrato D5 (M4 T4b).
func TestScoreDimensionsIntegration(t *testing.T) {
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	seedScoreFixture(t)
	router := api.NewRouter(pool)

	t.Run("score-detalle-con-dimensiones", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/score/"+t4bTick)
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		assertDimensions(t, body)
	})

	t.Run("scores-historico-con-dimensiones", func(t *testing.T) {
		// El helper `do` de m3 solo parsea objetos; /scores devuelve un
		// array, así que se captura el body crudo.
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/scores?ticker="+t4bTick, nil)
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		var list []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
			t.Fatalf("respuesta /scores no es un array JSON: %v", err)
		}
		if len(list) == 0 {
			t.Fatalf("histórico vacío para %s", t4bTick)
		}
		assertDimensions(t, list[0])
	})
}
