package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// flakyStore is a Store that can be told to fail or panic for specific runs, so
// resilience tests can inject a "poison run" and assert the scheduler isolates
// it. Unconfigured runs behave normally. It is safe for concurrent use.
type flakyStore struct {
	mu sync.Mutex

	runs      []RunState
	scheduled []ScheduledDAG

	materializeErrOn map[string]bool // runIDs whose MaterializeTasks errors
	createErrOn      map[string]bool // dagIDs whose CreateScheduledRun errors
	activeRunsPanics bool
	activeRunsBlocks bool // block until ctx is canceled (hung-query simulation)

	materialized []string
	createdRuns  []string
	transitions  []transition
	runStates    map[string]domain.DagRunState
}

func newFlakyStore() *flakyStore {
	return &flakyStore{
		materializeErrOn: map[string]bool{},
		createErrOn:      map[string]bool{},
		runStates:        map[string]domain.DagRunState{},
	}
}

func (f *flakyStore) ActiveRuns(ctx context.Context) ([]RunState, error) {
	if f.activeRunsPanics {
		panic("boom: ActiveRuns")
	}
	if f.activeRunsBlocks {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.runs, nil
}

func (f *flakyStore) ScheduledDAGs(context.Context) ([]ScheduledDAG, error) {
	return f.scheduled, nil
}

func (f *flakyStore) PoolBudgets(context.Context) (map[string]int, error) {
	return map[string]int{}, nil
}

func (f *flakyStore) CreateScheduledRun(_ context.Context, _, dagID string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErrOn[dagID] {
		return errors.New("poison: create failed for " + dagID)
	}
	f.createdRuns = append(f.createdRuns, dagID)
	return nil
}

func (f *flakyStore) MaterializeTasks(_ context.Context, runID string, _ []domain.TaskSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.materializeErrOn[runID] {
		return errors.New("poison: materialize failed for " + runID)
	}
	f.materialized = append(f.materialized, runID)
	return nil
}

func (f *flakyStore) ApplyTransition(_ context.Context, runID, taskID string, to domain.TaskState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transitions = append(f.transitions, transition{runID, taskID, to})
	return nil
}

// ApplyTransitions records one transition per task, matching the equivalent
// per-task ApplyTransition calls, so the scheduler's batching is invisible here.
func (f *flakyStore) ApplyTransitions(_ context.Context, runID string, taskIDs []string, to domain.TaskState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, taskID := range taskIDs {
		f.transitions = append(f.transitions, transition{runID, taskID, to})
	}
	return nil
}

func (f *flakyStore) ResetForRetry(context.Context, string, string) (bool, error) { return true, nil }
func (f *flakyStore) ResetForInfraReplace(context.Context, string, string) (bool, error) {
	return true, nil
}
func (f *flakyStore) RedispatchReschedule(context.Context, string, string) error { return nil }
func (f *flakyStore) RecordDispatchFailure(context.Context, string, string, time.Time) error {
	return nil
}
func (f *flakyStore) RecordDispatchBackpressure(context.Context, string, string, time.Time) error {
	return nil
}
func (f *flakyStore) FailDispatchExhausted(context.Context, string, string, string) error { return nil }

func (f *flakyStore) SetRunState(_ context.Context, runID string, state domain.DagRunState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runStates[runID] = state
	return nil
}
func (f *flakyStore) ClaimAlertAttempt(context.Context, string, int, time.Duration) (int, error) {
	return 1, nil
}
func (f *flakyStore) MarkRunAlertDelivered(context.Context, string, int) error { return nil }

func (f *flakyStore) SetTaskNote(context.Context, string, string, string) error { return nil }

func (f *flakyStore) snapshotMaterialized() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.materialized...)
}

// capturingRecorder records every scheduler decision, concurrency-safe.
type capturingRecorder struct {
	mu        sync.Mutex
	decisions []string
	alerts    []string // "<dag>/<type>/<result>" per RecordAlert call
}

func (r *capturingRecorder) RecordSchedulerDecision(d string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisions = append(r.decisions, d)
}
func (r *capturingRecorder) RecordTaskTransition(_, _, _ string)       {}
func (r *capturingRecorder) RecordUndispatchable(string)               {}
func (r *capturingRecorder) RecordSchedulerStepDown(string)            {}
func (r *capturingRecorder) ObserveSchedulerReacquire(_ time.Duration) {}
func (r *capturingRecorder) RecordAlert(dagID, channelType, result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alerts = append(r.alerts, dagID+"/"+channelType+"/"+result)
}

func (r *capturingRecorder) alertCount(entry string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, a := range r.alerts {
		if a == entry {
			n++
		}
	}
	return n
}

func (r *capturingRecorder) count(decision string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, d := range r.decisions {
		if d == decision {
			n++
		}
	}
	return n
}

// panicDispatcher panics when dispatching a configured task — a poison run that
// would otherwise crash the scheduler goroutine (and the whole process).
type panicDispatcher struct{ onTask string }

func (p *panicDispatcher) Dispatch(_ context.Context, _, _, _ string, task domain.TaskSpec) (executor.Disposition, error) {
	if task.TaskID == p.onTask {
		panic("boom: dispatching " + task.TaskID)
	}
	return executor.Dispatched, nil
}

func queuedEmptyRun(id string) RunState {
	return RunState{RunID: id, DagID: "etl", State: domain.DagRunStateQueued, Tasks: linearTasks(), States: map[string]domain.TaskState{}}
}

