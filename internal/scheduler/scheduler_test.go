package scheduler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

type transition struct {
	runID, taskID string
	to            domain.TaskState
}

type fakeStore struct {
	runs         []RunState
	materialize  []string
	transitions  []transition
	retried      []transition
	resetInfra   []transition
	redispatched []transition
	runStates    map[string]domain.DagRunState
	scheduled    []ScheduledDAG
	createdRuns  []string
	// createdTenants records the tenant of each created scheduled run, in the
	// same order as createdRuns.
	createdTenants []string
	notes          map[string]string
	createErr      bool
	// limitedTenants answers CreateScheduledRun with a tenant-limit refusal for
	// these tenants, as the store does once a tenant's daily run cap is reached.
	limitedTenants       map[string]bool
	limitedCalls         int
	dispatchFailures     []transition
	dispatchBackpressure []transition
	dispatchExhausted    []string
	dispatchRefused      []string
	// alertAttempts mirrors the real per-episode attempt claim: each call
	// consumes one, and the claim is refused once the budget is spent or the
	// episode is already delivered. Backoff is not simulated — the fake is for
	// the state machine; the timing predicate is proven in integration.
	alertAttempts  map[string]int
	deliveredRuns  map[string]bool
	markAlertedErr bool
	// alertMu guards the two maps above: the tick claims while the detached send
	// goroutine stamps. Real Postgres serializes this; the fake must too.
	alertMu sync.Mutex
	// activeRunsCalls counts ActiveRuns invocations so the follower-side gate
	// can be asserted: a follower must NOT read run state (single-writer
	// invariant, ADR 0031 / issue #208).
	activeRunsCalls int
	// poolBudgets is the per-pool slot cap the pool gate reads (keyed by PoolKey);
	// poolBudgetsCalls counts PoolBudgets invocations so a Lite/pools-disabled tick
	// can be asserted to never query pool budgets (ADR 0053 Stage 3).
	poolBudgets      map[string]int
	poolBudgetsCalls int
	// queuedExpect records the next_dispatch_at each guarded queued write was
	// conditioned on; queuedSuperseded makes that write find the row already
	// moved on (a buffered worker failed or re-offered it, or the agent reported).
	queuedExpect     map[string]*time.Time
	queuedSuperseded map[string]bool
}

func newFakeStore(runs ...RunState) *fakeStore {
	return &fakeStore{runs: runs, runStates: map[string]domain.DagRunState{}}
}

func (f *fakeStore) ActiveRuns(context.Context) ([]RunState, error) {
	f.activeRunsCalls++
	return f.runs, nil
}
func (f *fakeStore) ScheduledDAGs(context.Context) ([]ScheduledDAG, error) {
	return f.scheduled, nil
}
func (f *fakeStore) PoolBudgets(context.Context) (map[string]int, error) {
	f.poolBudgetsCalls++
	return f.poolBudgets, nil
}
func (f *fakeStore) CreateScheduledRun(_ context.Context, tenantID, dagID string, _ time.Time) error {
	if f.createErr {
		return errors.New("create scheduled run failed")
	}
	if f.limitedTenants[tenantID] {
		f.limitedCalls++
		return domain.Safef(domain.ErrLimitExceeded, "tenant limit max_runs_per_day of 1 reached")
	}
	f.createdRuns = append(f.createdRuns, dagID)
	f.createdTenants = append(f.createdTenants, tenantID)
	return nil
}
func (f *fakeStore) MaterializeTasks(_ context.Context, runID string, _ []domain.TaskSpec) error {
	f.materialize = append(f.materialize, runID)
	return nil
}
func (f *fakeStore) ApplyTransition(_ context.Context, runID, taskID string, to domain.TaskState) error {
	f.transitions = append(f.transitions, transition{runID, taskID, to})
	return nil
}

// MarkQueued mirrors the guarded queued write: it records the transition unless
// the test marked the row as already moved on.
func (f *fakeStore) MarkQueued(_ context.Context, runID, taskID string, expectNextDispatchAt *time.Time) (bool, error) {
	if f.queuedExpect == nil {
		f.queuedExpect = map[string]*time.Time{}
	}
	f.queuedExpect[taskID] = expectNextDispatchAt
	if f.queuedSuperseded[taskID] {
		return false, nil
	}
	f.transitions = append(f.transitions, transition{runID, taskID, domain.TaskStateQueued})
	return true, nil
}

