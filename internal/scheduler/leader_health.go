package scheduler

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LeaderHealthReader answers "is a live scheduler leading?" from shared DB state,
// for a process that does NOT run the scheduler itself — the split api role (ADR
// 0049), whose /api/v2/monitor/health must not report a fake-healthy scheduler
// from a nil in-process handle (finding F1). A live scheduler leader holds the
// leadership advisory lock (ADR 0009) on its own session; when its process dies
// the session drops and the lock releases, so lock presence is a real
// cross-process liveness signal with no extra heartbeat table.
//
// It intentionally does NOT filter by pg_backend_pid (that is Leader.HoldsLock's
// job, confirming THIS session's own hold) — here any live holder means a
// scheduler is up. Limitation: it detects a dead/absent leader, not a leader that
// is alive but stalled (lock held, loop wedged); the in-process Heartbeat still
// covers stalls in the all/scheduler role. Tick-accurate cross-process health
// would need a persisted heartbeat (future work).
type LeaderHealthReader struct {
	pool *pgxpool.Pool
	// timeout bounds the pg_locks probe so a slow/stuck DB does not hang the
	// health endpoint; a probe error is treated as "cannot confirm" → unhealthy.
	timeout time.Duration
}

// NewLeaderHealthReader builds a reader over the given (api-role) pool.
func NewLeaderHealthReader(pool *pgxpool.Pool) *LeaderHealthReader {
	return &LeaderHealthReader{pool: pool, timeout: 2 * time.Second}
}

// Heartbeat implements api.Heartbeater. healthy is true iff some live session
// holds the scheduler leadership lock. The timestamp is best-effort "now" when
// healthy (the reader has no cross-process tick time); it is unused by the
// handler when the status is unhealthy.
func (r *LeaderHealthReader) Heartbeat() (healthy bool, last time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	// Same (classid, objid, objsubid=1) encoding of the 64-bit advisory key as
	// Leader.HoldsLock, but WITHOUT the pid filter: any holder = a live leader.
	// pg_locks lists every database on the server while advisory locks are per
	// database, so the database filter keeps another install's leader out (#2049).
	classid := int32(LockID >> 32)
	objid := int32(LockID & 0xFFFFFFFF)
	var held bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM pg_locks
		   WHERE locktype = 'advisory' AND classid = $1 AND objid = $2 AND objsubid = 1
		     AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		 )`, classid, objid).Scan(&held)
	if err != nil {
		return false, time.Now().UTC()
	}
	return held, time.Now().UTC()
}
