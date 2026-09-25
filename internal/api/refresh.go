// Refresh endpoints (plan M4c): el dashboard recalcula métricas + scores
// (POST /refresh, rápido, sin red) o re-ingesta el pipeline completo
// (POST /force-refresh: edgar -> prices -> sector -> metrics -> scores).
//
// Política de seguridad: ambos son mutadores de datos y por defecto solo
// aceptan clientes loopback (deploy local :8082). Si el API se expone
// públicamente, el refresh queda deshabilitado (403) salvo que el proxy
// externo confine los clientes. Anti-concurrencia: un único refresh por
// proceso (mutex); un segundo POST mientras otro corre responde 409.
package api

import (
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/pipeline"
)

// refreshMu serializa los refrescos del proceso (anti-concurrencia M4c).
var refreshMu sync.Mutex

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

// guardRefresh aplica los controles comunes de ambos endpoints: pool
// disponible (503), cliente loopback (403) y anti-concurrencia (409). Devuelve
// false cuando la respuesta de error ya se escribió (o el handler no debe
// continuar porque otro refresh está corriendo).
func guardRefresh(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) bool {
	if pool == nil {
		writeError(w, http.StatusServiceUnavailable, CodeUnavailable, "base de datos no disponible")
		return false
	}
	if !isLoopbackClient(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, CodeForbidden, "refresh solo disponible desde localhost")
		return false
	}
	if !refreshMu.TryLock() {
		writeError(w, http.StatusConflict, CodeConflict, "refresh ya en ejecución")
		return false
	}
	return true
}

// handleRefresh: POST /refresh — recalcula métricas y scores de las
// securities activas con precio (GROWTH_RATE_DEFAULT / MARGIN_OF_SAFETY /
// COMPARABLES_* del entorno, mismo contrato que los endpoints de valoración).
// Respuesta: {"ok":true,"tickers":N,"duration_ms":D}.
func handleRefresh(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	if !guardRefresh(w, r, pool) {
		return
	}
	defer refreshMu.Unlock()

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
// re-ingesta fresca -> prices -> sector -> metrics -> scores) sobre el
// universo de securities activas con precio. Respuesta: campos de /refresh +
// {"steps":{etapa:conteo}}.
func handleForceRefresh(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	if !guardRefresh(w, r, pool) {
		return
	}
	defer refreshMu.Unlock()

	start := time.Now()
	params := LoadParameters()
	ua := os.Getenv("SEC_EDGAR_USER_AGENT")
	if ua == "" {
		ua = refreshUADefault
	}
	res, err := pipeline.ForceRefresh(r.Context(), pool, nil, params.GrowthRate, ua, false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "force-refresh falló: %v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"tickers":     res.Tickers,
		"duration_ms": time.Since(start).Milliseconds(),
		"steps":       res.Steps,
	})
}
