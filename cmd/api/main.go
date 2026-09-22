// Command api serves the Abys-Invest HTTP API (M1: health check).
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
)

const (
	version     = "0.1.0"
	defaultPort = "8080"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	port := os.Getenv("API_PORT")
	if port == "" {
		port = defaultPort
	}

	// La BD es opcional al arranque: sin ella /health responde 503 hasta que
	// el collector la deje disponible.
	dsn := os.Getenv("DATABASE_URL")
	var pool *pgxpool.Pool
	if dsn == "" {
		slog.Warn("DATABASE_URL no definido; /health responderá 503 (degraded)")
	} else if p, err := storage.Connect(ctx, dsn); err != nil {
		slog.Error("conexión a Postgres fallida; /health en degraded", "error", err)
	} else {
		pool = p
		defer pool.Close()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler(pool))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		logger.Info("api escuchando", "addr", srv.Addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("servidor http", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("apagando api")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("apagado http", "error", err)
	}
}

func healthHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dbOK := pool != nil && storage.Ping(r.Context(), pool) == nil

		w.Header().Set("Content-Type", "application/json")
		if dbOK {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status": "ok", "database": "connected", "version": version,
			})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "degraded", "database": "disconnected", "version": version,
		})
	}
}
