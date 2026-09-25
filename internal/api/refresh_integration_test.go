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
	"github.com/miky/abys-invest/internal/storage"
)

// E2E del plan M4c (B4/B3): los endpoints de refresh recalcular el pipeline
// desde el dashboard. Política: loopback-only (403 fuera) + anti-concurrencia
// 409 (documentada; no se dispara en CI porque un refresh sincrónico no se
// solapa sin un segundo worker real).
//
// Ejecución: DATABASE_URL=... go test ./internal/api -p 1 -tags=integration -count=1
// Reutiliza el pool de m3_integration_test.go (TestMain de package api_test,
// migraciones ya aplicadas). El refresh recalcula el universo de securities
// con precio (M3TST del fixture m3 + M4RFS propio); aquí solo se verifica el
// contrato HTTP/status, no los valores.

const m4rfsT = "M4RFS"

// seedRefreshFixture crea una security con fundamentales FY + serie de precios
// para que POST /refresh tenga trabajo real que recalcular (sin red: solo
// metrics + scores desde la BD).
func seedRefreshFixture(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	sec, err := storage.UpsertSecurity(ctx, pool, &storage.Security{
		Ticker: m4rfsT, CIK: "900000010", Name: "M4 Refresh Co", Type: "stock",
		Currency: "USD", Status: "active",
	})
	if err != nil {
		t.Fatalf("upsert security: %v", err)
	}

	periodEnd := time.Date(2025, 9, 28, 0, 0, 0, 0, time.UTC)
	fy := "FY"
	src := "test"
	u := "USD"
	fundVal := func(concept string, v float64) storage.Fundamental {
		vv := v
		return storage.Fundamental{
			SecurityID: sec.ID, Concept: concept, Value: &vv, Unit: &u,
			PeriodType: "P", PeriodStart: &periodEnd, PeriodEnd: periodEnd,
			FiscalYear: int16Ptr(2025), FiscalPeriod: &fy, FilingDate: &periodEnd,
			Source: src,
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	funds := []storage.Fundamental{
		fundVal("net_earnings", 112000), fundVal("shares_outstanding", 15400),
		fundVal("shareholders_equity", 62000), fundVal("total_liabilities", 302000),
		fundVal("free_cash_flow", 98500), fundVal("long_term_debt", 98959),
		fundVal("short_term_debt", 19987), fundVal("cash_and_equivalents", 29943),
		fundVal("operating_cash_flow", 118254), fundVal("capex", -9445),
	}
	if err := storage.UpsertFundamentals(ctx, tx, funds); err != nil {
		t.Fatalf("upsert fundamentals: %v", err)
	}
	start := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	prices := make([]storage.DailyPrice, 0, 300)
	for i := 0; i < 300; i++ {
		close := 100 + float64(i)*0.2
		prices = append(prices, storage.DailyPrice{
			SecurityID: sec.ID, Date: start.AddDate(0, 0, i),
			Close: close, AdjustedClose: close, Open: &close, High: &close, Low: &close, Source: "test",
		})
	}
	if err := storage.UpsertDailyPrices(ctx, tx, prices); err != nil {
		t.Fatalf("upsert prices: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// doRemote hace una request con RemoteAddr explícito (control de la política
// loopback del refresh).
func doRemote(t *testing.T, router http.Handler, method, path, remote string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	router.ServeHTTP(rec, req)
	var body map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec, body
}

func TestRefreshEndpoints(t *testing.T) {
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	seedRefreshFixture(t)
	router := api.NewRouter(pool)

	t.Run("refresh-loopback-ok", func(t *testing.T) {
		rec, body := doRemote(t, router, http.MethodPost, "/refresh", "127.0.0.1:8082")
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /refresh loopback esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		if body["ok"] != true {
			t.Fatalf("ok esperado true, got %v", body)
		}
		if n, _ := body["tickers"].(float64); n < 1 {
			t.Fatalf("tickers esperado >=1, got %v", body["tickers"])
		}
		if _, ok := body["duration_ms"]; !ok {
			t.Fatal("duration_ms ausente en la respuesta")
		}
	})

	t.Run("refresh-remote-forbidden", func(t *testing.T) {
		rec, body := doRemote(t, router, http.MethodPost, "/refresh", "198.51.100.7:5555")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST /refresh remoto esperado 403, got %d (%s)", rec.Code, rec.Body.String())
		}
		if errCode(t, body) != "forbidden" {
			t.Fatalf("error code esperado forbidden, got %v", body)
		}
	})

	t.Run("force-refresh-remote-forbidden", func(t *testing.T) {
		rec, body := doRemote(t, router, http.MethodPost, "/force-refresh", "203.0.113.9:4444")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST /force-refresh remoto esperado 403, got %d (%s)", rec.Code, rec.Body.String())
		}
		if errCode(t, body) != "forbidden" {
			t.Fatalf("error code esperado forbidden, got %v", body)
		}
	})

	t.Run("refresh-method-not-allowed", func(t *testing.T) {
		rec, _ := doRemote(t, router, http.MethodGet, "/refresh", "127.0.0.1:8082")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET /refresh esperado 405, got %d", rec.Code)
		}
	})
}

// El force-refresh en loopback ejecuta la ingesta real (SEC/EDGAR + Yahoo) y
// solo hace sentido en un entorno con red y USER-AGENT SEC propio; en CI se
// omite. El contrato de respuesta en éxito es {"ok":true,...,"steps":{...}}.
func TestForceRefreshLoopbackSkips(t *testing.T) {
	t.Skip("requiere acceso a SEC/EDGAR + Yahoo desde el entorno de test")
}
