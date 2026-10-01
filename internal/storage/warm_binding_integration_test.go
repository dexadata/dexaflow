//go:build integration

// Package storage_test — the write-side guard on the durable warm-attempt
// binding (ADR 0058 N1d-a1).
//
// BindWarmAttempt stamps warm_worker_id — the warm pod now serving an attempt —
// but ONLY while the (dag_run_id, task_id, try_number) tuple is still active
// (queued/running). The guard lives in the UPDATE's WHERE clause, so a fake-store
// unit test cannot prove it: only real Postgres shows that a settled attempt is
// never bound. Two cases lock the contract — a live attempt is bound, a terminal
// attempt is a no-op — so a later failover reaper never matches a stale pod name
// onto an attempt that already moved on.
package storage_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// openExecPG mirrors openExec but also hands back the *Postgres so a test can read
// warm_worker_id directly — no query exposes it, and reading it is the whole point.
func openExecPG(t *testing.T) (*storage.Postgres, *storage.Repository, *storage.SchedulerStore, *storage.ExecutionStore, context.Context) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	return pg, storage.NewRepository(pg), storage.NewSchedulerStore(pg), storage.NewExecutionStore(pg), ctx
}

// warmWorkerID reads the warm_worker_id column for one attempt directly. A nil
// return means the column is SQL NULL (never bound).
func warmWorkerID(t *testing.T, pg *storage.Postgres, ctx context.Context, runUUID, taskID string, try int) *string {
	t.Helper()
	var got *string
	err := pg.Pool.QueryRow(ctx,
		`SELECT warm_worker_id FROM task_instances
		 WHERE dag_run_id = $1::uuid AND task_id = $2 AND try_number = $3`,
		runUUID, taskID, try).Scan(&got)
	if err != nil {
		t.Fatalf("reading warm_worker_id: %v", err)
	}
	return got
}

// TestBindWarmAttemptStampsRunningTI: the ordinary path — a running attempt is
// bound to the warm pod that acked it, so warm_worker_id holds that pod name.
func TestBindWarmAttemptStampsRunningTI(t *testing.T) {
	pg, repo, sched, exec, ctx := openExecPG(t)
	dagID := fmt.Sprintf("warm_bind_live_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	// Seed our own worker-pod name so the assertion is on a value this test owns,
	// never on whatever another test may have left in a shared table.
	wantPod := fmt.Sprintf("leoflow-warm-%d", time.Now().UnixNano())
	if err := exec.BindWarmAttempt(ctx, runUUID, "load", 1, wantPod); err != nil {
		t.Fatalf("BindWarmAttempt on a running TI = %v, want nil", err)
	}

	got := warmWorkerID(t, pg, ctx, runUUID, "load", 1)
	if got == nil || *got != wantPod {
		t.Fatalf("warm_worker_id = %v, want %q (a running attempt must be bound)", got, wantPod)
	}
}

// TestBindWarmAttemptNoOpOnTerminalTI: once the attempt has settled terminal, the
// guarded UPDATE matches zero rows — the pod name must NOT be stamped onto a
// settled attempt (a benign no-op, not an error), so warm_worker_id stays NULL.
func TestBindWarmAttemptNoOpOnTerminalTI(t *testing.T) {
	pg, repo, sched, exec, ctx := openExecPG(t)
	dagID := fmt.Sprintf("warm_bind_terminal_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	// A reaper settles the attempt terminal before the (late) ack lands.
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateFailed); err != nil {
		t.Fatalf("ApplyTransition to failed: %v", err)
	}

	wantPod := fmt.Sprintf("leoflow-warm-%d", time.Now().UnixNano())
	if err := exec.BindWarmAttempt(ctx, runUUID, "load", 1, wantPod); err != nil {
		t.Fatalf("BindWarmAttempt on a terminal TI = %v, want nil (a guarded no-op is not an error)", err)
	}

	if got := warmWorkerID(t, pg, ctx, runUUID, "load", 1); got != nil {
		t.Fatalf("warm_worker_id = %q, want NULL — a settled attempt must never be bound", *got)
	}
}
