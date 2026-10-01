//go:build integration

package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// TestListRunningTasksIntegration is the query contract for the pod-lost reaper
// (#527): a TI transitioned to `running` appears in ListRunningTasks with a
// non-zero RunningSince (its started_at stamp), and a TI in any other state
// does not — the reaper only considers running TIs.
func TestListRunningTasksIntegration(t *testing.T) {
	repo, sched, _, ctx := openExec(t)
	dagID := fmt.Sprintf("podlost_list_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{
		{TaskID: "run_me", Type: domain.TaskTypePython},
		{TaskID: "still_scheduled", Type: domain.TaskTypePython},
	}
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
	// Only run_me goes to running; still_scheduled stays in its materialized
	// (non-running) state and must be absent from the candidate set.
	if err := sched.ApplyTransition(ctx, runUUID, "run_me", domain.TaskStateRunning); err != nil {
		t.Fatal(err)
	}

	cands, err := sched.ListRunningTasks(ctx)
	if err != nil {
		t.Fatalf("ListRunningTasks: %v", err)
	}
	c := findPodLostCandidate(cands, runUUID, "run_me")
	if c == nil {
		t.Fatalf("a running TI must appear in ListRunningTasks; got %+v", cands)
	}
	if c.RunningSince.IsZero() {
		t.Errorf("RunningSince must be the started_at stamp, got zero")
	}
	if c.DagID != dagID || c.TryNumber != 1 {
		t.Errorf("candidate identity = %+v, want dag=%s try=1", c, dagID)
	}
	if findPodLostCandidate(cands, runUUID, "still_scheduled") != nil {
		t.Errorf("a non-running TI must NOT appear in ListRunningTasks")
	}
}

// TestMarkTaskPodLostIntegration: marking a running TI pod_lost transitions it
// to `failed` and is idempotent — the WHERE state='running' guard no-ops a
// second call, defense in depth against a late terminal report overwriting.
func TestMarkTaskPodLostIntegration(t *testing.T) {
	repo, sched, _, ctx := openExec(t)
	dagID := fmt.Sprintf("podlost_mark_%d", time.Now().UnixNano())
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
	if err := sched.ApplyTransition(ctx, runUUID, "t", domain.TaskStateRunning); err != nil {
		t.Fatal(err)
	}
	cands, _ := sched.ListRunningTasks(ctx)
	c := findPodLostCandidate(cands, runUUID, "t")
	if c == nil {
		t.Fatalf("expected a running candidate")
	}

	applied, err := sched.MarkTaskPodLost(ctx, c.TaskInstanceID)
	if err != nil {
		t.Fatalf("MarkTaskPodLost: %v", err)
	}
	if !applied {
		t.Errorf("first MarkTaskPodLost on a running TI must report applied=true")
	}
	tis, _ := repo.TaskInstancesForRuns(ctx, "default", dagID, []string{"r1"})
	if len(tis) != 1 || tis[0].State != domain.TaskStateFailed {
		t.Errorf("after MarkTaskPodLost, TI state = %+v, want failed", tis)
	}
	// A failed TI is no longer in the running candidate set.
	cands, _ = sched.ListRunningTasks(ctx)
	if findPodLostCandidate(cands, runUUID, "t") != nil {
		t.Errorf("a failed TI must no longer appear in ListRunningTasks")
	}
	// Idempotent: the WHERE state='running' guard now matches 0 rows on the
	// second call — observable via applied=false.
	applied, err = sched.MarkTaskPodLost(ctx, c.TaskInstanceID)
	if err != nil {
		t.Errorf("second MarkTaskPodLost errored: %v", err)
	}
	if applied {
		t.Errorf("second MarkTaskPodLost on a failed TI must report applied=false (0 rows)")
	}
}

// findPodLostCandidate returns the candidate matching (run uuid, task id), or nil.
func findPodLostCandidate(cands []executor.PodLostCandidate, runUUID, taskID string) *executor.PodLostCandidate {
	for i := range cands {
		if cands[i].DagRunID == runUUID && cands[i].TaskID == taskID {
			return &cands[i]
		}
	}
	return nil
}
