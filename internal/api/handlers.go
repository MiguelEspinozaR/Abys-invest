package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/backtest"
	"github.com/miky/abys-invest/internal/compare"
	"github.com/miky/abys-invest/internal/storage"
)

// isNotFound maps the "no row" sentinel of the storage layer to the JSON
// 404 envelope (403 y 404 = ticker inexistente o sin datos).
func isNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// handleListSecurities: GET /securities?limit=&offset=
func handleListSecurities(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	limit, err := parseIntQuery(r.URL.Query().Get("limit"), 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	offset, err := parseIntQuery(r.URL.Query().Get("offset"), 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	secs, err := storage.ListSecurities(r.Context(), pool, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "error al listar securities")
		return
	}
	writeJSON(w, http.StatusOK, secs)
}

// handleGetSecurity: GET /securities/{ticker}
func handleGetSecurity(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, ticker string) {
	sec, err := storage.GetSecurityByTicker(r.Context(), pool, ticker)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, CodeNotFound, "security %q no encontrada", ticker)
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, "error al obtener security")
		return
	}
	writeJSON(w, http.StatusOK, sec)
}

// handlePrices: GET /prices/{ticker}?from=YYYY-MM-DD&to=YYYY-MM-DD
// (default: últimos 5 años).
func handlePrices(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, ticker string) {
	sec, err := storage.GetSecurityByTicker(r.Context(), pool, ticker)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, CodeNotFound, "security %q no encontrada", ticker)
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, "error al obtener security")
		return
	}

	from, to, err := parseDateRange(r, time.Now().UTC().AddDate(-5, 0, 0), time.Time{})
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	prices, err := storage.GetDailyPricesBySecurity(r.Context(), pool, sec.ID, from, to)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "error al leer precios de %s", ticker)
		return
	}
	if len(prices) == 0 {
		writeError(w, http.StatusNotFound, CodeNotFound, "sin precios para %q en el rango solicitado", ticker)
		return
	}
	writeJSON(w, http.StatusOK, prices)
}

// handleMetrics: GET /metrics/{ticker} → derived_metrics más reciente.
func handleMetrics(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, ticker string) {
	sec, err := storage.GetSecurityByTicker(r.Context(), pool, ticker)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, CodeNotFound, "security %q no encontrada", ticker)
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, "error al obtener security")
		return
	}
	mts, err := storage.GetLatestMetrics(r.Context(), pool, sec.ID)
	if err != nil || len(mts) == 0 {
		writeError(w, http.StatusNotFound, CodeNotFound, "sin métricas derivadas para %q (ejecuta analytics primero)", ticker)
		return
	}
	writeJSON(w, http.StatusOK, mts)
}

// handleScore: GET /score/{ticker}?as_of=YYYY-MM-DD (default: último).
func handleScore(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, ticker string) {
	var asOf time.Time
	if raw := r.URL.Query().Get("as_of"); raw != "" {
		parsed, err := time.Parse("2006-01-02", raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidation, "as_of inválido (formato YYYY-MM-DD)")
			return
		}
		asOf = parsed
	}

	var score *storage.Score
	var err error
	if !asOf.IsZero() {
		score, err = storage.GetScoreByTicker(r.Context(), pool, ticker, asOf)
	} else {
		score, err = storage.GetLatestScore(r.Context(), pool, ticker)
	}
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, CodeNotFound, "sin score para %q (ejecuta analytics -job scores primero)", ticker)
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, "error al leer score")
		return
	}
	// M4 (CA M4-1): enriquecer con las dimensiones recomputadas desde el
	// inputs_snapshot persistido. Campos previos intactos (scoreResponse
	// embebe storage.Score); las dimensiones son exactas porque el motor es
	// determinista y el score persistido se generó con ese snapshot.
	writeJSON(w, http.StatusOK, scoreResponse{
		Score:      *score,
		Dimensions: dimensionsFromSnapshot(score.InputsSnapshot),
	})
}

