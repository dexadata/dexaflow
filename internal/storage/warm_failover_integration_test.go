//go:build integration

// Package storage_test — the read-side of the warm failover reaper (ADR 0058
// N1d-a2).
//
// Two store reads back the failover reaper and the H3 double-run guard, and both
// depend on SQL predicates a fake-store unit test cannot prove:
//
//   - ListWarmBoundRunningTIs must return exactly the `running` TIs with a
//     non-null warm_worker_id — never a terminal bound TI (its worker no longer
//     matters) and never an unbound running TI (a dedicated task).
//   - ListStaleQueuedCandidates must now CARRY warm_worker_id, so the
//     dispatch-lost reaper's H3 defer can see which queued attempt a live warm
//     worker holds.
//
// The shared-DB harness means these rows live alongside whatever else the suite
// seeded, so each test seeds its own uniquely-named DAG and asserts only on rows
// it owns.
package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/storage"
)

// seedQueuedWarmTask brings a fresh DAG up to a single task instance in `queued`
// (the state the dispatch-lost reaper reads), binds it to warmPod, and returns
// the run UUID. Binding is legal in `queued` (the BindWarmAttempt guard allows
// queued/running), which is exactly the H3 case: a warm attempt still queued.
func seedQueuedWarmTask(t *testing.T, repo *storage.Repository, sched *storage.SchedulerStore, exec *storage.ExecutionStore, ctx context.Context, dagID, taskID, warmPod string) string {
	t.Helper()
	tasks := []domain.TaskSpec{{TaskID: taskID, Type: domain.TaskTypePython}}
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
	if err := sched.ApplyTransition(ctx, runUUID, taskID, domain.TaskStateQueued); err != nil {
		t.Fatalf("ApplyTransition to queued: %v", err)
	}
	if err := exec.BindWarmAttempt(ctx, runUUID, taskID, 1, 0, warmPod); err != nil {
		t.Fatalf("BindWarmAttempt on a queued TI: %v", err)
	}
	return runUUID
}

// TestListWarmBoundRunningTIsIntegration: only `running` TIs with a non-null
// warm_worker_id come back. Seed three rows this test owns — a running bound TI,
// a terminal bound TI, and an unbound running TI — and assert only the first is
// returned.
func TestListWarmBoundRunningTIsIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	stamp := time.Now().UnixNano()

	// (1) running + bound — the one that must come back.
	liveDag := fmt.Sprintf("warm_bound_live_%d", stamp)
	liveRun := seedRunningTask(t, repo, sched, ctx, liveDag, "load")
	wantPod := fmt.Sprintf("leoflow-warm-live-%d", stamp)
	if err := exec.BindWarmAttempt(ctx, liveRun, "load", 1, 0, wantPod); err != nil {
		t.Fatalf("BindWarmAttempt (live): %v", err)
	}

	// (2) bound-then-terminal — must NOT come back (state moved off `running`,
	// even though warm_worker_id is still set).
	termDag := fmt.Sprintf("warm_bound_term_%d", stamp)
	termRun := seedRunningTask(t, repo, sched, ctx, termDag, "load")
	if err := exec.BindWarmAttempt(ctx, termRun, "load", 1, 0, fmt.Sprintf("leoflow-warm-term-%d", stamp)); err != nil {
		t.Fatalf("BindWarmAttempt (terminal): %v", err)
	}
	if err := sched.ApplyTransition(ctx, termRun, "load", domain.TaskStateFailed); err != nil {
		t.Fatalf("ApplyTransition to failed: %v", err)
	}

	// (3) running + UNbound (a dedicated task) — must NOT come back.
	dedDag := fmt.Sprintf("warm_unbound_run_%d", stamp)
	dedRun := seedRunningTask(t, repo, sched, ctx, dedDag, "load")

	got, err := sched.ListWarmBoundRunningTIs(ctx)
	if err != nil {
		t.Fatalf("ListWarmBoundRunningTIs: %v", err)
	}

	// Assert ONLY on the rows this test owns (shared DB), keyed by DagRunID.
	var mine *executor.WarmBoundTI
	for i := range got {
		switch got[i].DagRunID {
		case liveRun:
			mine = &got[i]
		case termRun:
			t.Errorf("a terminal bound TI must not be listed: %+v", got[i])
		case dedRun:
			t.Errorf("an unbound running TI must not be listed: %+v", got[i])
		}
	}
	if mine == nil {
		t.Fatalf("the running bound TI (run %s, worker %s) must be listed", liveRun, wantPod)
	}
	if mine.WarmWorkerID != wantPod {
		t.Errorf("WarmWorkerID = %q, want %q", mine.WarmWorkerID, wantPod)
	}
	if mine.TaskID != "load" || mine.TryNumber != 1 {
		t.Errorf("identity = (task %q, try %d), want (load, 1)", mine.TaskID, mine.TryNumber)
	}
}

// TestListStaleQueuedCarriesWarmWorkerIDIntegration: a queued warm attempt shows
// up in ListStaleQueuedCandidates carrying its warm_worker_id, and a queued
// dedicated attempt carries "" — the exact input the H3 defer keys on.
func TestListStaleQueuedCarriesWarmWorkerIDIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	stamp := time.Now().UnixNano()

	warmDag := fmt.Sprintf("stale_warm_%d", stamp)
	warmPod := fmt.Sprintf("leoflow-warm-stale-%d", stamp)
	warmRun := seedQueuedWarmTask(t, repo, sched, exec, ctx, warmDag, "load", warmPod)

	dedDag := fmt.Sprintf("stale_dedicated_%d", stamp)
	dedRun := seedQueuedTaskNoBind(t, repo, sched, ctx, dedDag, "load")

	got, err := sched.ListStaleQueuedCandidates(ctx)
	if err != nil {
		t.Fatalf("ListStaleQueuedCandidates: %v", err)
	}

	var warmSeen, dedSeen bool
	for _, c := range got {
		if c.DagRunID == warmRun {
			warmSeen = true
			if c.WarmWorkerID != warmPod {
				t.Errorf("queued warm candidate WarmWorkerID = %q, want %q", c.WarmWorkerID, warmPod)
			}
		}
		if c.DagRunID == dedRun {
			dedSeen = true
			if c.WarmWorkerID != "" {
				t.Errorf("queued dedicated candidate WarmWorkerID = %q, want \"\" (NULL)", c.WarmWorkerID)
			}
		}
	}
	if !warmSeen {
		t.Fatalf("the queued warm attempt (run %s) must appear among stale-queued candidates", warmRun)
	}
	if !dedSeen {
		t.Fatalf("the queued dedicated attempt (run %s) must appear among stale-queued candidates", dedRun)
	}
}

// seedQueuedTaskNoBind brings a fresh DAG up to a single task instance in
// `queued` with NO warm binding (a dedicated task), returning the run UUID.
func seedQueuedTaskNoBind(t *testing.T, repo *storage.Repository, sched *storage.SchedulerStore, ctx context.Context, dagID, taskID string) string {
	t.Helper()
	tasks := []domain.TaskSpec{{TaskID: taskID, Type: domain.TaskTypePython}}
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
	if err := sched.ApplyTransition(ctx, runUUID, taskID, domain.TaskStateQueued); err != nil {
		t.Fatalf("ApplyTransition to queued: %v", err)
	}
	return runUUID
}
