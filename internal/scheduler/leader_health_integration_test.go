//go:build integration

package scheduler_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestLeaderHealthReaderReflectsLockPresence pins the F1 fix (ADR 0049): in the
// split api role the scheduler runs in ANOTHER process, so the api's
// /monitor/health must derive scheduler liveness from shared state, not report a
// fake "healthy" from a nil handle. A live scheduler holds the leadership
// advisory lock (ADR 0009); when its process dies the session drops and the lock
// releases. The reader (running on the api's pool) reports healthy iff some live
// session holds the lock — so a dead scheduler shows unhealthy, which is exactly
// what the fake-healthy nil handle failed to do.
func TestLeaderHealthReaderReflectsLockPresence(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	cfg := config.DatabaseSection{URL: url}

	pg, err := storage.NewPostgres(ctx, cfg)
	if err != nil {
		t.Fatalf("reader pool: %v", err)
	}
	t.Cleanup(pg.Close)
	reader := scheduler.NewLeaderHealthReader(pg.Pool)

	// No leader holds the lock yet → the api must report the scheduler unhealthy,
	// NOT fake-healthy.
	if healthy, _ := reader.Heartbeat(); healthy {
		t.Fatal("with no scheduler holding the leadership lock, the reader must report unhealthy (the fake-healthy nil-handle bug, F1)")
	}

	// Simulate a live scheduler taking leadership on its own session.
	leaderPool, err := storage.NewLeaderPool(ctx, cfg)
	if err != nil {
		t.Fatalf("leader pool: %v", err)
	}
	t.Cleanup(leaderPool.Close)
	leader := scheduler.NewLeader(leaderPool)
	acquired, err := leader.TryAcquire(ctx)
	if err != nil || !acquired {
		t.Fatalf("acquire leadership lock: acquired=%v err=%v", acquired, err)
	}

	if healthy, _ := reader.Heartbeat(); !healthy {
		t.Error("a live leader holds the lock → the reader must report the scheduler healthy")
	}

	// Leader steps down (or its process dies → session drops); the api must now
	// report unhealthy.
	if err := leader.Release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
	if healthy, _ := reader.Heartbeat(); healthy {
		t.Error("after the leader released the lock, the reader must report the scheduler unhealthy")
	}
}

// TestLeaderHealthReaderIgnoresLockInAnotherDatabase pins #2049: advisory locks
// are scoped to a database, but pg_locks lists the locks of every database on
// the server. Two installs sharing one Postgres server must not see each other's
// scheduler as healthy, so a session holding the leadership lock in another
// database must not count.
func TestLeaderHealthReaderIgnoresLockInAnotherDatabase(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()

	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("reader pool: %v", err)
	}
	t.Cleanup(pg.Close)
	reader := scheduler.NewLeaderHealthReader(pg.Pool)

	// Another install's scheduler: same server, the always-present "postgres"
	// database, holding the same leadership lock on its own session.
	other, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if other.Database == "postgres" {
		t.Skip(`DATABASE_URL must not be the "postgres" database, which this test uses as the other install`)
	}
	other.Database = "postgres"
	conn, err := pgx.ConnectConfig(ctx, other)
	if err != nil {
		t.Fatalf("connect to the other database: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, scheduler.LockID); err != nil {
		t.Fatalf("hold the leadership lock in the other database: %v", err)
	}

	if healthy, _ := reader.Heartbeat(); healthy {
		t.Error("the leadership lock held in another database must not make this install's scheduler healthy")
	}
}
