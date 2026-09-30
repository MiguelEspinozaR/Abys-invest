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

// QuoteBar is the market snapshot of the last REAL bar of a symbol: the REGULAR
// close (valuation_price, without dividend/split adjustment) and the date of
// that bar according to the exchange — never time.Now() (SPEC §22).
type QuoteBar struct {
	Symbol, Currency string
	Date             time.Time // date of the last bar of the chart (exchange)
	Price            float64   // regularMarketPrice = regular close
}

// GetQuoteBar returns the date and the regular price of the last real bar.
func (c *Client) GetQuoteBar(ctx context.Context, symbol string) (*QuoteBar, error) {
	parsed, err := c.GetHistorical(ctx, symbol, "1d", "1d")
	if err != nil {
		return nil, err
	}
	return quoteBarFromChartResult(parsed)
}

// quoteBarFromChartResult builds the QuoteBar from a parsed chart: the date is
// the date of the last bar the exchange reported and the price is
// regularMarketPrice. It fails (never invents a date) when there are no bars or
// the price is not usable, which is the same validation GetQuote always had.
func quoteBarFromChartResult(res *ChartResult) (*QuoteBar, error) {
	if res == nil || len(res.Bars) == 0 {
		return nil, fmt.Errorf("yahoo: chart sin barras para %s", tickerOf(res))
	}
	if res.CurrentPrice <= 0 {
		return nil, fmt.Errorf("yahoo: quote de %s no válido (precio=%v)", tickerOf(res), res.CurrentPrice)
	}
	last := res.Bars[len(res.Bars)-1]
	return &QuoteBar{
		Symbol:   res.Symbol,
		Currency: res.Currency,
		Date:     last.Date,
		Price:    res.CurrentPrice,
	}, nil
}

func tickerOf(res *ChartResult) string {
	if res == nil {
		return "?"
	}
	return res.Symbol
}

// GetQuote fetches only the current price of symbol via the chart API with
// range=1d&interval=1d (meta.regularMarketPrice).
func (c *Client) GetQuote(ctx context.Context, symbol string) (float64, error) {
	bar, err := c.GetQuoteBar(ctx, symbol)
	if err != nil {
		return 0, err
	}
	return bar.Price, nil
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

// IngestQuote refreshes the close of the last REAL bar of symbol (the exchange
// date, not today) and returns the persisted bar.
//
// SPEC §22: `close` is the valuation_price (regular close) and
// `adjusted_close` the historical_price (adjusted series). The quote only
// updates `close`; it never touches adjusted_close/OHLC/volume of an existing
// row (the historical series is the source of truth for returns, SMA and
// momentum). When no row exists for that date it inserts a synthetic bar with
// adjusted_close = close and source = 'yahoo_quote', which happens on a
// non-trading day or on a symbol whose chart is shorter than the catalog.
func (c *Client) IngestQuote(ctx context.Context, db batcher, securityID int64, symbol string) (*storage.DailyPrice, error) {
	bar, err := c.GetQuoteBar(ctx, symbol)
	if err != nil {
		return nil, err
	}

	date := bar.Date.Truncate(24 * time.Hour)
	tx, err := db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("yahoo: begin tx quote %s: %w", symbol, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	persisted, err := storage.UpdateQuoteClose(ctx, tx, securityID, date, bar.Price)
	if err != nil {
		return nil, fmt.Errorf("yahoo: upsert quote %s: %w", symbol, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("yahoo: commit quote %s: %w", symbol, err)
	}
	return persisted, nil
}

// NormalizeTicker uppercases and trims a ticker symbol.
func NormalizeTicker(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}
