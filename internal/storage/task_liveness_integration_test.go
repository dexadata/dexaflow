//go:build integration

// Package storage_test — the read-only task-instance liveness predicate.
//
// IsTaskInstanceLive answers whether the attempt (dag_run_id, task_id,
// try_number) is still in an active (non-terminal) state. It is the same
// predicate RecordTaskHeartbeat stamps on, minus the write: a pure read the
// secret path consults so a token whose task instance has finished, failed, been
// superseded by a retry, or been reaped stops resolving secrets (ADR 0055).
//
// A fake-store unit test cannot prove this: the predicate lives in the SELECT's
// WHERE clause, so only real Postgres shows that an active row reads live and a
// terminal / superseded one does not. Every case asserts BOTH sides — a live
// attempt reads live (the availability guard against a false-deny that would
// break a legitimate pipeline) and a dead attempt reads not-live (the security
// guard) — per the mandatory two-sided discipline.
package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// TestIsTaskInstanceLiveRunningAttemptIsLive: the ordinary path — a running,
// matching attempt reads live, so a live task's token always resolves secrets
// (the availability invariant that guards every pipeline).
func TestIsTaskInstanceLiveRunningAttemptIsLive(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("live_running_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	live, err := exec.IsTaskInstanceLive(ctx, auth.AgentIdentity{RunID: runUUID, TaskID: "load", TryNumber: 1})
	if err != nil {
		t.Fatalf("IsTaskInstanceLive on a running attempt: %v", err)
	}
	if !live {
		t.Fatalf("running attempt reads not-live; a live task's token must always resolve secrets")
	}
	// The unknown-attempt companion: a try that never existed is not live.
	if other, err := exec.IsTaskInstanceLive(ctx, auth.AgentIdentity{RunID: runUUID, TaskID: "load", TryNumber: 2}); err != nil || other {
		t.Fatalf("unknown attempt 2 reads live=%v err=%v, want live=false", other, err)
	}
}

// TestIsTaskInstanceLiveTerminalIsNotLive: once the row is settled terminal
// (success or failed), the attempt is not live, so the token stops resolving.
func TestIsTaskInstanceLiveTerminalIsNotLive(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state domain.TaskState
	}{
		{"success", domain.TaskStateSuccess},
		{"failed", domain.TaskStateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, sched, exec, ctx := openExec(t)
			dagID := fmt.Sprintf("live_terminal_%s_%d", tc.name, time.Now().UnixNano())
			runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

			// Live before the transition (two-sided: prove availability first).
			if live, err := exec.IsTaskInstanceLive(ctx, auth.AgentIdentity{RunID: runUUID, TaskID: "load", TryNumber: 1}); err != nil || !live {
				t.Fatalf("pre-terminal live=%v err=%v, want live=true", live, err)
			}
			if err := sched.ApplyTransition(ctx, runUUID, "load", tc.state); err != nil {
				t.Fatalf("ApplyTransition to %s: %v", tc.state, err)
			}
			if live, err := exec.IsTaskInstanceLive(ctx, auth.AgentIdentity{RunID: runUUID, TaskID: "load", TryNumber: 1}); err != nil || live {
				t.Fatalf("terminal (%s) attempt reads live=%v err=%v, want live=false", tc.state, live, err)
			}
		})
	}
}

