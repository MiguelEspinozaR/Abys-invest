//go:build integration

package macro

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
)

// Integration coverage for the BLS adapter -> macro_series pipeline.
// Requires DATABASE_URL; skipped otherwise.

var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		pool, err := storage.Connect(ctx, dsn)
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

func requireDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testPool == nil {
		t.Skip("DATABASE_URL no configurado; saltando tests de integración macro")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := storage.RunMigrations(ctx, testPool, "../../../migrations"); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return testPool
}

// testClient returns a BLS client backed by the real CPI fixture.
func testClient(t *testing.T) *Client {
	t.Helper()
	fixture := loadFixture(t, "bls_cpi_response.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	t.Cleanup(srv.Close)
	return NewClient("", WithBaseURL(srv.URL), WithRequestsPerMinute(60), WithRetryBase(time.Millisecond))
}

// TestBLSToMacroSeries ingesta CPI (fixture real) y verifica la persistencia
// con metadatos correctos (CA-2).
func TestBLSToMacroSeries(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	// Limpia la serie para que el test sea reproducible.
	if _, err := pool.Exec(ctx, `DELETE FROM macro_series WHERE series_code='CUSR0000SA0'`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	client := testClient(t)
	n, err := client.IngestSeries(ctx, pool, "CPI", "2020", "2026")
	if err != nil {
		t.Fatalf("IngestSeries: %v", err)
	}
	if n < 70 {
		t.Fatalf("se esperaban >=70 observaciones, hay %d", n)
	}

	rows, err := storage.GetMacroSeries(ctx, pool, "CUSR0000SA0", time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetMacroSeries: %v", err)
	}
	if len(rows) < 70 {
		t.Fatalf("filas en macro_series insuficientes: %d", len(rows))
	}
	for _, r := range rows {
		if r.Unit != "index" || r.Frequency != "monthly" || r.Source != "bls" {
			t.Fatalf("metadatos incorrectos: unit=%q freq=%q source=%q", r.Unit, r.Frequency, r.Source)
		}
	}

	latest, err := storage.GetLatestMacroValue(ctx, pool, "CUSR0000SA0")
	if err != nil {
		t.Fatalf("GetLatestMacroValue: %v", err)
	}
	if latest.Value <= 0 {
		t.Fatalf("latest inválido: %v", latest.Value)
	}
}

// TestIdempotencyMacro ingesta dos veces y verifica que no duplica filas.
func TestIdempotencyMacro(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	client := testClient(t)
	if _, err := client.IngestSeries(ctx, pool, "CPI", "2020", "2026"); err != nil {
		t.Fatalf("IngestSeries (1): %v", err)
	}
	if _, err := client.IngestSeries(ctx, pool, "CPI", "2020", "2026"); err != nil {
		t.Fatalf("IngestSeries (2): %v", err)
	}

	var dupes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM (
		SELECT series_code, date FROM macro_series WHERE series_code='CUSR0000SA0'
		GROUP BY series_code, date HAVING count(*) > 1) d`).Scan(&dupes); err != nil {
		t.Fatalf("count dupes: %v", err)
	}
	if dupes != 0 {
		t.Fatalf("se encontraron duplicados (series_code, date): %d", dupes)
	}
}
