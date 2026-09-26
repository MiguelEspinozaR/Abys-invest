//go:build integration

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/miky/abys-invest/internal/api"
	"github.com/miky/abys-invest/internal/storage"
)

// M5 integration coverage (plan M5 B5, SPEC §11bis CA-M5-1/CA-M5-2):
// GET /securities/search (ranking, case-insensitive, límites y 400) y
// GET/PUT/DELETE /watchlist (idempotencia, 404 y 400). Ejecución:
// DATABASE_URL=... go test ./internal/api -p 1 -tags=integration -count=1
//
// Fixtures con prefijo M5WT para no colisionar con el catálogo real.

const (
	m5Tick  = "M5WT"
	m5PeerA = "M5WTA"
	m5PeerB = "M5WTB"
)

// seedM5Fixture crea 3 securities de prueba y limpia sus entradas previas de
// la watchlist (BD compartida: la limpieza es acotada a los fixtures).
func seedM5Fixture(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	exchange, sector := "NAS", "Technology"
	for _, s := range []*storage.Security{
		{Ticker: m5Tick, CIK: "910000001", Name: "M5 Watchlist Corp", Type: "stock", Currency: "USD", Status: "active", Exchange: &exchange, Sector: &sector},
		{Ticker: m5PeerA, CIK: "910000002", Name: "M5 Alpha Industries", Type: "stock", Currency: "USD", Status: "active", Exchange: &exchange, Sector: &sector},
		{Ticker: m5PeerB, CIK: "910000003", Name: "Beta M5 Holdings", Type: "stock", Currency: "USD", Status: "active"},
	} {
		if _, err := storage.UpsertSecurity(ctx, pool, s); err != nil {
			t.Fatalf("upsert security %s: %v", s.Ticker, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM watchlist WHERE security_id IN (SELECT id FROM securities WHERE ticker LIKE 'M5WT%')`); err != nil {
		t.Fatalf("limpieza de watchlist de fixtures: %v", err)
	}
}

// doRaw ejecuta una petición y devuelve el recorder (sin parsear a map, para
// respuestas que son arrays JSON). El RemoteAddr se fija a loopback porque el
// cliente real de estos endpoints es el dashboard servido por el propio API en
// localhost; para ejercitar la política de mutadores de red usa doRemoteRaw.
func doRaw(router http.Handler, method, path string) *httptest.ResponseRecorder {
	return doRemoteRaw(router, method, path, m5LoopbackAddr)
}

// m5LoopbackAddr representa al cliente local del dashboard (navegador en
// localhost), el caso de uso legítimo de los endpoints mutadores.
const m5LoopbackAddr = "127.0.0.1:8082"

// doRemoteRaw ejecuta una petición con RemoteAddr explícito (mismo patrón que
// doRemote en refresh_integration_test.go:90, con el recorder completo para
// poder leer también respuestas array JSON).
func doRemoteRaw(router http.Handler, method, path, remote string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	router.ServeHTTP(rec, req)
	return rec
}

// doLoopback ejecuta una petición como cliente local (RemoteAddr loopback) y
// devuelve el recorder con el envelope parseado. Los mutadores de la watchlist
// son loopback-only (hallazgo F4), así que los casos de 400/404 del contrato
// REST deben pedirlo como el navegador en localhost, no con el 192.0.2.1
// sintético de do.
func doLoopback(t *testing.T, router http.Handler, method, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := doRemoteRaw(router, method, path, m5LoopbackAddr)
	var body map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec, body
}

// doAccept ejecuta GET path fijando el header Accept (negociación de contenido
// de /watchlist, plan M5 §B4). accept vacío = petición sin header (como la
// que hacen do/doRaw).
func doAccept(router http.Handler, path, accept string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	router.ServeHTTP(rec, req)
	return rec
}

func decodeSecurities(t *testing.T, rec *httptest.ResponseRecorder) []storage.Security {
	t.Helper()
	var out []storage.Security
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("respuesta no es un array de securities: %v (%s)", err, rec.Body.String())
	}
	return out
}

func decodeWatchlist(t *testing.T, rec *httptest.ResponseRecorder) []storage.WatchlistItem {
	t.Helper()
	var out []storage.WatchlistItem
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("respuesta no es un array de watchlist: %v (%s)", err, rec.Body.String())
	}
	return out
}

func isOK(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("esperado body JSON: %v (%s)", err, rec.Body.String())
	}
	if body["ok"] != true {
		t.Fatalf(`esperado {"ok":true}, got %v`, body)
	}
}

// expectStatus comprueba solo el código HTTP (respuestas con array JSON).
func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("esperado %d, got %d (%s)", want, rec.Code, rec.Body.String())
	}
}

func TestM5SearchEndpoint(t *testing.T) {
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	seedM5Fixture(t)
	router := api.NewRouter(pool)

	t.Run("ticker-exacto-primero", func(t *testing.T) {
		rec := doRaw(router, http.MethodGet, "/securities/search?q="+m5Tick)
		expectStatus(t, rec, http.StatusOK)
		got := decodeSecurities(t, rec)
		if len(got) == 0 || got[0].Ticker != m5Tick {
			t.Fatalf("la coincidencia exacta de ticker debe ir primera, got %d resultados", len(got))
		}
		// Ranking: exacto (0) → prefijos (1) → nombres (2), ticker ASC dentro
		// de cada rango.
		want := []string{m5Tick, m5PeerA, m5PeerB}
		if len(got) < 3 {
			t.Fatalf("se esperaban al menos 3 resultados para %q, hay %d", m5Tick, len(got))
		}
		for i, w := range want {
			if got[i].Ticker != w {
				t.Fatalf("ranking %d: se esperaba %s, hay %s", i, w, got[i].Ticker)
			}
		}
	})

	t.Run("prefijo-de-ticker", func(t *testing.T) {
		rec := doRaw(router, http.MethodGet, "/securities/search?q=M5WT")
		expectStatus(t, rec, http.StatusOK)
		got := decodeSecurities(t, rec)
		if len(got) < 3 {
			t.Fatalf("el prefijo M5WT debe traer los 3 fixtures, hay %d", len(got))
		}
	})

	t.Run("por-nombre", func(t *testing.T) {
		rec := doRaw(router, http.MethodGet, "/securities/search?q=Alpha%20Industries")
		expectStatus(t, rec, http.StatusOK)
		got := decodeSecurities(t, rec)
		if len(got) == 0 || got[0].Ticker != m5PeerA {
			t.Fatalf("búsqueda por nombre debe encontrar %s, got %d resultados", m5PeerA, len(got))
		}
	})

	t.Run("case-insensitive", func(t *testing.T) {
		rec := doRaw(router, http.MethodGet, "/securities/search?q=m5wt")
		expectStatus(t, rec, http.StatusOK)
		got := decodeSecurities(t, rec)
		if len(got) < 3 {
			t.Fatalf("q en minúsculas debe encontrar los 3 fixtures, hay %d", len(got))
		}
	})

	t.Run("limite", func(t *testing.T) {
		rec := doRaw(router, http.MethodGet, "/securities/search?q=M5WT&limit=2")
		expectStatus(t, rec, http.StatusOK)
		if got := decodeSecurities(t, rec); len(got) != 2 {
			t.Fatalf("limit=2 debe devolver 2 resultados, hay %d", len(got))
		}
	})

	t.Run("limite-maximo-50", func(t *testing.T) {
		rec := doRaw(router, http.MethodGet, "/securities/search?q=e&limit=500")
		// q=1 carácter es inválido: el mínimo manda antes que el recorte.
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400 para q de 1 carácter, got %d", rec.Code)
		}
		rec = doRaw(router, http.MethodGet, "/securities/search?q=en&limit=500")
		expectStatus(t, rec, http.StatusOK)
		if got := decodeSecurities(t, rec); len(got) > 50 {
			t.Fatalf("el límite máximo es 50, devolvió %d", len(got))
		}
	})

	t.Run("sin-resultados-array-vacio", func(t *testing.T) {
		rec := doRaw(router, http.MethodGet, "/securities/search?q=qqqqqqqqqq")
		expectStatus(t, rec, http.StatusOK)
		if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
			t.Fatalf("sin resultados se espera [], got %q", body)
		}
	})

	t.Run("q-requerido", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/securities/search")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400 sin q, got %d", rec.Code)
		}
		if errCode(t, body) != "validation_error" {
			t.Fatalf("error code inesperado: %v", body)
		}
		rec, body = do(t, router, http.MethodGet, "/securities/search?q=%20%20")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400 con q en blanco, got %d", rec.Code)
		}
		if errCode(t, body) != "validation_error" {
			t.Fatalf("error code inesperado: %v", body)
		}
	})

	t.Run("q-corto", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/securities/search?q=a")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400 para q de 1 carácter, got %d", rec.Code)
		}
		if errCode(t, body) != "validation_error" {
			t.Fatalf("error code inesperado: %v", body)
		}
	})

	t.Run("limit-invalido", func(t *testing.T) {
		rec, body := do(t, router, http.MethodGet, "/securities/search?q=M5WT&limit=abc")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400 para limit=abc, got %d", rec.Code)
		}
		if errCode(t, body) != "validation_error" {
			t.Fatalf("error code inesperado: %v", body)
		}
		rec, body = do(t, router, http.MethodGet, "/securities/search?q=M5WT&limit=0")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400 para limit=0, got %d", rec.Code)
		}
		if errCode(t, body) != "validation_error" {
			t.Fatalf("error code inesperado: %v", body)
		}
	})
}

func TestM5WatchlistEndpoints(t *testing.T) {
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	seedM5Fixture(t)
	router := api.NewRouter(pool)

	t.Run("lista-vacia-array", func(t *testing.T) {
		rec := doRaw(router, http.MethodGet, "/watchlist")
		expectStatus(t, rec, http.StatusOK)
		if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
			t.Fatalf("watchlist recién limpiada debe ser [], got %q", body)
		}
	})

	t.Run("put-añade", func(t *testing.T) {
		isOK(t, doRaw(router, http.MethodPut, "/watchlist/"+m5Tick))
		items := decodeWatchlist(t, doRaw(router, http.MethodGet, "/watchlist"))
		if len(items) != 1 || items[0].Ticker != m5Tick || items[0].Name != "M5 Watchlist Corp" {
			t.Fatalf("GET /watchlist debe incluir el ticker con su nombre: %+v", items)
		}
	})

	t.Run("put-repetido-idempotente", func(t *testing.T) {
		isOK(t, doRaw(router, http.MethodPut, "/watchlist/"+m5Tick))
		if items := decodeWatchlist(t, doRaw(router, http.MethodGet, "/watchlist")); len(items) != 1 {
			t.Fatalf("el PUT repetido no debe duplicar: %d entradas", len(items))
		}
	})

	t.Run("put-ticker-desnormalizado", func(t *testing.T) {
		// El ticker se normaliza (mayúsculas) igual que en el resto de la API.
		isOK(t, doRaw(router, http.MethodPut, "/watchlist/"+m5PeerA))
		items := decodeWatchlist(t, doRaw(router, http.MethodGet, "/watchlist"))
		found := false
		for _, it := range items {
			if it.Ticker == m5PeerA {
				found = true
			}
		}
		if !found {
			t.Fatalf("PUT /watchlist/m5wta (minúsculas) debe normalizar a %s: %+v", m5PeerA, items)
		}
	})

	t.Run("put-ticker-inexistente-404", func(t *testing.T) {
		rec, body := doLoopback(t, router, http.MethodPut, "/watchlist/NOPEXXX")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("esperado 404, got %d (%s)", rec.Code, rec.Body.String())
		}
		if errCode(t, body) != "not_found" {
			t.Fatalf("error code inesperado: %v", body)
		}
	})

	t.Run("put-ticker-invalido-400", func(t *testing.T) {
		rec, body := doLoopback(t, router, http.MethodPut, "/watchlist/AA%20BB")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400, got %d (%s)", rec.Code, rec.Body.String())
		}
		if errCode(t, body) != "validation_error" {
			t.Fatalf("error code inesperado: %v", body)
		}
	})

	t.Run("delete-elimina", func(t *testing.T) {
		isOK(t, doRaw(router, http.MethodDelete, "/watchlist/"+m5Tick))
		items := decodeWatchlist(t, doRaw(router, http.MethodGet, "/watchlist"))
		for _, it := range items {
			if it.Ticker == m5Tick {
				t.Fatalf("%s sigue en la watchlist tras DELETE: %+v", m5Tick, items)
			}
		}
	})

	t.Run("delete-repetido-idempotente", func(t *testing.T) {
		isOK(t, doRaw(router, http.MethodDelete, "/watchlist/"+m5Tick))
		isOK(t, doRaw(router, http.MethodDelete, "/watchlist/"+m5Tick))
	})

	t.Run("delete-ticker-inexistente-404", func(t *testing.T) {
		rec, body := doLoopback(t, router, http.MethodDelete, "/watchlist/NOPEXXX")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("esperado 404, got %d (%s)", rec.Code, rec.Body.String())
		}
		if errCode(t, body) != "not_found" {
			t.Fatalf("error code inesperado: %v", body)
		}
	})

	t.Run("delete-ticker-invalido-400", func(t *testing.T) {
		rec, _ := doLoopback(t, router, http.MethodDelete, "/watchlist/AA%20BB")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("esperado 400, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("limpieza-final", func(t *testing.T) {
		for _, tk := range []string{m5Tick, m5PeerA, m5PeerB} {
			isOK(t, doRaw(router, http.MethodDelete, "/watchlist/"+tk))
		}
	})
}

// TestM5WatchlistContentNegotiation (plan M5 §B4, decisión del orquestador
// 2026-09-25): GET /watchlist sirve el shell del SPA a los navegadores
// (Accept: text/html) y el array JSON a los clientes de la API (lo que manda
// getJSON → Accept: application/json). Cubre las tres ramas con el mismo
// montaje que cmd/api/main.go: rutas API + estáticos + middleware.
func TestM5WatchlistContentNegotiation(t *testing.T) {
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	seedM5Fixture(t)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM watchlist WHERE security_id IN (SELECT id FROM securities WHERE ticker LIKE 'M5WT%')`); err != nil {
			t.Errorf("limpieza final de watchlist de fixtures: %v", err)
		}
	})
	// Un elemento en la lista para distinguir el array del shell del SPA.
	isOK(t, doRaw(api.NewRouter(pool), http.MethodPut, "/watchlist/"+m5Tick))

	h := newStaticHandler(pool, mkStaticDist(t))

	t.Run("accept-json-devuelve-array", func(t *testing.T) {
		// Contrato REST (CA-M5-2) intacto: el cliente JSON recibe la lista.
		rec := doAccept(h, "/watchlist", "application/json")
		expectStatus(t, rec, http.StatusOK)
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("Content-Type esperado application/json, got %q", ct)
		}
		// F2: la respuesta varía por Accept, así que debe declararlo para que
		// las cachés guarden la variante JSON separada del shell del SPA.
		if v := rec.Header().Get("Vary"); !strings.Contains(v, "Accept") {
			t.Fatalf("variante JSON: se esperaba Vary: Accept, got %q", v)
		}
		items := decodeWatchlist(t, rec)
		if len(items) != 1 || items[0].Ticker != m5Tick {
			t.Fatalf("GET /watchlist con Accept: application/json debe devolver la lista: %+v", items)
		}
	})

	t.Run("navegador-acepta-html-sirve-index", func(t *testing.T) {
		// Accept real de un navegador en carga directa/F5 de /watchlist: el
		// gap era que el patrón exacto del endpoint ganaba al fallback SPA y se
		// veía el JSON en crudo.
		rec := doAccept(h, "/watchlist", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		expectStatus(t, rec, http.StatusOK)
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("Content-Type esperado text/html, got %q", ct)
		}
		// F2: también la variante HTML declara Vary: Accept.
		if v := rec.Header().Get("Vary"); !strings.Contains(v, "Accept") {
			t.Fatalf("variante HTML: se esperaba Vary: Accept, got %q", v)
		}
		if rec.Body.String() != fakeIndex {
			t.Fatalf("GET /watchlist con Accept: text/html debe servir index.html, got %q", rec.Body.String())
		}
	})

	t.Run("sin-html-en-accept-devuelve-json", func(t *testing.T) {
		// Sin header Accept y con */* (default de curl) se responde la API;
		// el navegador nunca cae aquí porque siempre declara text/html.
		for _, accept := range []string{"", "*/*", "application/json"} {
			rec := doAccept(h, "/watchlist", accept)
			expectStatus(t, rec, http.StatusOK)
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("Accept %q: esperado JSON, got ct=%q", accept, ct)
			}
			if v := rec.Header().Get("Vary"); !strings.Contains(v, "Accept") {
				t.Fatalf("Accept %q: se esperaba Vary: Accept, got %q", accept, v)
			}
			if items := decodeWatchlist(t, rec); len(items) != 1 || items[0].Ticker != m5Tick {
				t.Fatalf("Accept %q: se esperaba la lista con %s, got %+v", accept, m5Tick, items)
			}
		}
	})

	t.Run("prefijo-watchlist-siempre-404-json", func(t *testing.T) {
		// La negociación solo aplica a la ruta exacta /watchlist: el prefijo
		// "watchlist" sigue reservado a la API (apiRouteSegments), así que
		// /watchlist/extra es 404 JSON aunque el navegador pida HTML.
		for _, accept := range []string{"", "application/json", "text/html,application/xhtml+xml"} {
			rec := doAccept(h, "/watchlist/extra", accept)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("Accept %q: GET /watchlist/extra esperado 404, got %d (%s)", accept, rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Fatalf("Accept %q: /watchlist/extra debe responder envelope JSON, got ct=%q", accept, ct)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("Accept %q: envelope no parseable: %v (%s)", accept, err, rec.Body.String())
			}
			if errCode(t, body) != api.CodeNotFound {
				t.Fatalf("Accept %q: error code inesperado para /watchlist/extra: %v", accept, body)
			}
		}
	})
}

