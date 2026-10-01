//go:build integration

// Reschedule-mode sensor re-dispatch, end to end at the SQL layer (#380). The
// unit tests cover each layer with fakes; this proves the real DB round-trip the
// fakes cannot: the agent's Reschedule write parks the TI with reschedule_at, the
// scheduler loads it back into RunState, the planner releases it, and the
// re-dispatch returns it to none WITHOUT consuming a retry attempt.
//
// See [[tests-must-be-reality-anchored]] memory. Skips without DATABASE_URL.

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

func TestRescheduleRedispatchPreservesTryNumberIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)

	dagID := fmt.Sprintf("reschedule_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "probe", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	// Bring the probe to running — a reschedule-mode sensor pod executing.
	for _, st := range []domain.TaskState{domain.TaskStateScheduled, domain.TaskStateQueued, domain.TaskStateRunning} {
		if err := sched.ApplyTransition(ctx, runUUID, "probe", st); err != nil {
			t.Fatalf("ApplyTransition %s: %v", st, err)
		}
	}
	tryBefore := rescheduleTryNumber(t, sched, ctx, runUUID, "probe")

	// Poke-not-ready: the agent reports up_for_reschedule with a reschedule time
	// already in the past (so it is immediately ready to re-dispatch).
	id := auth.AgentIdentity{RunID: runUUID, TaskID: "probe", TryNumber: tryBefore}
	if err := exec.Reschedule(ctx, id, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}
	if got := taskInstanceState(t, sched, ctx, runUUID, "probe"); got != domain.TaskStateUpForReschedule {
		t.Fatalf("after Reschedule, state = %q, want up_for_reschedule", got)
	}

	// The scheduler loads reschedule_at and the planner releases the ready task to
	// none — the real DB round-trip driving the re-dispatch decision.
	rs := rescheduleActiveRun(t, sched, ctx, runUUID)
	if rs.RescheduleAt["probe"] == nil {
		t.Fatal("ActiveRuns did not load reschedule_at for the parked task")
	}
	if !reschedulePlanned(scheduler.PlanRun(rs), "probe", domain.TaskStateNone) {
		t.Fatal("planner did not release the ready up_for_reschedule task to none")
	}

	// Re-dispatch: back to none, try_number PRESERVED (no attempt consumed), and
	// reschedule_at cleared — the contract that distinguishes reschedule from retry.
	if err := sched.RedispatchReschedule(ctx, runUUID, "probe"); err != nil {
		t.Fatalf("RedispatchReschedule: %v", err)
	}
	if got := taskInstanceState(t, sched, ctx, runUUID, "probe"); got != domain.TaskStateNone {
		t.Fatalf("after re-dispatch, state = %q, want none", got)
	}
	rs = rescheduleActiveRun(t, sched, ctx, runUUID)
	if got := rs.Tries["probe"]; got != tryBefore {
		t.Fatalf("try_number = %d after reschedule, want %d (reschedule must not consume an attempt)", got, tryBefore)
	}
	if rs.RescheduleAt["probe"] != nil {
		t.Fatalf("reschedule_at not cleared on re-dispatch: %v", *rs.RescheduleAt["probe"])
	}
}

// TestRescheduleFirstRescheduleAtDeliveredAndPreservedIntegration proves the
// cumulative-timeout fix (#380, approach A): the first reschedule stamps
// first_reschedule_at, it is delivered on the re-dispatched pod's TaskSpec (so
// get_first_reschedule_date is real), and it is PRESERVED across reschedules — so
// a reschedule-mode sensor measures run_duration from the first poke and honors
// its timeout instead of polling forever.
func TestRescheduleFirstRescheduleAtDeliveredAndPreservedIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)

	dagID := fmt.Sprintf("reschedule_first_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "probe", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	id := auth.AgentIdentity{RunID: runUUID, TaskID: "probe", TryNumber: 1}
	bringToRunning := func() {
		for _, st := range []domain.TaskState{domain.TaskStateScheduled, domain.TaskStateQueued, domain.TaskStateRunning} {
			if err := sched.ApplyTransition(ctx, runUUID, "probe", st); err != nil {
				t.Fatalf("ApplyTransition %s: %v", st, err)
			}
		}
	}
	firstRescheduleOnSpec := func() string {
		spec, err := exec.TaskSpec(ctx, id)
		if err != nil {
			t.Fatalf("TaskSpec: %v", err)
		}
		return spec.FirstRescheduleAt
	}

	// Before any reschedule the spec carries no first-reschedule time (first attempt).
	if got := firstRescheduleOnSpec(); got != "" {
		t.Fatalf("first_reschedule_at = %q before any reschedule, want empty", got)
	}

	// First reschedule stamps it; it is delivered on the spec the next pod fetches.
	bringToRunning()
	if err := exec.Reschedule(ctx, id, time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("Reschedule #1: %v", err)
	}
	first := firstRescheduleOnSpec()
	if first == "" {
		t.Fatal("first_reschedule_at not stamped/delivered after the first reschedule")
	}

	// Re-dispatch, run again, reschedule again — first_reschedule_at must be PRESERVED
	// (COALESCE), so cumulative timeout spans pokes rather than resetting each one.
	if err := sched.RedispatchReschedule(ctx, runUUID, "probe"); err != nil {
		t.Fatalf("RedispatchReschedule: %v", err)
	}
	bringToRunning()
	if err := exec.Reschedule(ctx, id, time.Now().UTC().Add(2*time.Minute)); err != nil {
		t.Fatalf("Reschedule #2: %v", err)
	}
	if second := firstRescheduleOnSpec(); second != first {
		t.Fatalf("first_reschedule_at changed %q -> %q across reschedules; must be preserved", first, second)
	}
}

// rescheduleActiveRun returns the RunState for runUUID from ActiveRuns (carrying
// States, Tries, and RescheduleAt loaded from the DB).
func rescheduleActiveRun(t *testing.T, sched *storage.SchedulerStore, ctx context.Context, runUUID string) scheduler.RunState {
	t.Helper()
	runs, err := sched.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	for _, r := range runs {
		if r.RunID == runUUID {
			return r
		}
	}
	t.Fatalf("active run %s not found", runUUID)
	return scheduler.RunState{}
}

func rescheduleTryNumber(t *testing.T, sched *storage.SchedulerStore, ctx context.Context, runUUID, taskID string) int {
	t.Helper()
	return rescheduleActiveRun(t, sched, ctx, runUUID).Tries[taskID]
}

func reschedulePlanned(plan []scheduler.PlannedTransition, taskID string, to domain.TaskState) bool {
	for _, p := range plan {
		if p.TaskID == taskID && p.To == to {
			return true
		}
	}
	return false
}
