package executor

import (
	"context"
	"sync"
	"testing"
	"time"
)

// capturingRecorder records every scheduler decision the reapers meter, so a
// test can assert on the decision counters. It satisfies DecisionRecorder.
// Concurrency-safe to match the production recorder.
type capturingRecorder struct {
	mu        sync.Mutex
	decisions []string
}

func (r *capturingRecorder) RecordSchedulerDecision(d string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decisions = append(r.decisions, d)
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

// fakeReaperStore satisfies the full ReaperStore (all four reap interfaces), so
// a test can drive NewReaper + ReapOnce end-to-end and assert which candidates
// each reaper marked. Empty candidate slices make a reaper a no-op.
type fakeReaperStore struct {
	orphanCands  []ReapCandidate
	reapedRuns   []string
	agentCands   []AgentLostCandidate
	agentMarked  []string
	queuedCands  []StaleQueuedCandidate
	queuedMarked []string
	runningCands []PodLostCandidate
	podMarked    []string
	warmBound    []WarmBoundTI
}

func (f *fakeReaperStore) ListReapCandidates(context.Context) ([]ReapCandidate, error) {
	return f.orphanCands, nil
}
func (f *fakeReaperStore) ReapRun(_ context.Context, runID string) error {
	f.reapedRuns = append(f.reapedRuns, runID)
	return nil
}
func (f *fakeReaperStore) ListAgentLostCandidates(context.Context) ([]AgentLostCandidate, error) {
	return f.agentCands, nil
}
func (f *fakeReaperStore) MarkTaskAgentLost(_ context.Context, tiID string, _, _ int) (bool, error) {
	f.agentMarked = append(f.agentMarked, tiID)
	return true, nil
}
func (f *fakeReaperStore) ListStaleQueuedCandidates(context.Context) ([]StaleQueuedCandidate, error) {
	return f.queuedCands, nil
}
func (f *fakeReaperStore) MarkTaskDispatchLost(_ context.Context, tiID string, _, _ int) (bool, error) {
	f.queuedMarked = append(f.queuedMarked, tiID)
	return true, nil
}
func (f *fakeReaperStore) ListRunningTasks(context.Context, time.Duration) ([]PodLostCandidate, error) {
	return f.runningCands, nil
}
func (f *fakeReaperStore) MarkTaskPodLost(_ context.Context, tiID string, _, _ int) (bool, error) {
	f.podMarked = append(f.podMarked, tiID)
	return true, nil
}
func (f *fakeReaperStore) ListWarmBoundRunningTIs(context.Context) ([]WarmBoundTI, error) {
	return f.warmBound, nil
}

// TestReaperReapOnceDrivesReapers is the aggregate-seam contract, the executor
// side of what used to be TestStepReapsOrphanRunsOnLeader /
// TestStepRunsDispatchLostReaperOnLeader in the scheduler package: NewReaper
// wires the four reapers and ReapOnce runs them, so a stale orphan run and a
// stale queued TI are both reaped in one call. Nil pods (Lite) exercises the
// DB-only path — the orphan reaper still reaps and the dispatch-lost reaper
// falls back to its pure time threshold.
func TestReaperReapOnceDrivesReapers(t *testing.T) {
	past := time.Now().UTC().Add(-1 * time.Hour)
	store := &fakeReaperStore{
		orphanCands: []ReapCandidate{{RunID: "stuck-run", DagID: "etl", LastActivity: past}},
		queuedCands: []StaleQueuedCandidate{{TaskInstanceID: "stuck-ti", QueuedAt: past}},
	}
	rec := &capturingRecorder{}
	r := NewReaper(store, nil, nil, nil, rec, reapTestLogger(), DefaultReaperConfig(), nil)

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.reapedRuns) != 1 || store.reapedRuns[0] != "stuck-run" {
		t.Errorf("orphan reaper must reap stuck-run, got %v", store.reapedRuns)
	}
	if len(store.queuedMarked) != 1 || store.queuedMarked[0] != "stuck-ti" {
		t.Errorf("dispatch-lost reaper must fail stuck-ti, got %v", store.queuedMarked)
	}
}

