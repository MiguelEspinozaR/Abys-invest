package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/api"
)

// Marcadores del build falso generado en mkStaticDist.
const (
	fakeIndex = `<div id="root">FAKE-INDEX</div>`
	fakeAppJS = "console.log('app')"
)

// newStaticHandler reproduce la construcción del router en cmd/api/main.go:
// rutas API primero (NewRouter, con el directorio de estáticos para la
// negociación de contenido de /watchlist), estáticos después (RegisterStatic)
// y el middleware por encima. pool puede ser nil (API degradada, sin BD).
func newStaticHandler(pool *pgxpool.Pool, staticDir string) http.Handler {
	mux := api.NewRouter(pool, api.WithStaticDir(staticDir))
	api.RegisterStatic(mux, staticDir)
	return api.WithMiddleware(mux)
}

func staticGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return staticGetAccept(t, h, path, "")
}

// staticGetAccept permite fijar el header Accept (negociación de contenido de
// /watchlist, plan M5 §B4). accept vacío = petición sin header.
func staticGetAccept(t *testing.T, h http.Handler, path, accept string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	h.ServeHTTP(rec, req)
	return rec
}

// mkStaticDist crea un web/dist falso (index.html + assets/app.js).
func mkStaticDist(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir dist/assets: %v", err)
	}
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("index.html", fakeIndex)
	write("assets/app.js", fakeAppJS)
	return dir
}

// TestStaticServing cubre la T4 del plan M4 (D2): /static/*, / y SPA fallback
// con el dist presente, sin que el fallback intercepte las rutas API.
func TestStaticServing(t *testing.T) {
	dir := mkStaticDist(t)
	h := newStaticHandler(nil, dir)

	t.Run("raiz-sirve-index", func(t *testing.T) {
		rec := staticGet(t, h, "/")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET / esperado 200, got %d (%q)", rec.Code, rec.Body.String())
		}
		if rec.Body.String() != fakeIndex {
			t.Fatalf("GET / debe servir index.html, got %q", rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("Content-Type esperado text/html, got %q", ct)
		}
	})

	t.Run("static-file", func(t *testing.T) {
		rec := staticGet(t, h, "/static/assets/app.js")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /static/assets/app.js esperado 200, got %d", rec.Code)
		}
		if rec.Body.String() != fakeAppJS {
			t.Fatalf("contenido inesperado: %q", rec.Body.String())
		}
	})

	t.Run("spa-fallback", func(t *testing.T) {
		// Routing del react-router: paths de navegación sirven index.html.
		rec := staticGet(t, h, "/ticker/AAPL")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /ticker/AAPL esperado 200 (index.html), got %d", rec.Code)
		}
		if rec.Body.String() != fakeIndex {
			t.Fatalf("SPA fallback debería servir index.html, got %q", rec.Body.String())
		}
	})

	t.Run("health-sigue-siendo-api", func(t *testing.T) {
		// Sin BD (pool nil) /health responde 503 degraded JSON — pero es la
		// respuesta de la API, nunca el SPA.
		rec := staticGet(t, h, "/health")
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("/health debe responder JSON de la API, no SPA (ct=%q body=%q)", ct, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"status"`) {
			t.Fatalf("/health JSON inesperado: %q", rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), fakeIndex) {
			t.Fatalf("/health no debe servir index.html: %q", rec.Body.String())
		}
	})

	t.Run("ruta-api-desconocida-no-cae-en-spa", func(t *testing.T) {
		// El mux (Go 1.22+) enruta /score/UNKNOWN al handler de la API por
		// especificidad. Con pool nil el handler devuelve el envelope JSON del
		// error (500 internal_error vía panic-recovery); lo esencial para T4
		// es que NUNCA sea HTML/SPA. Los paths malformados de la API
		// (/securities/, /score/UNKNOWN/extra) caen en el catch-all y el
		// guard isAPIRoute les responde el envelope 404 JSON.
		for _, p := range []string{"/score/UNKNOWN", "/securities/", "/score/UNKNOWN/extra", "/prices/X/extra", "/backtest/rsi/x", "/alerts"} {
			rec := staticGet(t, h, p)
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("%s debe responder envelope JSON de la API, no SPA (ct=%q body=%q)", p, ct, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"error"`) {
				t.Fatalf("%s sin envelope de error: %q", p, rec.Body.String())
			}
		}
	})

	t.Run("path-traversal-no-escapo-del-dist", func(t *testing.T) {
		// ServeMux redirige los ".." (307) y FileServer/SPA limpian el path;
		// el contenido externo a web/dist nunca se sirve.
		for _, p := range []string{"/static/../go.mod", "/%2e%2e/go.mod"} {
			rec := staticGet(t, h, p)
			if strings.Contains(rec.Body.String(), "module github.com/miky/abys-invest") {
				t.Fatalf("%s filtró contenido fuera de staticDir", p)
			}
		}
	})

	// M5 (plan §B4, negociación de contenido): /watchlist es a la vez
	// endpoint JSON y página del SPA. Aquí se comprueba solo la rama del SPA
	// (no necesita BD: el shell se sirve antes de tocar el pool); el array
	// JSON con BD real lo cubre m5_integration_test.go.
	t.Run("watchlist-acepta-html-sirve-spa", func(t *testing.T) {
		rec := staticGetAccept(t, h, "/watchlist", "text/html,application/xhtml+xml")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /watchlist con Accept: text/html esperado 200, got %d (%q)", rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("Content-Type esperado text/html, got %q", ct)
		}
		if rec.Body.String() != fakeIndex {
			t.Fatalf("GET /watchlist con Accept: text/html debe servir index.html, got %q", rec.Body.String())
		}
	})

	t.Run("watchlist-sin-html-devuelve-json", func(t *testing.T) {
		// Sin Accept y con */* (default de curl) la respuesta es la de la
		// API; con pool nil eso es el envelope 503, nunca el shell del SPA.
		for _, accept := range []string{"", "*/*", "application/json"} {
			rec := staticGetAccept(t, h, "/watchlist", accept)
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("Accept %q: /watchlist debe responder JSON de la API (ct=%q body=%q)", accept, ct, rec.Body.String())
			}
			if rec.Body.String() == fakeIndex {
				t.Fatalf("Accept %q: /watchlist no debe servir index.html", accept)
			}
		}
	})
}

