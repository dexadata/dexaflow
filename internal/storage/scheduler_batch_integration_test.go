//go:build integration

package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// retriesPtr is a small helper for building TaskSpecs with explicit retries.
func retriesPtr(n int) *int { return &n }

// runByDag returns the active RunState for a dag from a fresh ActiveRuns read.
func runByDag(t *testing.T, sched *storage.SchedulerStore, runs []scheduler.RunState, dagID string) scheduler.RunState {
	t.Helper()
	for _, r := range runs {
		if r.DagID == dagID {
			return r
		}
	}
	t.Fatalf("no active run for dag %s", dagID)
	return scheduler.RunState{}
}

// TestMaterializeTasksBatchedRowsIntegration pins requirement (b): the batched
// MaterializeTasks (one COPY) writes exactly the rows the per-task INSERT loop
// used to — state 'none', try_number 1, and max_tries derived from each task's
// retries (retries+1, defaulting to 1 when unset).
func TestMaterializeTasksBatchedRowsIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	dagID := fmt.Sprintf("mat_batch_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},                         // no retries -> max_tries 1
		{TaskID: "b", Type: domain.TaskTypeBash, Retries: retriesPtr(2)},   // max_tries 3
		{TaskID: "c", Type: domain.TaskTypePython, Retries: retriesPtr(0)}, // max_tries 1
	}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)

	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}

	run := runByDag(t, sched, mustActive(t, sched, ctx), dagID)
	if len(run.States) != 3 {
		t.Fatalf("expected 3 materialized TIs, got %d", len(run.States))
	}
	wantMaxTries := map[string]int{"a": 1, "b": 3, "c": 1}
	for _, id := range []string{"a", "b", "c"} {
		if run.States[id] != domain.TaskStateNone {
			t.Errorf("task %s state = %s, want none", id, run.States[id])
		}
		if run.Tries[id] != 1 {
			t.Errorf("task %s try_number = %d, want 1", id, run.Tries[id])
		}
		if run.MaxTries[id] != wantMaxTries[id] {
			t.Errorf("task %s max_tries = %d, want %d", id, run.MaxTries[id], wantMaxTries[id])
		}
	}

	// Empty task set is a no-op, not an error (a DAG with no tasks).
	if err := sched.MaterializeTasks(ctx, runUUID, nil); err != nil {
		t.Errorf("MaterializeTasks(nil) should be a no-op, got %v", err)
	}
}

// TestApplyTransitionsMatchesSingleIntegration pins requirement (c): the batched
// ApplyTransitions produces state byte-identical to applying the same target
// per-task with ApplyTransition. Two identical DAGs are driven — one via the
// batch, one via the single-row path — and their resulting states must match.
func TestApplyTransitionsMatchesSingleIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	stamp := time.Now().UnixNano()
	batchDag := fmt.Sprintf("tr_batch_%d", stamp)
	singleDag := fmt.Sprintf("tr_single_%d", stamp)
	tasks := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "b", Type: domain.TaskTypePython},
		{TaskID: "c", Type: domain.TaskTypePython},
		{TaskID: "d", Type: domain.TaskTypePython},
	}
	for _, dagID := range []string{batchDag, singleDag} {
		registerSpec(t, repo, ctx, dagID, tasks)
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: "r1", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create run %s: %v", dagID, err)
		}
	}
	batchUUID := resolveRunUUID(t, sched, ctx, batchDag)
	singleUUID := resolveRunUUID(t, sched, ctx, singleDag)
	if err := sched.MaterializeTasks(ctx, batchUUID, tasks); err != nil {
		t.Fatalf("materialize batch: %v", err)
	}
	if err := sched.MaterializeTasks(ctx, singleUUID, tasks); err != nil {
		t.Fatalf("materialize single: %v", err)
	}

	// Batch path: a,b,c -> upstream_failed in one call; d left none.
	if err := sched.ApplyTransitions(ctx, batchUUID, []string{"a", "b", "c"}, domain.TaskStateUpstreamFailed); err != nil {
		t.Fatalf("ApplyTransitions: %v", err)
	}
	// Single path: same target, one call per task; d left none.
	for _, id := range []string{"a", "b", "c"} {
		if err := sched.ApplyTransition(ctx, singleUUID, id, domain.TaskStateUpstreamFailed); err != nil {
			t.Fatalf("ApplyTransition %s: %v", id, err)
		}
	}

	batchRun := runByDag(t, sched, mustActive(t, sched, ctx), batchDag)
	singleRun := runByDag(t, sched, mustActive(t, sched, ctx), singleDag)
	for _, id := range []string{"a", "b", "c", "d"} {
		if batchRun.States[id] != singleRun.States[id] {
			t.Errorf("task %s: batch state %s != single state %s",
				id, batchRun.States[id], singleRun.States[id])
		}
	}
	if batchRun.States["d"] != domain.TaskStateNone {
		t.Errorf("task d must stay none in the batch path, got %s", batchRun.States["d"])
	}

	// Empty task list is a no-op.
	if err := sched.ApplyTransitions(ctx, batchUUID, nil, domain.TaskStateSkipped); err != nil {
		t.Errorf("ApplyTransitions(nil) should be a no-op, got %v", err)
	}
}

// TestActiveRunsSharedVersionIntegration pins that two runs of the SAME DAG
// version project independent RunStates even though they share one cached spec:
// each run's Tasks carry the same content, and filling one run's retry defaults
// never bleeds into the other (the ActiveRuns per-run Tasks copy).
func TestActiveRunsSharedVersionIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	dagID := fmt.Sprintf("shared_ver_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	for _, rid := range []string{"r1", "r2"} {
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: rid, State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create run %s: %v", rid, err)
		}
	}

	runs := mustActive(t, sched, ctx)
	seen := 0
	for _, r := range runs {
		if r.DagID != dagID {
			continue
		}
		seen++
		if len(r.Tasks) != 1 || r.Tasks[0].TaskID != "a" {
			t.Errorf("run %s has unexpected tasks %+v", r.DisplayRunID, r.Tasks)
		}
	}
	if seen != 2 {
		t.Fatalf("expected 2 active runs for shared version, saw %d", seen)
	}
}