// handleListScores: GET /scores?ticker=&from=&to=&limit=
// (ListScores devuelve ordenado por as_of DESC).
func handleListScores(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	f := storage.ScoresFilter{Ticker: r.URL.Query().Get("ticker")}
	limit, err := parseIntQuery(r.URL.Query().Get("limit"), 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	_ = limit // ListScores no pagina aún: capa de dominio fija en el plan

	for _, key := range []string{"from", "to"} {
		if raw := r.URL.Query().Get(key); raw != "" {
			parsed, err := time.Parse("2006-01-02", raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, CodeValidation, "%s inválido (formato YYYY-MM-DD)", key)
				return
			}
			if key == "from" {
				f.From = parsed
			} else {
				f.To = parsed
			}
		}
	}
	scores, err := storage.ListScores(r.Context(), pool, f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, CodeInternal, "error al listar scores")
		return
	}
	// M4 (CA M4-1): mismo enriquecimiento que /score/{ticker} — cada ítem
	// lleva sus dimensiones recomputadas desde su inputs_snapshot (coste
	// trivial: n filas × recompute puro sobre el histórico de un ticker).
	// scoreResponse embebe storage.Score → contrato previo intacto.
	items := make([]scoreResponse, 0, len(scores))
	for i := range scores {
		items = append(items, scoreResponse{
			Score:      scores[i],
			Dimensions: dimensionsFromSnapshot(scores[i].InputsSnapshot),
		})
	}
	writeJSON(w, http.StatusOK, items)
}

// handleValuation: GET /valuation/{ticker}
func handleValuation(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, params Parameters, ticker string) {
	detail, err := LoadValuation(r.Context(), pool, params, ticker)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, CodeNotFound, "security %q no encontrada o sin datos de valoración", ticker)
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, "error al calcular valoración")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleComparables: GET /compare/comparables?ticker=&limit=
func handleComparables(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	ticker, err := normalizeTicker(r.URL.Query().Get("ticker"))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	limit, err := parseIntQuery(r.URL.Query().Get("limit"), compare.DefaultPeersLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	res, err := compare.ComputeComparables(r.Context(), pool, ticker, limit)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, CodeNotFound, "security %q no encontrada", ticker)
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, "error al calcular comparables")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleHistory: GET /compare/history?ticker=&years=
func handleHistory(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	ticker, err := normalizeTicker(r.URL.Query().Get("ticker"))
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	years, err := parseIntQuery(r.URL.Query().Get("years"), 5)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	res, err := compare.ComputeHistory(r.Context(), pool, ticker, years)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, CodeNotFound, "security %q no encontrada", ticker)
			return
		}
		writeError(w, http.StatusInternalServerError, CodeInternal, "error al leer histórico")
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleBacktest: GET /backtest/{strategy}?ticker=&fast=&slow=&initial_capital=
// handleCompareAssets: GET /compare?tickers=AAPL,MSFT&from=&to=
// Compara activos por rendimiento normalizado (base 100) y métricas de riesgo
// (plan D8): volatilidad anualizada, max drawdown y Sharpe (rf vía query, 0
// por defecto; param del plan /compare?tickers=&from=&to=).
func handleCompareAssets(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) {
	tickers, err := tickerListParam(r.URL.Query(), "tickers")
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	from, to, err := parseDateRange(r, time.Time{}, time.Time{})
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	rf, err := parseFloatQuery(r.URL.Query().Get("rf"), 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}

	all := make(map[string][]storage.DailyPrice, len(tickers))
	for _, t := range tickers {
		sec, err := storage.GetSecurityByTicker(r.Context(), pool, t)
		if err != nil {
			if isNotFound(err) {
				writeError(w, http.StatusNotFound, CodeNotFound, "security %q no encontrada", t)
				return
			}
			writeError(w, http.StatusInternalServerError, CodeInternal, "error al obtener security")
			return
		}
		prices, err := storage.GetDailyPricesBySecurity(r.Context(), pool, sec.ID, from, to)
		if err != nil {
			writeError(w, http.StatusInternalServerError, CodeInternal, "error al leer precios de %s", t)
			return
		}
		all[t] = prices
	}

	res := compare.CompareAssets(all, from, to, rf)
	writeJSON(w, http.StatusOK, map[string]any{
		"tickers":                tickers,
		"from":                   dateOrEmpty(from),
		"to":                     dateOrEmpty(to),
		"normalized_performance": res.NormalizedPerformance,
		"risk_metrics":           res.RiskMetrics,
	})
}

