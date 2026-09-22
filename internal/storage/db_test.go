//go:build integration

package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Integration tests for the storage layer. They require a reachable PostgreSQL
// database: set DATABASE_URL (otherwise the suite is skipped).
//
// Run with: go test ./internal/storage/... -tags=integration -v -count=1

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		pool, err := Connect(ctx, dsn)
		cancel()
		if err == nil {
			testPool = pool
		}
	}
	code := m.Run()
	if testPool != nil {
		testPool.Close()
	}
	os.Exit(code)
}

func requirePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testPool == nil {
		t.Skip("DATABASE_URL no configurado o BD no alcanzable; saltando tests de integración de storage")
	}
	return testPool
}

func requireMigrations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("RunMigrations falló: %v", err)
	}
}

func truncateDataTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `TRUNCATE fundamentals, edgar_staging, securities RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("TRUNCATE falló: %v", err)
	}
}

func TestRunMigrationsIdempotent(t *testing.T) {
	pool := requirePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("primera pasada de migraciones falló: %v", err)
	}
	// Segunda pasada: no debe fallar (idempotente).
	if err := RunMigrations(ctx, pool, "../../migrations"); err != nil {
		t.Fatalf("segunda pasada de migraciones falló (debe ser idempotente): %v", err)
	}

	var tables int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name IN ('securities','fundamentals','edgar_staging','xbrl_concept_map')`).Scan(&tables); err != nil {
		t.Fatalf("query tablas falló: %v", err)
	}
	if tables != 4 {
		t.Fatalf("se esperaban 4 tablas, hay %d", tables)
	}

	var concepts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM xbrl_concept_map`).Scan(&concepts); err != nil {
		t.Fatalf("query xbrl_concept_map falló: %v", err)
	}
	if concepts != 20 {
		t.Fatalf("diccionario canónico: se esperaban 20 conceptos, hay %d", concepts)
	}
}

func TestUpsertSecurityIdempotent(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	sec := &Security{Ticker: "AAPL", CIK: "0000320193", Name: "Apple Inc", Type: "stock", Currency: "USD", Status: "active"}
	first, err := UpsertSecurity(ctx, pool, sec)
	if err != nil {
		t.Fatalf("UpsertSecurity (1) falló: %v", err)
	}
	sec.Name = "APPLE INC."
	second, err := UpsertSecurity(ctx, pool, sec)
	if err != nil {
		t.Fatalf("UpsertSecurity (2) falló: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("upsert duplicó filas: id1=%d id2=%d", first.ID, second.ID)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM securities WHERE ticker = 'AAPL'`).Scan(&count); err != nil {
		t.Fatalf("count falló: %v", err)
	}
	if count != 1 {
		t.Fatalf("se esperaba 1 fila para AAPL, hay %d", count)
	}
	if second.Name != "APPLE INC." {
		t.Fatalf("el update no se aplicó: name=%q", second.Name)
	}

	got, err := GetSecurityByTicker(ctx, pool, "AAPL")
	if err != nil {
		t.Fatalf("GetSecurityByTicker falló: %v", err)
	}
	if got.CIK != "0000320193" {
		t.Fatalf("CIK inesperado: %q", got.CIK)
	}

	if _, err := GetSecurityByTicker(ctx, pool, "NOPE"); err != pgx.ErrNoRows {
		t.Fatalf("se esperaba ErrNoRows para ticker inexistente, got=%v", err)
	}

	listed, err := ListSecurities(ctx, pool, 10, 0)
	if err != nil {
		t.Fatalf("ListSecurities falló: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("ListSecurities: se esperaba 1, hay %d", len(listed))
	}
}

