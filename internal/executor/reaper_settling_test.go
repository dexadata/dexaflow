package executor

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

// settlingHarness builds a Reaper over a stale-everything store with every
// settling input injected: a fixed clock, the leadership stamp, the informer
// sync flag and the reconciler's last completed sweep. Each test flips exactly
// the input it is about, so the settling predicate is exercised one condition
// at a time with no wall-clock dependence.
type settlingHarness struct {
	store  *fakeReaperStore
	pods   *fakePodManager
	rec    *capturingRecorder
	logs   *bytes.Buffer
	reaper *Reaper
	now    time.Time
	since  time.Time // leadership acquired
	synced bool
	sweep  time.Time // reconciler's last completed sweep; zero = never
}

func newSettlingHarness(t *testing.T) *settlingHarness {
	t.Helper()
	h := &settlingHarness{
		store: staleEverythingStore(),
		pods:  &fakePodManager{active: map[string]bool{}},
		rec:   &capturingRecorder{},
		logs:  &bytes.Buffer{},
		now:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	logger := slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	h.reaper = NewReaper(h.store, h.pods, nil, &fakeWarmLister{}, h.rec, logger, DefaultReaperConfig(), ReaperGates{})
	h.reaper.now = func() time.Time { return h.now }
	h.reaper.SetLeaderSince(func() time.Time { return h.since })
	h.reaper.SetInformerSynced(func() bool { return h.synced })
	h.reaper.SetLastSweepCompleted(func() time.Time { return h.sweep })
	return h
}

// settle puts every settling input into its "satisfied" state: leadership
// acquired a full grace ago, informer synced, one sweep completed since.
func (h *settlingHarness) settle() {
	grace := DefaultReaperConfig().SettlingGrace
	h.since = h.now.Add(-grace)
	h.synced = true
	h.sweep = h.now.Add(-time.Second)
}

func (h *settlingHarness) reap(t *testing.T) {
	t.Helper()
	if err := h.reaper.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
}

// assertEverythingReaped is the positive control: with the gate open every one
// of the five reapers acts on its stale candidate.
func assertEverythingReaped(t *testing.T, store *fakeReaperStore) {
	t.Helper()
	if len(store.reapedRuns) != 1 || len(store.agentMarked) != 1 || len(store.queuedMarked) != 1 || len(store.podMarked) != 2 {
		t.Errorf("an open gate must let every reaper act: reapedRuns=%v agentMarked=%v queuedMarked=%v podMarked=%v",
			store.reapedRuns, store.agentMarked, store.queuedMarked, store.podMarked)
	}
}

// TestReapOnceSkipsWhileSettling: after a (re-)election the whole reaper tick is
// a no-op until the leader has SETTLED — the grace has elapsed, the pod informer
// has synced, and the reconciler has completed a sweep under this leadership.
// Each condition alone must hold the tick: a control-plane restart manufactures
// the signals several reapers act on (stale heartbeats, exited task pods, quiet
// runs), and only a completed post-leadership sweep separates "finished during
// the outage" from "lost". The skip is metered so operators can see it.
func TestReapOnceSkipsWhileSettling(t *testing.T) {
	grace := DefaultReaperConfig().SettlingGrace
	cases := map[string]func(h *settlingHarness){
		"grace not elapsed":          func(h *settlingHarness) { h.since = h.now.Add(-grace + time.Second) },
		"informer not synced":        func(h *settlingHarness) { h.synced = false },
		"no sweep since leadership":  func(h *settlingHarness) { h.sweep = time.Time{} },
		"last sweep predates leader": func(h *settlingHarness) { h.sweep = h.since.Add(-time.Second) },
	}
	for name, unsettle := range cases {
		t.Run(name, func(t *testing.T) {
			h := newSettlingHarness(t)
			h.settle()
			unsettle(h)
			h.reap(t)
			assertNothingDestroyed(t, h.store, h.pods)
			if got := h.rec.count("reap_settling_skip"); got != 1 {
				t.Errorf("reap_settling_skip = %d, want 1", got)
			}
			if got := h.rec.count("reap_settling_valve_open"); got != 0 {
				t.Errorf("the valve must stay shut inside 2×grace, reap_settling_valve_open = %d", got)
			}
		})
	}
}

// TestReapOnceActiveOnceSettled: with all three conditions satisfied the tick
// runs every reaper normally and records no settling decision.
func TestReapOnceActiveOnceSettled(t *testing.T) {
	h := newSettlingHarness(t)
	h.settle()
	h.reap(t)
	assertEverythingReaped(t, h.store)
	if got := h.rec.count("reap_settling_skip") + h.rec.count("reap_settling_valve_open"); got != 0 {
		t.Errorf("a settled leader must record no settling decision, got %d", got)
	}
}

// TestReapOnceSettlingValveOpens is the liveness valve: a reconciler that never
// completes a sweep (or an informer that never syncs) must not turn "reap wrong"
// into "never reap". Once settling has lasted 2×grace the tick proceeds anyway,
// with a WARN and a reap_settling_valve_open decision on every cycle it stays
// open, so the broken sweep is loud rather than silently disabling recovery.
func TestReapOnceSettlingValveOpens(t *testing.T) {
	grace := DefaultReaperConfig().SettlingGrace
	t.Run("no sweep", func(t *testing.T) {
		h := newSettlingHarness(t)
		h.settle()
		h.sweep = time.Time{}
		h.since = h.now.Add(-2 * grace)
		h.reap(t)
		assertEverythingReaped(t, h.store)
		if got := h.rec.count("reap_settling_valve_open"); got != 1 {
			t.Errorf("reap_settling_valve_open = %d, want 1", got)
		}
		if got := h.rec.count("reap_settling_skip"); got != 0 {
			t.Errorf("an open valve is not a skip, reap_settling_skip = %d", got)
		}
		if !strings.Contains(h.logs.String(), "level=WARN") || !strings.Contains(h.logs.String(), "valve") {
			t.Errorf("the open valve must be logged at WARN naming the valve, got %q", h.logs.String())
		}
	})
	t.Run("informer never synced", func(t *testing.T) {
		h := newSettlingHarness(t)
		h.settle()
		h.synced = false
		h.since = h.now.Add(-2 * grace)
		h.reap(t)
		assertEverythingReaped(t, h.store)
		if got := h.rec.count("reap_settling_valve_open"); got != 1 {
			t.Errorf("reap_settling_valve_open = %d, want 1", got)
		}
	})
	t.Run("just under 2×grace stays shut", func(t *testing.T) {
		h := newSettlingHarness(t)
		h.settle()
		h.sweep = time.Time{}
		h.since = h.now.Add(-2*grace + time.Second)
		h.reap(t)
		assertNothingDestroyed(t, h.store, h.pods)
		if got := h.rec.count("reap_settling_valve_open"); got != 0 {
			t.Errorf("reap_settling_valve_open = %d, want 0", got)
		}
	})
}

// TestReapOnceDrillRaceHoldsPodLostUntilSweep replays the production drill: a
// task pod finished while the control plane was down, so its TI is still
// `running` and no live pod exists; leadership was acquired just now. The
// pod-lost reaper must NOT mark it — not while the grace runs, and not even
// after the grace if no reconciler sweep has completed under this leadership,
// because only that sweep can recover the pod's durable success record. Once a
// sweep has completed the still-unsettled TI is reaped normally.
func TestReapOnceDrillRaceHoldsPodLostUntilSweep(t *testing.T) {
	grace := DefaultReaperConfig().SettlingGrace
	h := newSettlingHarness(t)
	h.store = &fakeReaperStore{runningCands: []PodLostCandidate{
		{TaskInstanceID: "finished-during-outage", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: h.now.Add(-10 * time.Minute)},
	}}
	h.reaper = NewReaper(h.store, h.pods, nil, nil, h.rec, reapTestLogger(), DefaultReaperConfig(), ReaperGates{})
	h.reaper.now = func() time.Time { return h.now }
	h.reaper.SetLeaderSince(func() time.Time { return h.since })
	h.reaper.SetInformerSynced(func() bool { return h.synced })
	h.reaper.SetLastSweepCompleted(func() time.Time { return h.sweep })
	h.since = h.now // leadership acquired this instant
	h.synced = true

	h.reap(t)
	if len(h.store.podMarked) != 0 {
		t.Fatalf("no pod-lost mark while the grace runs, got %v", h.store.podMarked)
	}

	h.now = h.now.Add(grace) // grace elapsed, still no sweep
	h.reap(t)
	if len(h.store.podMarked) != 0 {
		t.Fatalf("no pod-lost mark before a post-leadership sweep completed, got %v", h.store.podMarked)
	}
	if got := h.rec.count("reap_settling_skip"); got != 2 {
		t.Errorf("reap_settling_skip = %d, want 2", got)
	}

	h.sweep = h.now // the reconciler completed a sweep; the TI is still unsettled
	h.reap(t)
	if len(h.store.podMarked) != 1 || h.store.podMarked[0] != "finished-during-outage" {
		t.Errorf("a TI still unsettled after a completed sweep is genuinely lost, got %v", h.store.podMarked)
	}
}

// TestReapOnceSettlingCoversAgentLost carries the agent-lost half of the old
// per-reaper grace up to the single gate: a TI whose heartbeat went stale during
// a control-plane outage is not failed agent_lost by a leader that has not
// settled; once settled, a still-silent TI is reaped. The invariant is the same
// as before; it is now enforced in one place for every reaper.
func TestReapOnceSettlingCoversAgentLost(t *testing.T) {
	h := newSettlingHarness(t)
	h.store = &fakeReaperStore{agentCands: []AgentLostCandidate{
		{TaskInstanceID: "stale", DagRunID: "r", TaskID: "t", TryNumber: 1, LastHeartbeat: h.now.Add(-2 * time.Minute)},
	}}
	h.reaper = NewReaper(h.store, h.pods, nil, nil, h.rec, reapTestLogger(), DefaultReaperConfig(), ReaperGates{})
	h.reaper.now = func() time.Time { return h.now }
	h.reaper.SetLeaderSince(func() time.Time { return h.since })
	h.reaper.SetInformerSynced(func() bool { return h.synced })
	h.reaper.SetLastSweepCompleted(func() time.Time { return h.sweep })
	h.settle()
	h.since = h.now.Add(-10 * time.Second) // just became leader

	h.reap(t)
	if len(h.store.agentMarked) != 0 {
		t.Fatalf("must not reap agent_lost before the leader settled, got %v", h.store.agentMarked)
	}
	h.settle()
	h.reap(t)
	if len(h.store.agentMarked) != 1 || h.store.agentMarked[0] != "stale" {
		t.Errorf("must reap agent_lost once settled, got %v", h.store.agentMarked)
	}
}

// TestReapOnceNilSettlingPredicatesAreOpen: Lite wires no leadership, no
// informer and no reconciler, so every settling input is nil — and nil means
// "open", exactly as the nil destructive-gate inputs do. Nothing changes on the
// subprocess path. A partially wired reaper (leadership but no informer or
// sweep record) likewise gates only on what it was given.
func TestReapOnceNilSettlingPredicatesAreOpen(t *testing.T) {
	t.Run("nothing wired", func(t *testing.T) {
		store := staleEverythingStore()
		rec := &capturingRecorder{}
		r := NewReaper(store, &fakePodManager{active: map[string]bool{}}, nil, &fakeWarmLister{}, rec, reapTestLogger(), DefaultReaperConfig(), ReaperGates{})
		if err := r.ReapOnce(context.Background()); err != nil {
			t.Fatalf("ReapOnce: %v", err)
		}
		assertEverythingReaped(t, store)
		if got := rec.count("reap_settling_skip") + rec.count("reap_settling_valve_open"); got != 0 {
			t.Errorf("nil predicates must record no settling decision, got %d", got)
		}
	})
	t.Run("leadership only, grace elapsed", func(t *testing.T) {
		store := staleEverythingStore()
		r := NewReaper(store, &fakePodManager{active: map[string]bool{}}, nil, &fakeWarmLister{}, nil, reapTestLogger(), DefaultReaperConfig(), ReaperGates{})
		r.SetLeaderSince(func() time.Time { return time.Now().Add(-time.Hour) })
		if err := r.ReapOnce(context.Background()); err != nil {
			t.Fatalf("ReapOnce: %v", err)
		}
		assertEverythingReaped(t, store)
	})
	t.Run("not leading (zero stamp) leaves the settling gate open", func(t *testing.T) {
		// mayReap owns the not-leading case; a zero leadership stamp must not be
		// read as "settling forever".
		store := staleEverythingStore()
		r := NewReaper(store, &fakePodManager{active: map[string]bool{}}, nil, &fakeWarmLister{}, nil, reapTestLogger(), DefaultReaperConfig(), ReaperGates{})
		r.SetLeaderSince(func() time.Time { return time.Time{} })
		r.SetLastSweepCompleted(func() time.Time { return time.Time{} })
		if err := r.ReapOnce(context.Background()); err != nil {
			t.Fatalf("ReapOnce: %v", err)
		}
		assertEverythingReaped(t, store)
	})
}

// TestReapOnceSettlesAfterFirstReconcileSweep wires the real reconciler's sweep
// record into the reaper: before the reconciler has swept under this leadership
// the tick is held; the moment one Reconcile completes, the gate opens. This is
// the seam the maintenance loop relies on — reconcile, then reap — expressed
// without the loop.
func TestReapOnceSettlesAfterFirstReconcileSweep(t *testing.T) {
	grace := DefaultReaperConfig().SettlingGrace
	rec := NewReconciler(fake.NewClientset(), "leoflow", &fakeReporter{})
	store := staleEverythingStore()
	r := NewReaper(store, &fakePodManager{active: map[string]bool{}}, nil, &fakeWarmLister{}, nil, reapTestLogger(), DefaultReaperConfig(), ReaperGates{})
	r.SetLeaderSince(func() time.Time { return time.Now().Add(-grace - time.Second) })
	r.SetInformerSynced(func() bool { return true })
	r.SetLastSweepCompleted(rec.LastSweepCompletedAt)

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.reapedRuns)+len(store.agentMarked)+len(store.queuedMarked)+len(store.podMarked) != 0 {
		t.Fatal("the tick must be held until the reconciler has swept under this leadership")
	}
	if err := rec.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	assertEverythingReaped(t, store)
}

// TestDefaultReaperConfigSettlingGrace pins the ladder position of the one
// post-leadership grace: twice the agent-lost threshold, so a restart plus
// re-election (whose recorded silence can approach the threshold) cannot trip a
// mass false reap, and well above two 30s maintenance cycles so a settle that
// failed transiently on the first sweep is retried before the gate can open.
func TestDefaultReaperConfigSettlingGrace(t *testing.T) {
	cfg := DefaultReaperConfig()
	if cfg.SettlingGrace != 2*cfg.AgentLostThreshold {
		t.Errorf("SettlingGrace = %v, want 2 × AgentLostThreshold (%v)", cfg.SettlingGrace, 2*cfg.AgentLostThreshold)
	}
	if cfg.SettlingGrace <= 2*30*time.Second {
		t.Errorf("SettlingGrace = %v must exceed two 30s maintenance cycles", cfg.SettlingGrace)
	}
}
