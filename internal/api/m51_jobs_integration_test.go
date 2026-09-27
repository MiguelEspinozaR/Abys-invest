//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
)

// Cobertura de integración de M5.1 (plan T4) sobre el router real con BD de
// prueba y EJECUTOR FAKE (sin red): GET /pipeline/status, POST /force-refresh
// (202 + 409 + 403 + 503), PUT /watchlist con ingesta en segundo plano (200
// inmediato + encolado + drenaje) y GET /watchlist con score/signal.
//
// Ejecución: DATABASE_URL=... go test ./internal/api -p 1 -tags=integration -count=1
//
// Dos detalles de estructura (los mismos que explica el plan):
//   - el archivo es de `package api` (no api_test) para poder sustituir el
//     runner global `jobs`; no puede haber TestMain aquí porque el de
//     m3_integration_test.go es de package api_test y Go no admite dos;
//   - el init() de abajo reemplaza `jobs` para TODO el binario de test, de modo
//     que los tests M5 existentes (PUT /watchlist/M5WT) no disparen ingesta
//     real contra Yahoo al heredar el comportamiento nuevo (riesgo R7).

// --- ejecutor fake compartido -------------------------------------------------

type m51Run struct {
	kind    string
	tickers []string
}

type m51Fake struct {
	mu       sync.Mutex
	runs     []m51Run
	gate     chan struct{} // cerrado ⇒ los jobs no esperan
	arrived  chan struct{} // señal (bufferizada) de arranque
	released sync.Once
}

func (f *m51Fake) exec(_ context.Context, _ *pgxpool.Pool, kind string, tickers []string, onStep stepFunc) error {
	f.mu.Lock()
	f.runs = append(f.runs, m51Run{kind: kind, tickers: append([]string{}, tickers...)})
	f.mu.Unlock()
	select {
	case f.arrived <- struct{}{}:
	default:
	}
	<-f.gate
	if onStep != nil {
		onStep("prices", 1)
		onStep("scores", 1)
	}
	return nil
}

func (f *m51Fake) recorded() []m51Run {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]m51Run{}, f.runs...)
}

func (f *m51Fake) openGate() { f.released.Do(func() { close(f.gate) }) }

// m51OpenGate devuelve un gate ya cerrado ⇒ los jobs NO esperan.
func m51OpenGate() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// sharedM51Fake es el ejecutor del runner global: nunca deja salir a la red y
// NO bloquea (gate ya abierto). Importa porque los tests M5 existentes
// (PUT /watchlist/M5WT) heredan el disparo de ingesta de M5.1: si el fake
// compartido empezara con el gate cerrado, esos jobs quedarían "running" y
// bloquearían los tests siguientes. Los falsos de los casos de M5.1, que sí
// necesitan bloquear, se crean con el gate cerrado.
var sharedM51Fake = &m51Fake{gate: m51OpenGate(), arrived: make(chan struct{}, 16)}

// init sustituye el ejecutor real del proceso por el fake del binario de test.
func init() {
	jobs = newJobRunner(sharedM51Fake.exec)
}

// newM51Runner instala un runner nuevo con su propio fake (gate cerrado por
// defecto ⇒ el job queda en curso) y devuelve fake + router. La limpieza abre el
// gate (nunca dejar jobs colgados) y restaura el runner compartido.
func newM51Runner(t *testing.T, pool *pgxpool.Pool) (*m51Fake, http.Handler) {
	t.Helper()
	f := &m51Fake{gate: make(chan struct{}), arrived: make(chan struct{}, 16)}
	prev := jobs
	jobs = newJobRunner(f.exec)
	t.Cleanup(func() {
		f.openGate()
		jobs = prev
	})
	return f, NewRouter(pool)
}

// waitM51Job espera a que el fake haya arrancado n jobs (con timeout: esperar en
// un canal sin timeout cuelga la suite entera).
func waitM51Job(t *testing.T, f *m51Fake, n int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if len(f.recorded()) >= n {
			return
		}
		select {
		case <-f.arrived:
		case <-deadline:
			t.Fatalf("timeout esperando %d job(s); registrados %+v", n, f.recorded())
		}
	}
}

