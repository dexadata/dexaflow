//go:build integration

package storage_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Two clears with run_on_latest_version of the same finished run, both naming
// a task the current version dropped, with only_failed=false (#2046). Both
// read the run before either holds its lock; the first moves the run to the
// current version and marks the dropped task removed, the second waits on the
// lock and then finds the run no longer finished. The second clear must apply
// on the version the run now executes, so the dropped task stays removed
// (ADR 0086 D2), instead of the version it read before its transaction.
func TestConcurrentRebindClearsKeepTheDroppedTaskRemovedIntegration(t *testing.T) {
	f := seedDriftedRun(t, "rebind_race", []string{"a", "b"}, pythonTasks([]string{"a"}))
	ctx := t.Context()
	blocker, err := f.pg.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }() //nolint:errcheck // test cleanup
	var blockerPID int
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(ctx, `SELECT 1 FROM dag_runs WHERE id = $1::uuid FOR UPDATE`, f.runUUID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", []string{"a", "b"}, false, rebind)
		}()
	}
	// Release the run only once both clears wait on its lock: each has then
	// read the run as failed before its transaction.
	waitForLockWaiters(ctx, t, f.pg.Pool, blockerPID, 2)
	if err := blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatalf("clear: %v", e)
		}
	}
	if got := f.states(t); got != "a=none b=removed" {
		t.Errorf("after two concurrent re-binding clears: %q, want a=none b=removed", got)
	}
	if !f.onV2(t) {
		t.Error("the run is not on the current version after the clears")
	}
}

// lockWaiters counts the backends that wait, directly or behind another
// waiter, on a lock the backend blocker holds.
const lockWaiters = `WITH RECURSIVE waiting(pid) AS (
	SELECT pid FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid))
	UNION
	SELECT a.pid FROM pg_stat_activity a JOIN waiting w ON w.pid = ANY(pg_blocking_pids(a.pid))
) SELECT count(*) FROM waiting`

// waitForLockWaiters returns once want backends wait on the locks of the
// backend blocker, and fails the test if they do not within ten seconds.
func waitForLockWaiters(ctx context.Context, t *testing.T, pool *pgxpool.Pool, blocker, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := pool.QueryRow(ctx, lockWaiters, blocker).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d clears wait on the run lock after 10s", n, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