// ApplyTransitions mirrors the batched store: it records one transition per task,
// exactly as the equivalent per-task ApplyTransition calls would, so assertions
// on f.transitions are unchanged by the scheduler's batching.
func (f *fakeStore) ApplyTransitions(_ context.Context, runID string, taskIDs []string, to domain.TaskState) error {
	for _, taskID := range taskIDs {
		f.transitions = append(f.transitions, transition{runID, taskID, to})
	}
	return nil
}
func (f *fakeStore) ResetForRetry(_ context.Context, runID, taskID string) (bool, error) {
	f.retried = append(f.retried, transition{runID, taskID, domain.TaskStateNone})
	return true, nil
}
func (f *fakeStore) ResetForInfraReplace(_ context.Context, runID, taskID string) (bool, error) {
	f.resetInfra = append(f.resetInfra, transition{runID, taskID, domain.TaskStateNone})
	return true, nil
}
func (f *fakeStore) RecordDispatchFailure(_ context.Context, runID, taskID string, _ time.Time) error {
	f.dispatchFailures = append(f.dispatchFailures, transition{runID, taskID, ""})
	return nil
}

func (f *fakeStore) RecordDispatchBackpressure(_ context.Context, runID, taskID string, _ time.Time) error {
	f.dispatchBackpressure = append(f.dispatchBackpressure, transition{runID, taskID, ""})
	return nil
}

func (f *fakeStore) FailDispatchExhausted(_ context.Context, runID, taskID, _ string) error {
	f.dispatchExhausted = append(f.dispatchExhausted, taskID)
	return nil
}

// FailDispatchRefused mirrors the SQL: the task fails and its retry budget is
// spent (max_tries drops to the current try), so the planner never retries it.
func (f *fakeStore) FailDispatchRefused(_ context.Context, runID, taskID, _ string) error {
	f.dispatchRefused = append(f.dispatchRefused, taskID)
	for i := range f.runs {
		r := &f.runs[i]
		if r.RunID != runID {
			continue
		}
		if r.States != nil {
			r.States[taskID] = domain.TaskStateFailed
		}
		if r.MaxTries != nil && r.Tries != nil && r.MaxTries[taskID] > r.Tries[taskID] {
			r.MaxTries[taskID] = r.Tries[taskID]
		}
	}
	return nil
}

func (f *fakeStore) RedispatchReschedule(_ context.Context, runID, taskID string) error {
	f.redispatched = append(f.redispatched, transition{runID, taskID, domain.TaskStateNone})
	return nil
}
func (f *fakeStore) SetRunState(_ context.Context, runID string, state domain.DagRunState) error {
	f.runStates[runID] = state
	return nil
}

// The alert bookkeeping is the one part of this fake touched from two
// goroutines: the tick claims an attempt, while the detached send goroutine
// stamps delivery. Postgres serializes that in production; here a mutex stands
// in for it, so `-race` reports real defects rather than the fake's own.
func (f *fakeStore) ClaimAlertAttempt(_ context.Context, runID string, maxAttempts int, _ time.Duration) (int, error) {
	f.alertMu.Lock()
	defer f.alertMu.Unlock()
	if f.markAlertedErr {
		return 0, errors.New("mark alerted failed")
	}
	if f.alertAttempts == nil {
		f.alertAttempts = map[string]int{}
	}
	if f.deliveredRuns[runID] {
		return 0, nil // already paged for this failure episode
	}
	if f.alertAttempts[runID] >= maxAttempts {
		return 0, nil // attempt budget spent; stop hammering the endpoint
	}
	// Backoff is deliberately not simulated: this fake exercises the state
	// machine, and the timing predicate is proven against real Postgres in
	// internal/storage's integration suite.
	f.alertAttempts[runID]++
	return f.alertAttempts[runID], nil
}

