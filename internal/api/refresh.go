// Endpoints de refresh (plan M5.1, sobre el contrato de M4c): el dashboard
// recalcula métricas + scores (POST /refresh, rápido, sin red) o lanza el
// pipeline completo (POST /force-refresh: edgar → prices → sector → metrics →
// scores).
//
// Cambio de M5.1 (el incidente que originó el hito): POST /force-refresh ya NO
// ejecuta el pipeline con r.Context(). El trabajo de ~1 min moría a medias si el
// navegador navegaba o recargaba (Go cancelaba el contexto → Yahoo devolvía
// "context canceled" → 500 → "failed to fetch"). Ahora el handler responde 202
// y el job corre en segundo plano con contexto propio y progreso consultable en
// GET /pipeline/status (runner en jobs.go).
//
// Política de seguridad: los tres endpoints son mutadores de datos y por defecto
// solo aceptan clientes loopback (deploy local :8082). Si el API se expone
// públicamente quedan deshabilitados (403) salvo que el proxy externo confine
// los clientes. Anti-concurrencia: un único job en curso; POST /force-refresh
// responde 409 si ya hay otro, y POST /refresh (síncrono) ocupa el mismo lock.
package api

import (
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/pipeline"
)

// refreshUADefault es el User-Agent SEC fallback del force-refresh (mismo
// contrato de dev que cmd/collector: producción debe fijar SEC_EDGAR_USER_AGENT).
const refreshUADefault = "AbysInvest/1.0 (dev)"

// isLoopbackClient determina si el cliente remoto conecta desde una dirección
// loopback (127.0.0.0/8 o ::1). Cualquier redirección / proxy debe reescribir
// RemoteAddr para conservar la política.
func isLoopbackClient(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// guardPipeline aplica los controles comunes de los tres endpoints: pool
// disponible (503, primero) y cliente loopback (403, después — el mismo orden
// de precedencia que guardWatchlistMutation en handlers.go). NO incluye
// anti-concurrencia: cada endpoint toma el lock que necesita del runner
// (jobs.start / jobs.acquire). Devuelve false cuando la respuesta de error ya
// se escribió.
func guardPipeline(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) bool {
	if !requireDB(w, pool) {
		return false
	}
	if !isLoopbackClient(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, CodeForbidden, "refresh solo disponible desde localhost")
		return false
	}
	return true
}

// handleRefresh: POST /refresh — recalcula métricas y scores de las
// securities activas con precio (GROWTH_RATE_DEFAULT / MARGIN_OF_SAFETY /
// COMPARABLES_* del entorno, mismo contrato que los endpoints de valoración).
// Sigue siendo SÍNCRONO (no es una ingesta) y toma el mismo lock que el job en
// segundo plano: si hay un job en curso responde 409 para no competir por la BD
// (derived_metrics/scores) con él. Respuesta: {"ok":true,"tickers":N,
// "duration_ms":D}.
func handleRefresh(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	if !guardPipeline(w, r, pool) {
		return
	}
	if !jobs.acquire() {
		writeError(w, http.StatusConflict, CodeConflict, "refresh ya en ejecución")
		return
	}
	defer jobs.release()

	start := time.Now()
	params := LoadParameters()
	res, err := pipeline.RefreshMetricsAndScores(r.Context(), pool, params.GrowthRate, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "refresh falló: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"tickers":     res.Tickers,
		"duration_ms": time.Since(start).Milliseconds(),
	})
}

// handleForceRefresh: POST /force-refresh — pipeline completo (edgar con
// re-ingesta fresca → prices → sector → metrics → scores) sobre el universo
// watchlist ∪ securities activas con precio.
//
// M5.1: responde 202 Accepted con el estado INICIAL del job (PipelineStatus) y
// no ejecuta nada con r.Context() aquí dentro: el trabajo corre en la goroutine
// del runner, con contexto propio, y su progreso se consulta en
// GET /pipeline/status. 409 si ya hay un job en curso.
func handleForceRefresh(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	if !guardPipeline(w, r, pool) {
		return
	}
	st, started := jobs.start(pool, JobKindForce, nil)
	if !started {
		writeError(w, http.StatusConflict, CodeConflict, "ya hay un pipeline en ejecución")
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}
