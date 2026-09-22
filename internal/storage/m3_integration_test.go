//go:build integration

package storage

import (
	"context"
	"testing"
	"time"
)

// M3 integration coverage for the scores table (migration 009): idempotent
// upsert by (security_id, as_of, model_version), latest-row and history
// queries, and CHECK constraints (score 0-100, señal española).

func TestUpsertScoreIdempotent(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	sec, err := UpsertSecurity(ctx, pool, &Security{Ticker: "AAPL", CIK: "0000320193", Name: "Apple Inc", Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}

	asOf := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	mk := func(score int, signal string) *Score {
		return &Score{
			SecurityID:     sec.ID,
			AsOf:           asOf,
			Score:          score,
			Signal:         signal,
			Justification:  "AAPL: score calculado (test)",
			InputsSnapshot: []byte(`{"price":230}`),
			ModelVersion:   "1.0.0",
		}
	}

	apply := func(s *Score) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err := UpsertScore(ctx, tx, s); err != nil {
			t.Fatalf("UpsertScore: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}

	apply(mk(72, "comprar"))
	apply(mk(72, "comprar")) // idempotente: no duplica

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM scores WHERE security_id=$1`, sec.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("se esperaba 1 fila de score, hay %d", n)
	}

	// Re-upsert con valor distinto: actualiza en sitio (mismo as_of/model).
	apply(mk(55, "mantener"))
	got, err := GetScoreByTicker(ctx, pool, "AAPL", asOf)
	if err != nil {
		t.Fatalf("GetScoreByTicker: %v", err)
	}
	if got.Score != 55 || got.Signal != "mantener" {
		t.Fatalf("upsert no refrescó el score: %+v", got)
	}

	// 2ª fila en otra fecha: latest + historial.
	asOf2 := asOf.AddDate(0, 0, 7)
	apply(&Score{SecurityID: sec.ID, AsOf: asOf2, Score: 81, Signal: "comprar",
		Justification: "segunda fecha", ModelVersion: "1.0.0"})

	latest, err := GetLatestScore(ctx, pool, "AAPL")
	if err != nil {
		t.Fatalf("GetLatestScore: %v", err)
	}
	if latest.Score != 81 || !latest.AsOf.Equal(asOf2) {
		t.Fatalf("latest esperado score 81 @%v, got %+v", asOf2, latest)
	}

	history, err := ListScores(ctx, pool, ScoresFilter{Ticker: "AAPL"})
	if err != nil {
		t.Fatalf("ListScores: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("historial esperado de 2 filas, hay %d", len(history))
	}
	if history[0].AsOf.Before(history[1].AsOf) {
		t.Fatalf("historial no ordenado DESC: %v > %v esperado", history[1].AsOf, history[0].AsOf)
	}

	// Ticker sin score: ErrNoRows.
	if _, err := GetLatestScore(ctx, pool, "NOPE"); err == nil {
		t.Fatal("se esperaba error para ticker sin score")
	}
}

func TestScoreCheckConstraints(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	sec, err := UpsertSecurity(ctx, pool, &Security{Ticker: "CHK", CIK: "0000000002", Name: "Check", Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	asOf := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)

	bad := []*Score{
		{SecurityID: sec.ID, AsOf: asOf, Score: 101, Signal: "comprar", Justification: "x", ModelVersion: "1.0.0"},
		{SecurityID: sec.ID, AsOf: asOf, Score: -1, Signal: "comprar", Justification: "x", ModelVersion: "2.0.0"},
		{SecurityID: sec.ID, AsOf: asOf, Score: 50, Signal: "hold", Justification: "x", ModelVersion: "3.0.0"},
	}
	for _, s := range bad {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		err = UpsertScore(ctx, tx, s)
		tx.Rollback(ctx) //nolint:errcheck
		if err == nil {
			t.Fatalf("CHECK constraint no aplicado para %+v", s)
		}
	}
}
