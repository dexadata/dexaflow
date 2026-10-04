package executor

import (
	"context"
	"errors"
	"testing"
)

// fakeWarmLister serves a canned warm fleet and can inject a LIST error. It
// satisfies WarmPodLister (the read-only slice of the warm-pod client).
type fakeWarmLister struct {
	pods    []WarmPodInfo
	listErr error
}

func (f *fakeWarmLister) ListWarmPods(context.Context) ([]WarmPodInfo, error) {
	return f.pods, f.listErr
}

// fakeWarmBoundStore serves warm-bound running TIs and records pod_lost marks,
// satisfying WarmWorkerLostReapStore.
type fakeWarmBoundStore struct {
	bound    []WarmBoundTI
	listErr  error
	marked   []string
	markErr  error
	markNoop bool // MarkTaskPodLost reports 0 rows (a late settle raced)
}

func (f *fakeWarmBoundStore) ListWarmBoundRunningTIs(context.Context) ([]WarmBoundTI, error) {
	return f.bound, f.listErr
}

func (f *fakeWarmBoundStore) MarkTaskPodLost(_ context.Context, id string, _, _ int) (bool, error) {
	if f.markErr != nil {
		return false, f.markErr
	}
	if f.markNoop {
		return false, nil
	}
	f.marked = append(f.marked, id)
	return true, nil
}

func newWarmReaper(store WarmWorkerLostReapStore, lister WarmPodLister) *warmWorkerLostReaper {
	r := newWarmWorkerLostReaper(store, reapTestLogger(), nil)
	r.warmPods = lister
	return r
}

// TestWarmWorkerLostReaper_OnlyDeadWorkersReaped: an attempt bound to a worker
// NOT in the live set is failed as pod_lost; an attempt bound to a live worker
// is left alone. This is the failover contract — a warm pod dies mid-attempt and
// its in-flight work is recovered, while healthy workers keep serving.
func TestWarmWorkerLostReaper_OnlyDeadWorkersReaped(t *testing.T) {
	store := &fakeWarmBoundStore{bound: []WarmBoundTI{
		{TaskInstanceID: "ti-dead", DagRunID: "run-1", TaskID: "load", TryNumber: 1, WarmWorkerID: "pod-dead"},
		{TaskInstanceID: "ti-live", DagRunID: "run-2", TaskID: "xform", TryNumber: 1, WarmWorkerID: "pod-live"},
	}}
	lister := &fakeWarmLister{pods: []WarmPodInfo{
		{Name: "pod-live", Terminal: false},
	}}
	rec := &capturingRecorder{}
	r := newWarmWorkerLostReaper(store, reapTestLogger(), rec)
	r.warmPods = lister

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marked) != 1 || store.marked[0] != "ti-dead" {
		t.Fatalf("only the dead worker's attempt must be reaped, got %v", store.marked)
	}
	if got := rec.count("warm_worker_lost"); got != 1 {
		t.Errorf("warm_worker_lost meter = %d, want 1", got)
	}
}

// TestWarmWorkerLostReaper_FanOut: a single dead worker serving THREE attempts
// gets all three marked in one tick. This is the whole point of warm pools — one
// pod serves many attempts — so a worker's death must fan out to every attempt
// it held, not just one.
func TestWarmWorkerLostReaper_FanOut(t *testing.T) {
	store := &fakeWarmBoundStore{bound: []WarmBoundTI{
		{TaskInstanceID: "a", DagRunID: "run-1", TaskID: "t1", TryNumber: 1, WarmWorkerID: "pod-dead"},
		{TaskInstanceID: "b", DagRunID: "run-1", TaskID: "t2", TryNumber: 1, WarmWorkerID: "pod-dead"},
		{TaskInstanceID: "c", DagRunID: "run-2", TaskID: "t3", TryNumber: 1, WarmWorkerID: "pod-dead"},
	}}
	lister := &fakeWarmLister{pods: []WarmPodInfo{}} // pod-dead is gone from the fleet
	r := newWarmReaper(store, lister)
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marked) != 3 {
		t.Fatalf("a dead worker's 3 attempts must all be marked (fan-out), got %v", store.marked)
	}
}

// TestWarmWorkerLostReaper_TerminalWorkerIsDead: a bound attempt whose worker
// exists in the fleet BUT is terminal (Succeeded/Failed — can never serve again)
// is reaped, exactly as if the pod were gone. A terminal warm pod is not live.
func TestWarmWorkerLostReaper_TerminalWorkerIsDead(t *testing.T) {
	store := &fakeWarmBoundStore{bound: []WarmBoundTI{
		{TaskInstanceID: "ti-term", DagRunID: "run-1", TaskID: "load", TryNumber: 1, WarmWorkerID: "pod-term"},
	}}
	lister := &fakeWarmLister{pods: []WarmPodInfo{
		{Name: "pod-term", Terminal: true},
	}}
	r := newWarmReaper(store, lister)
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marked) != 1 || store.marked[0] != "ti-term" {
		t.Fatalf("a terminal worker must be treated as dead, got %v", store.marked)
	}
}

// TestWarmWorkerLostReaper_NilListerNoOp: with no lister (warm pools off / not
// wired) the reaper never touches the store — inert.
func TestWarmWorkerLostReaper_NilListerNoOp(t *testing.T) {
	store := &fakeWarmBoundStore{bound: []WarmBoundTI{
		{TaskInstanceID: "ti", DagRunID: "run-1", TaskID: "load", TryNumber: 1, WarmWorkerID: "pod-dead"},
	}}
	r := newWarmWorkerLostReaper(store, reapTestLogger(), nil) // r.warmPods stays nil
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marked) != 0 {
		t.Fatalf("nil lister must reap nothing, got %v", store.marked)
	}
}

