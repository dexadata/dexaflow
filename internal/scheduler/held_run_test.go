package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestStepLeavesHeldQueuedRunWaiting: a queued run of a paused DAG (Held) is
// not advanced: it is not started, nothing is materialized, transitioned or
// dispatched (#1705). A held run that is already running advances as usual so
// the tasks it started finish and its ready tasks keep going.
func TestStepLeavesHeldQueuedRunWaiting(t *testing.T) {
	store := newFakeStore(
		RunState{RunID: "waiting", DagID: "etl", State: domain.DagRunStateQueued, Held: true,
			Tasks: linearTasks(), States: map[string]domain.TaskState{}},
		RunState{RunID: "started", DagID: "etl", State: domain.DagRunStateRunning, Held: true,
			Tasks: linearTasks(), States: map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateNone}},
	)
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range store.materialize {
		if id == "waiting" {
			t.Errorf("held queued run must not be materialized: %v", store.materialize)
		}
	}
	if st, ok := store.runStates["waiting"]; ok {
		t.Errorf("held queued run must stay queued, got run state %q", st)
	}
	var startedMoved bool
	for _, tr := range store.transitions {
		if tr.runID == "waiting" {
			t.Errorf("held queued run must not transition tasks: %+v", tr)
		}
		if tr.runID == "started" && tr.taskID == "b" {
			startedMoved = true
		}
	}
	if !startedMoved {
		t.Errorf("held running run advances its ready task b: transitions %+v", store.transitions)
	}
}

// TestStepCountsHeldRunTowardMaxActiveRuns: a queued run waiting on its paused
// DAG still occupies a max_active_runs slot, so the tick creates no run past
// the cap for it.
func TestStepCountsHeldRunTowardMaxActiveRuns(t *testing.T) {
	last := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	store := newFakeStore(RunState{
		RunID: "waiting", DagID: "etl", State: domain.DagRunStateQueued, Held: true,
		Tasks: linearTasks(), States: map[string]domain.TaskState{},
	})
	store.scheduled = []ScheduledDAG{{
		DagID: "etl", Schedule: "@hourly", LastLogical: &last, Catchup: true, MaxActiveRuns: 1,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 0 {
		t.Errorf("held run counts toward max_active_runs: created %v", store.createdRuns)
	}
}
