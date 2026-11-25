package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

// SchedulerLockID is the fixed Postgres advisory-lock id gating scheduler
// leadership ("LeoFlow" in hex), per ADR 0009.
const SchedulerLockID int64 = 0x4C656F466C6F77

// SchedulerLease acquires and releases the advisory lock that restricts the
// scheduler loop to a single replica. It is the store side of the
// scheduler.Lease port: the scheduler decides when to campaign and when to
// step down, and never talks SQL itself (#2329, layering rule of #1948). It must run on a dedicated single-connection pool
// so the session holding the lock is stable.
type SchedulerLease struct {
	pool    *pgxpool.Pool
	holder  string
	fencing bool
	term    atomic.Int64
	// reader serves Current. It is never the leader pool: a term read that
	// times out closes its connection, and on the leader pool that is the
	// session holding the lock, so a slow read would end the leadership.
	reader termReader
}

// termReader is the query side of a pool Current reads the lease on.
type termReader interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// SetTermReader gives Current a pool other than the leader pool to read the
// lease term on: the scheduler pool, or the server's own.
func (l *SchedulerLease) SetTermReader(pool *pgxpool.Pool) { l.reader = pool }

// NewSchedulerLease builds a SchedulerLease over a dedicated (single-connection) pool.
func NewSchedulerLease(pool *pgxpool.Pool) *SchedulerLease {
	return &SchedulerLease{pool: pool}
}

// SetHolder names this replica in the lease row (the pod name), for an
// operator reading who leads.
func (l *SchedulerLease) SetHolder(holder string) { l.holder = holder }

// EnableFencing makes each acquisition end the sessions of older terms
// (scheduler.lease_fencing, ADR 0106). Call it before the first TryAcquire,
// together with FencedSchedulerPool for the scheduler's writes.
func (l *SchedulerLease) EnableFencing() { l.fencing = true }

// TryAcquire attempts to take the scheduler advisory lock without blocking.
// Once the lock is taken it increments the lease term (ADR 0106), which is
// this leadership's identity: the leader renews by it, and with fencing on its
// scheduler pool tags every session with it. A term that cannot be taken
// gives the lock back, so a replica never leads without one.
func (l *SchedulerLease) TryAcquire(ctx context.Context) (bool, error) {
	var acquired bool
	if err := l.pool.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", SchedulerLockID).Scan(&acquired); err != nil {
		return false, fmt.Errorf("acquiring scheduler lock: %w", err)
	}
	if !acquired {
		return false, nil
	}
	var term int64
	err := l.pool.QueryRow(ctx,
		`UPDATE scheduler_lease
		    SET term = term + 1, holder = $1, acquired_at = now(), renewed_at = now()
		  WHERE id = 1
		RETURNING term`, l.holder).Scan(&term)
	if err != nil {
		if !l.fencing && leaseMissing(err) {
			// The schema has no lease yet (a server started before migration
			// 060, or after its rollback). With fencing off the advisory lock
			// alone guards leadership, as before 0.6.0, so lead without a term
			// instead of letting no replica lead.
			slog.WarnContext(ctx, "the scheduler lease table is missing; leading on the advisory lock alone", "error", err)
			l.term.Store(0)
			return true, nil
		}
		if _, uerr := l.pool.Exec(ctx, "SELECT pg_advisory_unlock($1)", SchedulerLockID); uerr != nil {
			slog.WarnContext(ctx, "giving back the scheduler lock after a failed term increment", "error", uerr)
		}
		return false, fmt.Errorf("taking a scheduler lease term: %w", err)
	}
	l.term.Store(term)
	if l.fencing {
		l.endOlderTerms(ctx, term)
	}
	return true, nil
}

