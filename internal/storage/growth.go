package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const growthMetricColumns = `id, security_id, as_of, available_at, fundamentals_as_of,
	revenue_cagr_3y, revenue_cagr_5y, eps_cagr_3y, eps_cagr_5y, fcf_cagr_3y, fcf_cagr_5y,
	normalized_growth_rate, growth_confidence, growth_source, growth_clamped,
	revenue_discrepancy, inputs_snapshot, model_version, calculation_timestamp, created_at`

// UpsertGrowthMetric persists one Growth Engine result inside the given
// transaction, keyed by (security_id, as_of, model_version): values and snapshot
// are refreshed on conflict (idempotent re-runs of the stage) while
// created_at is preserved.
//
// The model_version is part of the key on purpose (SPEC §26): a future revision
// of the growth formula (2.0.0 in M6b) writes its own row and never overwrites
// the 1.0.0 history.
func UpsertGrowthMetric(ctx context.Context, tx pgx.Tx, g *GrowthMetric) error {
	_, err := tx.Exec(ctx, `
INSERT INTO growth_metrics (security_id, as_of, available_at, fundamentals_as_of,
	revenue_cagr_3y, revenue_cagr_5y, eps_cagr_3y, eps_cagr_5y, fcf_cagr_3y, fcf_cagr_5y,
	normalized_growth_rate, growth_confidence, growth_source, growth_clamped,
	revenue_discrepancy, inputs_snapshot, model_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
ON CONFLICT (security_id, as_of, model_version) DO UPDATE SET
	available_at           = EXCLUDED.available_at,
	fundamentals_as_of     = EXCLUDED.fundamentals_as_of,
	revenue_cagr_3y        = EXCLUDED.revenue_cagr_3y,
	revenue_cagr_5y        = EXCLUDED.revenue_cagr_5y,
	eps_cagr_3y            = EXCLUDED.eps_cagr_3y,
	eps_cagr_5y            = EXCLUDED.eps_cagr_5y,
	fcf_cagr_3y            = EXCLUDED.fcf_cagr_3y,
	fcf_cagr_5y            = EXCLUDED.fcf_cagr_5y,
	normalized_growth_rate = EXCLUDED.normalized_growth_rate,
	growth_confidence      = EXCLUDED.growth_confidence,
	growth_source          = EXCLUDED.growth_source,
	growth_clamped         = EXCLUDED.growth_clamped,
	revenue_discrepancy    = EXCLUDED.revenue_discrepancy,
	inputs_snapshot        = EXCLUDED.inputs_snapshot,
	calculation_timestamp  = now()`,
		g.SecurityID, g.AsOf, g.AvailableAt, g.FundamentalsAsOf,
		g.RevenueCAGR3y, g.RevenueCAGR5y, g.EPSCAGR3y, g.EPSCAGR5y, g.FCFCAGR3y, g.FCFCAGR5y,
		g.NormalizedGrowthRate, g.Confidence, g.Source, g.Clamped,
		g.RevenueDiscrepancy, g.InputsSnapshot, g.ModelVersion)
	if err != nil {
		return fmt.Errorf("storage: upsert growth metric: %w", err)
	}
	return nil
}

// GetLatestGrowthMetric returns the row of the maximum as_of of a security, or
// pgx.ErrNoRows when it has no Growth Engine result yet (the /valuation response
// then omits `growth`).
func GetLatestGrowthMetric(ctx context.Context, q DBTX, securityID int64) (*GrowthMetric, error) {
	row := q.QueryRow(ctx, `SELECT `+growthMetricColumns+` FROM growth_metrics
		WHERE security_id = $1
		ORDER BY as_of DESC, model_version DESC, id DESC LIMIT 1`, securityID)
	g := &GrowthMetric{}
	if err := scanGrowthMetric(row, g); err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("storage: get latest growth metric for security %d: %w", securityID, err)
	}
	return g, nil
}

// ListGrowthMetricsBySecurity returns the history of Growth Engine results of a
// security, ordered as_of DESC (§26). Different model_versions coexist.
func ListGrowthMetricsBySecurity(ctx context.Context, q DBTX, securityID int64) ([]GrowthMetric, error) {
	rows, err := q.Query(ctx, `SELECT `+growthMetricColumns+` FROM growth_metrics
		WHERE security_id = $1 ORDER BY as_of DESC, model_version DESC, id DESC`, securityID)
	if err != nil {
		return nil, fmt.Errorf("storage: list growth metrics for security %d: %w", securityID, err)
	}
	defer rows.Close()
	var out []GrowthMetric
	for rows.Next() {
		var g GrowthMetric
		if err := scanGrowthMetric(rows, &g); err != nil {
			return nil, fmt.Errorf("storage: list growth metrics scan: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list growth metrics rows: %w", err)
	}
	return out, nil
}

func scanGrowthMetric(row rowScanner, g *GrowthMetric) error {
	return row.Scan(
		&g.ID, &g.SecurityID, &g.AsOf, &g.AvailableAt, &g.FundamentalsAsOf,
		&g.RevenueCAGR3y, &g.RevenueCAGR5y, &g.EPSCAGR3y, &g.EPSCAGR5y, &g.FCFCAGR3y, &g.FCFCAGR5y,
		&g.NormalizedGrowthRate, &g.Confidence, &g.Source, &g.Clamped,
		&g.RevenueDiscrepancy, &g.InputsSnapshot, &g.ModelVersion,
		&g.CalculationTimestamp, &g.CreatedAt,
	)
}

// GetGrowthMetricAsOf returns the growth row of the security AT OR BEFORE asOf.
//
// NO LOOK-AHEAD (ADR D14): the quality engine reads the CAGRs from
// growth_metrics, and `latest` would let a score dated 2026-08-20 consume the
// CAGRs computed for a later date. The row is read with the same
// (security_id, as_of) discipline as every other reader of M6c, so a replayed
// score is the score that was written.
func GetGrowthMetricAsOf(ctx context.Context, q DBTX, securityID int64, asOf time.Time) (*GrowthMetric, error) {
	row := q.QueryRow(ctx, `SELECT `+growthMetricColumns+` FROM growth_metrics
		WHERE security_id = $1 AND as_of <= $2
		ORDER BY as_of DESC, model_version DESC, id DESC LIMIT 1`, securityID, asOf)
	g := &GrowthMetric{}
	if err := scanGrowthMetric(row, g); err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("storage: get growth metric as of %s for security %d: %w", asOf.Format("2006-01-02"), securityID, err)
	}
	return g, nil
}
