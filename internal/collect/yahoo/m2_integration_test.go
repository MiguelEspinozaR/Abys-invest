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
	"github.com/miky/abys-invest/internal/testsupport"
)

// Integration coverage for the Yahoo adapter -> daily_prices pipeline.
// Requires DATABASE_URL; skipped otherwise.

var testPool *pgxpool.Pool

// yahooRelease unlocks the shared integration advisory lock (see
// testsupport.LockIntegrationDB). Held for the whole suite because the suite
// TRUNCATEs tables shared with the other integration packages.
var yahooRelease func()

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		pool, err := storage.Connect(ctx, dsn)
		cancel()
		if err == nil {
			ctx2, cancel2 := context.WithTimeout(context.Background(), 15*time.Second)
			if err := storage.EnsureTestDatabase(ctx2, pool); err == nil {
				// Waiting for the shared lock can take as long as the other
				// suites ahead in the queue, so it gets its OWN generous
				// context: the setup timeout above is not a lock timeout.
				lockCtx, lockCancel := context.WithTimeout(context.Background(), 10*time.Minute)
				release, lerr := testsupport.LockIntegrationDB(lockCtx, pool)
				lockCancel()
				if lerr != nil {
					// LOUD, never a silent skip: a suite that cannot take the
					// lock would otherwise report "skipped" and the whole
					// package would look green while testing nothing.
					panic("yahoo integration: no se pudo tomar el advisory lock: " + lerr.Error())
				}
				yahooRelease = release
				testPool = pool
			} else {
				pool.Close()
			}
			cancel2()
		}
	}
	code := m.Run()
	if release := yahooRelease; release != nil {
		release()
	}
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

	// Quote: upsert del CIERRE REGULAR en la fecha del exchange (§22). Como la
	// serie histórica ya tiene esa fecha, la fila conserva source 'yahoo' y solo
	// se refresca `close`.
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
	// El quote NO puede convertir una barra histórica en una barra sintética:
	// ni source 'yahoo_quote' ni OHLC perdido (regresión §22, la que
	// corrompía la última barra de la serie de 5 años).
	if latest.Source != "yahoo" && latest.Source != "yahoo_quote" {
		t.Fatalf("source inesperado en la última barra: %q", latest.Source)
	}
	if latest.Open == nil {
		t.Fatal("la última barra no puede quedarse sin open (§22): el quote no debe degradar la serie histórica")
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
