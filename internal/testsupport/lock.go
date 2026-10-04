package testsupport

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// IntegrationLockKey is the CONSTANT shared by every integration suite that
// truncates the shared tables of `abys_test`.
//
// It is hashed with hashtext() so it lives in the same int8 space as the
// lock-family used by the suites, and — more importantly — so that every
// package agrees on ONE key by construction instead of by copy-paste. A lock key
// written twice in two files is two locks, which is the same as none.
const IntegrationLockKey = "abys_integration_lock"

// LockIntegrationDB takes the SHARED session advisory lock that serialises the
// database phase of the integration suites, and returns the func that releases
// it.
//
// WHY (this replaced `go test -p 1`, plan M6c debt item (a)):
//
// Every suite TRUNCATEs the same tables of the SAME `abys_test`. Running the
// packages in parallel, one suite's TRUNCATE blocks another's while its own
// transaction waits on the first one's TRUNCATE lock: a classic deadlock that
// `go test` reports as a random hang or a cancellation, and that `-p 1` used to
// paper over. A session advisory lock turns the implicit "don't run two suites
// at once" into an EXPLICIT one, so the suites can go back to parallel
// compilation/execution while the database phase stays serialised.
//
// WHY A DEDICATED CONNECTION (and not `pool.Exec("SELECT pg_advisory_lock(…)")`):
//
// A session advisory lock belongs to the CONNECTION that took it. Running the
// statement through the pool takes it on whatever idle connection the pool
// happens to hand out and immediately returns that connection to the pool: the
// lock is then held by a connection the pool no longer owns, which it may
// health-check and destroy mid-suite (pgxpool drops connections that fail a
// ping). When that happens the lock is silently RELEASED while the suite is
// still running, a second suite walks in, and the two truncate each other —
// exactly the failure this helper exists to remove, only now intermittent.
//
// Acquiring a `*pgxpool.Conn` and HOLDING it pins the session for as long as the
// suite runs, so the lock cannot be lost until the suite explicitly releases it.
//
// The lock is session-scoped, not transaction-scoped: it is NOT released by
// COMMIT/ROLLBACK, and it must be released explicitly (done by the returned
// func, called before os.Exit in each TestMain). Deadlock detection does not
// apply to advisory locks, so a suite that dies holding it would block the next
// one until PostgreSQL drops the session; that only happens if the whole test
// binary is killed.
func LockIntegrationDB(ctx context.Context, pool *pgxpool.Pool) (release func(), err error) {
	if pool == nil {
		return nil, fmt.Errorf("testsupport: pool nil")
	}
	// Dedicated connection: the session that owns the lock stays owned.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("testsupport: adquirir conexión para el lock: %w", err)
	}
	// hashtext() gives every package the same int8 key without hardcoding a
	// magic number that could drift between suites.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1))`, IntegrationLockKey); err != nil {
		conn.Release()
		return nil, fmt.Errorf("testsupport: advisory lock %q: %w", IntegrationLockKey, err)
	}
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		// Best effort: a failure here only delays the next suite until the
		// connection is closed, so it must not panic and mask a test failure.
		if _, err := conn.Exec(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock(hashtext($1))`, IntegrationLockKey); err != nil {
			slog.Warn("testsupport: no se pudo liberar el advisory lock de integración",
				"error", err, "lock", IntegrationLockKey)
		}
		conn.Release()
	}, nil
}
