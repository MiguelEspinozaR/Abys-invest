package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const dailyPriceColumns = `id, security_id, date, open, high, low, close, adjusted_close, volume, source, created_at`

// UpsertDailyPrices persists a batch of daily prices inside the given
// transaction. Keyed by (security_id, date): on conflict, the OHLCV values are
// refreshed while created_at is preserved (idempotent).
func UpsertDailyPrices(ctx context.Context, tx pgx.Tx, prices []DailyPrice) error {
	if len(prices) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for i := range prices {
		p := &prices[i]
		batch.Queue(`
INSERT INTO daily_prices (security_id, date, open, high, low, close, adjusted_close, volume, source)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (security_id, date) DO UPDATE SET
    open           = EXCLUDED.open,
    high           = EXCLUDED.high,
    low            = EXCLUDED.low,
    close          = EXCLUDED.close,
    adjusted_close = EXCLUDED.adjusted_close,
    volume         = EXCLUDED.volume,
    source         = EXCLUDED.source`,
			p.SecurityID, p.Date, p.Open, p.High, p.Low, p.Close, p.AdjustedClose, p.Volume, p.Source)
	}

	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("storage: upsert daily prices batch (row %d): %w", i, err)
		}
	}
	return nil
}

// GetDailyPricesBySecurity returns prices within [from, to]; nil/zero bounds
// are open-ended. Ordered by date ascending.
func GetDailyPricesBySecurity(ctx context.Context, q DBTX, securityID int64, from, to time.Time) ([]DailyPrice, error) {
	query := `SELECT ` + dailyPriceColumns + ` FROM daily_prices WHERE security_id = $1`
	args := []any{securityID}
	n := 2
	if !from.IsZero() {
		query += fmt.Sprintf(` AND date >= $%d`, n)
		args = append(args, from)
		n++
	}
	if !to.IsZero() {
		query += fmt.Sprintf(` AND date <= $%d`, n)
		args = append(args, to)
		n++
	}
	query += ` ORDER BY date ASC`

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: get daily prices for security %d: %w", securityID, err)
	}
	defer rows.Close()

	var out []DailyPrice
	for rows.Next() {
		var p DailyPrice
		if err := scanDailyPrice(rows, &p); err != nil {
			return nil, fmt.Errorf("storage: get daily prices scan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: get daily prices rows: %w", err)
	}
	return out, nil
}

// GetLatestPrice returns the most recent price for a security, or
// pgx.ErrNoRows when none exists.
func GetLatestPrice(ctx context.Context, q DBTX, securityID int64) (*DailyPrice, error) {
	row := q.QueryRow(ctx, `SELECT `+dailyPriceColumns+` FROM daily_prices
		WHERE security_id = $1 ORDER BY date DESC, id DESC LIMIT 1`, securityID)
	p := &DailyPrice{}
	if err := scanDailyPrice(row, p); err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("storage: get latest price for security %d: %w", securityID, err)
	}
	return p, nil
}

// ListSecuritiesWithPrices returns the IDs of securities having at least one
// price row, ordered by id (stable for deterministic batch jobs).
func ListSecuritiesWithPrices(ctx context.Context, q DBTX) ([]int64, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT security_id FROM daily_prices ORDER BY security_id`)
	if err != nil {
		return nil, fmt.Errorf("storage: list securities with prices: %w", err)
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("storage: list securities with prices scan: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list securities with prices rows: %w", err)
	}
	return out, nil
}

func scanDailyPrice(row rowScanner, p *DailyPrice) error {
	return row.Scan(
		&p.ID, &p.SecurityID, &p.Date, &p.Open, &p.High, &p.Low,
		&p.Close, &p.AdjustedClose, &p.Volume, &p.Source, &p.CreatedAt,
	)
}