const (
	m51LoopbackAddr = "127.0.0.1:8082"
	m51RemoteAddr   = "198.51.100.7:5555"
	m51TickA        = "M51WA"
	m51TickB        = "M51WB"
	m51IndexHTML    = `<div id="root">FAKE-INDEX-M51</div>`
)

// waitM51Status sondea GET /pipeline/status hasta que cond sea cierto: evita
// dormir a ciegas y hace el test determinista.
func waitM51Status(t *testing.T, h http.Handler, what string, cond func(PipelineStatus) bool) PipelineStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var st PipelineStatus
	for {
		st = m51Get(t, h, m51LoopbackAddr)
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando %s; último estado: %+v", what, st)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// m51Pool es el pool propio de este archivo (el TestMain de package api_test no
// es visible desde package api). Sin DATABASE_URL, nil ⇒ los tests con BD se
// saltan.
var (
	m51PoolOnce sync.Once
	m51DBPool   *pgxpool.Pool
)

func requireM51Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	m51PoolOnce.Do(func() {
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		pool, err := storage.Connect(ctx, dsn)
		if err != nil {
			return
		}
		if err := storage.EnsureTestDatabase(ctx, pool); err != nil {
			pool.Close()
			return
		}
		if err := storage.RunMigrations(ctx, pool, "../../migrations"); err != nil {
			pool.Close()
			return
		}
		m51DBPool = pool
	})
	if m51DBPool == nil {
		t.Skip("sin DATABASE_URL (o BD no alcanzable): test de integración M5.1 omitido")
	}
	return m51DBPool
}

// --- helpers de request -------------------------------------------------------

func m51Do(h http.Handler, method, path, remote string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remote
	h.ServeHTTP(rec, req)
	return rec
}

func m51DoAccept(h http.Handler, path, accept string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	h.ServeHTTP(rec, req)
	return rec
}

func m51DecodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body no parseable: %v (%s)", err, rec.Body.String())
	}
	return body
}

// m51DecodeBody lista: GET /watchlist devuelve un ARRAY, no un objeto.
func m51DecodeList(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var items []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("lista no parseable: %v (%s)", err, rec.Body.String())
	}
	return items
}

func m51ErrCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	body := m51DecodeBody(t, rec)
	detail, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("envelope de error ausente: %s", rec.Body.String())
	}
	code, _ := detail["code"].(string)
	return code
}

// m51Get consulta el estado por el router (read-only, sin pool ni loopback).
func m51Get(t *testing.T, h http.Handler, remote string) PipelineStatus {
	t.Helper()
	rec := m51Do(h, http.MethodGet, "/pipeline/status", remote)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /pipeline/status esperado 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var st PipelineStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("estado no parseable: %v (%s)", err, rec.Body.String())
	}
	return st
}

func m51StatusKeys(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := m51DecodeBody(t, rec)
	for _, key := range []string{"status", "kind", "tickers", "steps", "started_at", "finished_at", "error", "pending"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("falta la clave %q en GET /pipeline/status: %s", key, rec.Body.String())
		}
	}
	return body
}

func m51SeedFixtures(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	sector := "Technology"
	for _, s := range []*storage.Security{
		{Ticker: m51TickA, CIK: "915100001", Name: "M51 Watchlist A", Type: "stock", Currency: "USD", Status: "active", Sector: &sector},
		{Ticker: m51TickB, CIK: "915100002", Name: "M51 Watchlist B", Type: "stock", Currency: "USD", Status: "active"},
	} {
		if _, err := storage.UpsertSecurity(ctx, pool, s); err != nil {
			t.Fatalf("upsert security %s: %v", s.Ticker, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM watchlist WHERE security_id IN (SELECT id FROM securities WHERE ticker LIKE 'M51W%')`); err != nil {
		t.Fatalf("limpieza de watchlist de fixtures: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM watchlist WHERE security_id IN (SELECT id FROM securities WHERE ticker LIKE 'M51W%')`); err != nil {
			t.Errorf("limpieza final de watchlist de fixtures: %v", err)
		}
	})
}