// staleEverythingStore serves one long-stale candidate to EVERY reaper, so a
// test can prove that a gate stops all five destructive paths at once.
//
// If you grow this fixture, note that gateWiringStore exists precisely so the
// "exactly one gate skip per reaper" assertion does not read these counts: that
// test is about the wiring, and a second candidate here would have made it fail
// while blaming the wiring instead. Add candidates freely; add them there only
// deliberately.
func staleEverythingStore() *fakeReaperStore {
	past := time.Now().UTC().Add(-1 * time.Hour)
	return &fakeReaperStore{
		orphanCands:  []ReapCandidate{{RunID: "stuck-run", DagID: "etl", LastActivity: past}},
		agentCands:   []AgentLostCandidate{{TaskInstanceID: "silent", DagRunID: "r1", TaskID: "t", TryNumber: 1, LastHeartbeat: past}},
		queuedCands:  []StaleQueuedCandidate{{TaskInstanceID: "stuck-ti", DagRunID: "r2", TaskID: "t", TryNumber: 1, QueuedAt: past}},
		runningCands: []PodLostCandidate{{TaskInstanceID: "gone", DagRunID: "r3", TaskID: "t", TryNumber: 1, RunningSince: past}},
		warmBound:    []WarmBoundTI{{TaskInstanceID: "warm-orphan", DagRunID: "r4", TaskID: "t", TryNumber: 1, WarmWorkerID: "dead-worker"}},
	}
}

// assertNothingDestroyed fails the test if any reaper marked, reaped or deleted.
func assertNothingDestroyed(t *testing.T, store *fakeReaperStore, pods *fakePodManager) {
	t.Helper()
	if len(store.reapedRuns)+len(store.agentMarked)+len(store.queuedMarked)+len(store.podMarked) != 0 {
		t.Errorf("no store write may happen: reapedRuns=%v agentMarked=%v queuedMarked=%v podMarked=%v",
			store.reapedRuns, store.agentMarked, store.queuedMarked, store.podMarked)
	}
	if len(pods.deletedTasks)+len(pods.deletedRuns) != 0 {
		t.Errorf("no pod delete may happen: tasks=%v runs=%v", pods.deletedTasks, pods.deletedRuns)
	}
}

