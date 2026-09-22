// Package api exposes the Abys-Invest HTTP API (M3): securities, prices,
// metrics, valuation, score, comparables and SMA backtest endpoints with the
// plan-mandated JSON error envelope.
package api

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewRouter builds the API router (stdlib patterns, plan T8). The pool may be
// nil (API arranca degraded); todo endpoint que lo requiera responde 503.
func NewRouter(pool *pgxpool.Pool) *http.ServeMux {
	params := LoadParameters()
	mux := http.NewServeMux()

	// Estado y catálogo.
	mux.HandleFunc("GET /health", HealthHandler(pool))
	mux.HandleFunc("GET /securities", func(w http.ResponseWriter, r *http.Request) {
		handleListSecurities(w, r, pool)
	})
	mux.HandleFunc("GET /securities/{ticker}", func(w http.ResponseWriter, r *http.Request) {
		ticker, err := normalizeTicker(r.PathValue("ticker"))
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
			return
		}
		handleGetSecurity(w, r, pool, ticker)
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

	return mux
}
