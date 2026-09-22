package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const securityColumns = `id, ticker, cik, name, type, currency, status, exchange, sector, industry, created_at, updated_at`

// UpsertSecurity inserts a security keyed by ticker; on conflict it updates the
// catalog fields and returns the (possibly existing) row with its ID.
func UpsertSecurity(ctx context.Context, q DBTX, s *Security) (*Security, error) {
	row := q.QueryRow(ctx, `
INSERT INTO securities (ticker, cik, name, type, currency, status, exchange, sector, industry)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (ticker) DO UPDATE SET
    cik        = EXCLUDED.cik,
    name       = EXCLUDED.name,
    type       = EXCLUDED.type,
    currency   = EXCLUDED.currency,
    status     = EXCLUDED.status,
    exchange   = COALESCE(EXCLUDED.exchange, securities.exchange),
    sector     = COALESCE(EXCLUDED.sector, securities.sector),
    industry   = COALESCE(EXCLUDED.industry, securities.industry),
    updated_at = now()
RETURNING `+securityColumns,
		s.Ticker, s.CIK, s.Name, s.Type, s.Currency, s.Status, s.Exchange, s.Sector, s.Industry)

	out := &Security{}
	if err := scanSecurity(row, out); err != nil {
		return nil, fmt.Errorf("storage: upsert security %s: %w", s.Ticker, err)
	}
	return out, nil
}

// GetSecurityByTicker returns the security with the given ticker, or
// pgx.ErrNoRows when it does not exist.
func GetSecurityByTicker(ctx context.Context, q DBTX, ticker string) (*Security, error) {
	row := q.QueryRow(ctx, `SELECT `+securityColumns+` FROM securities WHERE ticker = $1`, ticker)
	out := &Security{}
	if err := scanSecurity(row, out); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("storage: get security by ticker %s: %w", ticker, err)
	}
	return out, nil
}

// ListSecurities returns securities ordered by ticker with pagination.
func ListSecurities(ctx context.Context, q DBTX, limit, offset int) ([]Security, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := q.Query(ctx, `SELECT `+securityColumns+` FROM securities ORDER BY ticker LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("storage: list securities: %w", err)
	}
	defer rows.Close()

	var out []Security
	for rows.Next() {
		var s Security
		if err := scanSecurity(rows, &s); err != nil {
			return nil, fmt.Errorf("storage: list securities scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list securities rows: %w", err)
	}
	return out, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSecurity(row rowScanner, s *Security) error {
	return row.Scan(
		&s.ID, &s.Ticker, &s.CIK, &s.Name, &s.Type, &s.Currency, &s.Status,
		&s.Exchange, &s.Sector, &s.Industry, &s.CreatedAt, &s.UpdatedAt,
	)
}
