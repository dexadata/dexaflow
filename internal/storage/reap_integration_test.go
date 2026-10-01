//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestReapRunOrphanedRunIntegration pins the end-to-end contract of the orphan
// reaper against a real Postgres: a `running` dag_run whose TIs all settled
// (success/failed/skipped) but whose run-level finalizer never fired is the
// exact shape #120 targets (server died between the last TI report and the
// next scheduler tick). The run is transitioned to `failed` with the
// orphaned note, the never-started downstream `load` is left alone (it was
// not "orphaned" — there was nothing to fail), and a second reap is a no-op
// (idempotent). This is the behavior the dashboard counter depends on.
//
// The companion case — a TI stuck in `running` because the agent died — is
// intentionally OUT OF SCOPE for this reaper to avoid killing legitimately
// slow tasks; it is covered separately by issue #128 (TI heartbeat reaper).
func TestReapRunOrphanedRunIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	dagID := fmt.Sprintf("reap_run_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{
		{TaskID: "extract", Type: domain.TaskTypePython},
		{TaskID: "load", Type: domain.TaskTypePython, DependsOn: []string{"extract"}},
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
	// Extract finished successfully but the scheduler crashed before the
	// finalizer for the run could fire. `load` never even started (its upstream
	// just settled). All TIs are terminal/none; only the dag_run is stuck.
	if err := sched.ApplyTransition(ctx, runUUID, "extract", domain.TaskStateSuccess); err != nil {
		t.Fatal(err)
	}

	cands, err := sched.ListReapCandidates(ctx)
	if err != nil {
		t.Fatalf("ListReapCandidates: %v", err)
	}
	if findCandidate(cands, runUUID) == nil {
		t.Fatalf("a run with all TIs settled but state=running must be a candidate; got %+v", cands)
	}

	if err := sched.ReapRun(ctx, runUUID); err != nil {
		t.Fatalf("ReapRun: %v", err)
	}

	// After reap, the run is gone from the active set: the scheduler view of
	// "active runs" (queued + running) must not include it any longer, which is
	// what makes the dashboard's running_dag_count drop.
	for _, r := range mustActive(t, sched, ctx) {
		if r.RunID == runUUID {
			t.Errorf("reaped run still appears in ActiveRuns: %+v", r)
		}
	}

	// Settled TIs are not rewritten by the reap: `extract` stays success, and
	// `load` stays `none` (a not-yet-active task was not "orphaned" — there was
	// nothing to fail). The reaper's TI-side update only targets active states.
	tis, _ := repo.TaskInstancesForRuns(ctx, "default", dagID, []string{"r1"})
	states := map[string]domain.TaskState{}
	for _, ti := range tis {
		states[ti.TaskID] = ti.State
	}
	if states["extract"] != domain.TaskStateSuccess {
		t.Errorf("already-settled extract should stay success, got %q", states["extract"])
	}
	if states["load"] != domain.TaskStateNone {
		t.Errorf("never-active load should stay none, got %q", states["load"])
	}

	// A second reap is a no-op: the WHERE guard skips the now-failed run, so the
	// repository never overwrites state set by a competing finalizer (defense in
	// depth — the run is no longer `running` after the first reap, but the
	// idempotency is part of the contract).
	if err := sched.ReapRun(ctx, runUUID); err != nil {
		t.Errorf("second ReapRun should be a no-op, got %v", err)
	}
}