// --- casos --------------------------------------------------------------------

// T4.1 — GET /pipeline/status responde 200 con las 8 claves siempre presentes.
func TestM51PipelineStatusShape(t *testing.T) {
	_, h := newM51Runner(t, nil)

	rec := m51Do(h, http.MethodGet, "/pipeline/status", m51RemoteAddr)
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := m51StatusKeys(t, rec)
	if body["status"] != JobStatusIdle {
		t.Fatalf("un runner recién creado debe estar idle, got %v", body["status"])
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type esperado application/json, got %q", ct)
	}
}

// T4.2 — D5: el estado es del proceso, no de la BD: con pool nil sigue siendo
// 200 (un 503 dejaría al SPA sin poder saber por qué falló un job). Y a la vez
// GET /watchlist sí es un endpoint de BD → 503.
func TestM51StatusPoolNilIs200(t *testing.T) {
	_, h := newM51Runner(t, nil)
	if rec := m51Do(h, http.MethodGet, "/pipeline/status", m51LoopbackAddr); rec.Code != http.StatusOK {
		t.Fatalf("GET /pipeline/status con pool nil debe ser 200 (D5), got %d (%s)", rec.Code, rec.Body.String())
	}
	rec := m51Do(h, http.MethodGet, "/watchlist", m51LoopbackAddr)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /watchlist con pool nil debe ser 503, got %d", rec.Code)
	}
	if code := m51ErrCode(t, rec); code != CodeUnavailable {
		t.Fatalf("error code inesperado: %q", code)
	}
}

// T4.3 — POST /force-refresh: 202 con el estado inicial, 409 mientras el job
// corre y done al terminar, con kind=force en el ejecutor.
func TestM51ForceRefreshAsync(t *testing.T) {
	pool := requireM51Pool(t)
	f, h := newM51Runner(t, pool)

	rec := m51Do(h, http.MethodPost, "/force-refresh", m51LoopbackAddr)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /force-refresh esperado 202, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := m51StatusKeys(t, rec)
	if body["status"] != JobStatusRunning {
		t.Fatalf("el 202 debe publicar status running, got %v", body["status"])
	}
	if body["kind"] != JobKindForce {
		t.Fatalf("el 202 debe publicar kind=force, got %v", body["kind"])
	}
	if body["started_at"] == nil {
		t.Fatal("el 202 debe publicar started_at")
	}
	if body["finished_at"] != nil || body["error"] != nil {
		t.Fatalf("el estado inicial no debe traer finished_at/error: %+v", body)
	}
	waitM51Job(t, f, 1)

	// Anti-concurrencia (criterio M4c): un segundo POST mientras corre → 409.
	rec = m51Do(h, http.MethodPost, "/force-refresh", m51LoopbackAddr)
	if rec.Code != http.StatusConflict {
		t.Fatalf("segundo POST /force-refresh esperado 409, got %d (%s)", rec.Code, rec.Body.String())
	}
	if code := m51ErrCode(t, rec); code != CodeConflict {
		t.Fatalf("error code esperado conflict, got %q", code)
	}

	// /pipeline/status es read-only: abierto a cualquier cliente y sin pool.
	st := m51Get(t, h, m51RemoteAddr)
	if st.Status != JobStatusRunning || st.Kind == nil || *st.Kind != JobKindForce {
		t.Fatalf("el status debe reflejar el job en curso: %+v", st)
	}

	f.openGate()
	waitM51Status(t, h, "job force done", func(s PipelineStatus) bool { return s.Status == JobStatusDone })
	runs := f.recorded()
	if len(runs) != 1 || runs[0].kind != JobKindForce {
		t.Fatalf("el ejecutor debe haber recibido exactamente un job force: %+v", runs)
	}
	final := m51Get(t, h, m51LoopbackAddr)
	if final.FinishedAt == nil {
		t.Fatal("el job terminado debe publicar finished_at")
	}
	if len(final.Steps) == 0 {
		t.Fatalf("el job debe publicar steps tras terminar: %+v", final.Steps)
	}
}

