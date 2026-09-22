package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const macroSeriesColumns = `id, series_code, date, value, unit, frequency, source, source_id, created_at`

// UpsertMacroSeries persists a batch of macro observations inside the given
// transaction. Keyed by (series_code, date): on conflict the value and
// metadata are refreshed (idempotent).
func UpsertMacroSeries(ctx context.Context, tx pgx.Tx, rows []MacroSeries) error {
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for i := range rows {
		r := &rows[i]
		batch.Queue(`
INSERT INTO macro_series (series_code, date, value, unit, frequency, source, source_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (series_code, date) DO UPDATE SET
    value     = EXCLUDED.value,
    unit      = EXCLUDED.unit,
    frequency = EXCLUDED.frequency,
    source    = EXCLUDED.source,
    source_id = EXCLUDED.source_id`,
			r.SeriesCode, r.Date, r.Value, r.Unit, r.Frequency, r.Source, r.SourceID)
	}

	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("storage: upsert macro series batch (row %d): %w", i, err)
		}
	}
	return nil
}

// GetMacroSeries returns observations of a series within [from, to]; nil/zero
// bounds are open-ended. Ordered by date ascending.
func GetMacroSeries(ctx context.Context, q DBTX, seriesCode string, from, to time.Time) ([]MacroSeries, error) {
	query := `SELECT ` + macroSeriesColumns + ` FROM macro_series WHERE series_code = $1`
	args := []any{seriesCode}
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
		return nil, fmt.Errorf("storage: get macro series %s: %w", seriesCode, err)
	}
	defer rows.Close()

	var out []MacroSeries
	for rows.Next() {
		var r MacroSeries
		if err := scanMacroSeries(rows, &r); err != nil {
			return nil, fmt.Errorf("storage: get macro series scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: get macro series rows: %w", err)
	}
	return out, nil
}

// GetLatestMacroValue returns the most recent observation of a series, or
// pgx.ErrNoRows when none exists.
func GetLatestMacroValue(ctx context.Context, q DBTX, seriesCode string) (*MacroSeries, error) {
	row := q.QueryRow(ctx, `SELECT `+macroSeriesColumns+` FROM macro_series
		WHERE series_code = $1 ORDER BY date DESC, id DESC LIMIT 1`, seriesCode)
	r := &MacroSeries{}
	if err := scanMacroSeries(row, r); err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("storage: get latest macro value %s: %w", seriesCode, err)
	}
	return r, nil
}

func scanMacroSeries(row rowScanner, r *MacroSeries) error {
	return row.Scan(
		&r.ID, &r.SeriesCode, &r.Date, &r.Value, &r.Unit, &r.Frequency,
		&r.Source, &r.SourceID, &r.CreatedAt,
	)
}
