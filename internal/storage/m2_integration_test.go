//go:build integration

package storage

import (
	"context"
	"testing"
	"time"
)

// T2/T10 integration coverage for the M2 tables: idempotent upserts,
// latest-row queries and derived_metrics behavior (missing inputs -> NULL).

func TestUpsertDailyPricesIdempotent(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	sec, err := UpsertSecurity(ctx, pool, &Security{Ticker: "AAPL", CIK: "0000320193", Name: "Apple Inc", Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}

	d1 := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	prices := []DailyPrice{
		{SecurityID: sec.ID, Date: d1, Close: 170.0, AdjustedClose: 170.0, Source: "yahoo"},
		{SecurityID: sec.ID, Date: d2, Close: 172.5, AdjustedClose: 172.5, Source: "yahoo"},
	}

	apply := func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err := UpsertDailyPrices(ctx, tx, prices); err != nil {
			t.Fatalf("UpsertDailyPrices: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}

	apply()
	apply() // idempotente

	n := 0
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM daily_prices WHERE security_id=$1`, sec.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("se esperaban 2 filas (no duplicadas), hay %d", n)
	}

	// Update in situ.
	prices[0].Close = 171.0
	apply()
	var v float64
	if err := pool.QueryRow(ctx, `SELECT close FROM daily_prices WHERE security_id=$1 AND date=$2`, sec.ID, d1).Scan(&v); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if v != 171.0 {
		t.Fatalf("update no aplicado: got %v", v)
	}

	latest, err := GetLatestPrice(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("GetLatestPrice: %v", err)
	}
	if !latest.Date.Equal(d2) {
		t.Fatalf("latest esperado %v, got %v", d2, latest.Date)
	}

	ids, err := ListSecuritiesWithPrices(ctx, pool)
	if err != nil {
		t.Fatalf("ListSecuritiesWithPrices: %v", err)
	}
	if len(ids) != 1 || ids[0] != sec.ID {
		t.Fatalf("ids con precios esperado [%d], got %v", sec.ID, ids)
	}
}

func TestUpsertMacroSeriesIdempotent(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	rows := []MacroSeries{
		{SeriesCode: "CUSR0000SA0", Date: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Value: 318.0, Unit: "index", Frequency: "monthly", Source: "bls"},
		{SeriesCode: "CUSR0000SA0", Date: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Value: 319.5, Unit: "index", Frequency: "monthly", Source: "bls"},
	}

	apply := func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err := UpsertMacroSeries(ctx, tx, rows); err != nil {
			t.Fatalf("UpsertMacroSeries: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
	}
	apply()
	apply()

	n := 0
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM macro_series WHERE series_code='CUSR0000SA0'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("se esperaban 2 filas, hay %d", n)
	}

	got, err := GetMacroSeries(ctx, pool, "CUSR0000SA0", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetMacroSeries: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("se esperaban 2 filas devueltas, hay %d", len(got))
	}
	latest, err := GetLatestMacroValue(ctx, pool, "CUSR0000SA0")
	if err != nil {
		t.Fatalf("GetLatestMacroValue: %v", err)
	}
	if latest.Value != 319.5 {
		t.Fatalf("latest esperado 319.5, got %v", latest.Value)
	}
}

func TestUpsertDerivedMetricsIdempotentAndNulls(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	sec, err := UpsertSecurity(ctx, pool, &Security{Ticker: "TEST", CIK: "0000000001", Name: "Test Corp", Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}

	asOf := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	// pe_ratio con value NULL (inputs insuficientes) + snapshot de diagnóstico.
	metrics := []DerivedMetric{
		{SecurityID: sec.ID, AsOf: asOf, Metric: "pe_ratio", Value: nil, InputsSnapshot: []byte(`{"price":170.0,"eps":null}`), ModelVersion: "1.0.0"},
		{SecurityID: sec.ID, AsOf: asOf, Metric: "roe", Value: ptr(0.25), InputsSnapshot: []byte(`{"net_earnings":100,"equity":400}`), ModelVersion: "1.0.0"},
	}

	apply := func() {
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
	}
	apply()
	apply()

	n := 0
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM derived_metrics WHERE security_id=$1`, sec.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("se esperaban 2 filas, hay %d", n)
	}

	// value NULL (no silenciado a cero).
	var peVal *float64
	if err := pool.QueryRow(ctx, `SELECT value FROM derived_metrics WHERE security_id=$1 AND metric='pe_ratio'`, sec.ID).Scan(&peVal); err != nil {
		t.Fatalf("scan pe_ratio: %v", err)
	}
	if peVal != nil {
		t.Fatalf("pe_ratio con inputs insuficientes debe ser NULL, got %v", *peVal)
	}

	latest, err := GetLatestMetrics(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("GetLatestMetrics: %v", err)
	}
	if len(latest) != 2 {
		t.Fatalf("se esperaban 2 métricas latest, hay %d", len(latest))
	}
}

func TestGetLatestMetricsScopedToSecurity(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	secA, err := UpsertSecurity(ctx, pool, &Security{Ticker: "TESTA", CIK: "0000000101", Name: "Test A", Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("UpsertSecurity A: %v", err)
	}
	secB, err := UpsertSecurity(ctx, pool, &Security{Ticker: "TESTB", CIK: "0000000102", Name: "Test B", Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("UpsertSecurity B: %v", err)
	}

	asOf := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	// Métricas diferentes para el mismo as_of
	metrics := []DerivedMetric{
		{SecurityID: secA.ID, AsOf: asOf, Metric: "pe_ratio", Value: ptr(10.0), InputsSnapshot: []byte(`{"test":true}`), ModelVersion: "1.0.0"},
		{SecurityID: secA.ID, AsOf: asOf, Metric: "roe", Value: ptr(0.2), InputsSnapshot: []byte(`{"test":true}`), ModelVersion: "1.0.0"},
		{SecurityID: secB.ID, AsOf: asOf, Metric: "pe_ratio", Value: ptr(99.0), InputsSnapshot: []byte(`{"test":true}`), ModelVersion: "1.0.0"},
		{SecurityID: secB.ID, AsOf: asOf, Metric: "roe", Value: ptr(0.9), InputsSnapshot: []byte(`{"test":true}`), ModelVersion: "1.0.0"},
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

	aMetrics, err := GetLatestMetrics(ctx, pool, secA.ID)
	if err != nil {
		t.Fatalf("GetLatestMetrics A: %v", err)
	}
	if len(aMetrics) != 2 {
		t.Fatalf("se esperaban 2 métricas para A, hay %d", len(aMetrics))
	}
	for i := range aMetrics {
		if aMetrics[i].SecurityID != secA.ID {
			t.Fatalf("métrica de otro security en A: %d", aMetrics[i].SecurityID)
		}
	}
	if aMetrics[0].Metric != "pe_ratio" || aMetrics[0].Value == nil || *aMetrics[0].Value != 10.0 {
		t.Fatalf("pe_ratio A incorrecto: %+v", aMetrics[0])
	}
	if aMetrics[1].Metric != "roe" || aMetrics[1].Value == nil || *aMetrics[1].Value != 0.2 {
		t.Fatalf("roe A incorrecto: %+v", aMetrics[1])
	}

	bMetrics, err := GetLatestMetrics(ctx, pool, secB.ID)
	if err != nil {
		t.Fatalf("GetLatestMetrics B: %v", err)
	}
	if len(bMetrics) != 2 {
		t.Fatalf("se esperaban 2 métricas para B, hay %d", len(bMetrics))
	}
	for i := range bMetrics {
		if bMetrics[i].SecurityID != secB.ID {
			t.Fatalf("métrica de otro security en B: %d", bMetrics[i].SecurityID)
		}
	}
	if bMetrics[0].Metric != "pe_ratio" || bMetrics[0].Value == nil || *bMetrics[0].Value != 99.0 {
		t.Fatalf("pe_ratio B incorrecto: %+v", bMetrics[0])
	}
	if bMetrics[1].Metric != "roe" || bMetrics[1].Value == nil || *bMetrics[1].Value != 0.9 {
		t.Fatalf("roe B incorrecto: %+v", bMetrics[1])
	}
}
