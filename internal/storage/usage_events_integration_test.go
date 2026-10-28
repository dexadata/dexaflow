//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// usageEvent is the part of a usage_events row these tests check.
type usageEvent struct {
	TryNumber, AttemptEpoch, Revision, PoolSlots int
	State, PreviousState                         string
	HasStart, HasEnd                             bool
}

// usageEventsOf reads the events of one run in the order they were written.
func usageEventsOf(t *testing.T, pg *storage.Postgres, ctx context.Context, runUUID string) []usageEvent {
	t.Helper()
	rows, err := pg.Pool.Query(ctx, `
		SELECT try_number, attempt_epoch, revision, pool_slots, state::text, previous_state::text,
		       started_at IS NOT NULL, ended_at IS NOT NULL
		FROM usage_events WHERE dag_run_id = $1::uuid ORDER BY id`, runUUID)
	if err != nil {
		t.Fatalf("read usage_events: %v", err)
	}
	defer rows.Close()
	var got []usageEvent
	for rows.Next() {
		var e usageEvent
		if err := rows.Scan(&e.TryNumber, &e.AttemptEpoch, &e.Revision, &e.PoolSlots,
			&e.State, &e.PreviousState, &e.HasStart, &e.HasEnd); err != nil {
			t.Fatalf("scan usage_events: %v", err)
		}
		got = append(got, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read usage_events: %v", err)
	}
	return got
}

// enableUsageEvents turns recording on for the test and off again after it,
// the state every other test of the database expects.
func enableUsageEvents(t *testing.T, usage *storage.UsageStore, ctx context.Context) {
	t.Helper()
	if _, err := usage.SetEventsEnabled(ctx, true); err != nil {
		t.Fatalf("SetEventsEnabled(true): %v", err)
	}
	t.Cleanup(func() {
		if _, err := usage.SetEventsEnabled(context.Background(), false); err != nil {
			t.Errorf("SetEventsEnabled(false): %v", err)
		}
	})
}

// TestUsageEventsSwitchIsOffByDefault covers ADR 0096 point 7: with the switch
// as the migration leaves it, a finished attempt records nothing, and the
// switch reports a change only when the value moves.
func TestUsageEventsSwitchIsOffByDefault(t *testing.T) {
	// Arrange
	pg, repo, sched, _, ctx := openExecPG(t)
	usage := storage.NewUsageStore(pg)
	dagID := fmt.Sprintf("usage_off_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	// Act
	changed, err := usage.SetEventsEnabled(ctx, false)
	transErr := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateSuccess)

	// Assert
	if err != nil || changed {
		t.Fatalf("SetEventsEnabled(false) = %v, %v; want no change from the default", changed, err)
	}
	if transErr != nil {
		t.Fatalf("ApplyTransition: %v", transErr)
	}
	if got := usageEventsOf(t, pg, ctx, runUUID); len(got) != 0 {
		t.Errorf("events with the switch off = %+v, want none", got)
	}
}

// TestUsageEventsRecordEachAttemptAndRevision covers ADR 0096 points 1 to 3 on
// the real write paths: the agent's result is revision 0 of the attempt, a
// user's mark over it is revision 1, and the relabel to up_for_retry writes
// nothing.
func TestUsageEventsRecordEachAttemptAndRevision(t *testing.T) {
	// Arrange
	pg, repo, sched, exec, ctx := openExecPG(t)
	usage := storage.NewUsageStore(pg)
	enableUsageEvents(t, usage, ctx)
	dagID := fmt.Sprintf("usage_revisions_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")
	id := auth.AgentIdentity{TenantID: defaultTenantID(), DagID: dagID, RunID: runUUID, TaskID: "load", TryNumber: 1}

	// Act
	reportErr := exec.ReportState(ctx, id, domain.TaskStateSuccess, 0, "")
	markErr := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateFailed)
	retryErr := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateUpForRetry)

	// Assert
	for name, err := range map[string]error{"report": reportErr, "mark": markErr, "retry": retryErr} {
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	got := usageEventsOf(t, pg, ctx, runUUID)
	want := []usageEvent{
		{TryNumber: 1, Revision: 0, PoolSlots: 1, State: "success", PreviousState: "running", HasStart: true, HasEnd: true},
		{TryNumber: 1, Revision: 1, PoolSlots: 1, State: "failed", PreviousState: "success", HasStart: true, HasEnd: true},
	}
	if len(got) != len(want) {
		t.Fatalf("events = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestUsageEventsRescheduleIsItsOwnAttempt covers ADR 0096 point 3: a
// redispatch after a reschedule bumps attempt_epoch, so each poke that ends in
// up_for_reschedule is revision 0 of its own attempt.
func TestUsageEventsRescheduleIsItsOwnAttempt(t *testing.T) {
	// Arrange
	pg, repo, sched, exec, ctx := openExecPG(t)
	usage := storage.NewUsageStore(pg)
	enableUsageEvents(t, usage, ctx)
	dagID := fmt.Sprintf("usage_reschedule_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "wait")
	id := auth.AgentIdentity{TenantID: defaultTenantID(), DagID: dagID, RunID: runUUID, TaskID: "wait", TryNumber: 1}
	// Each poke reports on the attempt the task is on, as an agent's
	// credential does.
	poke := func() {
		t.Helper()
		if err := pg.Pool.QueryRow(ctx, `SELECT attempt_epoch FROM task_instances WHERE dag_run_id = $1::uuid AND task_id = 'wait'`,
			runUUID).Scan(&id.AttemptEpoch); err != nil {
			t.Fatalf("read attempt_epoch: %v", err)
		}
		id.HasAttemptEpoch = true
		if err := exec.Reschedule(ctx, id, time.Now().Add(-time.Second)); err != nil {
			t.Fatalf("Reschedule: %v", err)
		}
	}

	// Act
	poke()
	if err := sched.RedispatchReschedule(ctx, runUUID, "wait"); err != nil {
		t.Fatalf("RedispatchReschedule: %v", err)
	}
	for _, st := range []domain.TaskState{domain.TaskStateQueued, domain.TaskStateRunning} {
		if err := sched.ApplyTransition(ctx, runUUID, "wait", st); err != nil {
			t.Fatalf("ApplyTransition to %s: %v", st, err)
		}
	}
	poke()

	// Assert
	got := usageEventsOf(t, pg, ctx, runUUID)
	if len(got) != 2 {
		t.Fatalf("events = %+v, want one per poke", got)
	}
	for i, e := range got {
		if e.State != "up_for_reschedule" || e.Revision != 0 || e.TryNumber != 1 {
			t.Errorf("event %d = %+v, want revision 0 of try 1 in up_for_reschedule", i, e)
		}
	}
	if got[1].AttemptEpoch <= got[0].AttemptEpoch {
		t.Errorf("attempt epochs = %d then %d, want the second poke on a later epoch", got[0].AttemptEpoch, got[1].AttemptEpoch)
	}
}

// TestUsageEventsFollowTheTransaction covers ADR 0096 point 1: an event exists
// if and only if the change that finished the attempt committed, and a write
// that finishes many attempts at once records one event for each.
func TestUsageEventsFollowTheTransaction(t *testing.T) {
	// Arrange
	pg, repo, sched, _, ctx := openExecPG(t)
	usage := storage.NewUsageStore(pg)
	enableUsageEvents(t, usage, ctx)
	dagID := fmt.Sprintf("usage_tx_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}, {TaskID: "b", Type: domain.TaskTypePython}, {TaskID: "c", Type: domain.TaskTypePython}}
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

	// Act
	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE task_instances SET state = 'skipped' WHERE dag_run_id = $1::uuid AND task_id = 'a'`, runUUID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	afterRollback := len(usageEventsOf(t, pg, ctx, runUUID))
	moved, cascadeErr := sched.ApplyTransitions(ctx, runUUID, []string{"a", "b", "c"}, domain.TaskStateNone, domain.TaskStateUpstreamFailed)

	// Assert
	if afterRollback != 0 {
		t.Errorf("events after a rolled back write = %d, want 0", afterRollback)
	}
	if cascadeErr != nil || len(moved) != 3 {
		t.Fatalf("ApplyTransitions = %v, %v; want the three tasks moved", moved, cascadeErr)
	}
	got := usageEventsOf(t, pg, ctx, runUUID)
	if len(got) != 3 {
		t.Fatalf("events after the cascade = %+v, want one per task", got)
	}
	for i, e := range got {
		if e.State != "upstream_failed" || e.PreviousState != "none" || e.Revision != 0 || e.HasStart {
			t.Errorf("event %d = %+v, want revision 0 in upstream_failed from none, never started", i, e)
		}
	}
}
