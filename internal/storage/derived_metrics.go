package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/miky/abys-invest/internal/metricver"
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
//
// M6c-T1 review P1-1 (ADR D13/R-M6c-1): a slug can coexist in TWO revisions
// for the same (security, as_of) after a bump (e.g. rows 762 of 2.0.0 next to
// the current 2.1.0). These readers serve the PRODUCT (metrics endpoint,
// relative stage, compare): they must return, PER SLUG, ONLY THE ROW OF ITS
// CURRENT REVISION as declared by metricver.DefiningVersion — the single
// source of truth. Mixing two revisions of the same slug into one measure is
// non-deterministic and would leak into the 2.2.0 score (§27). A 2.0.0 row is
// history: queryable by SQL, never by these product readers. A slug without a
// row of its current revision is OMITTED — it does not fall back to the old
// revision. The ORDER BY metric ASC keeps the result deterministic.
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
	return keepCurrentRevision(out), nil
}

// GetLatestMetrics returns the full metric set for the most recent as_of of a
// security (empty slice when the security has no metrics). Same current-revision
// guarantee as GetDerivedMetricsBySecurity (M6c-T1 review P1-1): per slug, only
// the row of metricver.DefiningVersion(dm.Metric) survives; stale revisions are
// history readable by SQL only.
func GetLatestMetrics(ctx context.Context, q DBTX, securityID int64) ([]DerivedMetric, error) {
	rows, err := q.Query(ctx, `SELECT `+derivedMetricColumns+` FROM derived_metrics
		WHERE security_id = $1 AND as_of = (SELECT max(as_of) FROM derived_metrics WHERE security_id = $1)
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
	return keepCurrentRevision(out), nil
}

// keepCurrentRevision conserva, por slug, SOLO la fila de la revisión vigente
// según metricver.DefiningVersion (ADR D13). Un slug sin su revisión vigente se
// omite: una fila 2.0.0 (o de cualquier revisión anterior) es historia
// consultable por SQL, no un sustituto del dato vigente.
func keepCurrentRevision(rows []DerivedMetric) []DerivedMetric {
	out := make([]DerivedMetric, 0, len(rows))
	for _, dm := range rows {
		if dm.ModelVersion == metricver.DefiningVersion(dm.Metric) {
			out = append(out, dm)
		}
	}
	return out
}

func scanDerivedMetric(row rowScanner, m *DerivedMetric) error {
	return row.Scan(
		&m.ID, &m.SecurityID, &m.AsOf, &m.Metric, &m.Value,
		&m.InputsSnapshot, &m.ModelVersion, &m.CreatedAt,
	)
}
