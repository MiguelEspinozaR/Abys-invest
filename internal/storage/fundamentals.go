package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const fundamentalColumns = `id, security_id, concept, value, unit, period_type, period_start, period_end, fiscal_year, fiscal_period, filing_date, source, source_fact_id, raw_value, created_at`

// UpsertFundamentals persists a batch of normalized facts inside the given
// transaction. Rows are keyed by (security_id, concept, period_type, period_end,
// fiscal_year, fiscal_period): on conflict existing values are preserved when the
// incoming ones are NULL (idempotent).
func UpsertFundamentals(ctx context.Context, tx pgx.Tx, funds []Fundamental) error {
	if len(funds) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for i := range funds {
		f := &funds[i]
		batch.Queue(`
INSERT INTO fundamentals (security_id, concept, value, unit, period_type, period_start, period_end, fiscal_year, fiscal_period, filing_date, source, source_fact_id, raw_value)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (security_id, concept, period_type, period_end, fiscal_year, fiscal_period)
DO UPDATE SET
    value          = COALESCE(EXCLUDED.value, fundamentals.value),
    unit           = COALESCE(EXCLUDED.unit, fundamentals.unit),
    period_start   = COALESCE(EXCLUDED.period_start, fundamentals.period_start),
    filing_date    = COALESCE(EXCLUDED.filing_date, fundamentals.filing_date),
    source_fact_id = COALESCE(EXCLUDED.source_fact_id, fundamentals.source_fact_id),
    raw_value      = COALESCE(EXCLUDED.raw_value, fundamentals.raw_value)`,
			f.SecurityID, f.Concept, f.Value, f.Unit, f.PeriodType, f.PeriodStart, f.PeriodEnd,
			f.FiscalYear, f.FiscalPeriod, f.FilingDate, f.Source, f.SourceFactID, f.RawValue)
	}

	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("storage: upsert fundamentals batch (row %d): %w", i, err)
		}
	}
	return nil
}

// FundamentalFilter narrows GetFundamentalsBySecurity.
type FundamentalFilter struct {
	Concept       string
	PeriodEndFrom *time.Time
	PeriodEndTo   *time.Time
	Limit         int
}

// GetFundamentalsBySecurity returns normalized facts for a security, ordered by
// period_end descending.
func GetFundamentalsBySecurity(ctx context.Context, q DBTX, securityID int64, opts FundamentalFilter) ([]Fundamental, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 100
	}

	query := `SELECT ` + fundamentalColumns + ` FROM fundamentals WHERE security_id = $1`
	args := []any{securityID}
	n := 2
	if opts.Concept != "" {
		query += fmt.Sprintf(` AND concept = $%d`, n)
		args = append(args, opts.Concept)
		n++
	}
	if opts.PeriodEndFrom != nil {
		query += fmt.Sprintf(` AND period_end >= $%d`, n)
		args = append(args, *opts.PeriodEndFrom)
		n++
	}
	if opts.PeriodEndTo != nil {
		query += fmt.Sprintf(` AND period_end <= $%d`, n)
		args = append(args, *opts.PeriodEndTo)
		n++
	}
	query += fmt.Sprintf(` ORDER BY period_end DESC, concept LIMIT $%d`, n)
	args = append(args, limit)

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: get fundamentals for security %d: %w", securityID, err)
	}
	defer rows.Close()

	var out []Fundamental
	for rows.Next() {
		var f Fundamental
		if err := scanFundamental(rows, &f); err != nil {
			return nil, fmt.Errorf("storage: get fundamentals scan: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: get fundamentals rows: %w", err)
	}
	return out, nil
}

func scanFundamental(row rowScanner, f *Fundamental) error {
	return row.Scan(
		&f.ID, &f.SecurityID, &f.Concept, &f.Value, &f.Unit, &f.PeriodType,
		&f.PeriodStart, &f.PeriodEnd, &f.FiscalYear, &f.FiscalPeriod, &f.FilingDate,
		&f.Source, &f.SourceFactID, &f.RawValue, &f.CreatedAt,
	)
}
