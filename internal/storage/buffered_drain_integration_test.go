//go:build integration

// Package storage_test — Level B integration for #133 (and the intent of the
// closed #134): the BufferedDispatcher's drain-on-Close must reach the REAL
// storage.SchedulerStore sink and move a `queued` TI to `failed` in real
// Postgres, never leaving it stranded `queued`. A fake-sink unit test can't
// prove the SQL transition; this drives the production wiring end to end.
//
// Deterministic (no flake): Close blocks on wg.Wait until the worker's
// dispatchOne — including the synchronous sink call — completes, so the DB is
// settled by the time Close returns.
//
// See [[tests-must-be-reality-anchored]] memory. Skips without DATABASE_URL.
package storage_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// failingInner is a dispatch.Inner that always errors, so the drain path hits
// the FailureSink (the real SchedulerStore) exactly as a genuine dispatch would.
type failingInner struct{}

func (failingInner) Dispatch(context.Context, string, string, string, domain.TaskSpec) (executor.Disposition, error) {
	return executor.Rejected, errors.New("simulated inner dispatch failure during drain")
}

func TestBuffered_Close_DrainsToRealSink_NoStuckQueuedIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)

	dagID := fmt.Sprintf("buffered_drain_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "load", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual",
		LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	// The scheduler accepted the buffered dispatch and wrote the TI `queued` —
	// the exact state a shutdown that dropped the buffer would strand.
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateQueued); err != nil {
		t.Fatalf("ApplyTransition to queued: %v", err)
	}

	// Production wiring: a BufferedDispatcher fronting a failing inner, with the
	// REAL SchedulerStore as the FailureSink (as setupK8sDispatch wires it).
	bd := dispatch.NewBuffered(failingInner{}, sched,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
		dispatch.BufferConfig{BufferSize: 4, Workers: 1})
	if _, err := bd.Dispatch(ctx, runUUID, dagID, "v1", tasks[0]); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Shutdown drains: inner fails -> reportFailure -> real sink -> the TI must
	// move queued -> failed in real Postgres, not sit stuck (#133).
	if err := bd.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateFailed {
		t.Fatalf("after drain, TI 'load' = %q, want failed — the drain must reach the real sink and never strand it `queued` (#133/#134)", st)
	}
}
