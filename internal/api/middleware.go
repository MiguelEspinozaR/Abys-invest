package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Version is the API revision reported by /health and included in error
// envelopes.
const Version = "0.1.0"

// statusCapturer wraps ResponseWriter to record the response status for logs.
type statusCapturer struct {
	http.ResponseWriter
	status int
}

func (c *statusCapturer) WriteHeader(code int) {
	c.status = code
	c.ResponseWriter.WriteHeader(code)
}

// WithMiddleware composes logging and panic recovery around the router.
// Failures are returned as structured JSON errors (plan T8).
func WithMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusCapturer{ResponseWriter: w, status: http.StatusOK}

		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic en handler", "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, CodeInternal, "error interno del servidor")
				return
			}
			slog.Info("http", "method", r.Method, "path", r.URL.Path,
				"status", sw.status, "dur_ms", time.Since(start).Milliseconds())
		}()

		next.ServeHTTP(sw, r)
	})
}

// HealthHandler serves the readiness probe (degraded → 503 when the database
// is unavailable). The cmd wrapper keeps package-main compatibility.
//
// M5.2: el body se amplía de forma ADITIVA (health.go). El status HTTP depende
// SOLO del Ping (decisión D3); si la BD responde, mide la latencia de ese ping y
// añade postgres_version, db_size y tables — si alguna de esas queries falla se
// responde 200 con los campos disponibles y un slog.Warn (una métrica
// secundaria nunca degrada el probe de readiness). Todo el handler vive bajo
// un presupuesto de 3 s (decisión D4). Sin BD (pool nil o ping fallido) el 503
// conserva EXACTAMENTE las 3 claves del contrato M1 (omitempty).
func HealthHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(contextBackground(r), healthTimeout)
		defer cancel()

		resp := HealthResponse{Status: "degraded", Database: "disconnected", Version: Version}
		if pool != nil {
			start := time.Now()
			if err := pool.Ping(ctx); err == nil {
				resp.Status, resp.Database = "ok", "connected"
				resp.LatencyMS = ptr(msSince(start)) // latencia del ping, no del request
				collectHealthMetrics(ctx, pool, &resp)
			}
		}
		code := http.StatusOK
		if resp.Status != "ok" {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, resp) // errors.go:43 ya fija Content-Type: application/json
	}
}

func contextBackground(r *http.Request) context.Context {
	if r == nil {
		return context.Background()
	}
	return r.Context()
}