func (f *fakeStore) MarkRunAlertDelivered(_ context.Context, runID string, attempt int) error {
	f.alertMu.Lock()
	defer f.alertMu.Unlock()
	if f.alertAttempts[runID] != attempt {
		// Mirrors the SQL guard: a stamp from a superseded episode matches no row.
		return nil
	}
	if f.deliveredRuns == nil {
		f.deliveredRuns = map[string]bool{}
	}
	f.deliveredRuns[runID] = true
	return nil
}

// attemptsFor reports how many attempts a run has consumed, under the same lock
// the scheduler's two goroutines contend on.
func (f *fakeStore) attemptsFor(runID string) int {
	f.alertMu.Lock()
	defer f.alertMu.Unlock()
	return f.alertAttempts[runID]
}

// wasDelivered reports whether the episode was stamped as paged.
func (f *fakeStore) wasDelivered(runID string) bool {
	f.alertMu.Lock()
	defer f.alertMu.Unlock()
	return f.deliveredRuns[runID]
}
func (f *fakeStore) SetTaskNote(_ context.Context, _, taskID, note string) error {
	if f.notes == nil {
		f.notes = map[string]string{}
	}
	f.notes[taskID] = note
	return nil
}
func newScheduler(store Store) *Scheduler {
	s := NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	// Default tests to leader so the writer-path assertions (which is most of
	// them) execute. Follower-mode tests opt out with `s.SetLeading(false)`.
	// Without this default every Step() returns early at the leadership gate
	// (#208) and the test asserts on a no-op tick.
	s.SetLeading(true)
	return s
}

func retriableRun(aState domain.TaskState, aTry int) *fakeStore {
	return newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": aState, "b": domain.TaskStateNone},
		Tries:    map[string]int{"a": aTry, "b": 1},
		MaxTries: map[string]int{"a": 3, "b": 3},
	})
}

func TestStepMovesRetriableFailureToUpForRetry(t *testing.T) {
	store := retriableRun(domain.TaskStateFailed, 1)
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasTransition(store.transitions, "a", domain.TaskStateUpForRetry) {
		t.Errorf("retriable failed a should move to up_for_retry, got %v", store.transitions)
	}
	if _, finalized := store.runStates["r1"]; finalized {
		t.Error("run must not finalize while a can still retry")
	}
}

func TestStepResetsUpForRetryTask(t *testing.T) {
	store := retriableRun(domain.TaskStateUpForRetry, 1)
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.retried) != 1 || store.retried[0].taskID != "a" {
		t.Errorf("up_for_retry a should be reset for retry, got %v", store.retried)
	}
}

func TestStepRedispatchesRescheduledTask(t *testing.T) {
	at := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:       map[string]domain.TaskState{"a": domain.TaskStateUpForReschedule, "b": domain.TaskStateNone},
		Tries:        map[string]int{"a": 1, "b": 1},
		MaxTries:     map[string]int{"a": 3, "b": 3},
		RescheduleAt: map[string]*time.Time{"a": &at},
		Now:          at.Add(time.Second), // reschedule_at has passed
	})
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.redispatched) != 1 || store.redispatched[0].taskID != "a" {
		t.Errorf("up_for_reschedule a should be re-dispatched, got %v", store.redispatched)
	}
	// Reschedule must NOT take the retry path (ResetForRetry bumps try_number) —
	// it consumes no attempt (#380).
	if len(store.retried) != 0 {
		t.Errorf("reschedule must not consume retry budget; retried=%v", store.retried)
	}
}

// TestStepReplacesInfraFailedTaskOffRetryBudget: a task a reaper failed as infra
// (state=failed, InfraFailed) is re-placed via ResetForInfraReplace (preserves
// try_number, bumps infra_attempts) — NOT via the retry rail (ADR 0051 Phase 1).
func TestStepReplacesInfraFailedTaskOffRetryBudget(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:        map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone},
		Tries:         map[string]int{"a": 1, "b": 1},
		MaxTries:      map[string]int{"a": 3, "b": 3},
		InfraFailed:   map[string]bool{"a": true},
		InfraAttempts: map[string]int{"a": 0},
	})
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.resetInfra) != 1 || store.resetInfra[0].taskID != "a" {
		t.Errorf("infra-failed a should be re-placed off-budget, got resetInfra=%v", store.resetInfra)
	}
	// The infra rail must NOT touch the retry budget (ResetForRetry bumps try_number).
	if len(store.retried) != 0 {
		t.Errorf("infra fault must not consume retry budget; retried=%v", store.retried)
	}
}

