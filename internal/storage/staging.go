package storage

import (
	"context"
	"fmt"
	"sort"
	"strings"

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

// StagedNormalizedCIKsSQL is the READ-ONLY selector of the default re-ingest
// universe (plan M6c-T1 W3, Az6): the CIKs whose `company_facts` payload is
// already in `edgar_staging` AND normalized, i.e. exactly the companies whose
// `fundamentals` were written by the canonicalizer and therefore the only ones
// a catalog change can affect.
//
// It is deliberately NOT `securities` (10.461 filas del catálogo SEC, de las que
// ~42 están ingeridas): re-canonizar las que nunca se descargaron sería un
// trabajo inútil y una descarga masiva contra EDGAR. Nor is it "all staging":
// a row with normalized=false is a payload whose normalization FAILED, and
// re-running the re-ingest over it is a diagnostic decision, not a default.
//
// SELECT-only on purpose: `make reingest-fundamentals` runs it before touching
// anything, and this function must never be able to write.
const StagedNormalizedCIKsSQL = `SELECT cik
	FROM edgar_staging
	WHERE payload_type = 'company_facts' AND normalized
	GROUP BY cik
	ORDER BY cik`

// StagedNormalizedCIKs returns the default re-ingest universe: CIKs with a
// normalized `company_facts` payload, deduplicated, sorted ascending. Read-only.
func StagedNormalizedCIKs(ctx context.Context, q DBTX) ([]string, error) {
	rows, err := q.Query(ctx, StagedNormalizedCIKsSQL)
	if err != nil {
		return nil, fmt.Errorf("storage: selector de universo con staging: %w", err)
	}
	defer rows.Close()

	var raw []string
	for rows.Next() {
		var cik string
		if err := rows.Scan(&cik); err != nil {
			return nil, fmt.Errorf("storage: scan del universo con staging: %w", err)
		}
		raw = append(raw, cik)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: iteración del universo con staging: %w", err)
	}
	return SortDedupCIKs(raw)
}

// SortDedupCIKs normalizes a CIK list coming from the selector: trims, drops
// empties, deduplicates and sorts ascending. Split out of StagedNormalizedCIKs so
// the pure part (order + dedupe, which is what the -companies CSV depends on)
// is testable without a database, and so the DB call stays a thin read.
func SortDedupCIKs(raw []string) ([]string, error) {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, cik := range raw {
		cik = strings.TrimSpace(cik)
		if cik == "" {
			continue
		}
		if !isCIK(cik) {
			return nil, fmt.Errorf("storage: CIK inválido %q en el universo con staging (10 dígitos, sin espacios)", cik)
		}
		if seen[cik] {
			continue
		}
		seen[cik] = true
		out = append(out, cik)
	}
	sort.Strings(out)
	return out, nil
}

// isCIK is the local shape check for a CIK (storage cannot import the edgar
// package: edgar imports storage). It mirrors edgar.NormalizeCIK, which is what
// the ingestion path will re-validate anyway; here it only avoids building a
// `-companies` CSV out of garbage rows.
func isCIK(s string) bool {
	if len(s) > 10 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func scanStaging(row rowScanner, s *EdgarStaging) error {
	return row.Scan(
		&s.ID, &s.CIK, &s.Ticker, &s.Accession, &s.FormType, &s.FilingDate,
		&s.PeriodEnd, &s.Payload, &s.PayloadType, &s.IngestedAt, &s.Normalized,
	)
}
