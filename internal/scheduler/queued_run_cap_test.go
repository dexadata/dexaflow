package scheduler

import (
	"context"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// queuedRun is a first-seen queued run of DAG etl with the given logical date
// and max_active_runs cap.
func queuedRun(id, logical string, maxActiveRuns int) RunState {
	return RunState{RunID: id, DagID: "etl", State: domain.DagRunStateQueued, LogicalDate: logical,
		MaxActiveRuns: maxActiveRuns, Tasks: linearTasks(), States: map[string]domain.TaskState{}}
}

// clearedRun is a queued run of DAG etl whose task instances a clear reset:
// they exist, so the run is not first-seen.
func clearedRun(id, logical string, maxActiveRuns int) RunState {
	return RunState{RunID: id, DagID: "etl", State: domain.DagRunStateQueued, LogicalDate: logical,
		MaxActiveRuns: maxActiveRuns, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateNone, "b": domain.TaskStateNone}}
}

func startedRuns(store *fakeStore) map[string]bool {
	out := map[string]bool{}
	for id, st := range store.runStates {
		if st == domain.DagRunStateRunning {
			out[id] = true
		}
	}
	return out
}

func touchedRuns(store *fakeStore) map[string]bool {
	out := map[string]bool{}
	for _, tr := range store.transitions {
		out[tr.runID] = true
	}
	return out
}

// TestStepStartsQueuedRunsOldestFirstUpToMaxActiveRuns: a tick moves queued
// runs to running oldest logical date first, only while the DAG has fewer than
// max_active_runs runs running, as Airflow's _start_queued_dagruns does
// (#1766). The snapshot order (queued_at) is not the start order.
func TestStepStartsQueuedRunsOldestFirstUpToMaxActiveRuns(t *testing.T) {
	running := RunState{RunID: "running", DagID: "etl", State: domain.DagRunStateRunning, MaxActiveRuns: 3,
		LogicalDate: "2026-01-01T00:00:00Z", Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateRunning, "b": domain.TaskStateNone}}
	store := newFakeStore(
		running,
		queuedRun("newest", "2026-01-04T00:00:00Z", 3),
		queuedRun("oldest", "2026-01-02T00:00:00Z", 3),
		queuedRun("middle", "2026-01-03T00:00:00Z", 3),
	)
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := startedRuns(store)
	if !got["oldest"] || !got["middle"] || got["newest"] {
		t.Errorf("with 1 running and max_active_runs=3, the two oldest queued runs start: started %v", got)
	}
	for _, id := range store.materialize {
		if id == "newest" {
			t.Errorf("a queued run past the cap is not materialized: %v", store.materialize)
		}
	}
}

// TestStepCapsClearedQueuedRunsOnUnpause: queued runs that clears re-queued
// (their task instances exist) start one at a time under max_active_runs=1
// once the DAG is unpaused, and the ones left queued are not planned: none of
// their tasks is scheduled or dispatched (#1766).
func TestStepCapsClearedQueuedRunsOnUnpause(t *testing.T) {
	store := newFakeStore(
		clearedRun("c2", "2026-01-02T00:00:00Z", 1),
		clearedRun("c1", "2026-01-01T00:00:00Z", 1),
		clearedRun("c3", "2026-01-03T00:00:00Z", 1),
	)
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := startedRuns(store); len(got) != 1 || !got["c1"] {
		t.Errorf("max_active_runs=1: only the oldest cleared run starts, started %v", got)
	}
	if touched := touchedRuns(store); touched["c2"] || touched["c3"] {
		t.Errorf("a queued run past the cap plans no task: transitions %+v", store.transitions)
	}
}

// TestStepMovesClearedRunBackToRunning: an admitted run that a clear put back
// in queued is stamped running again, without materializing its task instances
// a second time (#1766).
func TestStepMovesClearedRunBackToRunning(t *testing.T) {
	store := newFakeStore(clearedRun("cleared", "2026-01-01T00:00:00Z", 16))
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.runStates["cleared"] != domain.DagRunStateRunning {
		t.Errorf("an admitted cleared run moves back to running, got %q", store.runStates["cleared"])
	}
	if len(store.materialize) != 0 {
		t.Errorf("a cleared run keeps its task instances: materialized %v", store.materialize)
	}
}