// TestStepInfraBudgetExhaustedIsTerminal: once infra_attempts hits the bound the
// task stays failed — no further off-budget re-placement, and the run finalizes.
func TestStepInfraBudgetExhaustedIsTerminal(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:        map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone},
		Tries:         map[string]int{"a": 1, "b": 1},
		MaxTries:      map[string]int{"a": 3, "b": 3},
		InfraFailed:   map[string]bool{"a": true},
		InfraAttempts: map[string]int{"a": infraMaxAttempts},
	})
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.resetInfra) != 0 {
		t.Errorf("exhausted infra budget must not re-place; resetInfra=%v", store.resetInfra)
	}
	// Crucially, an exhausted infra budget must NOT fall back to the app-retry
	// rail — the task stays terminally failed, it does not silently gain retries.
	if len(store.retried) != 0 {
		t.Errorf("exhausted infra budget must not fall back to retry; retried=%v", store.retried)
	}
	// a stays failed (no transition emitted for it this tick).
	if hasTransition(store.transitions, "a", domain.TaskStateNone) {
		t.Errorf("exhausted infra task must not be moved to none; transitions=%v", store.transitions)
	}
}

func TestStepFinalizesFailedWhenRetriesExhausted(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateUpstreamFailed},
		Tries:    map[string]int{"a": 3, "b": 1},
		MaxTries: map[string]int{"a": 3, "b": 3},
	})
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.runStates["r1"] != domain.DagRunStateFailed {
		t.Errorf("exhausted failure should finalize the run failed, got %q", store.runStates["r1"])
	}
}

func linearTasks() []domain.TaskSpec {
	return []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "b", Type: domain.TaskTypePython, DependsOn: []string{"a"}},
	}
}

func TestStepMaterializesQueuedRun(t *testing.T) {
	store := newFakeStore(RunState{RunID: "r1", State: domain.DagRunStateQueued, Tasks: linearTasks(), States: map[string]domain.TaskState{}})
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.materialize) != 1 || store.materialize[0] != "r1" {
		t.Errorf("expected materialize r1, got %v", store.materialize)
	}
	if store.runStates["r1"] != domain.DagRunStateRunning {
		t.Errorf("queued run should start running, got %q", store.runStates["r1"])
	}
}

func TestStepSchedulesRootTask(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateNone, "b": domain.TaskStateNone},
	})
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.transitions) != 1 || store.transitions[0] != (transition{"r1", "a", domain.TaskStateScheduled}) {
		t.Errorf("expected a->scheduled, got %v", store.transitions)
	}
}

// TestStepMaxActiveTasksCapsAcrossSiblingRuns: two runs of the SAME DAG, each
// with ready tasks, draw on ONE per-DAG max_active_tasks budget. A single tick
// must not promote more than the cap across both runs — the per-DAG (not
// per-run) semantics of Airflow's max_active_tasks (ADR 0053 Stage 1). Without
// the within-tick admission threading, each run would independently promote up
// to the cap and the DAG would overshoot.
func TestStepMaxActiveTasksCapsAcrossSiblingRuns(t *testing.T) {
	mk := func(runID string) RunState {
		tasks := independent(3)
		return RunState{
			RunID: runID, DagID: "etl", State: domain.DagRunStateRunning,
			Tasks: tasks, States: scheduledStates(tasks), MaxActiveTasks: 2,
		}
	}
	store := newFakeStore(mk("r1"), mk("r2"))
	d := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(d)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.dispatched) != 2 {
		t.Errorf("dispatched %d tasks across sibling runs, want 2 (per-DAG max_active_tasks)", len(d.dispatched))
	}
}

