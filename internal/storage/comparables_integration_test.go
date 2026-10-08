//go:build integration

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/metricver"
)

// M6c-T1 W6a: una fila 2.0.0 de un métrico moderno NO es lo que el reader por
// métrica selecciona. Los readers de comparables filtran en el JOIN por la
// revisión que DEFINE la métrica (metricver.AllPairs → 2.1.0), así que una fila
// vieja 2.0.0 se conserva como historia legible (§26) pero queda EXCLUIDA de
// las medianas — es la materialización de ADR D13/R-M6c-1 (una métrica tiene
// UNA revisión de fórmula vigente, no dos).
func TestComparablesReaderPrefersCurrentRevision(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	sector := "tech"
	sec, err := UpsertSecurity(ctx, pool, &Security{
		Ticker: "RV21", CIK: "0000000002", Name: "Revision Test", Type: "stock",
		Currency: "USD", Status: "active", Sector: &sector,
	})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}

	asOf := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	// Mismo métrico, misma as_of, DOS revisiones: la 2.0.0 (historia, 762-style)
	// y la 2.1.0 (vigente tras M6c-T1 W6a). Valores distintos para que la
	// mediana delate cuál entró en el reader.
	metrics := []DerivedMetric{
		{SecurityID: sec.ID, AsOf: asOf, Metric: "roic", Value: ptr(0.30), InputsSnapshot: []byte(`{"rev":"2.0.0-history"}`), ModelVersion: "2.0.0"},
		{SecurityID: sec.ID, AsOf: asOf, Metric: "roic", Value: ptr(0.21), InputsSnapshot: []byte(`{"rev":"2.1.0-current"}`), ModelVersion: "2.1.0"},
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := UpsertDerivedMetrics(ctx, tx, metrics); err != nil {
		t.Fatalf("UpsertDerivedMetrics: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Ambas filas siguen en la tabla: la historia 2.0.0 NO se sobrescribe (§26).
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM derived_metrics WHERE security_id=$1 AND metric='roic'`, sec.ID).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 2 {
		t.Fatalf("las dos revisiones deben coexistir (2.0.0 historia + 2.1.0 vigente), hay %d filas", n)
	}

	pairs := metricver.AllPairs()

	// Reader de sector: el JOIN por (metric, model_version) debe dejar pasar
	// SOLO la 2.1.0.
	sc, err := GetSectorComparables(ctx, pool, sector, 0, pairs)
	if err != nil {
		t.Fatalf("GetSectorComparables: %v", err)
	}
	got := sc.Medians["roic"]
	if got == nil || *got != 0.21 {
		t.Fatalf("la mediana de sector debe salir de la fila 2.1.0 (0.21) e ignorar la 2.0.0 (0.30), got %v", printable(got))
	}

	// Reader histórico propio: mismo contrato, la 2.0.0 no infla la mediana.
	hm, err := GetHistoricalMedianWithCount(ctx, pool, sec.ID, asOf, 5, pairs)
	if err != nil {
		t.Fatalf("GetHistoricalMedianWithCount: %v", err)
	}
	hist := hm.Medians["roic"]
	if hist == nil || *hist != 0.21 {
		t.Fatalf("la mediana histórica debe salir de la fila 2.1.0 (0.21) e ignorar la 2.0.0 (0.30), got %v", printable(hist))
	}
	if hm.AsOfCount != 1 {
		t.Fatalf("AsOfCount debe ser 1 (una sola as_of), es %d", hm.AsOfCount)
	}
}

func printable(v *float64) any {
	if v == nil {
		return "nil"
	}
	return *v
}
