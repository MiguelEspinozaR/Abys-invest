package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const stagingColumns = `id, cik, ticker, accession, form_type, filing_date, period_end, payload, payload_type, ingested_at, normalized`

// InsertStaging inserts a raw SEC EDGAR payload. When a row with the same
// (accession, form_type, payload_type) already exists it is skipped (DO NOTHING)
// and a nil ID is returned (idempotent ingestion).
func InsertStaging(ctx context.Context, q DBTX, s *EdgarStaging) (*int64, error) {
	row := q.QueryRow(ctx, `
INSERT INTO edgar_staging (cik, ticker, accession, form_type, filing_date, period_end, payload, payload_type)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (accession, form_type, payload_type) DO NOTHING
RETURNING id`,
		s.CIK, s.Ticker, s.Accession, s.FormType, s.FilingDate, s.PeriodEnd, s.Payload, s.PayloadType)

	var id int64
	switch err := row.Scan(&id); {
	case err == nil:
		return &id, nil
	case err == pgx.ErrNoRows:
		// Conflict: already ingested, skipped.
		return nil, nil
	default:
		return nil, fmt.Errorf("storage: insert staging %s/%s: %w", s.Accession, s.PayloadType, err)
	}
}

// GetStagingByID returns a single staging row.
func GetStagingByID(ctx context.Context, q DBTX, id int64) (*EdgarStaging, error) {
	row := q.QueryRow(ctx, `SELECT `+stagingColumns+` FROM edgar_staging WHERE id = $1`, id)
	out := &EdgarStaging{}
	if err := scanStaging(row, out); err != nil {
		return nil, fmt.Errorf("storage: get staging %d: %w", id, err)
	}
	return out, nil
}

// GetUnprocessedStaging returns up to limit staging rows not yet normalized.
func GetUnprocessedStaging(ctx context.Context, q DBTX, limit int) ([]EdgarStaging, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := q.Query(ctx, `SELECT `+stagingColumns+` FROM edgar_staging WHERE normalized = false ORDER BY id LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: list unprocessed staging: %w", err)
	}
	defer rows.Close()

	var out []EdgarStaging
	for rows.Next() {
		var s EdgarStaging
		if err := scanStaging(rows, &s); err != nil {
			return nil, fmt.Errorf("storage: list unprocessed staging scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list unprocessed staging rows: %w", err)
	}
	return out, nil
}

// DeleteStagingByCIK removes the pending companyfacts staging rows of a CIK so
// a later insert re-ingests the payload from scratch (re-ingesta fresca usada
// por el pipeline Force Refresh). Returns the number of rows deleted.
func DeleteStagingByCIK(ctx context.Context, q DBTX, cik string) (int64, error) {
	tag, err := q.Exec(ctx, `
DELETE FROM edgar_staging
WHERE cik = $1 AND payload_type = 'company_facts'`, cik)
	if err != nil {
		return 0, fmt.Errorf("storage: delete staging %s: %w", cik, err)
	}
	return tag.RowsAffected(), nil
}

// MarkStagingNormalized flags a staging row as processed.
func MarkStagingNormalized(ctx context.Context, q DBTX, id int64) error {
	tag, err := q.Exec(ctx, `UPDATE edgar_staging SET normalized = true WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("storage: mark staging %d normalized: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("storage: mark staging %d normalized: row not found", id)
	}
	return nil
}

func scanStaging(row rowScanner, s *EdgarStaging) error {
	return row.Scan(
		&s.ID, &s.CIK, &s.Ticker, &s.Accession, &s.FormType, &s.FilingDate,
		&s.PeriodEnd, &s.Payload, &s.PayloadType, &s.IngestedAt, &s.Normalized,
	)
}