// TestStepPoolCapsAcrossDAGs: two DIFFERENT DAGs whose tasks all draw on one
// shared 2-slot pool. A single Pro tick must dispatch at most 2 across both DAGs
// — the cross-DAG semantics of named pools (ADR 0053 Stage 3). The per-DAG
// max_active_tasks gate cannot express this; the pool gate does.
func TestStepPoolCapsAcrossDAGs(t *testing.T) {
	mk := func(runID, dagID string) RunState {
		tasks := pooledTasks(3, "shared")
		return RunState{
			RunID: runID, DagID: dagID, TenantID: testTenant, State: domain.DagRunStateRunning,
			Tasks: tasks, States: scheduledStates(tasks),
		}
	}
	store := newFakeStore(mk("r1", "alpha"), mk("r2", "beta"))
	store.poolBudgets = map[string]int{PoolKey(testTenant, "shared"): 2}
	d := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetDispatcher(d)
	s.EnablePools() // Pro path
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.dispatched) != 2 {
		t.Errorf("dispatched %d across DAGs, want 2 (cross-DAG pool cap)", len(d.dispatched))
	}
}

// TestStepPoolsDisabledDispatchesAllAndSkipsQuery is the Lite-safety lock at the
// tick level: with pools disabled (Lite / non-Pro), the tick must NOT query pool
// budgets and must dispatch every ready task — byte-identical to the pre-pool
// behavior even if pool budgets exist in the store.
func TestStepPoolsDisabledDispatchesAllAndSkipsQuery(t *testing.T) {
	tasks := pooledTasks(3, "shared")
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "alpha", TenantID: testTenant, State: domain.DagRunStateRunning,
		Tasks: tasks, States: scheduledStates(tasks),
	})
	store.poolBudgets = map[string]int{PoolKey(testTenant, "shared"): 2}
	d := &fakeDispatcher{}
	s := newScheduler(store) // EnablePools NOT called → Lite
	s.SetDispatcher(d)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.dispatched) != 3 {
		t.Errorf("dispatched %d, want all 3 (pools disabled is a no-op)", len(d.dispatched))
	}
	if store.poolBudgetsCalls != 0 {
		t.Errorf("PoolBudgets called %d times, want 0 (Lite must not query pool budgets)", store.poolBudgetsCalls)
	}
}

func TestStepFinalizesCompletedRun(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateSuccess},
	})
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.runStates["r1"] != domain.DagRunStateSuccess {
		t.Errorf("completed run should be success, got %q", store.runStates["r1"])
	}
}

// TestStepFollowerSkipsAllWrites: a follower (no leadership lock) MUST NOT read
// or write scheduler state. Today only the reaper is gated; ActiveRuns and
// createDueRuns also run on every follower, violating the single-writer
// invariant of ADR 0031. The fix lifts the leader gate to the top of Step()
// after the heartbeat store (issue #208).
func TestStepFollowerSkipsAllWrites(t *testing.T) {
	store := newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateNone, "b": domain.TaskStateNone},
		Tries:  map[string]int{"a": 1, "b": 1}, MaxTries: map[string]int{"a": 3, "b": 3},
	})
	store.scheduled = []ScheduledDAG{{DagID: "etl", Schedule: "@hourly"}}
	s := newScheduler(store)
	s.SetLeading(false) // newScheduler defaults to leader; opt out for this follower-mode test.

	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step on follower must not return infra-level error: %v", err)
	}
	if store.activeRunsCalls != 0 {
		t.Errorf("follower must not read ActiveRuns; got %d calls", store.activeRunsCalls)
	}
	if len(store.transitions) != 0 {
		t.Errorf("follower must not write transitions; got %d", len(store.transitions))
	}
	if len(store.createdRuns) != 0 {
		t.Errorf("follower must not create scheduled runs; got %v", store.createdRuns)
	}
	// Heartbeat MUST still fire — the leadership check sits AFTER lastTick.Store
	// so the orchestrator can prove the instance is alive without granting it
	// the writer role.
	if s.lastTick.Load() == 0 {
		t.Errorf("follower must still update lastTick (heartbeat); got 0")
	}
}

