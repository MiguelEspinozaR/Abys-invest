package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// valuationResultColumns is the canonical SELECT list of valuation_results,
// kept in ONE place so the scanner and every query stay in sync.
const valuationResultColumns = `id, security_id, as_of, available_at, fundamentals_as_of,
	price, currency, normalized_growth_rate, growth_source, growth_confidence,
	wacc, wacc_source, wacc_confidence,
	graham_bear, graham_base, graham_bull, dcf_bear, dcf_base, dcf_bull,
	graham_mos, dcf_bear_mos, dcf_base_mos, dcf_bull_mos, target_mos,
	valuation_mean, valuation_stddev, valuation_dispersion, valuation_components,
	valuation_status, valuation_confidence, graham_status, graham_confidence,
	dcf_status, dcf_confidence, reasons, sensitivity, inputs_snapshot,
	model_version, calculation_timestamp, created_at`

// UpsertValuationResult persists one valuation inside the given transaction,
// keyed by (security_id, as_of, model_version) (ADR D4).
//
// On conflict it REFRESHES the values, the snapshot and calculation_timestamp
// and preserves created_at, which makes re-running the job for the same date
// idempotent. It NEVER touches a row of another model_version: the 1.x rows (if
// any were ever written) and the 2.0.0 rows coexist (§26).
//
// The DB CHECK (ck_valuation_results_has_value) is the last guarantee that an
// `unavailable` valuation with nothing to say cannot be stored; the job does not
// even attempt it (plan C1).
func UpsertValuationResult(ctx context.Context, tx pgx.Tx, v *ValuationResult) error {
	_, err := tx.Exec(ctx, `
INSERT INTO valuation_results (security_id, as_of, available_at, fundamentals_as_of,
	price, currency, normalized_growth_rate, growth_source, growth_confidence,
	wacc, wacc_source, wacc_confidence,
	graham_bear, graham_base, graham_bull, dcf_bear, dcf_base, dcf_bull,
	graham_mos, dcf_bear_mos, dcf_base_mos, dcf_bull_mos, target_mos,
	valuation_mean, valuation_stddev, valuation_dispersion, valuation_components,
	valuation_status, valuation_confidence, graham_status, graham_confidence,
	dcf_status, dcf_confidence, reasons, sensitivity, inputs_snapshot, model_version,
	calculation_timestamp)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
	$13, $14, $15, $16, $17, $18,
	$19, $20, $21, $22, $23, $24, $25, $26,
	$27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37,
	now())
ON CONFLICT (security_id, as_of, model_version) DO UPDATE SET
	available_at            = EXCLUDED.available_at,
	fundamentals_as_of      = EXCLUDED.fundamentals_as_of,
	price                   = EXCLUDED.price,
	currency                = EXCLUDED.currency,
	normalized_growth_rate  = EXCLUDED.normalized_growth_rate,
	growth_source           = EXCLUDED.growth_source,
	growth_confidence       = EXCLUDED.growth_confidence,
	wacc                    = EXCLUDED.wacc,
	wacc_source             = EXCLUDED.wacc_source,
	wacc_confidence         = EXCLUDED.wacc_confidence,
	graham_bear             = EXCLUDED.graham_bear,
	graham_base             = EXCLUDED.graham_base,
	graham_bull             = EXCLUDED.graham_bull,
	dcf_bear                = EXCLUDED.dcf_bear,
	dcf_base                = EXCLUDED.dcf_base,
	dcf_bull                = EXCLUDED.dcf_bull,
	graham_mos              = EXCLUDED.graham_mos,
	dcf_bear_mos            = EXCLUDED.dcf_bear_mos,
	dcf_base_mos            = EXCLUDED.dcf_base_mos,
	dcf_bull_mos            = EXCLUDED.dcf_bull_mos,
	target_mos              = EXCLUDED.target_mos,
	valuation_mean          = EXCLUDED.valuation_mean,
	valuation_stddev        = EXCLUDED.valuation_stddev,
	valuation_dispersion    = EXCLUDED.valuation_dispersion,
	valuation_components    = EXCLUDED.valuation_components,
	valuation_status        = EXCLUDED.valuation_status,
	valuation_confidence    = EXCLUDED.valuation_confidence,
	graham_status           = EXCLUDED.graham_status,
	graham_confidence       = EXCLUDED.graham_confidence,
	dcf_status              = EXCLUDED.dcf_status,
	dcf_confidence          = EXCLUDED.dcf_confidence,
	reasons                 = EXCLUDED.reasons,
	sensitivity             = EXCLUDED.sensitivity,
	inputs_snapshot         = EXCLUDED.inputs_snapshot,
	calculation_timestamp   = now()`,
		v.SecurityID, v.AsOf, v.AvailableAt, v.FundamentalsAsOf,
		v.Price, v.Currency, v.NormalizedGrowthRate, v.GrowthSource, v.GrowthConfidence,
		v.Wacc, v.WaccSource, v.WaccConfidence,
		v.GrahamBear, v.GrahamBase, v.GrahamBull, v.DcfBear, v.DcfBase, v.DcfBull,
		v.GrahamMos, v.DcfBearMos, v.DcfBaseMos, v.DcfBullMos, v.TargetMos,
		v.ValuationMean, v.ValuationStddev, v.ValuationDispersion, v.ValuationComponents,
		v.ValuationStatus, v.ValuationConfidence, v.GrahamStatus, v.GrahamConfidence,
		v.DcfStatus, v.DcfConfidence, v.Reasons, v.Sensitivity, v.InputsSnapshot, v.ModelVersion)
	if err != nil {
		return fmt.Errorf("storage: upsert valuation result: %w", err)
	}
	return nil
}

