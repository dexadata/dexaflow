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

// Schema reasons are the fixed classes of a schema this binary cannot serve
// yet (ADR 0090 decision 2), and the reason label of dexaflow_schema_ready
// (decision 6). They carry no version, so a migration that moves the version
// does not start a new series and restart an alert's wait; the versions are in
// SchemaWait.Reason. Later slices add "database replaced" (decision 3).
const (
	SchemaReasonAbsent     = "schema absent"
	SchemaReasonBehind     = "schema behind"
	SchemaReasonDirty      = "schema dirty"
	SchemaReasonUnreadable = "schema unreadable"
)

// SchemaWait is the boot gate's verdict: whether this binary can build the
// rest of the process on the schema it found, and if not, why.
type SchemaWait struct {
	// Class is one of the SchemaReason constants, empty when the schema is
	// current.
	Class string
	// Reason is the text /readyz and the log carry, with the versions:
	// "schema behind v40, requires v46". It never holds the DSN or a host,
	// since /readyz is unauthenticated.
	Reason string
	// Ahead is a clean schema newer than this binary, accepted with a warning.
	Ahead bool
	// Err is why the schema could not be read, for the log line only.
	Err error
}

// Ready reports whether the schema is one this binary can serve.
func (w SchemaWait) Ready() bool { return w.Class == "" }

// schemaWaitVerdict is the boot gate of ADR 0090 decision 1. An absent or
// behind schema waits instead of exiting, since a migration Job or an operator
// can still bring it current, and a crash loop only hides the pod that could
// say so. A dirty version other than this binary's latest waits too: a
// migration may be in flight. A dirty version equal to the latest is the one
// error: the migration this binary needs is half applied and no wait finishes
// it. A clean schema ahead of the binary is accepted, because expand-contract
// migrations keep older code working through a rollout or a `helm rollback`.
func schemaWaitVerdict(dbVersion uint, exists, dirty bool, latest uint) (SchemaWait, error) {
	switch {
	case !exists:
		return SchemaWait{Class: SchemaReasonAbsent, Reason: SchemaReasonAbsent}, nil
	case dirty && dbVersion == latest:
		return SchemaWait{}, fmt.Errorf("%w: schema is dirty at v%d, the version this binary requires: a migration to it did not complete; resolve it before starting (see the interrupted-migration steps in the upgrade guide)",
			domain.ErrSchemaNotCurrent, dbVersion)
	case dirty:
		return SchemaWait{Class: SchemaReasonDirty, Reason: fmt.Sprintf("schema dirty at v%d", dbVersion)}, nil
	case dbVersion < latest:
		return SchemaWait{Class: SchemaReasonBehind, Reason: fmt.Sprintf("schema behind v%d, requires v%d", dbVersion, latest)}, nil
	default:
		return SchemaWait{Ahead: dbVersion > latest}, nil
	}
}

// readyClass names the reason a running pod's readiness fails, for
// dexaflow_schema_ready, and is empty exactly when checkSchemaReady passes.
func readyClass(dbVersion uint, exists, dirty bool, latest uint) string {
	switch {
	case checkSchemaReady(dbVersion, exists, dirty, latest) == nil:
		return ""
	case !exists:
		return SchemaReasonAbsent
	case dbVersion < latest:
		return SchemaReasonBehind
	default:
		return SchemaReasonDirty
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
// the boot gate, schemaWaitVerdict, in exactly one state: dirty above this
// binary's latest, where boot waits and a running pod stays ready.
//
// Sharing one function between boot and readiness looks like the safe economy —
// two verdicts that cannot drift — but they answer different questions, and for
// `dirty` the answers genuinely differ:
//
//   - Boot asks "may this process START against a half-applied schema?" No: it
//     would run its first query against a table that may be half-migrated, so
//     it waits.
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
		return fmt.Errorf("%w: schema_migrations is absent — migrations have not run (apply them: the Helm pre-upgrade migration Job, or `dexaflow db migrate` for Lite); schema v%d is required",
			domain.ErrSchemaNotCurrent, latest)
	case dbVersion < latest:
		// Also covers dirty-and-behind: a migration in flight that has not yet
		// reached what this binary needs cannot serve it either.
		return fmt.Errorf("%w: schema is at v%d but this binary requires v%d — run the pending migrations (the Helm pre-upgrade migration Job, or `dexaflow db migrate` for Lite)",
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
		// schemaWaitVerdict: expand-contract keeps older code working.
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
		// schema its own boot gate would have waited on. Latched rather than
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
	if p.schemaObserver != nil {
		p.schemaObserver(readyClass(version, exists, dirty, latest))
	}
	return checkSchemaReady(version, exists, dirty, latest)
}

// SchemaWaitState reads the schema and returns the boot gate's verdict
// (schemaWaitVerdict). A read failure is not fatal: it comes back as the
// SchemaReasonUnreadable class with the cause in Err, so the caller keeps
// waiting. The only error is a schema dirty at this binary's latest.
func (p *Postgres) SchemaWaitState(ctx context.Context) (SchemaWait, error) {
	version, dirty, exists, latest, err := p.schemaState(ctx)
	if err != nil {
		return SchemaWait{Class: SchemaReasonUnreadable, Reason: SchemaReasonUnreadable, Err: err}, nil //nolint:nilerr // a read failure is a reason to keep waiting, carried in Err
	}
	w, err := schemaWaitVerdict(version, exists, dirty, latest)
	if w.Ahead {
		// Boot proceeds, but say so: this binary is older than the database
		// (a rollout in progress or a rollback).
		slog.WarnContext(ctx, "database schema is ahead of this binary; proceeding (expand-contract assumed backward-compatible)",
			"db_version", version, "binary_latest", latest)
	}
	return w, err
}

// SetSchemaObserver registers what readiness reports each schema verdict to:
// the class of a not-ready schema, or "" when it is ready. It feeds
// dexaflow_schema_ready for a pod that was already serving. Set it once, at
// boot, before the probes run.
func (p *Postgres) SetSchemaObserver(observe func(class string)) { p.schemaObserver = observe }

// schemaState gathers both sides of the comparison (what the database says and
// what this binary embeds), so the boot gate and the readiness probe cannot
// drift apart on either the query or the required version.
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