// TestStepCatchupBackfillsMissedSlots: a DAG with catchup=true whose leader
// was down for 6 hours produces 6 hourly runs on the next tick (#129).
// Without catchup wiring, the legacy single-run path silently dropped the 5
// intermediate slots.
func TestStepCatchupBackfillsMissedSlots(t *testing.T) {
	last := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "etl", Schedule: "@hourly", LastLogical: &last, Catchup: true,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The exact count varies by the wall clock fraction within the current
	// hour; assert "more than 1" to pin the catchup behavior without
	// flakiness — 1 means catchup did not trigger.
	if len(store.createdRuns) < 2 {
		t.Errorf("catchup=true with 6h gap should create multiple runs, got %d (%v)",
			len(store.createdRuns), store.createdRuns)
	}
}

// TestStepCatchupFalseCreatesOnlyLatest: same gap, catchup=false → exactly
// one run is created (the most recent slot). This is the operator-opt-out
// for "I don't want SLA misses backfilled."
func TestStepCatchupFalseCreatesOnlyLatest(t *testing.T) {
	last := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "etl", Schedule: "@hourly", LastLogical: &last, Catchup: false,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 1 || store.createdRuns[0] != "etl" {
		t.Errorf("catchup=false should create exactly one run, got %v", store.createdRuns)
	}
}

func TestStepCreatesDueScheduledRun(t *testing.T) {
	last := time.Now().UTC().Add(-2 * time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{DagID: "etl", Schedule: "@hourly", LastLogical: &last}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 1 || store.createdRuns[0] != "etl" {
		t.Errorf("expected one scheduled run for etl, got %v", store.createdRuns)
	}
}

func TestStepNoScheduledRunWhenNotDue(t *testing.T) {
	// last == now, so the next @hourly slot is strictly after now and the DAG is
	// not due. Using now-1min was clock-dependent: when the test ran within a
	// minute after the top of the hour, the just-passed hour boundary fell inside
	// [last, now] and the DAG became "due", flaking CI (it ran at 02:01).
	recent := time.Now().UTC()
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{DagID: "etl", Schedule: "@hourly", LastLogical: &recent}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 0 {
		t.Errorf("no run should be created when not due, got %v", store.createdRuns)
	}
}

func TestStepOnceScheduleFiresExactlyOnce(t *testing.T) {
	// @once with no prior run -> create exactly one run.
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{DagID: "once_dag", Schedule: "@once"}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 1 || store.createdRuns[0] != "once_dag" {
		t.Errorf("@once with no prior run should create exactly one run, got %v", store.createdRuns)
	}

	// @once that has ALREADY run (LastLogical set) -> create nothing more.
	ran := time.Now().UTC().Add(-time.Hour)
	store2 := newFakeStore()
	store2.scheduled = []ScheduledDAG{{DagID: "once_dag", Schedule: "@once", LastLogical: &ran}}
	if err := newScheduler(store2).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store2.createdRuns) != 0 {
		t.Errorf("@once that already ran must not run again, got %v", store2.createdRuns)
	}
}

