//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/xcom"
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

// TestHeavyWritesOutliveTheStatementTimeout covers the writes that may run
// longer than an API query should on the main pool: deleting a DAG and
// clearing its history cascade over every run and task instance it has, and
// the XCom janitor deletes every expired index row, also on the main pool
// when the scheduler has no pool of its own. Each is made to outlast a 200 ms
// timeout by waiting on a row lock held for 700 ms, and must still succeed,
// while an ordinary statement on the same pool keeps the timeout.
func TestHeavyWritesOutliveTheStatementTimeout(t *testing.T) {
	url := tuningDatabaseURL(t)
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url, StatementTimeoutMS: 200})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	locker, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(locker.Close)
	repo := storage.NewRepository(pg)

	// seed registers a DAG with one run and returns the run's id.
	seed := func(t *testing.T, dagID string) string {
		t.Helper()
		registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}})
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: "r1", State: domain.DagRunStateSuccess, RunType: "manual", LogicalDate: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create run: %v", err)
		}
		t.Cleanup(func() { _ = repo.DeleteDag(context.Background(), "default", dagID) })
		var runID string
		if err := locker.QueryRow(ctx, `SELECT r.id::text FROM dag_runs r JOIN dags d ON d.id = r.dag_id WHERE d.dag_id = $1`, dagID).Scan(&runID); err != nil {
			t.Fatal(err)
		}
		return runID
	}
	// holdLock locks the rows sql selects FOR UPDATE for 700 ms.
	holdLock := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		tx, err := locker.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM ("+sql+" FOR UPDATE) locked", args...).Scan(&n); err != nil || n == 0 {
			_ = tx.Rollback(ctx)
			t.Fatalf("locking %q: %d rows, err %v", sql, n, err)
		}
		time.AfterFunc(700*time.Millisecond, func() { _ = tx.Rollback(context.Background()) })
	}

	t.Run("DeleteDag", func(t *testing.T) {
		dagID := fmt.Sprintf("stmt_timeout_del_%d", time.Now().UnixNano())
		seed(t, dagID)
		holdLock(t, "SELECT 1 FROM dags WHERE dag_id = $1", dagID)
		if err := repo.DeleteDag(ctx, "default", dagID); err != nil {
			t.Errorf("DeleteDag under a 200 ms statement timeout: %v", err)
		}
	})
	t.Run("ClearDagHistory", func(t *testing.T) {
		dagID := fmt.Sprintf("stmt_timeout_clear_%d", time.Now().UnixNano())
		runID := seed(t, dagID)
		holdLock(t, "SELECT 1 FROM dag_runs WHERE id = $1", runID)
		if err := repo.ClearDagHistory(ctx, "default", dagID); err != nil {
			t.Errorf("ClearDagHistory under a 200 ms statement timeout: %v", err)
		}
	})
	t.Run("PurgeExpiredXCom", func(t *testing.T) {
		dagID := fmt.Sprintf("stmt_timeout_xcom_%d", time.Now().UnixNano())
		runID := seed(t, dagID)
		var tenantID string
		if err := pg.Pool.QueryRow(ctx, `SELECT d.tenant_id::text FROM dags d JOIN dag_runs r ON r.dag_id = d.id WHERE r.id = $1`, runID).Scan(&tenantID); err != nil {
			t.Fatal(err)
		}
		idx := storage.NewXComIndex(pg)
		if err := idx.RecordXCom(ctx, xcom.IndexEntry{
			TenantID: tenantID, RunID: runID, TaskID: "t", Name: "k", RedisKey: "rk", ContentType: "application/json",
			ExpiresAt: time.Now().Add(-time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		holdLock(t, "SELECT 1 FROM xcom_index WHERE dag_run_id = $1", runID)
		if err := idx.PurgeExpired(ctx); err != nil {
			t.Errorf("PurgeExpired under a 200 ms statement timeout: %v", err)
		}
	})
	if got := showStatementTimeout(t, ctx, pg.Pool); got != "200ms" {
		t.Errorf("main pool statement_timeout after the heavy writes = %q, want 200ms", got)
	}
}
