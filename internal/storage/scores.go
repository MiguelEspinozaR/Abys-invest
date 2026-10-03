package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const scoreColumns = `id, security_id, as_of, score, signal, justification, inputs_snapshot, model_version, parameter_set_id, created_at`

// UpsertScore persists one score row inside the given transaction. Keyed by
// (security_id, as_of, model_version, parameter_set_id) since migration 015
// (ADR D26): the parameter set is part of the IDENTITY of the result, so two
// sets coexist in the same as_of. Legacy rows (parameter_set_id NULL) keep
// working because Postgres matches NULLs in a unique constraint via the partial
// index uq_scores_legacy_null on the 3-column form.
// On conflict the score, signal,
// justification and snapshot are refreshed (idempotent; CA-6).
func UpsertScore(ctx context.Context, tx pgx.Tx, s *Score) error {
	const upsertCols = `score            = EXCLUDED.score,
    signal           = EXCLUDED.signal,
    justification    = EXCLUDED.justification,
    inputs_snapshot  = EXCLUDED.inputs_snapshot`

	// The conflict target DEPENDS on whether parameter_set_id is NULL, and this is
	// not a stylistic choice — it is a PostgreSQL rule.
	//
	// `uq_scores` is UNIQUE (security_id, as_of, model_version, parameter_set_id),
	// and PostgreSQL treats NULLs as DISTINCT inside a unique constraint: a row
	// with parameter_set_id NULL does NOT conflict with another NULL row through
	// that constraint. That hole is covered by the PARTIAL index
	// `uq_scores_legacy_null ... WHERE parameter_set_id IS NULL`.
	//
	// But `ON CONFLICT (a,b,c,d)` can only infer a NON-partial unique index. For a
	// NULL set it would infer nothing, fire no DO UPDATE, and the insert would die
	// on the partial index instead — i.e. the legacy upsert would stop being
	// idempotent (found by TestUpsertScoreIdempotent).
	//
	// The fix is to reproduce the partial index's PREDICATE in the conflict target,
	// which is exactly what PostgreSQL requires to infer it.
	var err error
	if s.ParameterSetID == nil {
		_, err = tx.Exec(ctx, `
INSERT INTO scores (security_id, as_of, score, signal, justification, inputs_snapshot, model_version, parameter_set_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, NULL)
ON CONFLICT (security_id, as_of, model_version) WHERE parameter_set_id IS NULL DO UPDATE SET
    `+upsertCols,
			s.SecurityID, s.AsOf, s.Score, s.Signal, s.Justification, s.InputsSnapshot, s.ModelVersion)
	} else {
		_, err = tx.Exec(ctx, `
INSERT INTO scores (security_id, as_of, score, signal, justification, inputs_snapshot, model_version, parameter_set_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (security_id, as_of, model_version, parameter_set_id) DO UPDATE SET
    `+upsertCols,
			s.SecurityID, s.AsOf, s.Score, s.Signal, s.Justification, s.InputsSnapshot, s.ModelVersion, s.ParameterSetID)
	}
	if err != nil {
		return fmt.Errorf("storage: upsert score: %w", err)
	}
	return nil
}

// ScoresFilter narrows ListScores. Zero/empty fields are open-ended.
type ScoresFilter struct {
	Ticker string
	From   time.Time
	To     time.Time
}