// TestWarmWorkerLostReaper_EmptyBoundNoOp: with a live lister but no warm-bound
// attempts in flight, the reaper marks nothing.
func TestWarmWorkerLostReaper_EmptyBoundNoOp(t *testing.T) {
	store := &fakeWarmBoundStore{bound: nil}
	lister := &fakeWarmLister{pods: []WarmPodInfo{{Name: "pod-live"}}}
	r := newWarmReaper(store, lister)
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marked) != 0 {
		t.Fatalf("no bound attempts must reap nothing, got %v", store.marked)
	}
}

// TestWarmWorkerLostReaper_ListPodsErrorMarksNothing: if the warm-pod LIST fails,
// the live set is empty for this tick, so "do no harm" — nothing is proven dead,
// nothing is marked. (A blind mark-all on a transient apiserver blip would kill
// every live warm attempt.)
func TestWarmWorkerLostReaper_ListPodsErrorMarksNothing(t *testing.T) {
	store := &fakeWarmBoundStore{bound: []WarmBoundTI{
		{TaskInstanceID: "ti", DagRunID: "run-1", TaskID: "load", TryNumber: 1, WarmWorkerID: "pod-x"},
	}}
	lister := &fakeWarmLister{listErr: errors.New("apiserver unavailable")}
	r := newWarmReaper(store, lister)
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marked) != 0 {
		t.Fatalf("a warm-pod LIST error must reap nothing this tick, got %v", store.marked)
	}
}

// TestWarmWorkerLostReaper_ListBoundErrorSurfaces: a store list error is returned
// so ReapOnce can log it; nothing is reaped.
func TestWarmWorkerLostReaper_ListBoundErrorSurfaces(t *testing.T) {
	store := &fakeWarmBoundStore{listErr: errors.New("db down")}
	lister := &fakeWarmLister{pods: []WarmPodInfo{}}
	r := newWarmReaper(store, lister)
	if err := r.run(context.Background()); err == nil {
		t.Fatal("expected the store list error to surface")
	}
	if len(store.marked) != 0 {
		t.Fatalf("nothing should be reaped on a list error, got %v", store.marked)
	}
}

// TestWarmWorkerLostReaper_NoopMarkIsBenign: a MarkTaskPodLost returning
// applied=false (a late terminal report settled the row between the list and the
// write) is a benign skip — metered as a no-op, not a false reap.
func TestWarmWorkerLostReaper_NoopMarkIsBenign(t *testing.T) {
	store := &fakeWarmBoundStore{
		bound:    []WarmBoundTI{{TaskInstanceID: "raced", DagRunID: "run-1", TaskID: "load", TryNumber: 1, WarmWorkerID: "pod-dead"}},
		markNoop: true,
	}
	lister := &fakeWarmLister{pods: []WarmPodInfo{}}
	rec := &capturingRecorder{}
	r := newWarmWorkerLostReaper(store, reapTestLogger(), rec)
	r.warmPods = lister
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := rec.count("warm_worker_lost_noop"); got != 1 {
		t.Errorf("warm_worker_lost_noop meter = %d, want 1", got)
	}
	if got := rec.count("warm_worker_lost"); got != 0 {
		t.Errorf("a no-op mark must not meter a real reap, got %d", got)
	}
}

// TestWarmWorkerLostReaper_PerTIErrorIsolated: a mark failure on one attempt does
// not stall the others (same isolation as the sibling reapers). The reaper never
// returns a per-TI error.
func TestWarmWorkerLostReaper_PerTIErrorIsolated(t *testing.T) {
	store := &fakeWarmBoundStore{
		bound: []WarmBoundTI{
			{TaskInstanceID: "a", DagRunID: "run-1", TaskID: "t1", TryNumber: 1, WarmWorkerID: "pod-dead"},
			{TaskInstanceID: "b", DagRunID: "run-1", TaskID: "t2", TryNumber: 1, WarmWorkerID: "pod-dead"},
		},
		markErr: errors.New("write failed"),
	}
	lister := &fakeWarmLister{pods: []WarmPodInfo{}}
	r := newWarmReaper(store, lister)
	if err := r.run(context.Background()); err != nil {
		t.Errorf("run err = %v, want nil (per-TI errors isolated)", err)
	}
}

// panickyWarmStore panics on the list to prove the reaper's recover keeps the
// scheduler tick alive.
type panickyWarmStore struct{}

func (panickyWarmStore) ListWarmBoundRunningTIs(context.Context) ([]WarmBoundTI, error) {
	panic("boom")
}
func (panickyWarmStore) MarkTaskPodLost(context.Context, string, int, int) (bool, error) {
	return true, nil
}

// TestWarmWorkerLostReaper_PanicRecovered: a panic anywhere in run is recovered,
// mirroring the sibling reapers, so one bad tick never crashes the scheduler.
func TestWarmWorkerLostReaper_PanicRecovered(t *testing.T) {
	r := newWarmReaper(panickyWarmStore{}, &fakeWarmLister{pods: []WarmPodInfo{}})
	// Must not panic out of run.
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run returned err after recovered panic: %v", err)
	}
}