// TestReaperReapOnceSkipsOnCanceledContext: a reaper tick that starts under an
// already-canceled context — the SIGTERM drain, or the run-context fan-out of a
// leader step-down — must not mark or delete anything. The successor leader
// redoes the reap under its own post-leadership grace; a terminating leader
// marking a TI failed on its way out is the same fault class as reaping before
// the fleet has settled. The skip is recorded so operators can see it.
func TestReaperReapOnceSkipsOnCanceledContext(t *testing.T) {
	store := staleEverythingStore()
	pods := &fakePodManager{active: map[string]bool{}}
	rec := &capturingRecorder{}
	r := NewReaper(store, pods, nil, &fakeWarmLister{}, rec, reapTestLogger(), DefaultReaperConfig(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := r.ReapOnce(ctx); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	assertNothingDestroyed(t, store, pods)
	if got := rec.count("reap_gate_skip"); got != 1 {
		t.Errorf("reap_gate_skip = %d, want 1", got)
	}
}

// TestReaperReapOnceSkipsDuringStepDown: while the scheduler reports a graceful
// leader step-down, the tick is not destructive even if its context has not been
// canceled yet (MarkSteppingDown runs BEFORE the cancel).
func TestReaperReapOnceSkipsDuringStepDown(t *testing.T) {
	store := staleEverythingStore()
	pods := &fakePodManager{active: map[string]bool{}}
	rec := &capturingRecorder{}
	steppingDown := func() bool { return true }
	r := NewReaper(store, pods, nil, &fakeWarmLister{}, rec, reapTestLogger(), DefaultReaperConfig(), steppingDown)

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	assertNothingDestroyed(t, store, pods)
	if got := rec.count("reap_gate_skip"); got != 1 {
		t.Errorf("reap_gate_skip = %d, want 1", got)
	}
}

// TestReaperReapOnceSkipsWhenNotLeading: a wired leadership predicate that
// reports "not leading" stops the tick; the same predicate reporting "leading"
// leaves the normal path unchanged (everything stale is reaped).
func TestReaperReapOnceSkipsWhenNotLeading(t *testing.T) {
	follower := staleEverythingStore()
	pods := &fakePodManager{active: map[string]bool{}}
	r := NewReaper(follower, pods, nil, &fakeWarmLister{}, nil, reapTestLogger(), DefaultReaperConfig(), nil)
	r.SetLeading(func() bool { return false })
	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	assertNothingDestroyed(t, follower, pods)

	leader := staleEverythingStore()
	r2 := NewReaper(leader, &fakePodManager{active: map[string]bool{}}, nil, &fakeWarmLister{}, nil, reapTestLogger(), DefaultReaperConfig(), func() bool { return false })
	r2.SetLeading(func() bool { return true })
	if err := r2.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(leader.reapedRuns) != 1 || len(leader.agentMarked) != 1 || len(leader.queuedMarked) != 1 || len(leader.podMarked) != 2 {
		t.Errorf("a leading, non-stepping-down reaper must reap normally: reapedRuns=%v agentMarked=%v queuedMarked=%v podMarked=%v",
			leader.reapedRuns, leader.agentMarked, leader.queuedMarked, leader.podMarked)
	}
}

// TestReapersHonorGateMidTick: the gate is re-checked immediately before EVERY
// destructive call inside each reaper, not only at the top of the tick — a
// cancel or step-down that lands after the candidate list was read must still
// stop the mark/delete that follows. Each reaper is driven directly with a gate
// that is already closed, so the list succeeds and the write is what gets
// refused; the per-reaper skip decision is recorded.
func TestReapersHonorGateMidTick(t *testing.T) {
	closed := func(context.Context) bool { return false }
	past := time.Now().UTC().Add(-1 * time.Hour)

	t.Run("orphan", func(t *testing.T) {
		store := &fakeReaperStore{orphanCands: []ReapCandidate{{RunID: "stuck", LastActivity: past}}}
		rec := &capturingRecorder{}
		r := newOrphanReaper(store, reapTestLogger(), time.Minute, rec)
		r.gate = closed
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.reapedRuns) != 0 || rec.count("orphan_gate_skip") != 1 {
			t.Errorf("reapedRuns=%v orphan_gate_skip=%d, want none / 1", store.reapedRuns, rec.count("orphan_gate_skip"))
		}
	})
	t.Run("agent-lost", func(t *testing.T) {
		store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{{TaskInstanceID: "silent", LastHeartbeat: past}}}
		rec := &capturingRecorder{}
		r := newAgentLostReaper(store, reapTestLogger(), time.Minute, rec)
		r.gate = closed
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.failed) != 0 || rec.count("agent_lost_gate_skip") != 1 {
			t.Errorf("failed=%v agent_lost_gate_skip=%d, want none / 1", store.failed, rec.count("agent_lost_gate_skip"))
		}
	})
	t.Run("dispatch-lost", func(t *testing.T) {
		store := &fakeReaperStore{queuedCands: []StaleQueuedCandidate{{TaskInstanceID: "stuck", QueuedAt: past}}}
		rec := &capturingRecorder{}
		r := newDispatchLostReaper(store, reapTestLogger(), time.Minute, rec)
		r.gate = closed
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.queuedMarked) != 0 || rec.count("dispatch_lost_gate_skip") != 1 {
			t.Errorf("queuedMarked=%v dispatch_lost_gate_skip=%d, want none / 1", store.queuedMarked, rec.count("dispatch_lost_gate_skip"))
		}
	})
	t.Run("pod-lost", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{{TaskInstanceID: "gone", DagRunID: "r", TaskID: "t", TryNumber: 1, RunningSince: past}}}
		rec := &capturingRecorder{}
		pods := &fakePodManager{active: map[string]bool{}}
		r := newPodLostReaper(store, reapTestLogger(), time.Minute, rec)
		r.pods = pods
		r.gate = closed
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 0 || len(pods.deletedTasks) != 0 || rec.count("pod_lost_gate_skip") != 1 {
			t.Errorf("marked=%v deleted=%v pod_lost_gate_skip=%d, want none / none / 1", store.marked, pods.deletedTasks, rec.count("pod_lost_gate_skip"))
		}
	})
	t.Run("warm-worker-lost", func(t *testing.T) {
		store := &fakeWarmBoundStore{bound: []WarmBoundTI{{TaskInstanceID: "warm-orphan", WarmWorkerID: "dead"}}}
		rec := &capturingRecorder{}
		r := newWarmWorkerLostReaper(store, reapTestLogger(), rec)
		r.warmPods = &fakeWarmLister{}
		r.gate = closed
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 0 || rec.count("warm_worker_lost_gate_skip") != 1 {
			t.Errorf("marked=%v warm_worker_lost_gate_skip=%d, want none / 1", store.marked, rec.count("warm_worker_lost_gate_skip"))
		}
	})
}