// TestStaticDisabled: sin directorio estático (STATIC_DIR inexistente), no se
// registra nada y el router conserva el comportamiento previo.
func TestStaticDisabled(t *testing.T) {
	h := newStaticHandler(nil, filepath.Join(t.TempDir(), "no-such-dist"))

	t.Run("no-registra-raiz", func(t *testing.T) {
		rec := staticGet(t, h, "/")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("sin estáticos GET / debe ser 404 del mux, got %d (%q)", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), fakeIndex) {
			t.Fatalf("sin estáticos no debe servirse index.html: %q", rec.Body.String())
		}
	})

	t.Run("no-registra-spa", func(t *testing.T) {
		rec := staticGet(t, h, "/ticker/AAPL")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("sin estáticos GET /ticker/AAPL debe ser 404 del mux, got %d (%q)", rec.Code, rec.Body.String())
		}
	})

	t.Run("health-intacto", func(t *testing.T) {
		rec := staticGet(t, h, "/health")
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("/health debe seguir siendo JSON de la API, got ct=%q body=%q", ct, rec.Body.String())
		}
	})

	t.Run("watchlist-json-sin-build-del-frontend", func(t *testing.T) {
		// Sin index.html no hay SPA que negociar: la negociación cae al JSON
		// de la API (con pool nil, 503) en vez de servir un 404 del FileServer.
		rec := staticGetAccept(t, h, "/watchlist", "text/html")
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("sin dist, /watchlist debe responder JSON, got ct=%q body=%q", ct, rec.Body.String())
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("sin dist y sin BD se espera 503, got %d (%q)", rec.Code, rec.Body.String())
		}
	})
}