// TestStepRespectsMaxActiveRunsWhenAtCap: with max_active_runs=1 and one
// already-active run for that DAG, even a backfill that would otherwise
// produce many catchup slots must produce zero new runs — the cap is a
// hard ceiling on concurrent active runs per DAG (Airflow #200).
//
// The fake run carries Tasks so PlanRun/FinalizeRun see a non-terminal
// topology and do not auto-finalize it during this tick (which would change
// the snapshot the cap is checked against).
func TestStepRespectsMaxActiveRunsWhenAtCap(t *testing.T) {
	last := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	store := newFakeStore(RunState{
		RunID: "r-existing", DagID: "etl", State: domain.DagRunStateRunning,
		Tasks:  linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateRunning, "b": domain.TaskStateNone},
	})
	store.scheduled = []ScheduledDAG{{
		DagID: "etl", Schedule: "@hourly", LastLogical: &last,
		Catchup: true, MaxActiveRuns: 1,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 0 {
		t.Errorf("DAG already at max_active_runs cap must create no new run, got %v", store.createdRuns)
	}
}

// TestStepOnceScheduleIgnoresMaxActiveRuns covers the documented
// invariant after dropping the dead headroom check on the @once branch:
// @once is single-shot regardless of cap. LastLogical=nil means "never
// fired"; the cap is irrelevant because @once will never compete with
// itself.
func TestStepOnceScheduleIgnoresMaxActiveRuns(t *testing.T) {
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "once_dag", Schedule: "@once", MaxActiveRuns: 1,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 1 || store.createdRuns[0] != "once_dag" {
		t.Errorf("@once must fire its single run regardless of cap, got %v", store.createdRuns)
	}
}

// TestStepRespectsMaxActiveRunsCapsBackfill: same backfill window but no
// existing active runs and max_active_runs=1 — at most ONE new run should be
// created this tick (the remaining slots are skipped, not queued).
func TestStepRespectsMaxActiveRunsCapsBackfill(t *testing.T) {
	last := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "etl", Schedule: "@hourly", LastLogical: &last,
		Catchup: true, MaxActiveRuns: 1,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 1 {
		t.Errorf("max_active_runs=1 should cap backfill at one new run, got %d (%v)",
			len(store.createdRuns), store.createdRuns)
	}
}

// TestStepMaxActiveRunsZeroMeansUnlimited: cap=0 preserves the legacy
// behavior (Airflow-faithful: a missing limit is unlimited). Backfill
// proceeds as if there were no cap.
func TestStepMaxActiveRunsZeroMeansUnlimited(t *testing.T) {
	last := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "etl", Schedule: "@hourly", LastLogical: &last,
		Catchup: true, MaxActiveRuns: 0,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) < 2 {
		t.Errorf("max_active_runs=0 (unlimited) should backfill the whole gap, got %d (%v)",
			len(store.createdRuns), store.createdRuns)
	}
}

func TestStepWarnsOnUnparseableScheduleAndCreatesNoRun(t *testing.T) {
	store := newFakeStore()
	// A 4-field cron (the real bug): the scheduler must not silently ignore it.
	store.scheduled = []ScheduledDAG{{DagID: "etl", Schedule: "*/3 * * *"}}
	var buf bytes.Buffer
	s := NewScheduler(store, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})), time.Millisecond)
	s.SetLeading(true) // direct NewScheduler call — needs explicit leader so createDueRuns runs (#208 gate).

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdRuns) != 0 {
		t.Errorf("a bad schedule must create no run, got %v", store.createdRuns)
	}
	if !strings.Contains(buf.String(), "unparseable") || !strings.Contains(buf.String(), "*/3 * * *") {
		t.Errorf("expected a WARN naming the bad schedule, got: %q", buf.String())
	}
	// A second tick with the same bad schedule must not re-warn (deduped).
	buf.Reset()
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "unparseable") {
		t.Errorf("the warning should be deduped per expression, but it logged again: %q", buf.String())
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	store := newFakeStore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newScheduler(store).Run(ctx); err == nil {
		t.Error("Run should return ctx error after cancel")
	}
}

func TestHeartbeatTracksTicks(t *testing.T) {
	s := NewScheduler(&fakeStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)), 10*time.Millisecond)

	// Before any tick: healthy by startup grace.
	if ok, _ := s.Heartbeat(); !ok {
		t.Error("fresh scheduler should be healthy (startup grace)")
	}
	// After a Step: healthy with a recent heartbeat.
	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
	ok, last := s.Heartbeat()
	if !ok {
		t.Error("scheduler should be healthy right after a tick")
	}
	if time.Since(last) > time.Second {
		t.Errorf("heartbeat %v is not recent", last)
	}
}

