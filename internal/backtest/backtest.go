// Package backtest implements deterministic SMA crossover backtests on the
// stored daily closes (SPEC §13.4: strategy "sma", fast 50 / slow 200).
package backtest

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/compare"
	"github.com/miky/abys-invest/internal/storage"
)

// ErrInsufficientData is returned when the ticker lacks enough bars to form
// the slow SMA window.
var ErrInsufficientData = fmt.Errorf("backtest: histórico insuficiente para la ventana lenta")

// Strategy constants (M3: only SMA; other strategies land in M4+).
const (
	StrategySMA = "sma"
	DefaultFast = 50
	DefaultSlow = 200
)

// SMAConfig parameterizes a crossover backtest.
type SMAConfig struct {
	Fast           int       // ventana rápida (SMA_n)
	Slow           int       // ventana lenta (SMA_m)
	StartDate      time.Time // no incluir barras anteriores a este día
	EndDate        time.Time // no incluir barras posteriores a este día
	InitialCapital float64
	RiskFreeRate   float64 // tasa libre de riesgo anual (decimal, default 0) para Sharpe
}

// Trade is one crossed signal (long position until the opposite crossover).
type Trade struct {
	EntryDate  time.Time `json:"entry_date"`
	EntryPrice float64   `json:"entry_price"`
	ExitDate   time.Time `json:"exit_date,omitempty"`
	ExitPrice  float64   `json:"exit_price,omitempty"`
	ReturnPct  float64   `json:"return_pct,omitempty"`
	Open       bool      `json:"open,omitempty"` // señal sin salida al final del histórico
}

// BacktestResult is the response body of /backtest/sma (plan D10 metrics:
// total_return, cagr, sharpe, max_drawdown, total_trades).
type BacktestResult struct {
	Ticker         string    `json:"ticker"`
	Strategy       string    `json:"strategy"`
	Fast           int       `json:"fast"`
	Slow           int       `json:"slow"`
	StartDate      time.Time `json:"start_date"`
	EndDate        time.Time `json:"end_date"`
	InitialCapital float64   `json:"initial_capital"`
	FinalCapital   float64   `json:"final_capital"`
	TotalReturn    float64   `json:"total_return"` // ratio, ej. 0.2631
	TotalReturnPct float64   `json:"total_return_pct"`
	CAGR           float64   `json:"cagr"`
	Sharpe         float64   `json:"sharpe"`
	MaxDrawdown    float64   `json:"max_drawdown"`
	Trades         []Trade   `json:"trades"`
	TotalTrades    int       `json:"total_trades"`
}

// RunSMABacktest executes the golden-rule cross: buy when SMA_fast crosses
// above SMA_slow, sell when it crosses below (SPEC §13.4).
func RunSMABacktest(ctx context.Context, pool *pgxpool.Pool, ticker string, cfg SMAConfig) (*BacktestResult, error) {
	if cfg.Fast <= 0 {
		cfg.Fast = DefaultFast
	}
	if cfg.Slow <= 0 {
		cfg.Slow = DefaultSlow
	}
	if cfg.Fast >= cfg.Slow {
		return nil, fmt.Errorf("backtest: fast (%d) debe ser menor que slow (%d)", cfg.Fast, cfg.Slow)
	}
	if cfg.InitialCapital <= 0 {
		cfg.InitialCapital = 10000
	}

	sec, err := storage.GetSecurityByTicker(ctx, pool, ticker)
	if err != nil {
		return nil, err // pgx.ErrNoRows → 404
	}
	prices, err := storage.GetDailyPricesBySecurity(ctx, pool, sec.ID, time.Time{}, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("backtest: precios de %s: %w", ticker, err)
	}
	// Ventana opcional from/to (barras fuera de rango se descartan).
	if !cfg.StartDate.IsZero() || !cfg.EndDate.IsZero() {
		filtered := prices[:0]
		for _, p := range prices {
			if !cfg.StartDate.IsZero() && p.Date.Before(cfg.StartDate) {
				continue
			}
			if !cfg.EndDate.IsZero() && p.Date.After(cfg.EndDate) {
				continue
			}
			filtered = append(filtered, p)
		}
		prices = filtered
	}
	if len(prices) < cfg.Slow {
		return nil, ErrInsufficientData
	}

	closes := make([]float64, len(prices))
	dates := make([]time.Time, len(prices))
	for i := range prices {
		closes[i] = prices[i].AdjustedClose
		dates[i] = prices[i].Date
	}
	res := runSMABacktestOnSeries(dates, closes, cfg)
	res.Ticker = ticker
	return res, nil
}