// TestReaperThreadsPresenceCacheToDispatchLost is the wiring check re-homed from
// the scheduler's TestStepThreadsPresenceCacheToReapers: a presence cache passed
// to NewReaper must reach the dispatch-lost reaper. A stale queued TI whose pod
// the cache shows as active is deferred, so ReapOnce does NOT fail it — proving
// the cache is threaded, not dropped on the floor.
func TestReaperThreadsPresenceCacheToDispatchLost(t *testing.T) {
	store := &fakeReaperStore{
		queuedCands: []StaleQueuedCandidate{
			{TaskInstanceID: "slow", DagRunID: "run-a", TaskID: "work", QueuedAt: time.Now().UTC().Add(-10 * time.Minute)},
		},
	}
	pods := &fakePodManager{active: map[string]bool{}}
	cache := &fakePresenceCache{active: map[string]bool{"run-a/work/0": true}}
	r := NewReaper(store, pods, cache, nil, nil, reapTestLogger(), DefaultReaperConfig(), nil)

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.queuedMarked) != 0 {
		t.Errorf("a cache-active queued TI must be deferred through ReapOnce, got %v", store.queuedMarked)
	}
	if pods.activeCalls != 0 {
		t.Errorf("a cache-active defer must skip the live LIST, got %d live calls", pods.activeCalls)
	}
}

// gateOpenThenClosed returns a destructiveGate that is open for the first n
// consultations and closed for every one after. It models a step-down or cancel
// that lands between two writes of the same reap, so a test can let the mark
// through and have the teardown that follows refused.
func gateOpenThenClosed(n int) destructiveGate {
	calls := 0
	return func(context.Context) bool {
		calls++
		return calls <= n
	}
}