// TestM5WatchlistMutationLoopbackOnly (hallazgo F4 de la REVIEW de M5, decisión
// del orquestador 2026-09-25): los PUT/DELETE /watchlist mutan PostgreSQL, así
// que quedan loopback-only con la misma política y el mismo envelope que
// /refresh y /force-refresh (M4c, guardWatchlistMutation + isLoopbackClient).
// Las lecturas (GET /watchlist y /securities/search) siguen abiertas a
// cualquier cliente.
func TestM5WatchlistMutationLoopbackOnly(t *testing.T) {
	if pool == nil {
		t.Skip("sin DATABASE_URL")
	}
	seedM5Fixture(t)
	router := api.NewRouter(pool)
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM watchlist WHERE security_id IN (SELECT id FROM securities WHERE ticker LIKE 'M5WT%')`); err != nil {
			t.Errorf("limpieza final de watchlist de fixtures: %v", err)
		}
	})
	const remote = "198.51.100.7:5555"

	t.Run("put-loopback-ok", func(t *testing.T) {
		isOK(t, doRemoteRaw(router, http.MethodPut, "/watchlist/"+m5Tick, m5LoopbackAddr))
		items := decodeWatchlist(t, doRemoteRaw(router, http.MethodGet, "/watchlist", remote))
		if len(items) != 1 || items[0].Ticker != m5Tick {
			t.Fatalf("el PUT desde loopback debe dejar la entrada: %+v", items)
		}
	})

	t.Run("put-remoto-403", func(t *testing.T) {
		rec := doRemoteRaw(router, http.MethodPut, "/watchlist/"+m5PeerA, remote)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("PUT /watchlist remoto esperado 403, got %d (%s)", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("envelope no parseable: %v (%s)", err, rec.Body.String())
		}
		if errCode(t, body) != api.CodeForbidden {
			t.Fatalf("error code esperado forbidden, got %v", body)
		}
		if items := decodeWatchlist(t, doRemoteRaw(router, http.MethodGet, "/watchlist", remote)); len(items) != 1 {
			t.Fatalf("el PUT remoto no debe mutar la BD: %+v", items)
		}
	})

	t.Run("delete-loopback-ok", func(t *testing.T) {
		isOK(t, doRemoteRaw(router, http.MethodDelete, "/watchlist/"+m5Tick, m5LoopbackAddr))
		if items := decodeWatchlist(t, doRemoteRaw(router, http.MethodGet, "/watchlist", remote)); len(items) != 0 {
			t.Fatalf("el DELETE desde loopback debe vaciar la entrada: %+v", items)
		}
	})

	t.Run("delete-remoto-403", func(t *testing.T) {
		// Con la entrada presente: el 403 debe aparecer antes de cualquier
		// borrado (y antes del 404 de catálogo).
		isOK(t, doRemoteRaw(router, http.MethodPut, "/watchlist/"+m5Tick, m5LoopbackAddr))
		rec := doRemoteRaw(router, http.MethodDelete, "/watchlist/"+m5Tick, remote)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("DELETE /watchlist remoto esperado 403, got %d (%s)", rec.Code, rec.Body.String())
		}
		items := decodeWatchlist(t, doRemoteRaw(router, http.MethodGet, "/watchlist", remote))
		if len(items) != 1 || items[0].Ticker != m5Tick {
			t.Fatalf("el DELETE remoto no debe borrar la entrada: %+v", items)
		}
		isOK(t, doRemoteRaw(router, http.MethodDelete, "/watchlist/"+m5Tick, m5LoopbackAddr))
	})

	t.Run("mutador-remoto-ipv6-no-loopback-403", func(t *testing.T) {
		// isLoopbackClient acepta 127.0.0.0/8 y ::1; cualquier otra IPv6 es
		// cliente de red igual que una IPv4.
		rec := doRemoteRaw(router, http.MethodPut, "/watchlist/"+m5Tick, "[2001:db8::5]:4444")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("PUT /watchlist desde IPv6 remota esperado 403, got %d (%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("mutador-loopback-ipv6-ok", func(t *testing.T) {
		isOK(t, doRemoteRaw(router, http.MethodPut, "/watchlist/"+m5Tick, "[::1]:4444"))
		isOK(t, doRemoteRaw(router, http.MethodDelete, "/watchlist/"+m5Tick, "[::1]:4444"))
	})

	t.Run("lecturas-abiertas-desde-remoto", func(t *testing.T) {
		// Read-only sin restricción: la negociación de contenido y la búsqueda
		// siguen respondiendo con normalidad a un cliente de red.
		rec := doRemoteRaw(router, http.MethodGet, "/watchlist", remote)
		expectStatus(t, rec, http.StatusOK)
		if v := rec.Header().Get("Vary"); !strings.Contains(v, "Accept") {
			t.Fatalf("GET /watchlist remoto: se esperaba Vary: Accept, got %q", v)
		}
		decodeWatchlist(t, rec)
		rec = doRemoteRaw(router, http.MethodGet, "/securities/search?q="+m5Tick, remote)
		expectStatus(t, rec, http.StatusOK)
		if got := decodeSecurities(t, rec); len(got) == 0 || got[0].Ticker != m5Tick {
			t.Fatalf("search remoto debe responder 200 con resultados, got %d", len(got))
		}
	})
}

// TestM5WatchlistPoolNil: sin pool (API degradada) los endpoints nuevos
// responden 503 con el envelope estándar en vez de reventar. Para los mutadores
// se fija RemoteAddr explícitamente (loopback y remoto): la precedencia es la
// de guardRefresh — pool no disponible (503) antes que política de red (403).
func TestM5WatchlistPoolNil(t *testing.T) {
	router := api.NewRouter(nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/watchlist"},
		{http.MethodPut, "/watchlist/AAPL"},
		{http.MethodDelete, "/watchlist/AAPL"},
		{http.MethodGet, "/securities/search?q=aa"},
	} {
		rec, body := do(t, router, tc.method, tc.path)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: esperado 503, got %d", tc.method, tc.path, rec.Code)
		}
		if errCode(t, body) != "service_unavailable" {
			t.Fatalf("%s %s: error code inesperado: %v", tc.method, tc.path, body)
		}
	}
	// Pool nil + mutador: 503 también desde loopback (el guard de BD precede al
	// de red, igual que en guardRefresh).
	for _, remote := range []string{m5LoopbackAddr, "198.51.100.7:5555"} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			rec := doRemoteRaw(router, method, "/watchlist/AAPL", remote)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s /watchlist/AAPL (remote %s): esperado 503, got %d", method, remote, rec.Code)
			}
		}
	}
}