func TestInsertStagingAndMarkNormalized(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	filing := time.Date(2025, 10, 30, 0, 0, 0, 0, time.UTC)
	payload := []byte(`{"cik":320193,"facts":{}}`)
	row := &EdgarStaging{
		CIK: "0000320193", Ticker: ptr("AAPL"), Accession: "0000320193-25-000001",
		FormType: "10-K", FilingDate: filing, Payload: payload, PayloadType: "company_facts",
	}

	id1, err := InsertStaging(ctx, pool, row)
	if err != nil {
		t.Fatalf("InsertStaging (1) falló: %v", err)
	}
	if id1 == nil {
		t.Fatal("InsertStaging (1) no devolvió id")
	}

	// Mismo accession + form + payload_type: se omite (DO NOTHING).
	row.Payload = []byte(`{"other":true}`)
	id2, err := InsertStaging(ctx, pool, row)
	if err != nil {
		t.Fatalf("InsertStaging (2) falló: %v", err)
	}
	if id2 != nil {
		t.Fatalf("InsertStaging duplicado no se omitió: id=%d", *id2)
	}

	pending, err := GetUnprocessedStaging(ctx, pool, 10)
	if err != nil {
		t.Fatalf("GetUnprocessedStaging falló: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("staging pendiente: se esperaba 1 fila, hay %d", len(pending))
	}
	if pending[0].ID != *id1 {
		t.Fatalf("la fila pendiente no es la insertada")
	}

	if err := MarkStagingNormalized(ctx, pool, *id1); err != nil {
		t.Fatalf("MarkStagingNormalized falló: %v", err)
	}
	pending, err = GetUnprocessedStaging(ctx, pool, 10)
	if err != nil {
		t.Fatalf("GetUnprocessedStaging (post-mark) falló: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("no debería quedar staging pendiente, hay %d", len(pending))
	}
}

func TestUpsertFundamentalsIdempotentBatch(t *testing.T) {
	pool := requirePool(t)
	requireMigrations(t, pool)
	truncateDataTables(t, pool)
	ctx := context.Background()

	sec, err := UpsertSecurity(ctx, pool, &Security{Ticker: "AAPL", CIK: "0000320193", Name: "Apple Inc", Type: "stock", Currency: "USD", Status: "active"})
	if err != nil {
		t.Fatalf("UpsertSecurity falló: %v", err)
	}

	end := time.Date(2025, 9, 27, 0, 0, 0, 0, time.UTC)
	fy := int16(2025)
	revenues := 391_000_000_000.0
	netInc := 94_000_000_000.0
	funds := []Fundamental{
		{SecurityID: sec.ID, Concept: "revenues", Value: &revenues, Unit: ptr("USD"), PeriodType: "duration", PeriodEnd: end, FiscalYear: &fy, FiscalPeriod: ptr("FY"), Source: "sec_edgar"},
		{SecurityID: sec.ID, Concept: "net_earnings", Value: &netInc, Unit: ptr("USD"), PeriodType: "duration", PeriodEnd: end, FiscalYear: &fy, FiscalPeriod: ptr("FY"), Source: "sec_edgar"},
	}

	apply := func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin falló: %v", err)
		}
		defer tx.Rollback(ctx) //nolint:errcheck
		if err := UpsertFundamentals(ctx, tx, funds); err != nil {
			t.Fatalf("UpsertFundamentals falló: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit falló: %v", err)
		}
	}

	apply()
	apply() // idempotente: no debe duplicar

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM fundamentals WHERE security_id = $1`, sec.ID).Scan(&count); err != nil {
		t.Fatalf("count falló: %v", err)
	}
	if count != 2 {
		t.Fatalf("se esperaban 2 filas, hay %d", count)
	}

	// Re-upsert con valor cambiado: actualiza en sitio.
	revenues = 392_000_000_000.0
	apply()
	var v float64
	if err := pool.QueryRow(ctx, `SELECT value FROM fundamentals WHERE security_id=$1 AND concept='revenues'`, sec.ID).Scan(&v); err != nil {
		t.Fatalf("scan falló: %v", err)
	}
	if v != revenues {
		t.Fatalf("valor no actualizado: got %v want %v", v, revenues)
	}

	got, err := GetFundamentalsBySecurity(ctx, pool, sec.ID, FundamentalFilter{Concept: "net_earnings"})
	if err != nil {
		t.Fatalf("GetFundamentalsBySecurity falló: %v", err)
	}
	if len(got) != 1 || got[0].Concept != "net_earnings" {
		t.Fatalf("filtro por concept incorrecto: %+v", got)
	}
}

func ptr[T any](v T) *T { return &v }