// TestStepQueuedRunCapCountsOnlyRunning: the start cap counts the DAG's runs
// in running, held ones included, and not the other queued runs: one held
// running run under max_active_runs=2 leaves room for exactly one start.
func TestStepQueuedRunCapCountsOnlyRunning(t *testing.T) {
	held := RunState{RunID: "held", DagID: "etl", State: domain.DagRunStateRunning, Held: true, MaxActiveRuns: 2,
		Tasks: linearTasks(), States: map[string]domain.TaskState{"a": domain.TaskStateRunning, "b": domain.TaskStateNone}}
	store := newFakeStore(held,
		queuedRun("q1", "2026-01-01T00:00:00Z", 2),
		queuedRun("q2", "2026-01-02T00:00:00Z", 2),
	)
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := startedRuns(store); len(got) != 1 || !got["q1"] {
		t.Errorf("one running run under max_active_runs=2: only q1 starts, started %v", got)
	}
}

// TestStepQueuedRunCapIsPerTenant: two tenants that own the same dag_id each
// get their own max_active_runs start budget (#209).
func TestStepQueuedRunCapIsPerTenant(t *testing.T) {
	a := queuedRun("tenant_a", "2026-01-01T00:00:00Z", 1)
	a.TenantID = "ta"
	b := queuedRun("tenant_b", "2026-01-02T00:00:00Z", 1)
	b.TenantID = "tb"
	store := newFakeStore(a, b)
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := startedRuns(store); !got["tenant_a"] || !got["tenant_b"] {
		t.Errorf("each tenant's etl starts its own run: started %v", got)
	}
}

// TestStepQueuedRunCapUnsetIsUnlimited: a non-positive max_active_runs starts
// every queued run, the same fail-open reading run creation uses.
func TestStepQueuedRunCapUnsetIsUnlimited(t *testing.T) {
	store := newFakeStore(
		queuedRun("q1", "2026-01-01T00:00:00Z", 0),
		queuedRun("q2", "2026-01-02T00:00:00Z", 0),
	)
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := startedRuns(store); !got["q1"] || !got["q2"] {
		t.Errorf("no cap: every queued run starts, started %v", got)
	}
}

// TestStepRecordsEachQueuedRunHeldByTheStartCap: every queued run the cap
// holds back is metered once per tick as max_active_runs_start_cap.
func TestStepRecordsEachQueuedRunHeldByTheStartCap(t *testing.T) {
	store := newFakeStore(
		queuedRun("q1", "2026-01-01T00:00:00Z", 1),
		queuedRun("q2", "2026-01-02T00:00:00Z", 1),
		queuedRun("q3", "2026-01-03T00:00:00Z", 1),
	)
	rec := &fakeRecorder{}
	s := newScheduler(store)
	s.SetRecorder(rec)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.Join(rec.decisions, ","), "max_active_runs_start_cap"); got != 2 {
		t.Errorf("two queued runs held by the cap: %d max_active_runs_start_cap decisions, want 2 (%v)", got, rec.decisions)
	}
	if strings.Contains(strings.Join(rec.decisions, ","), "start_run_superseded") {
		t.Errorf("a run that started is not metered as superseded: %v", rec.decisions)
	}
}

// TestStepLeavesQueuedRunThatMovedOnBeforeItsStart: the queued -> running
// write is guarded on the run still being queued. When a user marked the run
// after the tick read it, nothing starts, no task of it is planned, and the
// superseded start is metered as start_run_superseded.
func TestStepLeavesQueuedRunThatMovedOnBeforeItsStart(t *testing.T) {
	store := newFakeStore(clearedRun("marked", "2026-01-01T00:00:00Z", 16))
	store.startSuperseded = map[string]bool{"marked": true}
	rec := &fakeRecorder{}
	s := newScheduler(store)
	s.SetRecorder(rec)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.Join(rec.decisions, ","), "start_run_superseded"); got != 1 {
		t.Errorf("a start that found the run moved on is metered once: %d start_run_superseded decisions, want 1 (%v)", got, rec.decisions)
	}
	if st, ok := store.runStates["marked"]; ok {
		t.Errorf("a run that moved on is not stamped: run state %q", st)
	}
	if len(store.transitions) != 0 {
		t.Errorf("a run that moved on plans nothing: transitions %+v", store.transitions)
	}
}
