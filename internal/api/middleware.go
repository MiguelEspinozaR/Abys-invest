package api

import (
	"context"
	"encoding/json"
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
func HealthHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dbOK := pool != nil && pool.Ping(contextBackground(r)) == nil
		w.Header().Set("Content-Type", "application/json")
		if dbOK {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status": "ok", "database": "connected", "version": Version,
			})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "degraded", "database": "disconnected", "version": Version,
		})
	}
}

func contextBackground(r *http.Request) context.Context {
	if r == nil {
		return context.Background()
	}
	return r.Context()
}
