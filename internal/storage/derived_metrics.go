package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const derivedMetricColumns = `id, security_id, as_of, metric, value, inputs_snapshot, model_version, created_at`

// UpsertDerivedMetrics persists a batch of materialized metrics inside the
// given transaction. Keyed by (security_id, as_of, metric, model_version): on
// conflict the value and snapshot are refreshed (idempotent).
func UpsertDerivedMetrics(ctx context.Context, tx pgx.Tx, metrics []DerivedMetric) error {
	if len(metrics) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for i := range metrics {
		m := &metrics[i]
		batch.Queue(`
INSERT INTO derived_metrics (security_id, as_of, metric, value, inputs_snapshot, model_version)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (security_id, as_of, metric, model_version) DO UPDATE SET
    value           = EXCLUDED.value,
    inputs_snapshot = EXCLUDED.inputs_snapshot`,
			m.SecurityID, m.AsOf, m.Metric, m.Value, m.InputsSnapshot, m.ModelVersion)
	}

	br := tx.SendBatch(ctx, batch)
	defer br.Close()
	for i := 0; i < batch.Len(); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("storage: upsert derived metrics batch (row %d): %w", i, err)
		}
	}
	return nil
}

// GetDerivedMetricsBySecurity returns metrics for a security at the given
// as_of (all rows when as_of is zero). Ordered by metric name.
func GetDerivedMetricsBySecurity(ctx context.Context, q DBTX, securityID int64, asOf time.Time) ([]DerivedMetric, error) {
	query := `SELECT ` + derivedMetricColumns + ` FROM derived_metrics WHERE security_id = $1`
	args := []any{securityID}
	n := 2
	if !asOf.IsZero() {
		query += fmt.Sprintf(` AND as_of = $%d`, n)
		args = append(args, asOf)
		n++
	}
	query += ` ORDER BY metric ASC`

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: get derived metrics for security %d: %w", securityID, err)
	}
	defer rows.Close()

	var out []DerivedMetric
	for rows.Next() {
		var m DerivedMetric
		if err := scanDerivedMetric(rows, &m); err != nil {
			return nil, fmt.Errorf("storage: get derived metrics scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: get derived metrics rows: %w", err)
	}
	return out, nil
}

// GetLatestMetrics returns the full metric set for the most recent as_of of a
// security (empty slice when the security has no metrics).
func GetLatestMetrics(ctx context.Context, q DBTX, securityID int64) ([]DerivedMetric, error) {
	rows, err := q.Query(ctx, `SELECT `+derivedMetricColumns+` FROM derived_metrics
		WHERE as_of = (SELECT max(as_of) FROM derived_metrics WHERE security_id = $1)
		ORDER BY metric ASC`, securityID)
	if err != nil {
		return nil, fmt.Errorf("storage: get latest metrics for security %d: %w", securityID, err)
	}
	defer rows.Close()

	var out []DerivedMetric
	for rows.Next() {
		var m DerivedMetric
		if err := scanDerivedMetric(rows, &m); err != nil {
			return nil, fmt.Errorf("storage: get latest metrics scan: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: get latest metrics rows: %w", err)
	}
	return out, nil
}

func scanDerivedMetric(row rowScanner, m *DerivedMetric) error {
	return row.Scan(
		&m.ID, &m.SecurityID, &m.AsOf, &m.Metric, &m.Value,
		&m.InputsSnapshot, &m.ModelVersion, &m.CreatedAt,
	)
}
