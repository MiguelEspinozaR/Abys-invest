package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// SearchSecurities searches the whole catalog (plan M5) by ticker (prefix,
// case-insensitive) or name (substring, ILIKE) and returns at most limit
// results ranked by: exact ticker match (0), ticker prefix (1), name match (2),
// with ticker ASC as stable tie-break inside each rank. query is the
// already-trimmed user term; wildcard characters in it are escaped so they are
// matched literally.
//
// NOTE (perf): the name match is a substring ILIKE, which cannot use a btree
// index; the catalog is ~10k rows so a seq scan is fine. A pg_trgm GIN index
// would be the scaling path and needs an extension + migration (out of scope).
func SearchSecurities(ctx context.Context, q DBTX, query string, limit int) ([]Security, error) {
	if limit <= 0 {
		limit = 10
	}
	pattern := escapeLike(query)
	// upper lleva el término a mayúsculas: el ranking compara contra tickers
	// del catálogo (siempre en mayúsculas) con = y LIKE, que son case
	// sensitive; el WHERE usa ILIKE con el término tal cual.
	upper := strings.ToUpper(pattern)
	rows, err := q.Query(ctx, `
SELECT `+securityColumns+`
FROM securities
WHERE ticker ILIKE $1 ESCAPE E'\\' OR name ILIKE $2 ESCAPE E'\\'
ORDER BY CASE
             WHEN ticker = $3 THEN 0
             WHEN ticker LIKE $4 THEN 1
             ELSE 2
         END,
         ticker ASC
LIMIT $5`,
		pattern+"%", "%"+pattern+"%", upper, upper+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("storage: search securities: %w", err)
	}
	defer rows.Close()

	out := make([]Security, 0, limit)
	for rows.Next() {
		var s Security
		if err := scanSecurity(rows, &s); err != nil {
			return nil, fmt.Errorf("storage: search securities scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: search securities rows: %w", err)
	}
	return out, nil
}

// escapeLike neutralizes the ILIKE/LIKE wildcards of user input so `%` and `_`
// are searched literally (paired with ESCAPE '\' in the query).
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// UpdateSecuritySector sets sector/industry for a ticker, overwriting any
// previous value (the enricher runs on demand). Returns pgx.ErrNoRows when
// the ticker is not cataloged.
func UpdateSecuritySector(ctx context.Context, q DBTX, ticker string, sector, industry *string) error {
	tag, err := q.Exec(ctx, `
UPDATE securities
SET sector     = $2,
    industry   = $3,
    updated_at = now()
WHERE ticker = $1`, ticker, sector, industry)
	if err != nil {
		return fmt.Errorf("storage: update security sector %s: %w", ticker, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("storage: update security sector %s: %w", ticker, pgx.ErrNoRows)
	}
	return nil
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
