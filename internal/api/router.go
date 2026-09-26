// Package api exposes the Abys-Invest HTTP API (M3): securities, prices,
// metrics, valuation, score, comparables and SMA backtest endpoints with the
// plan-mandated JSON error envelope.
package api

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Option configura el router en la construcción. Hoy solo se necesita para
// M5: el directorio de estáticos, que permite a /watchlist servir el shell del
// SPA a los navegadores (negociación de contenido) sin duplicar el mecanismo de
// serving de static.go.
type Option func(*routerOptions)

type routerOptions struct{ staticDir string }

// WithStaticDir informa al router de dónde está el build del frontend. Es
// opcional: sin ella el router es solo API (comportamiento previo) y la
// negociación de /watchlist cae siempre en la respuesta JSON.
func WithStaticDir(dir string) Option {
	return func(o *routerOptions) { o.staticDir = dir }
}

// NewRouter builds the API router (stdlib patterns, plan T8). The pool may be
// nil (API arranca degraded); todo endpoint que lo requiera responde 503.
func NewRouter(pool *pgxpool.Pool, opts ...Option) *http.ServeMux {
	var cfg routerOptions
	for _, opt := range opts {
		opt(&cfg)
	}
	params := LoadParameters()
	mux := http.NewServeMux()

	// Estado y catálogo.
	mux.HandleFunc("GET /health", HealthHandler(pool))
	mux.HandleFunc("GET /securities", func(w http.ResponseWriter, r *http.Request) {
		handleListSecurities(w, r, pool)
	})
	// M5: /securities/search se registra explícitamente. El patrón literal
	// "/securities/search" es más específico que "/securities/{ticker}" y
	// ganaría por especificidad igual, pero registrarlo deja el contrato
	// visible (y evita depender de ese matiz de ServeMux).
	mux.HandleFunc("GET /securities/search", func(w http.ResponseWriter, r *http.Request) {
		handleSearchSecurities(w, r, pool)
	})
	mux.HandleFunc("GET /securities/{ticker}", func(w http.ResponseWriter, r *http.Request) {
		ticker, err := normalizeTicker(r.PathValue("ticker"))
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
			return
		}
		handleGetSecurity(w, r, pool, ticker)
	})

	// Watchlist persistida (plan M5, SPEC §11bis CA-M5-2): single-user, sin
	// auth. GET lista; PUT/DELETE por ticker son idempotentes y responden
	// 200 {"ok":true}.
	//
	// GET /watchlist es DUAL (ruta del SPA para el navegador + endpoint JSON
	// para la API; decisión del orquestador 2026-09-25, hallazgo F2 de la
	// REVIEW de M5): la misma URL devuelve el shell del SPA si el cliente
	// declara text/html y el array de la lista si pide JSON. En el despliegue
	// same-origin (el propio API sirve web/dist) el patrón exacto ganaba al
	// fallback SPA, así que un F5 o una carga directa mostraban el JSON en
	// crudo. Se resuelve por negociación de contenido (plan M5 §B4):
	// Accept: text/html → index.html del SPA; cualquier otro Accept (getJSON
	// manda application/json) → JSON de la lista. Ambas variantes declaran
	// Vary: Accept. El contrato REST no cambia.
	//
	// PUT/DELETE no se negocian: el cliente los llama con fetch, nunca con
	// navegación de documento, así que siempre son JSON. Son mutadores de la
	// BD → loopback-only como /refresh y /force-refresh (guardWatchlistMutation
	// en handlers.go, isLoopbackClient en refresh.go). GET /watchlist y
	// /securities/search son read-only y siguen abiertos a cualquier cliente.
	mux.HandleFunc("GET /watchlist", func(w http.ResponseWriter, r *http.Request) {
		if serveSPAIndexNegotiated(w, r, cfg.staticDir) {
			return
		}
		handleListWatchlist(w, r, pool)
	})
	mux.HandleFunc("PUT /watchlist/{ticker}", func(w http.ResponseWriter, r *http.Request) {
		ticker, err := normalizeTicker(r.PathValue("ticker"))
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
			return
		}
		handleAddWatchlist(w, r, pool, ticker)
	})
	mux.HandleFunc("DELETE /watchlist/{ticker}", func(w http.ResponseWriter, r *http.Request) {
		ticker, err := normalizeTicker(r.PathValue("ticker"))
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
			return
		}
		handleRemoveWatchlist(w, r, pool, ticker)
	})

	// Datos de mercado y métricas.
	mux.HandleFunc("GET /prices/{ticker}", func(w http.ResponseWriter, r *http.Request) {
		ticker, err := normalizeTicker(r.PathValue("ticker"))
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
			return
		}
		handlePrices(w, r, pool, ticker)
	})
	mux.HandleFunc("GET /metrics/{ticker}", func(w http.ResponseWriter, r *http.Request) {
		ticker, err := normalizeTicker(r.PathValue("ticker"))
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
			return
		}
		handleMetrics(w, r, pool, ticker)
	})

	// Valoración y score.
	mux.HandleFunc("GET /valuation/{ticker}", func(w http.ResponseWriter, r *http.Request) {
		ticker, err := normalizeTicker(r.PathValue("ticker"))
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
			return
		}
		handleValuation(w, r, pool, params, ticker)
	})
	mux.HandleFunc("GET /score/{ticker}", func(w http.ResponseWriter, r *http.Request) {
		ticker, err := normalizeTicker(r.PathValue("ticker"))
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
			return
		}
		handleScore(w, r, pool, ticker)
	})
	mux.HandleFunc("GET /scores", func(w http.ResponseWriter, r *http.Request) {
		handleListScores(w, r, pool)
	})

	// Comparativa de activos (plan D8): /compare?tickers=A,B,C&from=&to=
	mux.HandleFunc("GET /compare", func(w http.ResponseWriter, r *http.Request) {
		handleCompareAssets(w, r, pool)
	})
	// Comparables e histórico (plan T9): /compare/comparables?ticker=
	mux.HandleFunc("GET /compare/comparables", func(w http.ResponseWriter, r *http.Request) {
		handleComparables(w, r, pool)
	})
	mux.HandleFunc("GET /compare/history", func(w http.ResponseWriter, r *http.Request) {
		handleHistory(w, r, pool)
	})

	// Backtesting (plan T9; estrategia única: sma).
	mux.HandleFunc("GET /backtest/{strategy}", func(w http.ResponseWriter, r *http.Request) {
		handleBacktest(w, r, pool, r.PathValue("strategy"))
	})

	// Refresh desde el dashboard (plan M4c): recálculo de métricas/scores y
	// pipeline completo. Loopback-only + anti-concurrencia (ver refresh.go).
	mux.HandleFunc("POST /refresh", func(w http.ResponseWriter, r *http.Request) {
		handleRefresh(w, r, pool)
	})
	mux.HandleFunc("POST /force-refresh", func(w http.ResponseWriter, r *http.Request) {
		handleForceRefresh(w, r, pool)
	})

	return mux
}
