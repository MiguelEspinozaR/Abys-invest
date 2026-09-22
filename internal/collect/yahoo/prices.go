package yahoo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/miky/abys-invest/internal/storage"
)

const (
	// DefaultRange is the historical depth used by GetHistorical.
	DefaultRange = "5y"
	// DefaultInterval is the bar cadence used by GetHistorical.
	DefaultInterval = "1d"
	// sourceYahoo is stored in daily_prices.source (ADR-0003).
	sourceYahoo = "yahoo"
)

// batcher is satisfied by *pgxpool.Pool and pgx.Tx, letting the adapter run
// batch upserts and transactions without a hard dependency on pools.
type batcher interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
	Begin(ctx context.Context) (pgx.Tx, error)
}

// GetHistorical fetches the OHLCV series for symbol over dateRange (default
// "5y") at interval (default "1d").
func (c *Client) GetHistorical(ctx context.Context, symbol, dateRange, interval string) (*ChartResult, error) {
	if dateRange == "" {
		dateRange = DefaultRange
	}
	if interval == "" {
		interval = DefaultInterval
	}
	body, err := c.get(ctx, c.chartURL(symbol, dateRange, interval))
	if err != nil {
		return nil, err
	}
	parsed, err := ParseChartResponse(body)
	if err != nil {
		return nil, fmt.Errorf("yahoo: historical %s %s: %w", symbol, dateRange, err)
	}
	return parsed, nil
}

// GetQuote fetches only the current price of symbol via the chart API with
// range=1d&interval=1d (meta.regularMarketPrice).
func (c *Client) GetQuote(ctx context.Context, symbol string) (float64, error) {
	parsed, err := c.GetHistorical(ctx, symbol, "1d", "1d")
	if err != nil {
		return 0, err
	}
	if parsed.CurrentPrice <= 0 {
		return 0, fmt.Errorf("yahoo: quote de %s no válido (precio=%v)", symbol, parsed.CurrentPrice)
	}
	return parsed.CurrentPrice, nil
}

// IngestPrices fetches the historical OHLCV series for symbol and upserts it
// into daily_prices. It returns the number of bars persisted.
func (c *Client) IngestPrices(ctx context.Context, db batcher, securityID int64, symbol string) (int, error) {
	parsed, err := c.GetHistorical(ctx, symbol, DefaultRange, DefaultInterval)
	if err != nil {
		return 0, err
	}

	prices := make([]storage.DailyPrice, 0, len(parsed.Bars))
	for _, bar := range parsed.Bars {
		if !ValidBar(bar) {
			continue // filas rotas (close NULL/<=0) no se persisten
		}
		date := bar.Date.Truncate(24 * time.Hour)
		prices = append(prices, storage.DailyPrice{
			SecurityID:    securityID,
			Date:          date,
			Open:          bar.Open,
			High:          bar.High,
			Low:           bar.Low,
			Close:         *bar.Close,
			AdjustedClose: bar.AdjustedClose,
			Volume:        bar.Volume,
			Source:        sourceYahoo,
		})
	}
	if len(prices) == 0 {
		return 0, fmt.Errorf("yahoo: sin barras válidas para %s", symbol)
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("yahoo: begin tx ingesta %s: %w", symbol, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := storage.UpsertDailyPrices(ctx, tx, prices); err != nil {
		return 0, fmt.Errorf("yahoo: upsert precios %s: %w", symbol, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("yahoo: commit precios %s: %w", symbol, err)
	}
	return len(prices), nil
}

// IngestQuote fetches the current price of symbol and upserts it as a
// daily bar of today (close = quote). Returns the persisted bar.
func (c *Client) IngestQuote(ctx context.Context, db batcher, securityID int64, symbol string) (*storage.DailyPrice, error) {
	price, err := c.GetQuote(ctx, symbol)
	if err != nil {
		return nil, err
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	bar := storage.DailyPrice{
		SecurityID:    securityID,
		Date:          today,
		Close:         price,
		AdjustedClose: price,
		Source:        sourceYahoo,
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("yahoo: begin tx quote %s: %w", symbol, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := storage.UpsertDailyPrices(ctx, tx, []storage.DailyPrice{bar}); err != nil {
		return nil, fmt.Errorf("yahoo: upsert quote %s: %w", symbol, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("yahoo: commit quote %s: %w", symbol, err)
	}
	return &bar, nil
}

// NormalizeTicker uppercases and trims a ticker symbol.
func NormalizeTicker(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}