// TestReapersHonorGateAtTeardown: the destructive gate is consulted a SECOND time
// right before the pod teardown that follows a successful mark — not only at the
// top of reapOne. With a gate that closes after the first consultation the DB
// mark applies (it was authorized), but the pod delete is refused and metered as
// <reaper>_teardown_gate_skip. The always-closed gate in
// TestReapersHonorGateMidTick can never reach this branch because the top check
// returns first; this test is the proof of the "re-checked before EVERY
// mark/delete" claim for the four reapers that own a teardown. The
// warm-worker-lost reaper deletes no pod (a warm worker outlives its attempts),
// so it has no teardown re-check to cover.
func TestReapersHonorGateAtTeardown(t *testing.T) {
	past := time.Now().UTC().Add(-1 * time.Hour)

	t.Run("orphan", func(t *testing.T) {
		store := &fakeReaperStore{orphanCands: []ReapCandidate{{RunID: "stuck", LastActivity: past}}}
		rec := &capturingRecorder{}
		pods := &fakePodManager{active: map[string]bool{}}
		r := newOrphanReaper(store, reapTestLogger(), time.Minute, rec)
		r.pods = pods
		r.gate = gateOpenThenClosed(1)
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.reapedRuns) != 1 {
			t.Errorf("the authorized mark must apply, reapedRuns=%v", store.reapedRuns)
		}
		if len(pods.deletedRuns) != 0 {
			t.Errorf("the teardown must be refused once the gate closed, deletedRuns=%v", pods.deletedRuns)
		}
		if got := rec.count("orphan_teardown_gate_skip"); got != 1 {
			t.Errorf("orphan_teardown_gate_skip = %d, want 1", got)
		}
	})
	t.Run("agent-lost", func(t *testing.T) {
		store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{{TaskInstanceID: "silent", DagRunID: "r", TaskID: "t", TryNumber: 1, LastHeartbeat: past}}}
		rec := &capturingRecorder{}
		pods := &fakePodManager{active: map[string]bool{}}
		r := newAgentLostReaper(store, reapTestLogger(), time.Minute, rec)
		r.pods = pods
		r.gate = gateOpenThenClosed(1)
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.failed) != 1 {
			t.Errorf("the authorized mark must apply, failed=%v", store.failed)
		}
		if len(pods.deletedTasks) != 0 {
			t.Errorf("the teardown must be refused once the gate closed, deletedTasks=%v", pods.deletedTasks)
		}
		if got := rec.count("agent_lost_teardown_gate_skip"); got != 1 {
			t.Errorf("agent_lost_teardown_gate_skip = %d, want 1", got)
		}
	})
	t.Run("dispatch-lost", func(t *testing.T) {
		store := &fakeReaperStore{queuedCands: []StaleQueuedCandidate{{TaskInstanceID: "stuck", DagRunID: "r", TaskID: "t", TryNumber: 1, QueuedAt: past}}}
		rec := &capturingRecorder{}
		pods := &fakePodManager{active: map[string]bool{}}
		r := newDispatchLostReaper(store, reapTestLogger(), time.Minute, rec)
		r.pods = pods
		r.gate = gateOpenThenClosed(1)
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.queuedMarked) != 1 {
			t.Errorf("the authorized mark must apply, queuedMarked=%v", store.queuedMarked)
		}
		if len(pods.deletedTasks) != 0 {
			t.Errorf("the teardown must be refused once the gate closed, deletedTasks=%v", pods.deletedTasks)
		}
		if got := rec.count("dispatch_lost_teardown_gate_skip"); got != 1 {
			t.Errorf("dispatch_lost_teardown_gate_skip = %d, want 1", got)
		}
	})
	t.Run("pod-lost", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{{TaskInstanceID: "gone", DagRunID: "r", TaskID: "t", TryNumber: 1, RunningSince: past}}}
		rec := &capturingRecorder{}
		pods := &fakePodManager{active: map[string]bool{}}
		r := newPodLostReaper(store, reapTestLogger(), time.Minute, rec)
		r.pods = pods
		r.gate = gateOpenThenClosed(1)
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 1 {
			t.Errorf("the authorized mark must apply, marked=%v", store.marked)
		}
		if len(pods.deletedTasks) != 0 {
			t.Errorf("the teardown must be refused once the gate closed, deletedTasks=%v", pods.deletedTasks)
		}
		if got := rec.count("pod_lost_teardown_gate_skip"); got != 1 {
			t.Errorf("pod_lost_teardown_gate_skip = %d, want 1", got)
		}
	})
}

// leadingOpenThenClosed returns a leadership predicate (the SetLeading shape)
// that reports leading for the first n consultations and not-leading after. It
// models a leadership loss that lands after ReapOnce's entry check and before
// the reapers reach their writes.
func leadingOpenThenClosed(n int) func() bool {
	calls := 0
	return func() bool {
		calls++
		return calls <= n
	}
}