func TestHeartbeatIsLeadershipAware(t *testing.T) {
	s := NewScheduler(&fakeStore{}, slog.New(slog.NewTextHandler(io.Discard, nil)), 10*time.Millisecond)

	// Not leading: healthy regardless of ticks — a follower (or an instance that
	// stepped down) is correctly idle, not stalled. Even a very old lastTick is OK.
	s.lastTick.Store(time.Now().Add(-time.Hour).UnixNano())
	if ok, _ := s.Heartbeat(); !ok {
		t.Error("a non-leader must report healthy (idle), not stalled")
	}

	// Becoming leader resets the clock (startup grace), then a tick is fresh+healthy.
	s.SetLeading(true)
	if ok, _ := s.Heartbeat(); !ok {
		t.Error("a fresh leader should be healthy (startup grace)")
	}
	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
	if ok, _ := s.Heartbeat(); !ok {
		t.Error("a leader that just ticked should be healthy")
	}

	// A leader whose ticks went stale is unhealthy, so the UI/monitor surfaces it.
	s.lastTick.Store(time.Now().Add(-time.Hour).UnixNano())
	if ok, _ := s.Heartbeat(); ok {
		t.Error("a leader with a stale heartbeat must be unhealthy")
	}

	// After stepping down it is idle again — healthy, not falsely stalled.
	s.SetLeading(false)
	if ok, _ := s.Heartbeat(); !ok {
		t.Error("after stepping down, an idle instance should be healthy")
	}
}

type fakeRecorder struct {
	decisions        []string
	undispatchable   []string
	stepDowns        map[string]int
	reacquireSamples []time.Duration
}

func (r *fakeRecorder) RecordSchedulerDecision(d string) {
	r.decisions = append(r.decisions, d)
}
func (r *fakeRecorder) RecordTaskTransition(_, _, _ string) {}
func (r *fakeRecorder) RecordUndispatchable(reason string) {
	r.undispatchable = append(r.undispatchable, reason)
}
func (r *fakeRecorder) RecordSchedulerStepDown(reason string) {
	if r.stepDowns == nil {
		r.stepDowns = map[string]int{}
	}
	r.stepDowns[reason]++
}
func (r *fakeRecorder) ObserveSchedulerReacquire(d time.Duration) {
	r.reacquireSamples = append(r.reacquireSamples, d)
}
func (r *fakeRecorder) RecordAlert(_, _, _ string) {}

func freshRun() *fakeStore {
	// 'a' starts scheduled so a single Step plans it none->queued via launchQueued
	// (the planner moves none->scheduled->queued across ticks).
	return newFakeStore(RunState{
		RunID: "r1", DagID: "etl", State: domain.DagRunStateRunning, Tasks: linearTasks(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateScheduled, "b": domain.TaskStateNone},
		Tries:    map[string]int{"a": 0, "b": 0},
		MaxTries: map[string]int{"a": 1, "b": 1},
	})
}

func TestStepRecordsUndispatchableWhenNoExecutor(t *testing.T) {
	store := freshRun()
	rec := &fakeRecorder{}
	s := newScheduler(store)
	s.SetRecorder(rec)
	// No dispatcher and no inline runner: task 'a' becomes queued with nothing
	// to launch it -> the undispatchable signal must fire (#46).
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.undispatchable) != 1 || rec.undispatchable[0] != "no_executor" {
		t.Errorf("expected one no_executor record, got %v", rec.undispatchable)
	}
	// 'a' is FAILED fast (it can never run) rather than left queued forever (#50).
	if !hasTransition(store.transitions, "a", domain.TaskStateFailed) {
		t.Errorf("task a should be failed (no executor), got %v", store.transitions)
	}
	if hasTransition(store.transitions, "a", domain.TaskStateQueued) {
		t.Errorf("task a must NOT be left queued; it can never run, got %v", store.transitions)
	}
	// The reason is surfaced as a task note for the UI.
	if note := store.notes["a"]; !strings.Contains(note, "no executor available") {
		t.Errorf("task a should get an explanatory note, got %q", note)
	}
}

func TestStepDoesNotRecordUndispatchableWithDispatcher(t *testing.T) {
	store := freshRun()
	rec := &fakeRecorder{}
	disp := &fakeDispatcher{}
	s := newScheduler(store)
	s.SetRecorder(rec)
	s.SetDispatcher(disp)
	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.undispatchable) != 0 {
		t.Errorf("with a dispatcher there should be no undispatchable records, got %v", rec.undispatchable)
	}
	if len(disp.dispatched) != 1 || disp.dispatched[0] != "a" {
		t.Errorf("task a should be dispatched, got %v", disp.dispatched)
	}
}
