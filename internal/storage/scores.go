package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const scoreColumns = `id, security_id, as_of, score, signal, justification, inputs_snapshot, model_version, created_at`

// UpsertScore persists one score row inside the given transaction. Keyed by
// (security_id, as_of, model_version): on conflict the score, signal,
// justification and snapshot are refreshed (idempotent; CA-6).
func UpsertScore(ctx context.Context, tx pgx.Tx, s *Score) error {
	_, err := tx.Exec(ctx, `
INSERT INTO scores (security_id, as_of, score, signal, justification, inputs_snapshot, model_version)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (security_id, as_of, model_version) DO UPDATE SET
    score            = EXCLUDED.score,
    signal           = EXCLUDED.signal,
    justification    = EXCLUDED.justification,
    inputs_snapshot  = EXCLUDED.inputs_snapshot`,
		s.SecurityID, s.AsOf, s.Score, s.Signal, s.Justification, s.InputsSnapshot, s.ModelVersion)
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
		&s.InputsSnapshot, &s.ModelVersion, &s.CreatedAt,
	)
}
