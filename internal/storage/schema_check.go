package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/migrations"
	"github.com/jackc/pgx/v5/pgconn"
)

// SchemaVersion reads the applied migration version from golang-migrate's
// schema_migrations table. exists is false when the table is absent (migrations
// never ran). It issues only a SELECT, so it is compatible with the read-only
// role:api DB identity (ADR 0049), which skips migrations by design.
func (p *Postgres) SchemaVersion(ctx context.Context) (version uint, dirty, exists bool, err error) {
	var v int64
	var d bool
	// probePool, not Pool: this is the readiness path's second acquire, and the
	// one that made a saturated pool look like a broken database (#1042).
	row := p.probePool().QueryRow(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1")
	if scanErr := row.Scan(&v, &d); scanErr != nil {
		// An absent table (42P01 undefined_table) means migrations never ran — a
		// legible "exists=false", not a hard error. Anything else is a real fault.
		var pgErr *pgconn.PgError
		if errors.As(scanErr, &pgErr) && pgErr.Code == "42P01" {
			return 0, false, false, nil
		}
		return 0, false, false, scanErr
	}
	if v < 0 {
		return 0, false, false, fmt.Errorf("schema_migrations.version is negative (%d)", v)
	}
	// #nosec G115 -- a migration version is a small, non-negative counter.
	return uint(v), d, true, nil
}

// checkSchemaCurrent fails fast when the database schema does not match the
// binary's embedded latest migration, with an actionable message — instead of
// booting and erroring at the first query against a missing/old table.
func checkSchemaCurrent(dbVersion uint, exists, dirty bool, latest uint) error {
	switch {
	case !exists:
		return fmt.Errorf("%w: schema_migrations is absent — migrations have not run (apply them: the Helm pre-upgrade migration Job, or `leoflow db migrate` for Lite); schema v%d is required",
			domain.ErrSchemaNotCurrent, latest)
	case dirty:
		return fmt.Errorf("%w: schema is dirty at v%d — a migration did not complete; resolve it before starting",
			domain.ErrSchemaNotCurrent, dbVersion)
	case dbVersion < latest:
		return fmt.Errorf("%w: schema is at v%d but this binary requires v%d — run the pending migrations (the Helm pre-upgrade migration Job, or `leoflow db migrate` for Lite)",
			domain.ErrSchemaNotCurrent, dbVersion, latest)
	default:
		// dbVersion >= latest. An AHEAD schema (dbVersion > latest) is NOT fatal:
		// expand-contract migrations are backward-compatible by construction, so
		// older code is expected to run against a newer schema during a rollout or a
		// `helm rollback`. Hard-failing here would CrashLoopBackOff the old pods on
		// rollback and turn a recoverable bad deploy into an outage. The caller logs
		// a warning for the ahead case; boot proceeds.
		return nil
	}
}

// dirtyAhead reports the one dirty state a RUNNING pod must keep serving
// through: a migration in flight at a version this binary does not need yet.
//
// It is the discriminator that keeps the readiness exemption narrow. Widening it
// to "dirty is fine" would let a pod serve against a half-applied version of its
// own required schema; narrowing it to nothing is what empties the Service
// during an upgrade. Only the strictly-ahead case is both in-flight and
// irrelevant to this binary.
func dirtyAhead(dbVersion uint, exists, dirty bool, latest uint) bool {
	return exists && dirty && dbVersion > latest
}

// checkSchemaReady is the READINESS gate, and it deliberately differs from
// checkSchemaCurrent in exactly one state: dirty above this binary's latest.
//
// Sharing one function between boot and readiness looks like the safe economy —
// two verdicts that cannot drift — but they answer different questions, and for
// `dirty` the answers genuinely differ:
//
//   - Boot asks "may this process START against a half-applied schema?" No: it
//     would run its first query against a table that may be half-migrated.
//   - Readiness asks "should this pod, which was serving correctly a second ago,
//     be pulled out of the Service?" A migration in flight is not a reason to.
//
// Treating run-time dirty as not-ready is an outage, not a conservatism. The
// migrate Job is a `pre-upgrade` Helm hook, so it runs while the OLD pods are
// live and in the Service's endpoints, and golang-migrate commits dirty=true for
// the whole execution of each migration body. Every old replica reads the same
// row, so any migration longer than failureThreshold × periodSeconds (3 × 10s in
// the chart) flips them all NotReady at the same instant and the Service reaches
// zero endpoints. In the "all" role that Service also carries gRPC, so running
// task pods lose the control plane mid-upgrade — the agent_lost cascade (#860).
//
// A failed migration that leaves dirty=true permanently is the same shape: it
// degrades the control plane today, and would remove it from rotation entirely.
//
// Everything else is the boot verdict. An absent or behind schema is not ready:
// no amount of waiting makes this pod able to serve, and it is the #1023 state.
func checkSchemaReady(dbVersion uint, exists, dirty bool, latest uint) error {
	switch {
	case !exists:
		return fmt.Errorf("%w: schema_migrations is absent — migrations have not run (apply them: the Helm pre-upgrade migration Job, or `leoflow db migrate` for Lite); schema v%d is required",
			domain.ErrSchemaNotCurrent, latest)
	case dbVersion < latest:
		// Also covers dirty-and-behind: a migration in flight that has not yet
		// reached what this binary needs cannot serve it either.
		return fmt.Errorf("%w: schema is at v%d but this binary requires v%d — run the pending migrations (the Helm pre-upgrade migration Job, or `leoflow db migrate` for Lite)",
			domain.ErrSchemaNotCurrent, dbVersion, latest)
	case dirtyAhead(dbVersion, exists, dirty, latest):
		// The whole divergence, expressed through the one predicate so the gate
		// and the log line above cannot come to disagree about which state it is.
		return nil
	case dirty:
		// dbVersion == latest and dirty (below and ahead are already handled),
		// so the half-applied migration is the very one this binary requires.
		// Its own schema is not usable.
		return fmt.Errorf("%w: schema is dirty at v%d, the version this binary requires — a migration to it did not complete; resolve it before this pod can serve",
			domain.ErrSchemaNotCurrent, dbVersion)
	default:
		// Clean and at or above latest. See the ahead-schema reasoning in
		// checkSchemaCurrent: expand-contract keeps older code working.
		return nil
	}
}

// SchemaReady is the readiness probe's entry point: it re-asserts on every probe
// what boot asserted once, because a boot check says nothing about a database
// that changes UNDER a running pod.
//
// It exists because a Ping only proves the connection. A database emptied by an
// ephemeral-volume recycle, restored from an older backup, failed over to a
// lagging replica, or repointed by a changed database.url stays pingable while
// serving nothing (#1023). One SELECT against a single-row table, so it is cheap
// enough to run per probe; the caller bounds it with the probe's own deadline.
//
// The verdict is checkSchemaReady, NOT the boot gate — see there for the one
// state where the two must disagree.
func (p *Postgres) SchemaReady(ctx context.Context) error {
	version, dirty, exists, latest, err := p.schemaState(ctx)
	if err != nil {
		return err
	}
	if dirtyAhead(version, exists, dirty, latest) {
		// Worth one line, because it explains why this pod is serving against a
		// schema its own boot gate would have refused. Latched rather than
		// logged per probe: readiness runs every periodSeconds, and a migration
		// can take minutes. The latch resets when the condition clears, so the
		// NEXT upgrade logs again instead of going silent for the process's life.
		if p.dirtyAheadLogged.CompareAndSwap(false, true) {
			slog.InfoContext(ctx, "a migration is in flight above this binary's schema version; staying ready (expand-contract assumed backward-compatible)",
				"db_version", version, "binary_latest", latest)
		}
	} else {
		p.dirtyAheadLogged.Store(false)
	}
	return checkSchemaReady(version, exists, dirty, latest)
}

// CheckSchemaCurrent reads the DB schema version and compares it to the binary's
// embedded latest, failing fast on a mismatch. Called at boot after the pool is
// open, before the server serves.
func (p *Postgres) CheckSchemaCurrent(ctx context.Context) error {
	version, dirty, exists, latest, err := p.schemaState(ctx)
	if err != nil {
		return err
	}
	if exists && !dirty && version > latest {
		// Ahead schema (see checkSchemaCurrent): boot proceeds — expand-contract
		// migrations keep older code working — but surface it, since it means this
		// binary is older than the DB (a rollout in progress or a rollback).
		slog.Warn("database schema is ahead of this binary; proceeding (expand-contract assumed backward-compatible)",
			"db_version", version, "binary_latest", latest)
	}
	return checkSchemaCurrent(version, exists, dirty, latest)
}

// schemaState gathers both sides of the comparison — what the database says and
// what this binary embeds — so the boot gate and the readiness probe cannot drift
// apart on either the query or the required version.
func (p *Postgres) schemaState(ctx context.Context) (version uint, dirty, exists bool, latest uint, err error) {
	latest, err = migrations.Latest()
	if err != nil {
		return 0, false, false, 0, fmt.Errorf("reading embedded schema version: %w", err)
	}
	version, dirty, exists, err = p.SchemaVersion(ctx)
	if err != nil {
		return 0, false, false, 0, fmt.Errorf("reading database schema version: %w", err)
	}
	return version, dirty, exists, latest, nil
}
