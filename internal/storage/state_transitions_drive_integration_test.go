//go:build integration

package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// TestStateTransitionsTheTableListsIntegration drives, through the store
// methods production calls, the task transitions of the scheduler's table
// that no other test of the package makes, so the recorder in
// state_transitions_integration_test.go sees every entry made by a query
// (#2146): the agent reporting before queued is recorded, a reschedule poke
// on a task not yet running, a clear of an active or skipped task, and a
// clear that drops tasks in each state it may mark removed. Fixtures set a
// state with a raw UPDATE, which the recorder leaves out.
func TestStateTransitionsTheTableListsIntegration(t *testing.T) {
	pg, repo, sched, exec, ctx := openExecPG(t)
	setState := func(t *testing.T, runUUID, taskID string, st domain.TaskState) {
		t.Helper()
		if _, err := pg.Pool.Exec(ctx, `UPDATE task_instances SET state=$3::task_state
			WHERE dag_run_id=$1::uuid AND task_id=$2`, runUUID, taskID, string(st)); err != nil {
			t.Fatal(err)
		}
	}
	newRun := func(t *testing.T, prefix string, ids []string) (dagID, runUUID string) {
		t.Helper()
		dagID = fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
		registerSpec(t, repo, ctx, dagID, pythonTasks(ids))
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create run: %v", err)
		}
		runUUID = resolveRunUUID(t, sched, ctx, dagID)
		if err := sched.MaterializeTasks(ctx, runUUID, pythonTasks(ids)); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		return dagID, runUUID
	}
	expect := func(t *testing.T, runUUID, taskID string, want domain.TaskState) {
		t.Helper()
		if got := taskInstanceState(t, sched, ctx, runUUID, taskID); got != want {
			t.Errorf("%s = %s, want %s", taskID, got, want)
		}
	}

	t.Run("agent reports", func(t *testing.T) {
		for _, c := range []struct {
			from, report domain.TaskState
		}{
			{domain.TaskStateNone, domain.TaskStateSkipped},
			{domain.TaskStateNone, domain.TaskStateSuccess},
			{domain.TaskStateNone, domain.TaskStateFailed},
			{domain.TaskStateScheduled, domain.TaskStateSkipped},
			{domain.TaskStateQueued, domain.TaskStateSkipped},
			{domain.TaskStateRunning, domain.TaskStateSkipped},
			{domain.TaskStateScheduled, domain.TaskStateSuccess},
		} {
			dagID, runUUID := newRun(t, "drive_report", []string{"t"})
			setState(t, runUUID, "t", c.from)
			id := auth.AgentIdentity{TenantID: defaultTenantID(), DagID: dagID, RunID: runUUID, TaskID: "t", TryNumber: 1}
			if err := exec.ReportState(ctx, id, c.report, 0, ""); err != nil {
				t.Fatalf("report %s from %s: %v", c.report, c.from, err)
			}
			expect(t, runUUID, "t", c.report)
		}
	})

	t.Run("reschedule poke", func(t *testing.T) {
		for _, from := range []domain.TaskState{domain.TaskStateScheduled, domain.TaskStateQueued} {
			dagID, runUUID := newRun(t, "drive_poke", []string{"t"})
			setState(t, runUUID, "t", from)
			id := auth.AgentIdentity{TenantID: defaultTenantID(), DagID: dagID, RunID: runUUID, TaskID: "t", TryNumber: 1}
			if err := exec.Reschedule(ctx, id, time.Now().Add(time.Minute)); err != nil {
				t.Fatalf("reschedule from %s: %v", from, err)
			}
			expect(t, runUUID, "t", domain.TaskStateUpForReschedule)
		}
	})

	t.Run("clear", func(t *testing.T) {
		from := map[string]domain.TaskState{
			"a": domain.TaskStateScheduled, "b": domain.TaskStateQueued, "c": domain.TaskStateRunning,
			"d": domain.TaskStateSkipped, "e": domain.TaskStateUpstreamFailed,
		}
		dagID, runUUID := newRun(t, "drive_clear", []string{"a", "b", "c", "d", "e"})
		for id, st := range from {
			setState(t, runUUID, id, st)
		}
		if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", []string{"a", "b", "c", "d", "e"}, false, domain.ClearOptions{}); err != nil {
			t.Fatalf("clear: %v", err)
		}
		for id := range from {
			expect(t, runUUID, id, domain.TaskStateNone)
		}
	})

	t.Run("clear onto a version that drops tasks", func(t *testing.T) {
		from := map[string]domain.TaskState{
			"a": domain.TaskStateScheduled, "b": domain.TaskStateSuccess, "c": domain.TaskStateSkipped,
			"d": domain.TaskStateUpstreamFailed, "e": domain.TaskStateUpForRetry, "f": domain.TaskStateUpForReschedule,
			"g": domain.TaskStateNone,
		}
		f := seedDriftedRun(t, "drive_removed", []string{"keep", "a", "b", "c", "d", "e", "f", "g"}, pythonTasks([]string{"keep"}))
		for id, st := range from {
			f.setTask(t, id, string(st))
		}
		if _, err := f.repo.ClearTaskInstances(ctx, "default", f.dagID, "r1", nil, true, rebind); err != nil {
			t.Fatalf("clear: %v", err)
		}
		if !f.onV2(t) {
			t.Fatal("the run was not moved to the version that drops the tasks")
		}
		for id := range from {
			expect(t, f.runUUID, id, domain.TaskStateRemoved)
		}
	})
}
