//go:build integration

package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
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

	cands, err := sched.ListRunningTasks(ctx, 0)
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
	cands, _ := sched.ListRunningTasks(ctx, 0)
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
	cands, _ = sched.ListRunningTasks(ctx, 0)
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

// TestListRunningTasksExcludesWarmAttemptsIntegration: a warm attempt runs in a
// shared warm pod that carries no per-task labels, so the pod-lost reaper's
// presence check always reads it as absent. The warm-worker-lost reaper owns
// those attempts; the pod-lost candidate set must never contain one, or a warm
// task that outlives the grace period is failed as pod_lost while it runs.
func TestListRunningTasksExcludesWarmAttemptsIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("podlost_warm_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{
		{TaskID: "warm", Type: domain.TaskTypePython},
		{TaskID: "dedicated", Type: domain.TaskTypePython},
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
	for _, task := range []string{"warm", "dedicated"} {
		if err := sched.ApplyTransition(ctx, runUUID, task, domain.TaskStateRunning); err != nil {
			t.Fatal(err)
		}
	}
	if err := exec.BindWarmAttempt(ctx, runUUID, "warm", 1, "warm-worker-0"); err != nil {
		t.Fatalf("BindWarmAttempt: %v", err)
	}

	cands, err := sched.ListRunningTasks(ctx, 0)
	if err != nil {
		t.Fatalf("ListRunningTasks: %v", err)
	}
	if findPodLostCandidate(cands, runUUID, "warm") != nil {
		t.Errorf("a running warm attempt must NOT be a pod-lost candidate")
	}
	if findPodLostCandidate(cands, runUUID, "dedicated") == nil {
		t.Errorf("a running dedicated attempt must still be a pod-lost candidate")
	}
}

// TestListRunningTasksAppliesGraceBeforeLimitIntegration: the grace period is
// applied in SQL, so attempts still inside it never take one of the LIMIT slots
// a past-grace attempt needs.
func TestListRunningTasksAppliesGraceBeforeLimitIntegration(t *testing.T) {
	repo, sched, _, ctx := openExec(t)
	dagID := fmt.Sprintf("podlost_grace_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "fresh", Type: domain.TaskTypePython}}
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
	if err := sched.ApplyTransition(ctx, runUUID, "fresh", domain.TaskStateRunning); err != nil {
		t.Fatal(err)
	}

	cands, err := sched.ListRunningTasks(ctx, time.Hour)
	if err != nil {
		t.Fatalf("ListRunningTasks: %v", err)
	}
	if findPodLostCandidate(cands, runUUID, "fresh") != nil {
		t.Errorf("an attempt running for less than the grace period must not be listed")
	}
	cands, err = sched.ListRunningTasks(ctx, 0)
	if err != nil {
		t.Fatalf("ListRunningTasks: %v", err)
	}
	if findPodLostCandidate(cands, runUUID, "fresh") == nil {
		t.Errorf("with no grace period the running attempt must be listed")
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

// TestListRunningTasksReportsHeartbeatIntegration: a running TI reports whether
// it has heartbeated, which is what lets Lite judge one that never did (its
// agent died before the first heartbeat) without touching the agent-lost query.
func TestListRunningTasksReportsHeartbeatIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("podlost_hb_%d", time.Now().UnixNano())
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
	cands, err := sched.ListRunningTasks(ctx, 0)
	if err != nil {
		t.Fatalf("ListRunningTasks: %v", err)
	}
	c := findPodLostCandidate(cands, runUUID, "t")
	if c == nil {
		t.Fatalf("expected a running candidate")
	}
	if c.Heartbeated {
		t.Fatalf("a TI that never heartbeated must report Heartbeated=false")
	}
	if err := exec.RecordHeartbeat(ctx, auth.AgentIdentity{RunID: runUUID, TaskID: "t", TryNumber: 1}); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}
	cands, _ = sched.ListRunningTasks(ctx, 0)
	if c = findPodLostCandidate(cands, runUUID, "t"); c == nil || !c.Heartbeated {
		t.Fatalf("a TI that heartbeated must report Heartbeated=true: %+v", c)
	}
}