// T4.4 — política: 403 desde cliente remoto y 503 con pool nil (loopback y
// remoto), siempre después de la comprobación de BD.
func TestM51ForceRefreshPolicy(t *testing.T) {
	_, h := newM51Runner(t, nil) // pool nil: 503 antes que 403

	rec := m51Do(h, http.MethodPost, "/force-refresh", m51RemoteAddr)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("pool nil + remoto debe ser 503, got %d (%s)", rec.Code, rec.Body.String())
	}
	if code := m51ErrCode(t, rec); code != CodeUnavailable {
		t.Fatalf("error code inesperado: %q", code)
	}

	pool := requireM51Pool(t)
	_, h = newM51Runner(t, pool)
	rec = m51Do(h, http.MethodPost, "/force-refresh", m51RemoteAddr)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /force-refresh remoto esperado 403, got %d (%s)", rec.Code, rec.Body.String())
	}
	if code := m51ErrCode(t, rec); code != CodeForbidden {
		t.Fatalf("error code esperado forbidden, got %q", code)
	}
}

// T4.5 — PUT /watchlist: 200 {"ok":true} INMEDIATO (con el job bloqueado),
// ingesta en segundo plano visible en /pipeline/status, encolado del segundo
// ticker y drenaje como un único job watchlist.
func TestM51WatchlistIngestBackground(t *testing.T) {
	pool := requireM51Pool(t)
	m51SeedFixtures(t, pool)
	f, h := newM51Runner(t, pool)

	// El fake sigue con el gate cerrado: el PUT no puede esperar a la ingesta.
	start := time.Now()
	rec := m51Do(h, http.MethodPut, "/watchlist/"+m51TickA, m51LoopbackAddr)
	elapsed := time.Since(start)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /watchlist esperado 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	body := m51DecodeBody(t, rec)
	if body["ok"] != true {
		t.Fatalf(`esperado {"ok":true}, got %v`, body)
	}
	if len(body) != 1 {
		t.Fatalf("el PUT no debe añadir campos nuevos al contrato: %s", rec.Body.String())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("el PUT debe responder de inmediato, tardó %v (la ingesta no puede bloquear)", elapsed)
	}
	waitM51Job(t, f, 1)

	st := m51Get(t, h, m51LoopbackAddr)
	if st.Status != JobStatusRunning || st.Kind == nil || *st.Kind != JobKindWatchlist {
		t.Fatalf("esperaba job watchlist en curso, got %+v", st)
	}
	if len(st.Tickers) != 1 || st.Tickers[0] != m51TickA {
		t.Fatalf("tickers deben contener el ticker añadido, got %+v", st.Tickers)
	}

	// Segundo ticker con job en curso: 200 y cola FIFO (nunca falla).
	rec = m51Do(h, http.MethodPut, "/watchlist/"+m51TickB, m51LoopbackAddr)
	if rec.Code != http.StatusOK {
		t.Fatalf("el PUT con job en curso debe seguir siendo 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	st = m51Get(t, h, m51LoopbackAddr)
	if len(st.Pending) != 1 || st.Pending[0] != m51TickB {
		t.Fatalf("el segundo ticker debe quedar encolado, got %+v", st.Pending)
	}

	f.openGate()
	// Se espera a que arranque el SEGUNDO job (el drenado) antes de mirar el
	// estado: el "done" intermedio entre job1 y job2 también cumpliría un
	// predicado de "done", y el test debe comprobar el final del drenaje.
	waitM51Job(t, f, 2)
	waitM51Status(t, h, "job watchlist drenado done", func(s PipelineStatus) bool {
		return s.Kind != nil && *s.Kind == JobKindWatchlist && s.Status == JobStatusDone && len(s.Pending) == 0
	})
	runs := f.recorded()
	if len(runs) != 2 {
		t.Fatalf("se esperaban 2 jobs (watchlist + watchlist drenado), hubo %d: %+v", len(runs), runs)
	}
	if runs[0].kind != JobKindWatchlist || len(runs[0].tickers) != 1 || runs[0].tickers[0] != m51TickA {
		t.Fatalf("el primer job debe ser la ingesta de %s: %+v", m51TickA, runs[0])
	}
	if runs[1].kind != JobKindWatchlist || len(runs[1].tickers) != 1 || runs[1].tickers[0] != m51TickB {
		t.Fatalf("el segundo job debe llevar exactamente el ticker encolado: %+v", runs[1])
	}
	final := m51Get(t, h, m51LoopbackAddr)
	if len(final.Pending) != 0 {
		t.Fatalf("la cola debe quedar vacía tras el drenaje: %+v", final.Pending)
	}
}

// T4.6 — GET /watchlist ampliado: score/signal no nulos para el security con
// score, null con las claves presentes para el resto, y negociación de
// contenido + Vary: Accept intactos.
func TestM51WatchlistWithScoreAndNegotiation(t *testing.T) {
	pool := requireM51Pool(t)
	m51SeedFixtures(t, pool)
	f, h := newM51Runner(t, pool)

	// Alta de los dos fixtures (ingesta en segundo plano con el fake, sin red).
	for _, tk := range []string{m51TickA, m51TickB} {
		if rec := m51Do(h, http.MethodPut, "/watchlist/"+tk, m51LoopbackAddr); rec.Code != http.StatusOK {
			t.Fatalf("PUT /watchlist/%s: %d (%s)", tk, rec.Code, rec.Body.String())
		}
	}
	// Estos PUTs usan el runner LOCAL (f), no el compartido: se abre su gate para
	// que los jobs drenen y el caso no dependa del orden de ejecución.
	f.openGate()
	waitM51Job(t, f, 2)
	waitM51Status(t, h, "ingestas de fixtures terminadas", func(s PipelineStatus) bool {
		return s.Status == JobStatusDone
	})

	// Score solo para el primer fixture (contrato de la migración 009).
	secA, err := storage.GetSecurityByTicker(context.Background(), pool, m51TickA)
	if err != nil {
		t.Fatalf("GetSecurityByTicker(%s): %v", m51TickA, err)
	}
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := storage.UpsertScore(ctx, tx, &storage.Score{
		SecurityID: secA.ID, AsOf: time.Date(2025, 9, 30, 0, 0, 0, 0, time.UTC),
		Score: 73, Signal: "comprar", Justification: "fixture M5.1", ModelVersion: "test",
	}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("UpsertScore: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM scores WHERE security_id = $1`, secA.ID); err != nil {
			t.Errorf("limpieza del score fixture: %v", err)
		}
	})

	rec := m51DoAccept(h, "/watchlist", "application/json")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /watchlist esperado 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if v := rec.Header().Get("Vary"); !strings.Contains(v, "Accept") {
		t.Fatalf("GET /watchlist debe seguir declarando Vary: Accept, got %q", v)
	}
	var items []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("lista no parseable: %v (%s)", err, rec.Body.String())
	}
	// La BD de test es compartida (-p 1) con los fixtures de otros paquetes:
	// se filtra a los de M5.1 en vez de asumir que la lista tiene solo 2.
	byTicker := map[string]map[string]any{}
	seen := 0
	for _, it := range items {
		ticker, _ := it["ticker"].(string)
		if ticker != m51TickA && ticker != m51TickB {
			continue
		}
		seen++
		for _, key := range []string{"score", "signal"} {
			if _, ok := it[key]; !ok {
				t.Fatalf("falta la clave %q en el item: %s", key, rec.Body.String())
			}
		}
		byTicker[ticker] = it
	}
	if seen != 2 {
		t.Fatalf("se esperaban los 2 fixtures M5.1 en la watchlist, hay %d: %s", seen, rec.Body.String())
	}
	scored := byTicker[m51TickA]
	if scored["score"] != float64(73) || scored["signal"] != "comprar" {
		t.Fatalf("el item con score debe traer score/signal no nulos: %+v", scored)
	}
	if byTicker[m51TickB]["score"] != nil || byTicker[m51TickB]["signal"] != nil {
		t.Fatalf("el item sin score debe traer score/signal null: %+v", byTicker[m51TickB])
	}
	// Contrato de id intacto: sigue siendo securities.id.
	if id, ok := scored["id"].(float64); !ok || int64(id) != secA.ID {
		t.Fatalf("id debe seguir siendo securities.id (%d), got %v", secA.ID, scored["id"])
	}

	// Negociación de contenido intacta: el navegador recibe el shell del SPA.
	dist := m51StaticDir(t)
	hs := NewRouter(pool, WithStaticDir(dist))
	RegisterStatic(hs, dist)
	html := m51DoAccept(hs, "/watchlist", "text/html,application/xhtml+xml")
	if html.Code != http.StatusOK || !strings.HasPrefix(html.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("GET /watchlist con Accept: text/html debe servir el shell, got %d ct=%q", html.Code, html.Header().Get("Content-Type"))
	}
	if html.Body.String() != m51IndexHTML {
		t.Fatalf("el shell del SPA cambió: %q", html.Body.String())
	}
}

