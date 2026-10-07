//go:build integration

package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestStagedNormalizedCIKs es la semántica del universo por defecto de la
// re-ingesta (`make reingest-fundamentals` sin TICKERS=). Es la pieza que
// evita la caída de la que habla el plan (Az6): re-canonizar las ~10.400
// empresas del catálogo `securities` cuando sólo ~40 se han ingerido.
//
// Semántica exigida, y por qué cada filtro:
//   - `payload_type = 'company_facts'`: `edgar_staging` también guarda otros
//     tipos de payload (submissions, etc.); una fila de otro tipo no tiene
//     `fundamentals` que re-canonizar.
//   - `normalized = true`: una fila pendiente es una normalización que FALLÓ o
//     que nunca se ejecutó; reintentarla es una decisión de diagnóstico.
//   - CIK, no ticker: el staging guarda el CIK (10 dígitos) y es la clave con la
//     que `DeleteStagingByCIK` borra en `-fresh`.
//
// Corre contra `abys_test` vía `make integration` (el TestMain de storage toma
// el advisory lock compartido y `truncateDataTables` aborta si el nombre de la BD
// no acaba en `_test`).
func TestStagedNormalizedCIKs(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	// Vacío: sin staging normalizado el selector devuelve lista vacía (no error);
	// es el CLI el que lo convierte en mensaje y salida 1, para no "ingerir" el
	// DefaultCompany por sorpresa. Se comprueba PRIMERO para no gastar un segundo
	// TRUNCATE de tablas compartidas (este paquete corre en paralelo con cmd/api).
	empty, err := StagedNormalizedCIKs(ctx, pool)
	if err != nil {
		t.Fatalf("StagedNormalizedCIKs (vacío): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("sin staging normalizado el universo debe estar vacío, got %v", empty)
	}

	filing := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	seed := func(cik, ticker, payloadType string, normalized bool) {
		t.Helper()
		id, err := InsertStaging(ctx, pool, &EdgarStaging{
			CIK: cik, Ticker: ptr(ticker), Accession: "companyfacts/" + cik,
			FormType: "10-K", FilingDate: filing,
			Payload: []byte(`{"cik":0,"facts":{}}`), PayloadType: payloadType,
		})
		if err != nil {
			t.Fatalf("InsertStaging(%s, %s): %v", cik, payloadType, err)
		}
		if id == nil {
			t.Fatalf("InsertStaging(%s, %s) no devolvió id", cik, payloadType)
		}
		if normalized {
			if err := MarkStagingNormalized(ctx, pool, *id); err != nil {
				t.Fatalf("MarkStagingNormalized(%s): %v", cik, err)
			}
		}
	}

	// Catálogo `securities` mucho mayor que lo ingerido (la caída de la Az6: ~10.400
	// empresas en el catálogo vs ~40 ingeridas). Se siembra ANTES que el staging
	// para que el selector tenga la tentación de devolverlo entero.
	const catalogSize = 25
	for i := 0; i < catalogSize; i++ {
		if _, err := UpsertSecurity(ctx, pool, &Security{
			Ticker: fmt.Sprintf("CAT%02d", i), CIK: fmt.Sprintf("0000%06d", 900000+i),
			Name: fmt.Sprintf("Catalog Corp %02d", i), Type: "stock",
			Currency: "USD", Status: "active",
		}); err != nil {
			t.Fatalf("UpsertSecurity del catálogo %d: %v", i, err)
		}
	}

	seed("0001045810", "NVDA", "company_facts", true) // entra
	seed("0000320193", "AAPL", "company_facts", true) // entra
	seed("0000199617", "GEV", "company_facts", false)
	seed("0000036404", "XOM", "submissions", true)

	got, err := StagedNormalizedCIKs(ctx, pool)
	if err != nil {
		t.Fatalf("StagedNormalizedCIKs: %v", err)
	}
	want := []string{"0000320193", "0001045810"} // ordenado ascendente
	if len(got) != len(want) {
		t.Fatalf("universo con staging: got %v, want %v (sólo companyfacts normalizados, NO las %d del catálogo securities)", got, want, catalogSize)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("universo con staging: got %v, want %v", got, want)
		}
	}

	// Duplicado real: dos filas del mismo CIK companyfacts normalizadas (p. ej.
	// tras un accession distinto) deben colapsar en una sola empresa.
	second := "0000320193-24-000123"
	id, err := InsertStaging(ctx, pool, &EdgarStaging{
		CIK: "0000320193", Ticker: ptr("AAPL"), Accession: second,
		FormType: "10-K", FilingDate: filing,
		Payload: []byte(`{"cik":0,"facts":{}}`), PayloadType: "company_facts",
	})
	if err != nil || id == nil {
		t.Fatalf("InsertStaging(acceso distinto) falló: id=%v err=%v", id, err)
	}
	if err := MarkStagingNormalized(ctx, pool, *id); err != nil {
		t.Fatalf("MarkStagingNormalized(acceso distinto): %v", err)
	}
	if got, err = StagedNormalizedCIKs(ctx, pool); err != nil {
		t.Fatalf("StagedNormalizedCIKs (2): %v", err)
	} else if len(got) != 2 {
		t.Fatalf("dos filas del mismo CIK deben dar UNA empresa, got %v", got)
	}
}
