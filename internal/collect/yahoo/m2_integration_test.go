//go:build integration

package yahoo

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

// Integration coverage for the Yahoo adapter -> daily_prices pipeline.
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
		t.Skip("DATABASE_URL no configurado; saltando tests de integración yahoo")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := storage.RunMigrations(ctx, testPool, "../../../migrations"); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return testPool
}

// testClient returns a client backed by an httptest server that serves the
// real AAPL chart fixtures (reproducible, sin red externa).
func testClient(t *testing.T) *Client {
	t.Helper()
	hist := loadFixture(t, "chart_aapl_5y.json")
	quote := loadFixture(t, "chart_aapl_1d.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("range") == "1d" {
			w.Write(quote)
			return
		}
		w.Write(hist)
	}))
	t.Cleanup(srv.Close)
	return NewClient(WithBaseURL(srv.URL), WithRequestsPerSecond(1000), WithRetryBase(time.Millisecond))
}

// TestYahooPricesE2E ingesta el histórico OHLCV de AAPL (fixture real) y
// verifica persistencia, quote y latest (CA-1, CA-6).
func TestYahooPricesE2E(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: "M2YAHOO", CIK: "9900000001", Name: "M2 Yahoo Test",
		Type: "stock", Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("UpsertSecurity: %v", err)
	}

	// Limpia precios de este security (el test es reproducible).
	if _, err := pool.Exec(ctx, `DELETE FROM daily_prices WHERE security_id=$1`, sec.ID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	client := testClient(t) // httptest sirviendo fixtures reales
	n, err := client.IngestPrices(ctx, pool, sec.ID, "AAPL")
	if err != nil {
		t.Fatalf("IngestPrices: %v", err)
	}
	if n < 1000 {
		t.Fatalf("se esperaban >=1000 barras ingeridas, hay %d", n)
	}

	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM daily_prices WHERE security_id=$1`, sec.ID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows < 1000 {
		t.Fatalf("filas en daily_prices insuficientes: %d", rows)
	}

	// Quote actual upsert (día de hoy).
	bar, err := client.IngestQuote(ctx, pool, sec.ID, "AAPL")
	if err != nil {
		t.Fatalf("IngestQuote: %v", err)
	}
	if bar.Close <= 0 {
		t.Fatalf("quote inválido: %v", bar.Close)
	}

	latest, err := storage.GetLatestPrice(ctx, pool, sec.ID)
	if err != nil {
		t.Fatalf("GetLatestPrice: %v", err)
	}
	if latest.Close <= 0 {
		t.Fatalf("latest close inválido: %v", latest.Close)
	}
	if latest.Source != "yahoo" {
		t.Fatalf("source esperado 'yahoo', got %q", latest.Source)
	}
}

// TestIdempotencyPrices ingesta dos veces y verifica que no duplica filas.
func TestIdempotencyPrices(t *testing.T) {
	pool := requireDB(t)
	ctx := context.Background()

	sec, err := storage.GetSecurityByTicker(ctx, pool, "M2YAHOO")
	if err != nil {
		t.Fatalf("security M2YAHOO no disponible (correr TestYahooPricesE2E antes): %v", err)
	}

	client := testClient(t)
	if _, err := client.IngestPrices(ctx, pool, sec.ID, "AAPL"); err != nil {
		t.Fatalf("IngestPrices (1): %v", err)
	}
	if _, err := client.IngestPrices(ctx, pool, sec.ID, "AAPL"); err != nil {
		t.Fatalf("IngestPrices (2): %v", err)
	}

	var dupes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM (
		SELECT security_id, date FROM daily_prices WHERE security_id=$1
		GROUP BY security_id, date HAVING count(*) > 1) d`, sec.ID).Scan(&dupes); err != nil {
		t.Fatalf("count dupes: %v", err)
	}
	if dupes != 0 {
		t.Fatalf("se encontraron duplicados (security_id, date): %d", dupes)
	}
}