// ListScores returns persisted scores (optionally filtered by ticker and
// date window), ordered by as_of descending. Scores of tickers without
// historical rows are skipped.
func ListScores(ctx context.Context, q DBTX, f ScoresFilter) ([]Score, error) {
	query := `SELECT ` + scoreColumns + ` FROM scores`
	args := []any{}
	n := 1
	if f.Ticker != "" {
		query += fmt.Sprintf(` WHERE security_id = (SELECT id FROM securities WHERE ticker = $%d)`, n)
		args = append(args, f.Ticker)
		n++
	}
	if !f.From.IsZero() || !f.To.IsZero() {
		if f.Ticker != "" {
			query += ` AND`
		} else {
			query += ` WHERE`
		}
		if !f.From.IsZero() {
			query += fmt.Sprintf(` as_of >= $%d`, n)
			args = append(args, f.From)
			n++
		}
		if !f.From.IsZero() && !f.To.IsZero() {
			query += ` AND`
		}
		if !f.To.IsZero() {
			query += fmt.Sprintf(` as_of <= $%d`, n)
			args = append(args, f.To)
			n++
		}
	}
	query += ` ORDER BY as_of DESC, id DESC`

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list scores: %w", err)
	}
	defer rows.Close()

	var out []Score
	for rows.Next() {
		var s Score
		if err := scanScore(rows, &s); err != nil {
			return nil, fmt.Errorf("storage: list scores scan: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list scores rows: %w", err)
	}
	return out, nil
}

// GetScoreByTicker returns the score of a ticker at exactly as_of, or
// pgx.ErrNoRows when it does not exist.
func GetScoreByTicker(ctx context.Context, q DBTX, ticker string, asOf time.Time) (*Score, error) {
	row := q.QueryRow(ctx, `SELECT `+scoreColumns+` FROM scores
		WHERE security_id = (SELECT id FROM securities WHERE ticker = $1) AND as_of = $2`,
		ticker, asOf)
	s := &Score{}
	if err := scanScore(row, s); err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("storage: get score by ticker %s @%s: %w", ticker, asOf.Format("2006-01-02"), err)
	}
	return s, nil
}

// GetScoreByTickerAndVersion is GetScoreByTicker restricted to ONE model
// revision (B15: el gate por versión).
//
// The endpoint needs this because 2.0.0 and 2.1.0 coexist for the SAME as_of:
// asking for "the score of AAPL" is ambiguous, and picking one silently would
// make the dimensions served depend on which job ran last.
func GetScoreByTickerAndVersion(ctx context.Context, q DBTX, ticker string, asOf time.Time, modelVersion string) (*Score, error) {
	row := q.QueryRow(ctx, `SELECT `+scoreColumns+` FROM scores
		WHERE security_id = (SELECT id FROM securities WHERE ticker = $1)
		  AND as_of = $2 AND model_version = $3`,
		ticker, asOf, modelVersion)
	s := &Score{}
	if err := scanScore(row, s); err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("storage: get score %s @%s v%s: %w", ticker, asOf.Format("2006-01-02"), modelVersion, err)
	}
	return s, nil
}

// GetLatestScoreByVersion is GetLatestScore restricted to ONE model revision.
func GetLatestScoreByVersion(ctx context.Context, q DBTX, ticker, modelVersion string) (*Score, error) {
	row := q.QueryRow(ctx, `SELECT `+scoreColumns+` FROM scores
		WHERE security_id = (SELECT id FROM securities WHERE ticker = $1)
		  AND model_version = $2
		ORDER BY as_of DESC, id DESC LIMIT 1`, ticker, modelVersion)
	s := &Score{}
	if err := scanScore(row, s); err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("storage: get latest score %s v%s: %w", ticker, modelVersion, err)
	}
	return s, nil
}

// GetLatestScore returns the most recent score of a ticker, or pgx.ErrNoRows
// when the ticker or any score row is missing.
func GetLatestScore(ctx context.Context, q DBTX, ticker string) (*Score, error) {
	row := q.QueryRow(ctx, `SELECT `+scoreColumns+` FROM scores
		WHERE security_id = (SELECT id FROM securities WHERE ticker = $1)
		ORDER BY as_of DESC, id DESC LIMIT 1`, ticker)
	s := &Score{}
	if err := scanScore(row, s); err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("storage: get latest score for %s: %w", ticker, err)
	}
	return s, nil
}

func scanScore(row rowScanner, s *Score) error {
	return row.Scan(
		&s.ID, &s.SecurityID, &s.AsOf, &s.Score, &s.Signal, &s.Justification,
		&s.InputsSnapshot, &s.ModelVersion, &s.ParameterSetID, &s.CreatedAt,
	)
}
