//go:build integration

package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestResetForRetryGuardsSourceState locks the audit-#13 fix: ResetForRetry
// (ResetTaskInstanceToNone) must only reset a TI that is actually parked in
// up_for_retry. The failure it guards against: the planner computes a
// up_for_retry → none transition, but by the time it is applied the TI has been
// re-dispatched and is `running` again (a stale/concurrent decision). Without a
// source-state guard the reset would yank the live TI back to none and bump
// try_number, orphaning its running pod. The sibling RedispatchReschedule is
// guarded to its parked state for the same reason; this brings the retry rail
// into line.
func TestResetForRetryGuardsSourceState(t *testing.T) {
	repo, sched, _, ctx := openExec(t)
	dagID := fmt.Sprintf("reset_guard_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}

	// The TI is live (running) — NOT parked in up_for_retry. A reset here is the
	// stale-decision case and must be a no-op.
	if err := sched.ApplyTransition(ctx, runUUID, "t", domain.TaskStateRunning); err != nil {
		t.Fatal(err)
	}
	applied, err := sched.ResetForRetry(ctx, runUUID, "t")
	if err != nil {
		t.Fatalf("ResetForRetry (guarded no-op) must not error; got %v", err)
	}
	if applied {
		t.Fatalf("ResetForRetry on a running TI must report applied=false (no reset)")
	}
	tis, _ := repo.TaskInstancesForRuns(ctx, "default", dagID, []string{"r1"})
	if len(tis) != 1 || tis[0].State != domain.TaskStateRunning {
		t.Fatalf("a running TI must survive ResetForRetry, got %+v", tis)
	}

	// Now legitimately parked in up_for_retry — the reset must fire.
	if err := sched.ApplyTransition(ctx, runUUID, "t", domain.TaskStateUpForRetry); err != nil {
		t.Fatal(err)
	}
	applied, err = sched.ResetForRetry(ctx, runUUID, "t")
	if err != nil {
		t.Fatalf("ResetForRetry on up_for_retry: %v", err)
	}
	if !applied {
		t.Fatalf("ResetForRetry on an up_for_retry TI must report applied=true")
	}
	tis, _ = repo.TaskInstancesForRuns(ctx, "default", dagID, []string{"r1"})
	if len(tis) != 1 || tis[0].State != domain.TaskStateNone {
		t.Fatalf("an up_for_retry TI must reset to none, got %+v", tis)
	}
}

// TestClearTaskInstancesResetsAnyState guards the OTHER side of the audit-#13
// split: the admin "clear task" action (ClearTaskInstances, onlyFailed=false)
// must still reset a TI from any state — it uses the deliberately-unguarded
// ResetTaskInstanceToNone primitive, NOT the up_for_retry-guarded retry query.
// This locks that adding the retry guard did not silently no-op clear-task on a
// succeeded (or otherwise non-up_for_retry) task.
func TestClearTaskInstancesResetsAnyState(t *testing.T) {
	repo, sched, _, ctx := openExec(t)
	dagID := fmt.Sprintf("clear_anystate_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	// A succeeded task — NOT up_for_retry. The guarded retry query would no-op
	// here; the admin clear primitive must not.
	if err := sched.ApplyTransition(ctx, runUUID, "t", domain.TaskStateSuccess); err != nil {
		t.Fatal(err)
	}

	cleared, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1",
		[]string{"t"}, false /*onlyFailed*/, domain.ClearOptions{})
	if err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("ClearTaskInstances cleared %d, want 1", cleared)
	}
	tis, _ := repo.TaskInstancesForRuns(ctx, "default", dagID, []string{"r1"})
	if len(tis) != 1 || tis[0].State != domain.TaskStateNone {
		t.Fatalf("clear-task must reset a succeeded TI to none, got %+v", tis)
	}
}