// TestListReapCandidatesIgnoresActiveTaskInstancesIntegration is the critical
// "do no harm" pin: a legitimately-active task instance (image pull, slow K8s
// startup, long-running job) MUST NOT cause its run to appear as an orphan
// candidate. The reaper only considers runs where every task instance is in a
// terminal or never-started state — if anything is scheduled/queued/running,
// execution is in flight and the scheduler/agent owns the next transition.
func TestListReapCandidatesIgnoresActiveTaskInstancesIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)

	// One DAG per scenario — easier to reason about than two runs in one DAG.
	liveDag := fmt.Sprintf("reap_live_%d", time.Now().UnixNano())
	stuckDag := fmt.Sprintf("reap_stuck_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, liveDag, tasks)
	registerSpec(t, repo, ctx, stuckDag, tasks)
	for _, id := range []string{liveDag, stuckDag} {
		if _, err := repo.CreateDagRun(ctx, "default", id, domain.DagRun{
			RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	liveUUID := resolveRunUUID(t, sched, ctx, liveDag)
	stuckUUID := resolveRunUUID(t, sched, ctx, stuckDag)
	if err := sched.MaterializeTasks(ctx, liveUUID, tasks); err != nil {
		t.Fatalf("Materialize live: %v", err)
	}
	if err := sched.MaterializeTasks(ctx, stuckUUID, tasks); err != nil {
		t.Fatalf("Materialize stuck: %v", err)
	}
	// "live" — TI is `running` right now (slow K8s pull, mid-execution).
	if err := sched.ApplyTransition(ctx, liveUUID, "t", domain.TaskStateRunning); err != nil {
		t.Fatal(err)
	}
	// "stuck" — TI finished successfully, but FinalizeRun missed the run.
	if err := sched.ApplyTransition(ctx, stuckUUID, "t", domain.TaskStateSuccess); err != nil {
		t.Fatal(err)
	}

	cands, err := sched.ListReapCandidates(ctx)
	if err != nil {
		t.Fatalf("ListReapCandidates: %v", err)
	}
	if findCandidate(cands, liveUUID) != nil {
		t.Errorf("live run with a `running` TI must NOT appear as a candidate")
	}
	if findCandidate(cands, stuckUUID) == nil {
		t.Errorf("stuck run (all TIs terminal but state=running) must appear")
	}
}

// TestReapRunIgnoresTerminalRunIntegration pins the safety guard: a run already
// in a terminal state (success or failed) is never overwritten. The reap is a
// safety net for stuck `running` runs, never a takeover.
func TestReapRunIgnoresTerminalRunIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	dagID := fmt.Sprintf("reap_terminal_%d", time.Now().UnixNano())
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
	if err := sched.ApplyTransition(ctx, runUUID, "t", domain.TaskStateSuccess); err != nil {
		t.Fatal(err)
	}
	// Move the run to its natural terminal state.
	if err := sched.SetRunState(ctx, runUUID, domain.DagRunStateSuccess); err != nil {
		t.Fatal(err)
	}

	// Try to reap: the WHERE state='running' guard short-circuits, so nothing
	// changes. The note field stays untouched and the state stays success.
	if err := sched.ReapRun(ctx, runUUID); err != nil {
		t.Fatalf("ReapRun on terminal: %v", err)
	}
	runs, err := repo.LatestRunsForDags(ctx, "default", []string{dagID}, 1)
	if err != nil {
		t.Fatalf("LatestRunsForDags: %v", err)
	}
	got := runs[dagID]
	if len(got) != 1 || got[0].State != domain.DagRunStateSuccess {
		t.Errorf("reap must not overwrite a terminal run; got %+v", runs)
	}
}

// resolveRunUUID returns the just-created run's internal UUID via ActiveRuns.
// The UUID is the key MaterializeTasks, ApplyTransition, and ReapRun all use.
func resolveRunUUID(t *testing.T, sched *storage.SchedulerStore, ctx context.Context, dagID string) string {
	t.Helper()
	runs, err := sched.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	for _, r := range runs {
		if r.DagID == dagID {
			return r.RunID
		}
	}
	t.Fatalf("could not resolve run UUID for dag %s", dagID)
	return ""
}

// findCandidate returns the candidate matching runUUID or nil.
func findCandidate(cands []executor.ReapCandidate, runUUID string) *executor.ReapCandidate {
	for i := range cands {
		if cands[i].RunID == runUUID {
			return &cands[i]
		}
	}
	return nil
}

// mustActive returns the current active-runs snapshot or fails the test.
func mustActive(t *testing.T, sched *storage.SchedulerStore, ctx context.Context) []scheduler.RunState {
	t.Helper()
	runs, err := sched.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	return runs
}