// endOlderTerms ends every session a leader of an older term tagged
// (application_name dexaflow-leader/<term>), so a statement or a transaction
// of that term still in flight aborts instead of committing after the term
// moved (ADR 0106, point 4). The deposed leader's pool reconnects with its old
// tag, and the fence trigger refuses its writes from then on. Best effort: a
// session of another role cannot be signaled, and the trigger still fences
// its next statement.
func (l *SchedulerLease) endOlderTerms(ctx context.Context, term int64) {
	var ended int
	err := l.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE pg_terminate_backend(pid))
		   FROM pg_stat_activity
		  WHERE datname = current_database()
		    AND usename = current_user
		    AND pid <> pg_backend_pid()
		    AND CASE WHEN application_name ~ '^dexaflow-leader/[0-9]{1,18}$'
		             THEN split_part(application_name, '/', 2)::bigint < $1
		             ELSE false END`, term).Scan(&ended)
	if err != nil {
		slog.WarnContext(ctx, "ending the sessions of older scheduler terms", "term", term, "error", err)
		return
	}
	if ended > 0 {
		slog.InfoContext(ctx, "ended the sessions of older scheduler terms", "term", term, "sessions", ended)
	}
}

// Term is the lease term of this replica's latest leadership, 0 before the
// first one. It stays at the last term after a step-down, so the sessions a
// deposed leader tagged keep a tag the fence refuses.
func (l *SchedulerLease) Term() int64 { return l.term.Load() }

// Release frees the scheduler advisory lock. The term is kept: see Term.
func (l *SchedulerLease) Release(ctx context.Context) error {
	if _, err := l.pool.Exec(ctx, "SELECT pg_advisory_unlock($1)", SchedulerLockID); err != nil {
		return fmt.Errorf("releasing scheduler lock: %w", err)
	}
	return nil
}

// HoldsLock renews the lease by term and reports whether this leader's session
// still holds the advisory lock. A renewal that matches no row means another
// replica took a newer term: it returns scheduler.ErrLeaseSuperseded, and the
// leader steps down at once whatever its own session says (ADR 0106, point 2).
//
// The advisory lock check follows.
// The lock is session-scoped, so if the dedicated connection dropped (network
// blip, idle reap, lifetime recycle) and was replaced, the new session does not
// hold it and another replica may have taken over — this returns false, letting
// the caller step down instead of running on as a stale leader (the split-brain
// guard). A query error (connection down) is surfaced so the caller treats it as
// lost leadership too.
func (l *SchedulerLease) HoldsLock(ctx context.Context) (bool, error) {
	// pg_locks splits the 64-bit advisory key into (classid, objid) with
	// objsubid=1; matching pid against pg_backend_pid() confines the check to
	// this leader's own session (verified against Postgres).
	if err := l.renew(ctx); err != nil {
		return false, err
	}
	classid := int32(SchedulerLockID >> 32)
	objid := int32(SchedulerLockID & 0xFFFFFFFF)
	var held bool
	err := l.pool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM pg_locks
		   WHERE locktype = 'advisory' AND classid = $1 AND objid = $2
		     AND objsubid = 1 AND pid = pg_backend_pid()
		 )`, classid, objid).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("checking scheduler lock: %w", err)
	}
	return held, nil
}

// renew moves the lease's renewed_at forward when this replica still holds
// the current term. A replica that never led has no term to renew.
func (l *SchedulerLease) renew(ctx context.Context) error {
	term := l.term.Load()
	if term == 0 {
		return nil
	}
	tag, err := l.pool.Exec(ctx,
		`UPDATE scheduler_lease SET renewed_at = now() WHERE id = 1 AND term = $1`, term)
	if err != nil {
		return fmt.Errorf("renewing the scheduler lease: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("lease term %d: %w", term, scheduler.ErrLeaseSuperseded)
	}
	return nil
}

// OpenSchedulerLease opens the leader pool (NewLeaderPool) and the lease over
// it. The func it returns closes the pool, which ends the session and gives
// the advisory lock back.
func OpenSchedulerLease(ctx context.Context, cfg config.DatabaseSection) (*SchedulerLease, func(), error) {
	pool, err := NewLeaderPool(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return NewSchedulerLease(pool), pool.Close, nil
}

// Current reads whether this replica's term is still the lease's current one
// (scheduler.LeaseTerms). A replica that never led holds no current term.
func (l *SchedulerLease) Current(ctx context.Context) (bool, error) {
	term := l.term.Load()
	if term == 0 {
		return false, nil
	}
	reader := l.reader
	if reader == nil {
		reader = l.pool
	}
	var current bool
	if err := reader.QueryRow(ctx, `SELECT term = $1 FROM scheduler_lease WHERE id = 1`, term).Scan(&current); err != nil {
		return false, fmt.Errorf("reading the scheduler lease term: %w", err)
	}
	return current, nil
}

// leaseMissing reports whether err says the lease table or its row is not
// there: the schema predates migration 060.
func leaseMissing(err error) bool {
	if errors.Is(err, pgx.ErrNoRows) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42P01" // undefined_table
}

var _ scheduler.LeaseTerms = (*SchedulerLease)(nil)