// backtestParams mirrors the SMA configuration in the response envelope.
type backtestParams struct {
	Fast int `json:"fast"`
	Slow int `json:"slow"`
}

// backtestResponse is the body of /backtest/{strategy}?tickers=... (plan D8):
// {strategy, params, results: {TICKER: BacktestResult}}.
type backtestResponse struct {
	Strategy string                              `json:"strategy"`
	Params   backtestParams                      `json:"params"`
	From     string                              `json:"from,omitempty"`
	To       string                              `json:"to,omitempty"`
	Results  map[string]*backtest.BacktestResult `json:"results"`
}

// handleBacktest: GET /backtest/{strategy}?tickers=AAPL,MSFT&fast=&slow=
// (también acepta el parámetro singular ?ticker= para compatibilidad).
func handleBacktest(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, strategy string) {
	switch strategy {
	case backtest.StrategySMA:
	default:
		writeError(w, http.StatusBadRequest, CodeUnsupported, "estrategia %q no soportada (actual: %q)", strategy, backtest.StrategySMA)
		return
	}
	tickers, err := tickerListParam(r.URL.Query(), "tickers")
	if err != nil {
		// Fallback: parámetro singular ?ticker= (contexto previo).
		if single := r.URL.Query().Get("ticker"); single != "" {
			tickers, err = tickerListParam(r.URL.Query(), "ticker")
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
			return
		}
	}
	from, to, err := parseDateRange(r, time.Time{}, time.Time{})
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	fast, err := parseIntQuery(r.URL.Query().Get("fast"), backtest.DefaultFast)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	slow, err := parseIntQuery(r.URL.Query().Get("slow"), backtest.DefaultSlow)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}
	capital, err := parseFloatQuery(r.URL.Query().Get("initial_capital"), 10000)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidation, "%v", err)
		return
	}

	results := make(map[string]*backtest.BacktestResult, len(tickers))
	for _, t := range tickers {
		res, err := backtest.RunSMABacktest(r.Context(), pool, t, backtest.SMAConfig{
			Fast: fast, Slow: slow, InitialCapital: capital,
			StartDate: from, EndDate: to,
		})
		if err != nil {
			switch {
			case isNotFound(err):
				writeError(w, http.StatusNotFound, CodeNotFound, "security %q no encontrada", t)
			case errors.Is(err, backtest.ErrInsufficientData):
				writeError(w, http.StatusNotFound, CodeNotFound, "sin histórico suficiente para %q (se requieren %d barras)", t, slow)
			default:
				writeError(w, http.StatusInternalServerError, CodeInternal, "%v", err)
			}
			return
		}
		results[t] = res
	}
	writeJSON(w, http.StatusOK, backtestResponse{
		Strategy: strategy,
		Params:   backtestParams{Fast: fast, Slow: slow},
		From:     dateOrEmpty(from),
		To:       dateOrEmpty(to),
		Results:  results,
	})
}

// parseDateRange parses ?from=/?to= (YYYY-MM-DD) with defaults; returning an
// error carries a client-friendly message.
func parseDateRange(r *http.Request, defFrom, defTo time.Time) (time.Time, time.Time, error) {
	from, to := defFrom, defTo
	var err error
	if raw := r.URL.Query().Get("from"); raw != "" {
		if from, err = time.Parse("2006-01-02", raw); err != nil {
			return from, to, errors.New("'from' inválido (formato YYYY-MM-DD)")
		}
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		if to, err = time.Parse("2006-01-02", raw); err != nil {
			return from, to, errors.New("'to' inválido (formato YYYY-MM-DD)")
		}
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		return from, to, errors.New("'from' no puede ser posterior a 'to'")
	}
	return from, to, nil
}