// TestNewReaperWiresGateIntoEveryReaper: the destructive gate is only a
// guarantee if NewReaper hands it to all five reapers. TestReapersHonorGateMidTick
// drives each reaper with its own gate, so it cannot notice a dropped
// assignment in NewReaper — a reaper with a nil gate is unconditionally open.
// Here the whole Reaper is driven through ReapOnce with a leadership predicate
// that is open exactly once: the entry check consumes that one, so every
// reaper's own pre-write check finds it closed. Nothing may be destroyed and each
// reaper must meter its own gate skip exactly once — a reaper whose gate was
// not wired would write instead. The settling gate is open (no leadership
// stamp), so it does not stand between the entry check and the reapers.
//
// It uses its own gateWiringStore rather than the shared staleEverythingStore:
// the "exactly once" assertion is a statement about the candidate count, so a
// second candidate added to the shared fixture for another test's sake would
// fail this one with a message accusing the wiring (#924).
func TestNewReaperWiresGateIntoEveryReaper(t *testing.T) {
	store := gateWiringStore()
	pods := &fakePodManager{active: map[string]bool{}}
	rec := &capturingRecorder{}
	r := NewReaper(store, pods, nil, &fakeWarmLister{}, rec, reapTestLogger(), DefaultReaperConfig(), nil)
	r.SetLeading(leadingOpenThenClosed(1))

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	assertNothingDestroyed(t, store, pods)
	if got := rec.count("reap_gate_skip"); got != 0 {
		t.Errorf("the entry check must pass (it consumed the open consultation), reap_gate_skip = %d", got)
	}
	// Guard against a false accusation: if the settling gate ever stood between
	// the entry check and the reapers here, every count below would read 0 and
	// the message would blame the wiring. Nil predicates and no leadership stamp
	// must keep settling open in this test.
	if got := rec.count("reap_settling_skip"); got != 0 {
		t.Fatalf("settling must not stand between the entry check and the reapers in this test (candidates come from gateWiringStore, one per reaper), reap_settling_skip = %d", got)
	}
	for _, skip := range []string{
		"orphan_gate_skip",
		"agent_lost_gate_skip",
		"dispatch_lost_gate_skip",
		"pod_lost_gate_skip",
		"warm_worker_lost_gate_skip",
	} {
		if got := rec.count(skip); got != 1 {
			t.Errorf("%s = %d, want exactly 1 — gateWiringStore serves exactly one candidate per reaper, so is that reaper's gate wired in NewReaper?", skip, got)
		}
	}
}

// gateWiringStore serves exactly ONE long-stale candidate to each of the five
// destructive reapers, and exists solely for TestNewReaperWiresGateIntoEveryReaper:
// that test counts one gate skip per reaper, so its assertion is only as stable
// as the candidate count of the store behind it. The shared staleEverythingStore
// has four other callers; growing it to serve a second candidate for one of them
// would have failed the wiring test for a reason that has nothing to do with
// wiring (#924).
func gateWiringStore() *fakeReaperStore {
	past := time.Now().UTC().Add(-1 * time.Hour)
	return &fakeReaperStore{
		orphanCands:  []ReapCandidate{{RunID: "stuck-run", DagID: "etl", LastActivity: past}},
		agentCands:   []AgentLostCandidate{{TaskInstanceID: "silent", DagRunID: "r1", TaskID: "t", TryNumber: 1, LastHeartbeat: past}},
		queuedCands:  []StaleQueuedCandidate{{TaskInstanceID: "stuck-ti", DagRunID: "r2", TaskID: "t", TryNumber: 1, QueuedAt: past}},
		runningCands: []PodLostCandidate{{TaskInstanceID: "gone", DagRunID: "r3", TaskID: "t", TryNumber: 1, RunningSince: past}},
		warmBound:    []WarmBoundTI{{TaskInstanceID: "warm-orphan", DagRunID: "r4", TaskID: "t", TryNumber: 1, WarmWorkerID: "dead-worker"}},
	}
}
