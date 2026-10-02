//go:build integration

package storage_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

func tuningDatabaseURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	if strings.Contains(url, "statement_timeout") {
		t.Skip("DATABASE_URL sets statement_timeout itself; this test is about database.statement_timeout_ms")
	}
	return url
}

func showStatementTimeout(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var v string
	if err := pool.QueryRow(ctx, `SHOW statement_timeout`).Scan(&v); err != nil {
		t.Fatalf("show statement_timeout: %v", err)
	}
	return v
}

// TestStatementTimeoutReachesOnlyTheMainPool checks the sessions Postgres
// actually sees: database.statement_timeout_ms bounds statements on the main
// (API) pool, while the scheduler's dedicated pool and the leader election
// pool, whose session holds the scheduler advisory lock, run without it.
func TestStatementTimeoutReachesOnlyTheMainPool(t *testing.T) {
	url := tuningDatabaseURL(t)
	ctx := context.Background()
	cfg := config.DatabaseSection{URL: url, StatementTimeoutMS: 1234, SchedulerMaxConns: 2}

	pg, err := storage.NewPostgres(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	sched, closeSched, err := pg.ForScheduler(ctx, cfg)
	if err != nil {
		t.Fatalf("scheduler pool: %v", err)
	}
	t.Cleanup(closeSched)
	leader, err := storage.NewLeaderPool(ctx, cfg)
	if err != nil {
		t.Fatalf("leader pool: %v", err)
	}
	t.Cleanup(leader.Close)

	if sched == pg || sched.Pool == pg.Pool {
		t.Fatal("scheduler_max_conns is set but the scheduler shares the main pool")
	}
	if got := sched.Pool.Config().MaxConns; got != 2 {
		t.Errorf("scheduler pool MaxConns = %d, want 2", got)
	}
	if got := showStatementTimeout(t, ctx, pg.Pool); got != "1234ms" {
		t.Errorf("main pool statement_timeout = %q, want 1234ms", got)
	}
	if got := showStatementTimeout(t, ctx, sched.Pool); got != "0" {
		t.Errorf("scheduler pool statement_timeout = %q, want 0", got)
	}
	if got := showStatementTimeout(t, ctx, leader); got != "0" {
		t.Errorf("leader pool statement_timeout = %q, want 0", got)
	}
}

// TestSchedulerSharesTheMainPoolByDefault keeps today's topology when
// database.scheduler_max_conns is unset: the scheduler gets the main handle
// back, and releasing it leaves the main pool open.
func TestSchedulerSharesTheMainPoolByDefault(t *testing.T) {
	url := tuningDatabaseURL(t)
	ctx := context.Background()
	cfg := config.DatabaseSection{URL: url}
	pg, err := storage.NewPostgres(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	sched, closeSched, err := pg.ForScheduler(ctx, cfg)
	if err != nil {
		t.Fatalf("scheduler pool: %v", err)
	}
	if sched != pg {
		t.Fatal("scheduler_max_conns is unset but the scheduler got its own pool")
	}
	closeSched()
	if err := pg.Pool.Ping(ctx); err != nil {
		t.Errorf("releasing the shared scheduler handle closed the main pool: %v", err)
	}
}
