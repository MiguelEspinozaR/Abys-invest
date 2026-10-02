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

// FYAvailability is the temporal provenance of a set of FY facts (SPEC §4):
// the two dates a calculation dated `as_of` must report to be reproducible.
//
//	AvailableAt     = max(filing_date) of the facts used. The day the LAST of
//	                  those numbers became public — the date from which the
//	                  calculation is reproducible (NOT the price date).
//	FundamentalsAsOf = period_end of the annual fiscal year used, i.e. WHICH
//	                  books were used, independently of when they were filed.
type FYAvailability struct {
	AvailableAt      time.Time
	FundamentalsAsOf time.Time
}

// GetLatestFYFundamentalsAsOf returns, for every requested concept, the newest
// fact that was BOTH annual and already public on `asOf`, together with the
// temporal provenance of that selection. It closes the M6a-F1 debt, which the
// old GetLatestFYFundamentals could not: a valuation dated `as_of` must not
// silently consume a 10-K filed weeks later (look-ahead bias).
//
// The filters are the ones that make the selection a FISCAL YEAR and not any
// FY-tagged period:
//
//   - fiscal_period = 'FY': only annual filings. In EDGAR 'FY' also carries
//     quarterly periods (AAPL net_earnings has 2018-03-31, 2018-06-30...), and
//     annualising a quarter is nonsense.
//   - period_start IS NULL (INSTANT fact, e.g. shares_outstanding, total_debt —
//     a cover-page count that has no duration at all) OR a duration between
//     330 and 400 days. Both are legitimate annual inputs; excluding the
//     instants would silently drop the share count and the debt, and the
//     valuation would degrade with no error and no reason. The 330-400 window
//     covers 52/53-week fiscal years.
//   - filing_date IS NULL OR filing_date <= asOf: no look-ahead. A 10-K filed
//     after asOf cannot have informed a value dated asOf, even when it restates
//     the same fiscal year.
//   - DISTINCT ON (concept) with the deterministic tie-break
//     `period_end DESC, filing_date DESC NULLS LAST, id DESC`: the most recent
//     restatement of a period wins, deterministically.
//
// A concept without usable data is simply ABSENT from the map (conservative
// engine rule: nil, never 0).
//
// Deviation from the plan's sketch (B3), justified: the plan returns only
// `available_at`, but the 014 row also has `fundamentals_as_of` and both dates
// come from the same selection, so they travel together in FYAvailability
// instead of being derived twice.
func GetLatestFYFundamentalsAsOf(ctx context.Context, q DBTX, securityID int64, concepts []string, asOf time.Time) (map[string]*float64, FYAvailability, error) {
	out := map[string]*float64{}
	var avail FYAvailability
	if len(concepts) == 0 {
		return out, avail, nil
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (concept) concept, value, period_end, filing_date, period_start
		FROM fundamentals
		WHERE security_id = $1 AND concept = ANY($2) AND fiscal_period = 'FY'
		  AND (filing_date IS NULL OR filing_date <= $3)
		  AND (period_start IS NULL OR (period_end - period_start) BETWEEN 330 AND 400)
		ORDER BY concept, period_end DESC, filing_date DESC NULLS LAST, id DESC`,
		securityID, concepts, asOf)
	if err != nil {
		return nil, avail, fmt.Errorf("storage: fundamentales FY as-of security %d: %w", securityID, err)
	}
	defer rows.Close()
	var maxPeriodEnd time.Time // max period_end of the ANNUAL (duration) facts
	var maxInstantPeriodEnd time.Time
	var maxFiling time.Time
	for rows.Next() {
		var concept string
		var value *float64
		var periodEnd time.Time
		var filingDate *time.Time
		var periodStart *time.Time
		if err := rows.Scan(&concept, &value, &periodEnd, &filingDate, &periodStart); err != nil {
			return nil, avail, fmt.Errorf("storage: scan fundamentales FY as-of: %w", err)
		}
		out[concept] = value
		if periodStart == nil {
			if periodEnd.After(maxInstantPeriodEnd) {
				maxInstantPeriodEnd = periodEnd
			}
		} else if periodEnd.After(maxPeriodEnd) {
			maxPeriodEnd = periodEnd
		}
		if filingDate != nil && filingDate.After(maxFiling) {
			maxFiling = *filingDate
		}
	}
	if err := rows.Err(); err != nil {
		return nil, avail, fmt.Errorf("storage: rows fundamentales FY as-of: %w", err)
	}
	avail.AvailableAt = maxFiling
	// FundamentalsAsOf is the FY period_end when at least one annual fact was
	// used; otherwise the newest instant date (a balance sheet without income
	// statement, still better than a zero time that would look like 0001-01-01).
	if !maxPeriodEnd.IsZero() {
		avail.FundamentalsAsOf = maxPeriodEnd
	} else {
		avail.FundamentalsAsOf = maxInstantPeriodEnd
	}
	return out, avail, nil
}

// GetLatestFYFundamentals returns the newest FY row of every requested
// concept (DISTINCT ON concept ordered by period_end DESC). Missing concepts
// are simply absent from the map (conservative engine rule).
//
// DEPRECATED: it applies NO temporal filter, so it can consume a fact filed
// after the valuation date (the M6a-F1 look-ahead debt). New code must call
// GetLatestFYFundamentalsAsOf; it is kept because the M6a WACC stage still
// calls it and changing its behaviour would rewrite M6a results in the same
// milestone.
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
