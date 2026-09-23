//go:build integration

package api_test

import (
	"net/http"
	"testing"
)

// TestStaticWithDB: con la BD real (pool del TestMain de
// m3_integration_test.go) y el SPA fallback registrado, las rutas API
// conservan su contrato exacto:
//   - /health responde 200 ok (no index.html);
//   - /score/UNKNOWN mantiene el 404 JSON not_found (riesgo M4-R2);
//   - el SPA sigue sirviendo index.html para paths de navegación.
//
// pool nil se cubre en TestStaticServing (suite unit, sin BD).
func TestStaticWithDB(t *testing.T) {
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	dir := mkStaticDist(t)
	h := newStaticHandler(pool, dir)

	t.Run("health-200", func(t *testing.T) {
		rec, body := do(t, h, http.MethodGet, "/health")
		if rec.Code != http.StatusOK {
			t.Fatalf("/health esperado 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		if body["status"] != "ok" {
			t.Fatalf("status inesperado: %v", body)
		}
		if rec.Body.String() == fakeIndex {
			t.Fatalf("/health no debe servir index.html")
		}
	})

	t.Run("score-unknown-404-json", func(t *testing.T) {
		rec, body := do(t, h, http.MethodGet, "/score/UNKNOWN")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("/score/UNKNOWN esperado 404, got %d (%s)", rec.Code, rec.Body.String())
		}
		if errCode(t, body) != "not_found" {
			t.Fatalf("error code inesperado: %v", body)
		}
		if rec.Body.String() == fakeIndex {
			t.Fatalf("/score/UNKNOWN no debe servir index.html")
		}
	})

	t.Run("spa-sigue-funcionando", func(t *testing.T) {
		rec := staticGet(t, h, "/ticker/AAPL")
		if rec.Code != http.StatusOK || rec.Body.String() != fakeIndex {
			t.Fatalf("SPA fallback roto: code=%d body=%q", rec.Code, rec.Body.String())
		}
	})
}