// GetLatestValuationResult returns the most recent row of a security, with the
// tie-break per ADR-D17: highest model_version wins, then most recent as_of,
// then highest id. A valuation of a NEWER version therefore hides an older one
// instead of being averaged with it.
func GetLatestValuationResult(ctx context.Context, q DBTX, securityID int64) (*ValuationResult, error) {
	row := q.QueryRow(ctx, `SELECT `+valuationResultColumns+` FROM valuation_results
		WHERE security_id = $1
		ORDER BY model_version DESC, as_of DESC, id DESC LIMIT 1`, securityID)
	v := &ValuationResult{}
	if err := scanValuationResult(row, v); err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("storage: get latest valuation result for security %d: %w", securityID, err)
	}
	return v, nil
}

// GetValuationResultAsOf returns the row of a security for an EXACT date and
// model version, or pgx.ErrNoRows. It is what the API and the scores job use to
// prove the row was computed with the data available on that date (§4, §26).
func GetValuationResultAsOf(ctx context.Context, q DBTX, securityID int64, asOf time.Time, modelVersion string) (*ValuationResult, error) {
	row := q.QueryRow(ctx, `SELECT `+valuationResultColumns+` FROM valuation_results
		WHERE security_id = $1 AND as_of = $2 AND model_version = $3
		LIMIT 1`, securityID, asOf, modelVersion)
	v := &ValuationResult{}
	if err := scanValuationResult(row, v); err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("storage: get valuation result as of %s (security %d): %w",
			asOf.Format("2006-01-02"), securityID, err)
	}
	return v, nil
}

// ListValuationResultsBySecurity returns the whole valuation history of a
// security, ordered by model_version DESC, then as_of DESC (§26 + ADR-D17).
// Every model version appears: a historical 1.x row is not rewritten by 2.0.0.
func ListValuationResultsBySecurity(ctx context.Context, q DBTX, securityID int64) ([]ValuationResult, error) {
	rows, err := q.Query(ctx, `SELECT `+valuationResultColumns+` FROM valuation_results
		WHERE security_id = $1 ORDER BY model_version DESC, as_of DESC, id DESC`, securityID)
	if err != nil {
		return nil, fmt.Errorf("storage: list valuation results for security %d: %w", securityID, err)
	}
	defer rows.Close()
	var out []ValuationResult
	for rows.Next() {
		var v ValuationResult
		if err := scanValuationResult(rows, &v); err != nil {
			return nil, fmt.Errorf("storage: list valuation results scan: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list valuation results rows: %w", err)
	}
	return out, nil
}

func scanValuationResult(row rowScanner, v *ValuationResult) error {
	return row.Scan(
		&v.ID, &v.SecurityID, &v.AsOf, &v.AvailableAt, &v.FundamentalsAsOf,
		&v.Price, &v.Currency, &v.NormalizedGrowthRate, &v.GrowthSource, &v.GrowthConfidence,
		&v.Wacc, &v.WaccSource, &v.WaccConfidence,
		&v.GrahamBear, &v.GrahamBase, &v.GrahamBull, &v.DcfBear, &v.DcfBase, &v.DcfBull,
		&v.GrahamMos, &v.DcfBearMos, &v.DcfBaseMos, &v.DcfBullMos, &v.TargetMos,
		&v.ValuationMean, &v.ValuationStddev, &v.ValuationDispersion, &v.ValuationComponents,
		&v.ValuationStatus, &v.ValuationConfidence, &v.GrahamStatus, &v.GrahamConfidence,
		&v.DcfStatus, &v.DcfConfidence, &v.Reasons, &v.Sensitivity, &v.InputsSnapshot,
		&v.ModelVersion, &v.CalculationTimestamp, &v.CreatedAt,
	)
}