// T4.7 — DELETE /watchlist sigue siendo 200 (no dispara ingesta) y /pipeline es
// espacio de nombres de la API: 405 por método y 404 JSON para /pipeline/extra
// (nunca index.html), incluido con el SPA registrado.
func TestM51WatchlistRemoveAndPipelineNamespace(t *testing.T) {
	pool := requireM51Pool(t)
	m51SeedFixtures(t, pool)
	f, h := newM51Runner(t, pool)

	if rec := m51Do(h, http.MethodPut, "/watchlist/"+m51TickA, m51LoopbackAddr); rec.Code != http.StatusOK {
		t.Fatalf("PUT /watchlist/%s: %d", m51TickA, rec.Code)
	}
	waitM51Job(t, f, 1)
	before := len(f.recorded())
	if rec := m51Do(h, http.MethodDelete, "/watchlist/"+m51TickA, m51LoopbackAddr); rec.Code != http.StatusOK {
		t.Fatalf("DELETE /watchlist esperado 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	f.openGate()
	time.Sleep(100 * time.Millisecond) // margen para un eventual job espurio
	if after := len(f.recorded()); after != before {
		t.Fatalf("DELETE /watchlist no debe disparar ingesta: %d → %d jobs", before, after)
	}
	items, err := json.Marshal(m51DecodeList(t, m51DoAccept(h, "/watchlist", "application/json")))
	if err != nil {
		t.Fatalf("lista no serializable: %v", err)
	}
	if strings.Contains(string(items), m51TickA) {
		t.Fatalf("el DELETE debe quitar la entrada de la watchlist: %s", items)
	}

	// Método no permitido sobre el patrón registrado (comportamiento stdlib).
	rec := m51Do(h, http.MethodPost, "/pipeline/status", m51LoopbackAddr)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /pipeline/status esperado 405, got %d", rec.Code)
	}

	// Con el SPA registrado, /pipeline/extra debe ser 404 JSON y no index.html.
	dist := m51StaticDir(t)
	hs := NewRouter(pool, WithStaticDir(dist))
	RegisterStatic(hs, dist)
	for _, p := range []string{"/pipeline/extra", "/pipeline/"} {
		rec := m51DoAccept(hs, p, "text/html")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s esperado 404, got %d (%s)", p, rec.Code, rec.Body.String())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("GET %s debe responder envelope JSON, got ct=%q", p, ct)
		}
		if rec.Body.String() == m51IndexHTML {
			t.Fatalf("GET %s no debe servir index.html", p)
		}
		if code := m51ErrCode(t, rec); code != CodeNotFound {
			t.Fatalf("GET %s: error code inesperado %q", p, code)
		}
	}
	// GET /pipeline/status sigue siendo JSON puro, también para un navegador
	// (no hay página SPA para esa ruta).
	rec = m51DoAccept(hs, "/pipeline/status", "text/html")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("GET /pipeline/status con Accept: text/html debe ser JSON 200, got %d ct=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// m51StaticDir crea un dist falso (index.html + assets/app.js) para montar el
// handler completo como hace cmd/api/main.go.
func m51StaticDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir dist/assets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(m51IndexHTML), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("console.log('app')"), 0o644); err != nil {
		t.Fatalf("write assets/app.js: %v", err)
	}
	return dir
}