// TestIsTaskInstanceLiveSupersededByRetry: after a retry bumps try_number, the
// old attempt is not live and the new attempt is live — the token follows the
// work, so a zombie previous-attempt pod stops resolving the instant the retry
// supersedes it, while the new attempt resolves normally.
func TestIsTaskInstanceLiveSupersededByRetry(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("live_superseded_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	// Real retry rail: failed -> up_for_retry -> ResetForRetry (bumps try to 2)
	// -> queued -> running.
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateFailed); err != nil {
		t.Fatalf("ApplyTransition to failed: %v", err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateUpForRetry); err != nil {
		t.Fatalf("ApplyTransition to up_for_retry: %v", err)
	}
	if applied, err := sched.ResetForRetry(ctx, runUUID, "load"); err != nil || !applied {
		t.Fatalf("ResetForRetry applied=%v err=%v, want applied=true", applied, err)
	}
	for _, st := range []domain.TaskState{domain.TaskStateQueued, domain.TaskStateRunning} {
		if err := sched.ApplyTransition(ctx, runUUID, "load", st); err != nil {
			t.Fatalf("ApplyTransition to %s: %v", st, err)
		}
	}

	// Old attempt (try 1) is superseded — not live.
	if live, err := exec.IsTaskInstanceLive(ctx, auth.AgentIdentity{RunID: runUUID, TaskID: "load", TryNumber: 1}); err != nil || live {
		t.Fatalf("superseded attempt 1 reads live=%v err=%v, want live=false", live, err)
	}
	// New attempt (try 2) is live.
	if live, err := exec.IsTaskInstanceLive(ctx, auth.AgentIdentity{RunID: runUUID, TaskID: "load", TryNumber: 2}); err != nil || !live {
		t.Fatalf("current attempt 2 reads live=%v err=%v, want live=true", live, err)
	}
}

// TestIsTaskInstanceLiveClearedOldRunReruns is the D3 hazard, pinned: clearing
// and rerunning an arbitrarily OLD run mints a live new attempt. The predicate
// carries no run-recency / logical_date term, so an old logical date never
// spuriously denies the rerun — the exact unacceptable case the ADR warns
// against. Availability side: the reran attempt reads live. Security side: the
// cleared (superseded) old attempt reads not-live.
func TestIsTaskInstanceLiveClearedOldRunReruns(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("live_cleared_old_%d", time.Now().UnixNano())

	// A run with a logical date a year in the past — the "old run" the hazard is
	// about. Seed it running, fail it, then clear-and-rerun.
	tasks := []domain.TaskSpec{{TaskID: "load", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual",
		LogicalDate: time.Now().UTC().AddDate(-1, 0, 0),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	for _, st := range []domain.TaskState{domain.TaskStateQueued, domain.TaskStateRunning, domain.TaskStateFailed} {
		if err := sched.ApplyTransition(ctx, runUUID, "load", st); err != nil {
			t.Fatalf("ApplyTransition to %s: %v", st, err)
		}
	}

	// Clear the failed task: archives try 1 and bumps the live row to try 2 in
	// state none. RunOnLatestVersion is explicit here because this test is about
	// liveness, not about which version a clear picks — and it is no longer what
	// the UI sends, whose clear dialogs default the field to false.
	if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", []string{"load"}, true, domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: true}); err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}
	// none is not an active state — not live yet.
	if live, err := exec.IsTaskInstanceLive(ctx, auth.AgentIdentity{RunID: runUUID, TaskID: "load", TryNumber: 2}); err != nil || live {
		t.Fatalf("cleared-but-not-yet-dispatched attempt 2 reads live=%v err=%v, want live=false", live, err)
	}
	// Re-dispatch the rerun to running.
	for _, st := range []domain.TaskState{domain.TaskStateQueued, domain.TaskStateRunning} {
		if err := sched.ApplyTransition(ctx, runUUID, "load", st); err != nil {
			t.Fatalf("ApplyTransition to %s: %v", st, err)
		}
	}

	// The rerun of a year-old run is live — no recency denial (the hazard).
	if live, err := exec.IsTaskInstanceLive(ctx, auth.AgentIdentity{RunID: runUUID, TaskID: "load", TryNumber: 2}); err != nil || !live {
		t.Fatalf("rerun of an old run reads live=%v err=%v, want live=true (a recency clause would wrongly deny this)", live, err)
	}
	// The cleared old attempt (try 1) is not live.
	if live, err := exec.IsTaskInstanceLive(ctx, auth.AgentIdentity{RunID: runUUID, TaskID: "load", TryNumber: 1}); err != nil || live {
		t.Fatalf("cleared old attempt 1 reads live=%v err=%v, want live=false", live, err)
	}
}
