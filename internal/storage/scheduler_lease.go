package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
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
	pool *pgxpool.Pool
}

// NewSchedulerLease builds a SchedulerLease over a dedicated (single-connection) pool.
func NewSchedulerLease(pool *pgxpool.Pool) *SchedulerLease {
	return &SchedulerLease{pool: pool}
}

// TryAcquire attempts to take the scheduler advisory lock without blocking.
func (l *SchedulerLease) TryAcquire(ctx context.Context) (bool, error) {
	var acquired bool
	if err := l.pool.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", SchedulerLockID).Scan(&acquired); err != nil {
		return false, fmt.Errorf("acquiring scheduler lock: %w", err)
	}
	return acquired, nil
}

// Release frees the scheduler advisory lock.
func (l *SchedulerLease) Release(ctx context.Context) error {
	if _, err := l.pool.Exec(ctx, "SELECT pg_advisory_unlock($1)", SchedulerLockID); err != nil {
		return fmt.Errorf("releasing scheduler lock: %w", err)
	}
	return nil
}

// HoldsLock reports whether this leader's session still holds the advisory lock.
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
