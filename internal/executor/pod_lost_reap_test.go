package executor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakePodLostStore serves running-TI candidates and records pod_lost marks.
type fakePodLostStore struct {
	candidates []PodLostCandidate
	listErr    error
	marked     []string
	markErr    error
	markNoop   bool // when true, MarkTaskPodLost reports 0 rows updated (a late terminal report won the race)
	listGrace  []time.Duration
}

func (f *fakePodLostStore) ListRunningTasks(_ context.Context, grace time.Duration) ([]PodLostCandidate, error) {
	f.listGrace = append(f.listGrace, grace)
	return f.candidates, f.listErr
}

func (f *fakePodLostStore) MarkTaskPodLost(_ context.Context, id string, _, _ int) (bool, error) {
	if f.markErr != nil {
		return false, f.markErr
	}
	if f.markNoop {
		return false, nil
	}
	f.marked = append(f.marked, id)
	return true, nil
}

func TestIsPodLostCandidate(t *testing.T) {
	const grace = 60 * time.Second
	now := time.Now().UTC()
	cases := []struct {
		name  string
		since time.Time
		want  bool
	}{
		{"zero running-since is alive (do no harm)", time.Time{}, false},
		{"within grace is not yet a candidate", now.Add(-30 * time.Second), false},
		{"exactly at grace is a candidate", now.Add(-60 * time.Second), true},
		{"well past grace is a candidate", now.Add(-5 * time.Minute), true},
		{"future running-since (clock skew) is alive", now.Add(1 * time.Second), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsPodLostCandidate(PodLostCandidate{RunningSince: tc.since}, grace, now); got != tc.want {
				t.Errorf("IsPodLostCandidate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodLostReaper(t *testing.T) {
	const grace = 60 * time.Second
	now := time.Now().UTC()
	past := now.Add(-2 * time.Minute) // comfortably past the grace period

	// newReaper wires a K8s reaper (pods set); the Lite case sets pods=nil.
	newReaper := func(store PodLostReapStore, pods PodManager) *podLostReaper {
		r := newPodLostReaper(store, reapTestLogger(), grace, nil)
		r.pods = pods
		return r
	}

	t.Run("running TI past grace with no live pod is failed as pod_lost + torn down", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{
			{TaskInstanceID: "lost", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
		}}
		pods := &fakePodManager{active: map[string]bool{}} // no live pod for run-a/work
		if err := newReaper(store, pods).run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 1 || store.marked[0] != "lost" {
			t.Fatalf("expected TI 'lost' marked pod_lost, got %v", store.marked)
		}
		if len(pods.deletedTasks) != 1 || pods.deletedTasks[0] != (deletedTask{"run-a", "work", 1}) {
			t.Fatalf("expected best-effort teardown of (run-a,work,try 1), got %v", pods.deletedTasks)
		}
	})

	t.Run("a live pod defers — never reaped", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{
			{TaskInstanceID: "live", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
		}}
		pods := &fakePodManager{active: map[string]bool{"run-a/work/1": true}}
		if err := newReaper(store, pods).run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 0 {
			t.Fatalf("a running TI with a live pod must not be reaped, got %v", store.marked)
		}
	})

	// The store applies the grace period before its LIMIT, so attempts still
	// inside it never crowd out the ones the reaper can act on.
	t.Run("the grace period is handed to the store", func(t *testing.T) {
		store := &fakePodLostStore{}
		pods := &fakePodManager{active: map[string]bool{}}
		if err := newReaper(store, pods).run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.listGrace) != 1 || store.listGrace[0] != grace {
			t.Fatalf("ListRunningTasks grace = %v, want [%v]", store.listGrace, grace)
		}
	})

	t.Run("within the grace period is not reaped (do no harm)", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{
			{TaskInstanceID: "fresh", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: now.Add(-10 * time.Second)},
		}}
		pods := &fakePodManager{active: map[string]bool{}}
		if err := newReaper(store, pods).run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 0 {
			t.Fatalf("a freshly-running TI must not be reaped, got %v", store.marked)
		}
	})

	t.Run("pod liveness query error defers (do no harm)", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{
			{TaskInstanceID: "unknown", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
		}}
		pods := &fakePodManager{activeErr: errors.New("apiserver unavailable")}
		if err := newReaper(store, pods).run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 0 {
			t.Fatalf("liveness-unknown must defer, got %v", store.marked)
		}
	})

	t.Run("Lite (nil PodManager) is a no-op — never reaps a subprocess task", func(t *testing.T) {
		store := &fakePodLostStore{candidates: []PodLostCandidate{
			{TaskInstanceID: "subproc", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
		}}
		r := newPodLostReaper(store, reapTestLogger(), grace, nil) // r.pods stays nil
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(store.marked) != 0 {
			t.Fatalf("Lite must not reap on 'no pod' (there are no pods), got %v", store.marked)
		}
	})

	t.Run("a no-op mark (late terminal report won) skips the teardown", func(t *testing.T) {
		store := &fakePodLostStore{
			candidates: []PodLostCandidate{
				{TaskInstanceID: "raced", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
			},
			markNoop: true,
		}
		pods := &fakePodManager{active: map[string]bool{}} // no live pod → would reap, but the mark no-ops
		if err := newReaper(store, pods).run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(pods.deletedTasks) != 0 {
			t.Fatalf("a no-op mark (0 rows) must not tear down a pod, got %v", pods.deletedTasks)
		}
	})

	t.Run("a list error surfaces and reaps nothing", func(t *testing.T) {
		store := &fakePodLostStore{listErr: errors.New("db down")}
		pods := &fakePodManager{active: map[string]bool{}}
		if err := newReaper(store, pods).run(context.Background()); err == nil {
			t.Fatal("expected the list error to surface")
		}
		if len(store.marked) != 0 {
			t.Fatalf("nothing should be reaped on a list error, got %v", store.marked)
		}
	})
}

// TestPodLostReaper_TerminalPodIsTheReconcilersToSettle is the load-bearing
// separation between "the pod is gone" and "the pod is finished but still
// there". Pod-lost used to see one bool for both and reap on either: it marked
// the task instance pod_lost and then DELETED the pod, destroying the
// termination log the reconciler recovers the attempt's durable outcome from —
// so a task that had SUCCEEDED read as failed, and its run with it. A pod that
// is present in a terminal phase must therefore produce no state write, no pod
// delete, and one metered defer; only a genuine absence authorizes the reap.
func TestPodLostReaper_TerminalPodIsTheReconcilersToSettle(t *testing.T) {
	const grace = 60 * time.Second
	past := time.Now().UTC().Add(-2 * time.Minute) // comfortably past the grace

	cases := []struct {
		name string
		// pods is the pod-manager fixture for the one candidate below.
		pods *fakePodManager
		// wantMarked is whether the TI is transitioned to pod_lost.
		wantMarked bool
		// wantDeleted is whether the attempt's pod is torn down.
		wantDeleted bool
		// wantDecisions is the exact count expected for each decision named.
		wantDecisions map[string]int
	}{
		{
			name:          "present terminal pod defers to the reconciler",
			pods:          &fakePodManager{terminal: map[string]bool{"run-a/work/1": true}},
			wantMarked:    false,
			wantDeleted:   false,
			wantDecisions: map[string]int{"pod_lost_terminal_pod_defer": 1, "pod_lost": 0},
		},
		{
			name:          "genuine absence still reaps",
			pods:          &fakePodManager{}, // neither live nor terminal: no pod at all
			wantMarked:    true,
			wantDeleted:   true,
			wantDecisions: map[string]int{"pod_lost_terminal_pod_defer": 0, "pod_lost": 1},
		},
		{
			name:          "live pod defers without a terminal decision",
			pods:          &fakePodManager{active: map[string]bool{"run-a/work/1": true}},
			wantMarked:    false,
			wantDeleted:   false,
			wantDecisions: map[string]int{"pod_lost_terminal_pod_defer": 0, "pod_lost": 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakePodLostStore{candidates: []PodLostCandidate{
				{TaskInstanceID: "ti", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
			}}
			rec := &capturingRecorder{}
			r := newPodLostReaper(store, reapTestLogger(), grace, rec)
			r.pods = tc.pods

			if err := r.run(context.Background()); err != nil {
				t.Fatalf("run: %v", err)
			}
			if marked := len(store.marked) > 0; marked != tc.wantMarked {
				t.Errorf("marked pod_lost = %v (%v), want %v", marked, store.marked, tc.wantMarked)
			}
			if deleted := len(tc.pods.deletedTasks) > 0; deleted != tc.wantDeleted {
				t.Errorf("pod deleted = %v (%v), want %v", deleted, tc.pods.deletedTasks, tc.wantDeleted)
			}
			for decision, want := range tc.wantDecisions {
				if got := rec.count(decision); got != want {
					t.Errorf("decision %q metered %d times, want %d", decision, got, want)
				}
			}
		})
	}
}

// TestPodLostReaper_UnreadableApiserverFailsClosed locks the CONSEQUENCE the
// leader-settling gate's liveness valve leans on (see Reaper.settling), not its
// premise: this reaper never reaps on a failed pod read. The premise — that
// whatever stops the reconciler's broad sweep also denies this narrow read — is
// usually but not always true (see Reaper.settling for how the two diverge), so
// what a test can pin is the fail-closed behavior on an apiserver that is
// unreachable,
// unauthorized, throttled — also denies pod-lost its only authorization to reap.
// The valve may therefore open on a broken sweep without pod-lost turning that
// into a false reap. If this reaper ever reaps on a query error, the valve stops
// being safe and this test must fail.
//
// The cache is wired with a MISS in the second case: a fall-through from a cold
// or lagged cache lands on the same failing live read, so it authorizes nothing
// either.
func TestPodLostReaper_UnreadableApiserverFailsClosed(t *testing.T) {
	const grace = 60 * time.Second
	past := time.Now().UTC().Add(-2 * time.Minute)

	cases := []struct {
		name  string
		cache PodPresenceCache
	}{
		{"no cache wired: the live read is the only authorization", nil},
		{"cache miss falls through to the same failing read", &fakePresenceCache{active: map[string]bool{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakePodLostStore{candidates: []PodLostCandidate{
				{TaskInstanceID: "unreadable", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: past},
			}}
			pods := &fakePodManager{activeErr: errors.New("Get \"https://10.0.0.1:443/api/v1/pods\": dial tcp: i/o timeout")}
			rec := &capturingRecorder{}
			r := newPodLostReaper(store, reapTestLogger(), grace, rec)
			r.pods = pods
			r.cache = tc.cache

			if err := r.run(context.Background()); err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(store.marked) != 0 {
				t.Errorf("reaped on an unreadable apiserver: %v", store.marked)
			}
			if len(pods.deletedTasks) != 0 {
				t.Errorf("deleted a pod on an unreadable apiserver: %v", pods.deletedTasks)
			}
			if got := rec.count("pod_lost_pod_query_error"); got != 1 {
				t.Errorf("pod_lost_pod_query_error metered %d times, want 1", got)
			}
			if got := rec.count("pod_lost"); got != 0 {
				t.Errorf("pod_lost metered %d times, want 0", got)
			}
		})
	}
}
