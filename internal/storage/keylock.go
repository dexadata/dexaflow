package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// KeyMigrationLockID is the Postgres advisory-lock id that excludes a Lite
// server from a key migration ("LiteKey" in hex), distinct from the scheduler's
// leadership lock (ADR 0009, ADR 0065 section 3).
//
// A Lite server, and the `dexaflow lite` process supervising it, hold it SHARED
// on their own sessions for their whole lifetime. `dexaflow lite migrate-key`
// takes it EXCLUSIVE for the whole migration. So a migration cannot start while
// a server runs against the datastore, and a server cannot start while a
// migration runs. Advisory locks are scoped to one database of one cluster:
// every party takes it in the Lite database.
const KeyMigrationLockID int64 = 0x4C6974654B6579

// RowQueryer runs a single-row query. *pgx.Conn and *pgxpool.Pool satisfy it.
type RowQueryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// TryKeyMigrationLockShared takes the key-migration lock in shared mode on the
// caller's session without waiting. It reports false while a migration holds it.
func TryKeyMigrationLockShared(ctx context.Context, q RowQueryer) (bool, error) {
	var ok bool
	if err := q.QueryRow(ctx, "SELECT pg_try_advisory_lock_shared($1)", KeyMigrationLockID).Scan(&ok); err != nil {
		return false, fmt.Errorf("taking the key-migration lock (shared): %w", err)
	}
	return ok, nil
}

// TryKeyMigrationLockExclusive takes the key-migration lock exclusively on the
// caller's session without waiting. It reports false while any server, or
// another migration, holds it.
func TryKeyMigrationLockExclusive(ctx context.Context, q RowQueryer) (bool, error) {
	var ok bool
	if err := q.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", KeyMigrationLockID).Scan(&ok); err != nil {
		return false, fmt.Errorf("taking the key-migration lock: %w", err)
	}
	return ok, nil
}

// HoldsKeyMigrationLock reports whether the caller's own session still holds the
// key-migration lock, in either mode. The lock is session-scoped: when the
// connection that took it dropped and was replaced, the new session does not
// hold it and this returns false; a query error means the session is gone.
// Either way the holder has lost the lock.
func HoldsKeyMigrationLock(ctx context.Context, q RowQueryer) (bool, error) {
	// pg_locks stores a 64-bit advisory key as two oids, high half in classid
	// and low half in objid, with objsubid = 1.
	classid := KeyMigrationLockID >> 32
	objid := KeyMigrationLockID & 0xFFFFFFFF
	var held bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM pg_locks
		   WHERE locktype = 'advisory' AND classid::bigint = $1 AND objid::bigint = $2
		     AND objsubid = 1 AND pid = pg_backend_pid() AND granted
		 )`, classid, objid).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("checking the key-migration lock: %w", err)
	}
	return held, nil
}