// runSMABacktestOnSeries is the deterministic core (pure; unit-tested without
// a database): buy when SMA_fast crosses above SMA_slow, sell when it crosses
// below. Golden rule: entries/exits only at crossovers, never intra-swing.
func runSMABacktestOnSeries(dates []time.Time, closes []float64, cfg SMAConfig) *BacktestResult {
	if cfg.Fast <= 0 {
		cfg.Fast = DefaultFast
	}
	if cfg.Slow <= 0 {
		cfg.Slow = DefaultSlow
	}
	if cfg.InitialCapital <= 0 {
		cfg.InitialCapital = 10000
	}

	fast := calculateSMA(closes, cfg.Fast)
	slow := calculateSMA(closes, cfg.Slow)

	res := &BacktestResult{
		Strategy: StrategySMA, Fast: cfg.Fast, Slow: cfg.Slow,
		StartDate: dates[0], EndDate: dates[len(dates)-1],
		InitialCapital: cfg.InitialCapital,
		FinalCapital:   cfg.InitialCapital,
		Trades:         []Trade{},
	}
	capital := cfg.InitialCapital
	var open *Trade
	lastPosition := false // true = in the market

	// A partir del índice slow-1 ambas ventanas están completas.
	for i := cfg.Slow - 1; i < len(closes); i++ {
		inMarket := fast[i] > slow[i]
		if cfg.StartDate.IsZero() || !dates[i].Before(cfg.StartDate) {
			if inMarket && !lastPosition {
				if open != nil {
					closeTrade(open, dates[i], closes[i])
					capital = applyExit(capital, open.ReturnPct)
					res.Trades = append(res.Trades, *open)
				}
				open = &Trade{EntryDate: dates[i], EntryPrice: closes[i]}
			} else if !inMarket && lastPosition && open != nil {
				closeTrade(open, dates[i], closes[i])
				capital = applyExit(capital, open.ReturnPct)
				res.Trades = append(res.Trades, *open)
				open = nil
			}
		}
		lastPosition = inMarket
	}
	if open != nil {
		// Posición aún abierta: valorada al último cierre (marcada como
		// abierta) y su retorno no realizado se suma al capital final
		// (mark-to-market sobre close, documentado en la CA M3).
		open.ExitDate = dates[len(dates)-1]
		open.ExitPrice = closes[len(closes)-1]
		open.ReturnPct = pct(open.EntryPrice, open.ExitPrice)
		open.Open = true
		res.Trades = append(res.Trades, *open)
		capital = applyExit(capital, open.ReturnPct)
	}

	res.TotalTrades = len(res.Trades)
	res.FinalCapital = capital
	if capital > 0 && cfg.InitialCapital > 0 {
		res.TotalReturn = capital/cfg.InitialCapital - 1
		res.TotalReturnPct = res.TotalReturn * 100
	} else {
		res.TotalReturn = -1
		res.TotalReturnPct = -100
	}
	// Métricas D10 sobre la ventana efectiva (base: activo subyacente, misma
	// convención que /compare; documentado).
	days := int(res.EndDate.Sub(res.StartDate).Hours() / 24)
	if res.FinalCapital > 0 && res.InitialCapital > 0 && days > 0 {
		res.CAGR = math.Pow(res.FinalCapital/res.InitialCapital, 365.25/float64(days)) - 1
	}
	res.MaxDrawdown = compare.MaxDrawdown(seriesToPrices(dates, closes))
	res.Sharpe = compare.SharpeRatio(seriesToPrices(dates, closes), cfg.RiskFreeRate)
	return res
}

// seriesToPrices adapts the pure-series closes to the storage.DailyPrice shape
// expected by the compare risk helpers (AdjustedClose is the trading series).
func seriesToPrices(dates []time.Time, closes []float64) []storage.DailyPrice {
	out := make([]storage.DailyPrice, len(closes))
	for i := range closes {
		out[i] = storage.DailyPrice{Date: dates[i], AdjustedClose: closes[i]}
	}
	return out
}

// calculateSMA returns the i-th simple moving average of the window ending at
// index i (0 before the window is complete; those indexes are never used).
func calculateSMA(closes []float64, window int) []float64 {
	out := make([]float64, len(closes))
	var sum float64
	for i := 0; i < len(closes); i++ {
		sum += closes[i]
		if i >= window {
			sum -= closes[i-window]
		}
		if i >= window-1 {
			out[i] = sum / float64(window)
		}
	}
	return out
}

func closeTrade(t *Trade, exitDate time.Time, exitPrice float64) {
	t.ExitDate = exitDate
	t.ExitPrice = exitPrice
	t.ReturnPct = pct(t.EntryPrice, exitPrice)
}

func applyExit(capital, returnPct float64) float64 {
	return capital * (1 + returnPct/100)
}

func pct(from, to float64) float64 {
	if from <= 0 {
		return 0
	}
	return (to - from) / from * 100
}
