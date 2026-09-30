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

// GetLatestFYFundamentals returns the newest FY row of every requested
// concept (DISTINCT ON concept ordered by period_end DESC). Missing concepts
// are simply absent from the map (conservative engine rule).
func GetLatestFYFundamentals(ctx context.Context, q DBTX, securityID int64, concepts []string) (map[string]*float64, error) {
	out := map[string]*float64{}
	if len(concepts) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (concept) concept, value
		FROM fundamentals
		WHERE security_id = $1 AND concept = ANY($2) AND fiscal_period = 'FY'
		ORDER BY concept, period_end DESC`, securityID, concepts)
	if err != nil {
		return nil, fmt.Errorf("storage: fundamentales FY security %d: %w", securityID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var concept string
		var value *float64
		if err := rows.Scan(&concept, &value); err != nil {
			return nil, fmt.Errorf("storage: scan fundamentales FY: %w", err)
		}
		out[concept] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: rows fundamentales FY: %w", err)
	}
	return out, nil
}

// FYPoint is one annual FY fact of `fundamentals` with its availability date
// (filing_date): the pair (value, available_at) allows reproducing a calculation
// as of a date without look-ahead (SPEC §4).
type FYPoint struct {
	PeriodEnd   time.Time // end of the fiscal year
	Value       float64
	AvailableAt time.Time // filing_date
}

// GetFYAnnualSeries returns, for every requested concept, the series of ANNUAL
// FY facts available at asOf, deduplicated and ordered ASC by period_end.
//
// The filters live here and not in the engine (plan D2) because they are the
// ones that make a CAGR correct:
//
//   - fiscal_period = 'FY' AND duration between minDays and maxDays: in EDGAR
//     'FY' also carries quarterly periods (AAPL net_earnings has 2018-03-31,
//     2018-06-30...), and a CAGR over quarters is meaningless. A fiscal year is
//     not exactly 365 days (52/53-week years), hence the 330-400 day window.
//   - filing_date IS NULL OR filing_date <= asOf: no look-ahead. A 10-K filed
//     after asOf cannot have informed a value dated asOf, even if it restates
//     the same fiscal year.
//   - DISTINCT ON (concept, period_end) with the deterministic tie-break
//     `fiscal_year DESC, filing_date DESC NULLS LAST, id DESC`: the same
//     period_end can be stored 3 times (free_cash_flow 2023-09-30), and the
//     most recent restatement wins, deterministically.
//
// A concept without data simply does not appear in the map (conservative rule).
func GetFYAnnualSeries(ctx context.Context, q DBTX, securityID int64, concepts []string, asOf time.Time, minDays, maxDays int) (map[string][]FYPoint, error) {
	out := map[string][]FYPoint{}
	if len(concepts) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
SELECT DISTINCT ON (concept, period_end) concept, period_end, value, filing_date
FROM fundamentals
WHERE security_id = $1
  AND concept = ANY($2)
  AND fiscal_period = 'FY'
  AND period_start IS NOT NULL
  AND (period_end - period_start) BETWEEN $3 AND $4
  AND (filing_date IS NULL OR filing_date <= $5)
ORDER BY concept, period_end DESC, fiscal_year DESC, filing_date DESC NULLS LAST, id DESC`,
		securityID, concepts, minDays, maxDays, asOf)
	if err != nil {
		return nil, fmt.Errorf("storage: serie FY anual del security %d: %w", securityID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var concept string
		var p FYPoint
		var filingDate *time.Time
		if err := rows.Scan(&concept, &p.PeriodEnd, &p.Value, &filingDate); err != nil {
			return nil, fmt.Errorf("storage: scan serie FY anual: %w", err)
		}
		if filingDate != nil {
			p.AvailableAt = *filingDate
		}
		// La query viene en DESC (para el DISTINCT ON); cada serie se invierte a
		// ASC para que el motor use los últimos puntos de la serie.
		out[concept] = append(out[concept], p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: rows serie FY anual: %w", err)
	}
	for concept := range out {
		points := out[concept]
		for i, j := 0, len(points)-1; i < j; i, j = i+1, j-1 {
			points[i], points[j] = points[j], points[i]
		}
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
