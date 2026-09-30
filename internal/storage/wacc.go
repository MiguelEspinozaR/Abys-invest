package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const waccMetricColumns = `id, security_id, as_of, available_at, equity_value, debt_value,
	beta, beta_observed, beta_updated_at, cost_of_equity, cost_of_debt_pretax,
	cost_of_debt_after_tax, tax_rate, risk_free_rate, equity_risk_premium, wacc,
	weight_equity, weight_debt, wacc_source, wacc_confidence, inputs_snapshot,
	model_version, calculation_timestamp, created_at`

// UpsertWaccMetric persists one WACC evaluation inside the given transaction,
// keyed by (security_id, as_of, model_version): values and snapshot are
// refreshed on conflict (idempotent) and created_at is preserved. Same version
// isolation as UpsertGrowthMetric (§26).
//
// The row records the provenance of every parameter (beta_observed,
// wacc_source, wacc_confidence), so a reader can tell a per-company CAPM from a
// configured constant without replaying the calculation.
func UpsertWaccMetric(ctx context.Context, tx pgx.Tx, w *WaccMetric) error {
	_, err := tx.Exec(ctx, `
INSERT INTO wacc_metrics (security_id, as_of, available_at, equity_value, debt_value,
	beta, beta_observed, beta_updated_at, cost_of_equity, cost_of_debt_pretax,
	cost_of_debt_after_tax, tax_rate, risk_free_rate, equity_risk_premium, wacc,
	weight_equity, weight_debt, wacc_source, wacc_confidence, inputs_snapshot, model_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
ON CONFLICT (security_id, as_of, model_version) DO UPDATE SET
	available_at           = EXCLUDED.available_at,
	equity_value           = EXCLUDED.equity_value,
	debt_value             = EXCLUDED.debt_value,
	beta                   = EXCLUDED.beta,
	beta_observed          = EXCLUDED.beta_observed,
	beta_updated_at        = EXCLUDED.beta_updated_at,
	cost_of_equity         = EXCLUDED.cost_of_equity,
	cost_of_debt_pretax    = EXCLUDED.cost_of_debt_pretax,
	cost_of_debt_after_tax = EXCLUDED.cost_of_debt_after_tax,
	tax_rate               = EXCLUDED.tax_rate,
	risk_free_rate         = EXCLUDED.risk_free_rate,
	equity_risk_premium    = EXCLUDED.equity_risk_premium,
	wacc                   = EXCLUDED.wacc,
	weight_equity          = EXCLUDED.weight_equity,
	weight_debt            = EXCLUDED.weight_debt,
	wacc_source            = EXCLUDED.wacc_source,
	wacc_confidence        = EXCLUDED.wacc_confidence,
	inputs_snapshot        = EXCLUDED.inputs_snapshot,
	calculation_timestamp  = now()`,
		w.SecurityID, w.AsOf, w.AvailableAt, w.EquityValue, w.DebtValue,
		w.Beta, w.BetaObserved, w.BetaUpdatedAt, w.CostOfEquity, w.CostOfDebtPreTax,
		w.CostOfDebtAfterTax, w.TaxRate, w.RiskFreeRate, w.EquityRiskPremium, w.Wacc,
		w.WeightEquity, w.WeightDebt, w.Source, w.Confidence, w.InputsSnapshot, w.ModelVersion)
	if err != nil {
		return fmt.Errorf("storage: upsert wacc metric: %w", err)
	}
	return nil
}

// GetLatestWaccMetric returns the row of the maximum as_of of a security, or
// pgx.ErrNoRows when it has no WACC evaluation yet (the /valuation response then
// omits `wacc`).
func GetLatestWaccMetric(ctx context.Context, q DBTX, securityID int64) (*WaccMetric, error) {
	row := q.QueryRow(ctx, `SELECT `+waccMetricColumns+` FROM wacc_metrics
		WHERE security_id = $1
		ORDER BY as_of DESC, model_version DESC, id DESC LIMIT 1`, securityID)
	w := &WaccMetric{}
	if err := scanWaccMetric(row, w); err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("storage: get latest wacc metric for security %d: %w", securityID, err)
	}
	return w, nil
}

// ListWaccMetricsBySecurity returns the history of WACC evaluations of a
// security, ordered as_of DESC (§26).
func ListWaccMetricsBySecurity(ctx context.Context, q DBTX, securityID int64) ([]WaccMetric, error) {
	rows, err := q.Query(ctx, `SELECT `+waccMetricColumns+` FROM wacc_metrics
		WHERE security_id = $1 ORDER BY as_of DESC, model_version DESC, id DESC`, securityID)
	if err != nil {
		return nil, fmt.Errorf("storage: list wacc metrics for security %d: %w", securityID, err)
	}
	defer rows.Close()
	var out []WaccMetric
	for rows.Next() {
		var w WaccMetric
		if err := scanWaccMetric(rows, &w); err != nil {
			return nil, fmt.Errorf("storage: list wacc metrics scan: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list wacc metrics rows: %w", err)
	}
	return out, nil
}

func scanWaccMetric(row rowScanner, w *WaccMetric) error {
	return row.Scan(
		&w.ID, &w.SecurityID, &w.AsOf, &w.AvailableAt, &w.EquityValue, &w.DebtValue,
		&w.Beta, &w.BetaObserved, &w.BetaUpdatedAt, &w.CostOfEquity, &w.CostOfDebtPreTax,
		&w.CostOfDebtAfterTax, &w.TaxRate, &w.RiskFreeRate, &w.EquityRiskPremium, &w.Wacc,
		&w.WeightEquity, &w.WeightDebt, &w.Source, &w.Confidence, &w.InputsSnapshot,
		&w.ModelVersion, &w.CalculationTimestamp, &w.CreatedAt,
	)
}
