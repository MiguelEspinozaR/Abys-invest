//go:build integration

package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/miky/abys-invest/internal/metricver"
	"github.com/miky/abys-invest/internal/modelcfg"
	"github.com/miky/abys-invest/internal/relative"
	"github.com/miky/abys-invest/internal/storage"
)

func pf(v float64) *float64 { return &v }

// M6c-T1 review P1-1 (ADR D13, R-M6c-1): ComputeRelativeStage lee los VALORES
// del ticker vía storage.GetDerivedMetricsBySecurity. Tras el bump, roic vive
// en DOS revisiones del mismo (security, as_of) — 0.21@2.1.0 (vigente) y
// 0.30@2.0.0 (stale). Si la stage mezclara revisiones, el orden de inserción
// decidiría el valor de roic y el score 2.2.0 sería no determinista (§27).
//
// El test siembra ambas revisiones, ejecuta la stage DOS veces (determinismo) y
// la compara contra una referencia del MISMO engine construida con los valores
// que devuelve el reader YA filtrado (0.21): si la stage usara 0.30, la
// referencia stale lo delataría. Un tercer slug (fcf_yield) vive SOLO en
// 2.0.0: su omisión garantiza que la stage no "cae a la vieja" incluso en el
// caso en que, por orden físico/índice, el ganador del roic coincida por
// casualidad con el vigente — la garantía por slug se cita (y se prueba) en
// internal/storage: TestDerivedMetricsReadersFiltranRevisionVigente.
func TestComputeRelativeStageFiltraRevisionVigente(t *testing.T) {
	if integPool == nil {
		t.Skip("sin DATABASE_URL")
	}
	truncateM6(t)
	ctx := context.Background()

	sec, err := storage.UpsertSecurity(ctx, integPool, &storage.Security{
		Ticker: "W6REL", CIK: "0000000903", Name: "W6 Relative Stage", Type: "stock",
		Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}
	asOf := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	old := asOf.AddDate(0, -6, 0) // dentro de la ventana de 5 años: base de la mediana histórica §16

	rows := []storage.DerivedMetric{
		// Fecha histórica: solo filas de la revisión vigente.
		{SecurityID: sec.ID, AsOf: old, Metric: "roic", Value: pf(0.21), InputsSnapshot: []byte(`{"h":1}`), ModelVersion: "2.1.0"},
		{SecurityID: sec.ID, AsOf: old, Metric: "net_debt_to_ebitda", Value: pf(2.0), InputsSnapshot: []byte(`{"h":1}`), ModelVersion: "2.1.0"},
		{SecurityID: sec.ID, AsOf: old, Metric: "ev_ebitda", Value: pf(9.0), InputsSnapshot: []byte(`{"h":1}`), ModelVersion: "2.1.0"},
		{SecurityID: sec.ID, AsOf: old, Metric: "pe_ratio", Value: pf(15.0), InputsSnapshot: []byte(`{"h":1}`), ModelVersion: "1.0.0"},
		// Fecha actual: la fila STALE 2.0.0 compite con la vigente 2.1.0 de roic.
		{SecurityID: sec.ID, AsOf: asOf, Metric: "roic", Value: pf(0.21), InputsSnapshot: []byte(`{"cur":1}`), ModelVersion: "2.1.0"},
		{SecurityID: sec.ID, AsOf: asOf, Metric: "roic", Value: pf(0.30), InputsSnapshot: []byte(`{"stale":1}`), ModelVersion: "2.0.0"},
		{SecurityID: sec.ID, AsOf: asOf, Metric: "net_debt_to_ebitda", Value: pf(2.5), InputsSnapshot: []byte(`{"cur":1}`), ModelVersion: "2.1.0"},
		{SecurityID: sec.ID, AsOf: asOf, Metric: "ev_ebitda", Value: pf(8.0), InputsSnapshot: []byte(`{"cur":1}`), ModelVersion: "2.1.0"},
		{SecurityID: sec.ID, AsOf: asOf, Metric: "pe_ratio", Value: pf(17.0), InputsSnapshot: []byte(`{"cur":1}`), ModelVersion: "1.0.0"},
		// Slug STALE-ONLY: fcf_yield existe SOLO en 2.0.0 (el bump aún no la
		// recalculó) y es una de las 9 métricas de §16. Sin el filtro, la stage la
		// tomaría y contaminaría coverage; con el filtro se omite entera (no cae a
		// la vieja). Su valor entraría en Result.Metrics, así que es verificable
		// desde fuera del reader.
		{SecurityID: sec.ID, AsOf: asOf, Metric: "fcf_yield", Value: pf(0.08), InputsSnapshot: []byte(`{"stale-only":1}`), ModelVersion: "2.0.0"},
	}
	tx, err := integPool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := storage.UpsertDerivedMetrics(ctx, tx, rows); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("UpsertDerivedMetrics: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	mc := modelcfg.DefaultModelConfig()
	run := func() *relative.Result {
		t.Helper()
		res, sectorCount, err := ComputeRelativeStage(ctx, integPool, *sec, asOf, nil, mc)
		if err != nil {
			t.Fatalf("ComputeRelativeStage: %v", err)
		}
		if sectorCount != 0 {
			t.Fatalf("sin sector sembrado sectorCount=0, got %d", sectorCount)
		}
		return res
	}

	first := run()
	if first.Score == nil {
		t.Fatalf("con 3 métricas con valor+mediana histórica la dimensión debe puntuar, got %+v", first)
	}
	second := run()
	if *first.Score != *second.Score || first.Coverage != second.Coverage || first.Confidence != second.Confidence {
		t.Fatalf("determinismo roto entre dos invocaciones: %+v vs %+v", first, second)
	}

	// Referencia honesta: los valores que YA devuelve el reader filtrado (P1-1) y
	// las medianas versionadas del storage, en el MISMO engine.
	values, err := storage.GetDerivedMetricsBySecurity(ctx, integPool, sec.ID, asOf)
	if err != nil {
		t.Fatalf("GetDerivedMetricsBySecurity: %v", err)
	}
	hist, err := storage.GetHistoricalMedianWithCount(ctx, integPool, sec.ID, asOf, mc.RelativeHistoricalYears, metricver.AllPairs())
	if err != nil {
		t.Fatalf("GetHistoricalMedianWithCount: %v", err)
	}
	in := relative.Inputs{Ticker: sec.Ticker, AsOf: asOf, Metrics: map[string]*float64{}}
	for _, dm := range values {
		in.Metrics[dm.Metric] = dm.Value
	}
	in.HistoricalMedian = hist.Medians
	in.HistoricalAsOfCount = hist.AsOfCount
	reference := relative.Calculate(in, relative.ConfigFromModelConfig(mc))

	if reference.Score == nil || *first.Score != *reference.Score {
		t.Fatalf("la stage debe producir el MISMO score que el engine con los valores filtrados (vigente): stage %v vs ref %v", first.Score, reference.Score)
	}
	if first.HistoricalScore == nil || reference.HistoricalScore == nil ||
		*first.HistoricalScore != *reference.HistoricalScore || first.Coverage != reference.Coverage {
		t.Fatalf("stage != referencia de revisión vigente: %+v vs %+v", first, reference)
	}

	// Y la prueba de que NO tocó la fila stale: la referencia con roic=0.30
	// (2.0.0) da un histórico DISTINTO. Si la stage hubiera leído sin filtrar,
	// coincidiría con ESTA (o sería no determinista).
	inStale := in
	inStale.Metrics = map[string]*float64{}
	for k, v := range in.Metrics {
		inStale.Metrics[k] = v
	}
	inStale.Metrics["roic"] = pf(0.30)
	stale := relative.Calculate(inStale, relative.ConfigFromModelConfig(mc))
	if stale.HistoricalScore == nil || *stale.HistoricalScore == *first.HistoricalScore {
		t.Fatalf("el valor stale 0.30 debe cambiar el histórico (usado=%v, stale=%v): o la stage leyó 2.0.0 o el test no distingue", *first.HistoricalScore, *stale.HistoricalScore)
	}

	// Y el valor que CHAPTER el trace de la stage es el vigente 0.21, mientras
	// que el slug stale-only (fcf_yield@2.0.0) NO llega a la stage (omisión, no
	// caída a la vieja) y, por tanto, coverage coincide con la referencia.
	for _, m := range first.Metrics {
		if m.Name == "roic" && (m.Value == nil || *m.Value != 0.21) {
			t.Fatalf("la stage debe llegar a roic=0.21 (revisión vigente), got %v", m.Value)
		}
		if m.Name == "fcf_yield" && m.Value != nil {
			t.Fatalf("fcf_yield solo tiene revisión stale (2.0.0) y debe OMITIRSE en la stage, llegó con valor %v (coverage contaminado)", *m.Value)
		}
	}
}