func newResilienceScheduler(store Store) *Scheduler {
	s := NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	// Resilience suite exercises the writer path (panics inside the run loop,
	// flaky stores, etc.). All Step() calls must execute past the leadership
	// gate (#208) — default the helper to leader.
	s.SetLeading(true)
	return s
}

// TestStepIsolatesErroringRun asserts a per-run error does not block the other
// runs or new-run creation (no head-of-line blocking).
func TestStepIsolatesErroringRun(t *testing.T) {
	store := newFlakyStore()
	store.runs = []RunState{queuedEmptyRun("bad"), queuedEmptyRun("good")}
	store.materializeErrOn["bad"] = true
	last := time.Now().UTC().Add(-2 * time.Hour)
	store.scheduled = []ScheduledDAG{{DagID: "cron_dag", Schedule: "@hourly", LastLogical: &last}}
	rec := &capturingRecorder{}
	s := newResilienceScheduler(store)
	s.SetRecorder(rec)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step should not return a per-run error, got %v", err)
	}
	// The good run advanced despite the bad run failing first.
	if got := store.snapshotMaterialized(); len(got) != 1 || got[0] != "good" {
		t.Errorf("good run should advance despite bad run; materialized=%v", got)
	}
	// New scheduled-run creation still ran (it is after the per-run loop).
	if len(store.createdRuns) != 1 || store.createdRuns[0] != "cron_dag" {
		t.Errorf("due run creation must not be blocked by a poison run; created=%v", store.createdRuns)
	}
	if rec.count("run_error") != 1 {
		t.Errorf("the per-run error should be metered once, got %d", rec.count("run_error"))
	}
}

// TestCreateDueRunsIsolatesFailingDag asserts one DAG's run-creation failure
// does not block run creation for the other due DAGs (no head-of-line blocking).
func TestCreateDueRunsIsolatesFailingDag(t *testing.T) {
	last := time.Now().UTC().Add(-2 * time.Hour)
	store := newFlakyStore()
	store.scheduled = []ScheduledDAG{
		{DagID: "bad", Schedule: "@hourly", LastLogical: &last},
		{DagID: "good", Schedule: "@hourly", LastLogical: &last},
	}
	store.createErrOn["bad"] = true
	rec := &capturingRecorder{}
	s := newResilienceScheduler(store)
	s.SetRecorder(rec)

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step should not surface a per-DAG create error, got %v", err)
	}
	if len(store.createdRuns) != 1 || store.createdRuns[0] != "good" {
		t.Errorf("the good DAG should get a run despite the bad one failing; created=%v", store.createdRuns)
	}
	if rec.count("create_run_error") != 1 {
		t.Errorf("the failed creation should be metered once, got %d", rec.count("create_run_error"))
	}
}

// TestStepIsolatesPanickingRun asserts a panic in one run is recovered and the
// other runs still advance — a poison run cannot crash the scheduler.
func TestStepIsolatesPanickingRun(t *testing.T) {
	bad := RunState{
		RunID: "bad", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
		Tries:  map[string]int{"a": 0}, MaxTries: map[string]int{"a": 1},
	}
	store := newFlakyStore()
	store.runs = []RunState{bad, queuedEmptyRun("good")}
	rec := &capturingRecorder{}
	s := newResilienceScheduler(store)
	s.SetRecorder(rec)
	s.SetDispatcher(&panicDispatcher{onTask: "a"}) // panics advancing "bad"

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step must not propagate a panic, got %v", err)
	}
	if got := store.snapshotMaterialized(); len(got) != 1 || got[0] != "good" {
		t.Errorf("good run should advance despite the panicking run; materialized=%v", got)
	}
	if rec.count("panic") != 1 {
		t.Errorf("the recovered panic should be metered once, got %d", rec.count("panic"))
	}
}

// TestTickRecoversPanicOutsideRunLoop asserts the top-level backstop: a panic in
// Step itself (e.g. listing runs) is recovered, so the loop never dies.
func TestTickRecoversPanicOutsideRunLoop(t *testing.T) {
	store := newFlakyStore()
	store.activeRunsPanics = true
	rec := &capturingRecorder{}
	s := newResilienceScheduler(store)
	s.SetRecorder(rec)

	// Must not panic out of tick.
	s.tick(context.Background())

	if rec.count("panic") != 1 {
		t.Errorf("a panic in Step should be recovered and metered, got %d", rec.count("panic"))
	}
}

// TestTickTimesOutHungStep asserts a hung tick is canceled by the step timeout,
// so a stuck query cannot freeze the loop forever.
func TestTickTimesOutHungStep(t *testing.T) {
	store := newFlakyStore()
	store.activeRunsBlocks = true // blocks until ctx is canceled
	s := newResilienceScheduler(store)
	s.SetStepTimeout(50 * time.Millisecond)

	done := make(chan struct{})
	go func() { s.tick(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tick did not return; the step timeout failed to cancel a hung Step")
	}
}

// TestRunSurvivesRepeatedPanics asserts the loop keeps ticking through panics:
// it recovers each time and never crashes the goroutine.
func TestRunSurvivesRepeatedPanics(t *testing.T) {
	store := newFlakyStore()
	store.activeRunsPanics = true
	rec := &capturingRecorder{}
	s := newResilienceScheduler(store) // 1ms interval
	s.SetRecorder(rec)
	s.SetStepTimeout(time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Run(ctx); close(done) }()
	time.Sleep(60 * time.Millisecond)
	cancel()
	<-done

	if got := rec.count("panic"); got < 3 {
		t.Errorf("the loop should recover and keep ticking through panics; recovered %d (want >= 3)", got)
	}
}
