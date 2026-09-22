// Command api serves the Abys-Invest HTTP API (M1 health + M3: securities,
// prices, metrics, valuation, score, comparables y backtest SMA).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/api"
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

	// El router del paquete api expone todas las rutas M3; /health usa el
	// mismo HealthHandler que los tests package-main verifican.
	handler := api.WithMiddleware(api.NewRouter(pool))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
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

// healthHandler se mantiene como wrapper para los tests package-main
// existentes (health_test.go, health_integration_test.go); la ruta /health la
// sirve el router vía api.HealthHandler con el mismo contrato.
func healthHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return api.HealthHandler(pool)
}
