// Command api serves the Abys-Invest HTTP API (M1 health + M3: securities,
// prices, metrics, valuation, score, comparables y backtest SMA) y los
// estáticos del frontend (plan M4 D2: /static/*, / y SPA fallback).
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
	version          = "0.1.0"
	defaultPort      = "8080"
	defaultStaticDir = "./web/dist"
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
	//
	// Estáticos del frontend (plan M4 D2): STATIC_DIR default ./web/dist.
	// Las rutas API se registran primero; el fallback SPA solo actúa para
	// paths no matcheados (riesgo M4-R2 mitigado). Si el directorio no
	// existe aún (build del frontend pendiente), RegisterStatic no registra
	// nada y el router queda solo con las rutas API.
	//
	// WithStaticDir da al router el mismo directorio para la negociación de
	// contenido de GET /watchlist (plan M5 §B4: el navegador con
	// Accept: text/html recibe index.html, el cliente JSON la lista).
	staticDir := os.Getenv("STATIC_DIR")
	if staticDir == "" {
		staticDir = defaultStaticDir
	}

	mux := api.NewRouter(pool, api.WithStaticDir(staticDir))
	api.RegisterStatic(mux, staticDir)
	handler := api.WithMiddleware(mux)

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
