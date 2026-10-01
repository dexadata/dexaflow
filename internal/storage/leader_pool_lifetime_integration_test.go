//go:build integration

package storage_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestLeaderPoolKeepsItsSessionAndItsLock is the regression test for #1199.
//
// The scheduler's advisory lock is SESSION-scoped: it lives and dies with the
// connection holding it. pgxpool.ParseConfig applies a default MaxConnLifetime
// of one hour when the DSN carries no pool_max_conn_lifetime, and leoflow's DSNs
// do not, so the leader pool's one connection was replaced every hour and
// leadership went with it. A 15.7-hour soak recorded 15 step-downs, one per
// hour, in a single process where nothing was contending.
//
// Two things are asserted, because the fix has an obvious wrong form that is
// worse than the bug. The deadline is computed as time.Now().Add(lifetime), so
// setting it to ZERO expires the connection immediately and every Acquire opens
// a new session: that turns an hourly step-down into one per watch tick. A test
// that only asserted "not one hour" would pass on it.
func TestLeaderPoolKeepsItsSessionAndItsLock(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	if strings.Contains(url, "pool_max_conn_lifetime") {
		t.Skip("DATABASE_URL pins pool_max_conn_lifetime; this test is about the default")
	}
	ctx := context.Background()

	pool, err := storage.NewLeaderPool(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("leader pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// The configured deadline itself. An hour is the pgxpool default that caused
	// the churn; anything at or below a day is short enough for a long-lived
	// scheduler to hit, and zero is the trap above.
	if got := pool.Config().MaxConnLifetime; got <= 24*time.Hour {
		t.Fatalf("leader pool MaxConnLifetime is %s; a session-scoped advisory lock dies when the connection is recycled, so anything a running scheduler can reach means it steps down on a timer (#1199)", got)
	}
	if got := pool.Config().MaxConnIdleTime; got <= 24*time.Hour {
		t.Fatalf("leader pool MaxConnIdleTime is %s; idle recycling drops the session and the lock with it", got)
	}

	// And the behavior, which is what the configuration is for. A pool whose
	// lifetime is zero satisfies "is not one hour" and fails here on the first
	// iteration.
	leader := scheduler.NewLeader(pool)
	acquired, err := leader.TryAcquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !acquired {
		t.Skip("another process holds the scheduler lock against this database")
	}
	t.Cleanup(func() { _ = leader.Release(context.Background()) })

	first := backendPID(t, ctx, pool)
	for i := range 6 {
		time.Sleep(200 * time.Millisecond)
		if pid := backendPID(t, ctx, pool); pid != first {
			t.Fatalf("the leader pool changed backend (%d -> %d) after %d acquires; the session holding the lock is not stable", first, pid, i+1)
		}
		held, err := leader.HoldsLock(ctx)
		if err != nil {
			t.Fatalf("HoldsLock: %v", err)
		}
		if !held {
			t.Fatalf("the leader stopped holding its own lock after %d acquires with nothing contending; this is the step-down the soak recorded once an hour", i+1)
		}
	}
}

func backendPID(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int32 {
	t.Helper()
	var pid int32
	if err := pool.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatalf("pg_backend_pid: %v", err)
	}
	return pid
}
