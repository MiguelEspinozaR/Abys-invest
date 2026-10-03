// Package testsupport holds helpers shared by the integration suites (`-tags=integration`).
//
// It exists because of ADR D29 (Az8): beta_history is now the CANONICAL source of
// beta and securities.beta only its cache. Tests that want a CAPM to work must
// therefore seed the OBSERVATION, dated at the value date they are testing — not
// just the cache. Seeding the cache alone silently degrades the WACC to
// configured_fallback, and a test that only checked "beta_observed == true" would
// have kept passing while testing nothing.
package testsupport

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/miky/abys-invest/internal/storage"
)

// SeedBetaAsOf writes a beta observation dated `asOf` in beta_history and refreshes
// the securities.beta cache, i.e. exactly what UpdateSecurityReference does at run
// time, but with a date the test chooses.
//
// The date matters: GetBetaAsOf refuses a row later than the valuation date and a
// row older than BETA_MAX_AGE_DAYS, so seeding at time.Now() would make a test
// dated 2026-09-26 read as `beta_missing`.
func SeedBetaAsOf(t *testing.T, pool *pgxpool.Pool, securityID int64, asOf time.Time, beta float64) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("testsupport: begin seed beta: %v", err)
	}
	obs := storage.BetaObservation{
		SecurityID: securityID,
		AsOf:       asOf.UTC().Truncate(24 * time.Hour),
		Beta:       beta,
		Source:     storage.BetaSourceYahoo,
	}
	if err := storage.UpsertBetaObservation(ctx, tx, &obs); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("testsupport: seed beta observation: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE securities SET beta = $2, beta_updated_at = $3 WHERE id = $1`,
		securityID, beta, obs.AsOf); err != nil {
		tx.Rollback(ctx) //nolint:errcheck
		t.Fatalf("testsupport: refresh beta cache: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("testsupport: commit seed beta: %v", err)
	}
}

// AssertBetaCacheMatchesHistory asserts the D4 invariant: the securities.beta cache
// equals the LAST row of beta_history. A cache that disagrees with the history is
// the failure mode that would make the WACC silently use a beta nobody observed.
func AssertBetaCacheMatchesHistory(t *testing.T, pool *pgxpool.Pool, securityID int64) {
	t.Helper()
	ctx := context.Background()
	obs, err := storage.GetLatestBetaObservation(ctx, pool, securityID)
	if err != nil {
		t.Fatalf("testsupport: leer última observación de beta: %v", err)
	}
	var cached *float64
	if err := pool.QueryRow(ctx, `SELECT beta FROM securities WHERE id = $1`, securityID).Scan(&cached); err != nil {
		t.Fatalf("testsupport: leer caché de beta: %v", err)
	}
	if cached == nil {
		t.Fatalf("testsupport: la caché de beta está vacía y la historia tiene %v", obs.Beta)
	}
	if math.Abs(*cached-obs.Beta) > 1e-9 {
		t.Fatalf("testsupport: caché %v != última fila de la historia %v", *cached, obs.Beta)
	}
}
