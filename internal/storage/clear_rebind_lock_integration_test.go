//go:build integration

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// The lock decideMove takes on the run a clear may move (ADR 0086): it holds
// only a finished run, and it is FOR NO KEY UPDATE, so it never blocks the
// key-share lock a live attempt's XCom insert takes on the run through its
// foreign key.

// lockedRunFixture registers v1 and v2 of a DAG with one task and creates
// run r1 in state, returning the DAG and the run as read now.
func lockedRunFixture(t *testing.T, state string) (*Repository, *pgxpool.Pool, queries.Dag, queries.DagRun, context.Context) {
	t.Helper()
	repo, _, ctx := countingRepo(t)
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	dagID := fmt.Sprintf("clear_lock_%d", time.Now().UnixNano())
	register := func(v string) {
		spec := domain.DAGSpec{SchemaVersion: "1.0", DagID: dagID, DagVersion: v, Image: "img:" + v,
			Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}}}
		hash, err := spec.CanonicalHash()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil {
			t.Fatal(err)
		}
	}
	register("v1")
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	register("v2")
	dag, err := repo.resolveDag(ctx, "default", dagID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE dag_runs SET state=$3::dag_run_state WHERE dag_id=$1 AND run_id=$2`, dag.ID, "r1", state); err != nil {
		t.Fatal(err)
	}
	run, err := repo.q.GetDagRun(ctx, queries.GetDagRunParams{TenantID: dag.TenantID, DagID: dag.ID, RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	return repo, pool, dag, run, ctx
}

// tryLock takes mode on the run row from another connection without waiting,
// in its own transaction, and reports the error (nil when the lock was free).
func tryLock(ctx context.Context, pool *pgxpool.Pool, run queries.DagRun, mode string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // test cleanup
	_, err = tx.Exec(ctx, `SELECT 1 FROM dag_runs WHERE id = $1 FOR `+mode+` NOWAIT`, run.ID)
	return err
}

var rebindOpts = domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: true}

// A run read as failed before the clear's transaction and re-opened by
// another clear in between is not locked: the lock re-checks the finished
// state, so the run keeps its version and its row stays free for the orphan
// reaper, which takes the task instances first and the run second.
func TestDecideMoveDoesNotLockARunThatIsNoLongerFinishedIntegration(t *testing.T) {
	repo, pool, dag, stale, ctx := lockedRunFixture(t, "failed")
	if _, err := pool.Exec(ctx, `UPDATE dag_runs SET state='queued' WHERE id=$1`, stale.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // test cleanup
	_, _, moves, err := decideMove(ctx, repo.q.WithTx(tx), dag, stale, rebindOpts)
	if err != nil || moves {
		t.Fatalf("decideMove = moves %v, err %v; want the run kept", moves, err)
	}
	if err := tryLock(ctx, pool, stale, "UPDATE"); err != nil {
		t.Errorf("the re-opened run is locked by the clear: %v", err)
	}
}

// A finished run is locked against a second clear, but not against the
// key-share lock of an XCom insert of a live attempt on the same run.
func TestDecideMoveLockLetsKeyShareThroughIntegration(t *testing.T) {
	repo, pool, dag, run, ctx := lockedRunFixture(t, "failed")
	tx, err := repo.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // test cleanup
	_, _, moves, err := decideMove(ctx, repo.q.WithTx(tx), dag, run, rebindOpts)
	if err != nil || !moves {
		t.Fatalf("decideMove = moves %v, err %v; want the finished run moved", moves, err)
	}
	if err := tryLock(ctx, pool, run, "NO KEY UPDATE"); err == nil {
		t.Error("a second clear could lock the run the first one holds")
	}
	if err := tryLock(ctx, pool, run, "KEY SHARE"); err != nil {
		t.Errorf("the clear blocks a key-share lock on the run: %v", err)
	}
}
