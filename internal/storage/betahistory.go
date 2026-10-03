package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// Beta sources (ADR D29).
const (
	BetaSourceYahoo = "yahoo"
	BetaSourceCache = "securities_cache"
)

// Reasons of the beta reading. They travel to wacc_metrics.reasons so a CAPM with
// a stale beta says so instead of pretending the beta is current.
const (
	ReasonBetaMissing = "beta_missing"
	ReasonBetaStale   = "beta_stale"
)

// DefaultBetaMaxAgeDays is BETA_MAX_AGE_DAYS of Az7(a)/ADR D29: how old an
// observation may be and still be an OBSERVATION.
//
// 180 days is chosen because a beta estimated on a 2-year weekly window goes stale
// with the market regime, not with the calendar: past two quarters the number is a
// historical average presented as a current risk. Past this age the honest reading
// is "not observed", which means the configured fallback and confidence low.
const DefaultBetaMaxAgeDays = 180

const betaColumns = `id, security_id, as_of, beta, source, fetched_at`

// GetBetaAsOf returns the beta that was OBSERVABLE at asOf, plus the reason when
// there is none (ADR D29 / E4):
//
//	no row with as_of <= asOf              → nil, "beta_missing"
//	asOf - row.as_of > maxAgeDays           → nil, "beta_stale"  (NOT used)
//	otherwise                               → the value, as_of, "history"
//
// The lookup is `as_of <= $2 ORDER BY as_of DESC LIMIT 1`: never a future row, and
// never "the closest" (a row after the valuation date must not inform it — the same
// look-ahead rule as ADR D14).
//
// A nil observation with an empty reason is impossible by construction: the caller
// gets either a value or a reason, so there is no third way to silently treat a
// missing beta as 1.0.
func GetBetaAsOf(ctx context.Context, q DBTX, securityID int64, asOf time.Time, maxAgeDays int) (*BetaObservation, string, error) {
	if maxAgeDays <= 0 {
		maxAgeDays = DefaultBetaMaxAgeDays
	}
	var obs BetaObservation
	err := q.QueryRow(ctx, `SELECT `+betaColumns+`
		FROM beta_history
		WHERE security_id = $1 AND as_of <= $2
		ORDER BY as_of DESC
		LIMIT 1`, securityID, asOf).Scan(
		&obs.ID, &obs.SecurityID, &obs.AsOf, &obs.Beta, &obs.Source, &obs.FetchedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ReasonBetaMissing, nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("storage: beta as-of security %d: %w", securityID, err)
	}
	age := asOf.Sub(obs.AsOf)
	if age > time.Duration(maxAgeDays)*24*time.Hour {
		return nil, ReasonBetaStale, nil
	}
	return &obs, "", nil
}

// GetLatestBetaObservation returns the most recent row regardless of age. It exists
// for the cache invariant test and for the catalog view, NOT for WACC: WACC must go
// through GetBetaAsOf so the staleness rule cannot be bypassed.
func GetLatestBetaObservation(ctx context.Context, q DBTX, securityID int64) (*BetaObservation, error) {
	var obs BetaObservation
	err := q.QueryRow(ctx, `SELECT `+betaColumns+`
		FROM beta_history WHERE security_id = $1
		ORDER BY as_of DESC LIMIT 1`, securityID).Scan(
		&obs.ID, &obs.SecurityID, &obs.AsOf, &obs.Beta, &obs.Source, &obs.FetchedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pgx.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("storage: latest beta security %d: %w", securityID, err)
	}
	return &obs, nil
}

// UpsertBetaObservation writes one observation (security, as_of). Re-running the
// sector job on the same day updates the row instead of duplicating it, which is
// what makes the collector idempotent.
func UpsertBetaObservation(ctx context.Context, tx pgx.Tx, o *BetaObservation) error {
	return upsertBetaObservationExec(ctx, tx, o)
}

func upsertBetaObservationExec(ctx context.Context, q DBTX, o *BetaObservation) error {
	// Reject non-finite beta (NaN/Inf) before persisting (hardening H2)
	if math.IsNaN(o.Beta) || math.IsInf(o.Beta, 0) {
		return fmt.Errorf("storage: beta %v no es finito (NaN/Inf)", o.Beta)
	}
	if o.Beta <= 0 || o.Beta > 10 {
		return fmt.Errorf("storage: beta %v fuera de (0,10] (M6a-F6)", o.Beta)
	}
	src := o.Source
	if src == "" {
		src = BetaSourceYahoo
	}
	_, err := q.Exec(ctx, `
INSERT INTO beta_history (security_id, as_of, beta, source)
VALUES ($1, $2, $3, $4)
ON CONFLICT (security_id, as_of) DO UPDATE SET
    beta   = EXCLUDED.beta,
    source = EXCLUDED.source,
    fetched_at = now()`, o.SecurityID, o.AsOf, o.Beta, src)
	if err != nil {
		return fmt.Errorf("storage: upsert beta history security %d: %w", o.SecurityID, err)
	}
	return nil
}
