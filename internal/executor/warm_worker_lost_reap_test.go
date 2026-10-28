package executor

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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
	// oomMarked records the attempts marked through the OOMKilled variant.
	oomMarked []string
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

func (f *fakeWarmBoundStore) MarkTaskPodLostOOMKilled(_ context.Context, id string, _, _ int) (bool, error) {
	if f.markErr != nil {
		return false, f.markErr
	}
	if f.markNoop {
		return false, nil
	}
	f.oomMarked = append(f.oomMarked, id)
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
func (panickyWarmStore) MarkTaskPodLostOOMKilled(context.Context, string, int, int) (bool, error) {
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

// TestWarmWorkerLostReaper_WritesKilledMarker: every attempt a dead warm worker
// held ends its own log with one "killed: pod_lost" line naming the worker, so
// the log does not stop mid-line with no cause (#861). An attempt on a live
// worker, or one whose mark was a noop, gets nothing.
func TestWarmWorkerLostReaper_WritesKilledMarker(t *testing.T) {
	store := &fakeWarmBoundStore{bound: []WarmBoundTI{
		{TaskInstanceID: "ti-a", TenantID: "t1", DagID: "dag-x", DagRunID: "run-1", TaskID: "load", TryNumber: 1, AttemptEpoch: 2, WarmWorkerID: "pod-dead"},
		{TaskInstanceID: "ti-b", TenantID: "t1", DagID: "dag-x", DagRunID: "run-2", TaskID: "load", TryNumber: 3, WarmWorkerID: "pod-dead"},
		{TaskInstanceID: "ti-live", TenantID: "t1", DagID: "dag-x", DagRunID: "run-3", TaskID: "load", TryNumber: 1, WarmWorkerID: "pod-live"},
	}}
	sink := &fakeLogSink{}
	r := newWarmReaper(store, &fakeWarmLister{pods: []WarmPodInfo{{Name: "pod-live"}}})
	r.sink = sink
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, key := range []string{"t1/dag-x/run-1/load/1", "t1/dag-x/run-2/load/3"} {
		evs := sink.events[key]
		if len(evs) != 1 {
			t.Fatalf("%s: want exactly one marker, got %v", key, sink.events)
		}
		if !strings.HasPrefix(evs[0].Message, "killed: pod_lost") || !strings.Contains(evs[0].Message, "pod-dead") {
			t.Errorf("%s: marker = %q, want a killed: pod_lost line naming the worker", key, evs[0].Message)
		}
	}
	if _, ok := sink.events["t1/dag-x/run-3/load/1"]; ok {
		t.Errorf("an attempt on a live worker must not get a marker")
	}

	noop := &fakeWarmBoundStore{bound: store.bound[:1], markNoop: true}
	sink = &fakeLogSink{}
	r = newWarmReaper(noop, &fakeWarmLister{})
	r.sink = sink
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(sink.events) != 0 {
		t.Errorf("a noop mark must not write a marker, got %v", sink.events)
	}
}

// TestWarmWorkerLostReaper_NamesOOMKill: on cgroup v2 an OOM kills the whole
// warm-worker container, so the reaper is what settles the attempt (#1218).
// When the dead worker is still listed with an OOMKilled container, the attempt
// is marked through the OOMKilled variant (still infra, ADR 0051 and ADR 0058
// D7) and the marker names the OOM kill. A worker that is already gone, or one
// that terminated for another reason, keeps today's mark and text.
func TestWarmWorkerLostReaper_NamesOOMKill(t *testing.T) {
	bound := func(id, worker string) WarmBoundTI {
		return WarmBoundTI{TaskInstanceID: id, TenantID: "t1", DagID: "d", DagRunID: "run-" + id, TaskID: "load", TryNumber: 1, WarmWorkerID: worker}
	}
	store := &fakeWarmBoundStore{bound: []WarmBoundTI{
		bound("oom", "pod-oom"),
		bound("gone", "pod-gone"),
		bound("error", "pod-error"),
	}}
	lister := &fakeWarmLister{pods: []WarmPodInfo{
		{Name: "pod-oom", Terminal: true, TerminationReason: "OOMKilled"},
		{Name: "pod-error", Terminal: true, TerminationReason: "Error"},
	}}
	sink := &fakeLogSink{}
	r := newWarmReaper(store, lister)
	r.sink = sink
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !reflect.DeepEqual(store.oomMarked, []string{"oom"}) {
		t.Errorf("OOMKilled marks = %v, want [oom]", store.oomMarked)
	}
	if !reflect.DeepEqual(store.marked, []string{"gone", "error"}) {
		t.Errorf("plain pod_lost marks = %v, want [gone error]", store.marked)
	}
	oom := sink.events["t1/d/run-oom/load/1"]
	if len(oom) != 1 || !strings.HasPrefix(oom[0].Message, "killed: pod_lost") || !strings.Contains(oom[0].Message, "OOMKilled") {
		t.Errorf("OOM marker = %v, want one killed: pod_lost line naming OOMKilled", oom)
	}
	for _, key := range []string{"t1/d/run-gone/load/1", "t1/d/run-error/load/1"} {
		evs := sink.events[key]
		if len(evs) != 1 || strings.Contains(evs[0].Message, "OOMKilled") {
			t.Errorf("%s marker = %v, want today's text with no OOM claim", key, evs)
		}
	}
}

// oomKilledWarmPod is a terminal warm worker whose warm-worker container the
// kubelet reported OOMKilled, as a cgroup v2 OOM in a task leaves it.
func oomKilledWarmPod(name string) *corev1.Pod {
	p := warmInformerPod(name, "dv-1", corev1.PodFailed)
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: warmContainerName,
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			Reason: "OOMKilled", ExitCode: 137,
		}},
	}}
	return p
}

// TestWarmWorkerLostReaper_NamesOOMKillOfDeletedWorker: the common case after a
// cgroup v2 OOM is that the warm worker is already gone when the reaper runs, so
// its container status cannot be listed (#1835). The informer saw the pod's final
// state on its delete and recorded it on the warm-pod client, so the reaper still
// marks the attempt through the OOMKilled variant and the marker names the OOM
// kill. A deleted worker with no recorded terminal state keeps the plain
// pod_lost text.
func TestWarmWorkerLostReaper_NamesOOMKillOfDeletedWorker(t *testing.T) {
	cs := fake.NewClientset(oomKilledWarmPod("pod-oom"), warmInformerPod("pod-plain", "dv-1", corev1.PodRunning))
	warmPods := NewKubernetesWarmPods(cs, "leoflow", nil)
	wi, err := NewWarmPodInformer(cs, "leoflow")
	if err != nil {
		t.Fatalf("NewWarmPodInformer: %v", err)
	}
	wi.RecordTerminationsTo(warmPods)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wi.Start(ctx)
	if !wi.WaitForCacheSync(ctx) {
		t.Fatal("warm pod cache did not sync")
	}
	for _, name := range []string{"pod-oom", "pod-plain"} {
		if err := cs.CoreV1().Pods("leoflow").Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			t.Fatalf("delete %s: %v", name, err)
		}
	}
	// The informer drops a pod from its cache before its delete handler records
	// the final state, so wait for the record too, not only for an empty cache.
	if !waitFor(t, func() bool {
		_, ok := wi.CachedWarmPods()
		return ok && len(wi.listCachedNames()) == 0 && warmPods.LastTerminationReason("pod-oom") == "OOMKilled"
	}) {
		t.Fatal("the informer did not observe both deletes")
	}

	bound := func(id, worker string) WarmBoundTI {
		return WarmBoundTI{TaskInstanceID: id, TenantID: "t1", DagID: "d", DagRunID: "run-" + id, TaskID: "load", TryNumber: 1, WarmWorkerID: worker}
	}
	store := &fakeWarmBoundStore{bound: []WarmBoundTI{bound("oom", "pod-oom"), bound("plain", "pod-plain")}}
	sink := &fakeLogSink{}
	r := newWarmReaper(store, warmPods)
	r.sink = sink
	if err := r.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !reflect.DeepEqual(store.oomMarked, []string{"oom"}) {
		t.Errorf("OOMKilled marks = %v, want [oom]", store.oomMarked)
	}
	if !reflect.DeepEqual(store.marked, []string{"plain"}) {
		t.Errorf("plain pod_lost marks = %v, want [plain]", store.marked)
	}
	if evs := sink.events["t1/d/run-oom/load/1"]; len(evs) != 1 || !strings.Contains(evs[0].Message, "OOMKilled") {
		t.Errorf("OOM marker = %v, want one killed: pod_lost line naming OOMKilled", evs)
	}
	if evs := sink.events["t1/d/run-plain/load/1"]; len(evs) != 1 || strings.Contains(evs[0].Message, "OOMKilled") {
		t.Errorf("plain marker = %v, want today's text with no OOM claim", evs)
	}
}

// recordingWarmLister is a fake lister that also remembers terminal reasons,
// like KubernetesWarmPods does.
type recordingWarmLister struct {
	fakeWarmLister
	reasons map[string]string
}

func (f *recordingWarmLister) LastTerminationReason(name string) string { return f.reasons[name] }

// TestWarmWorkerLostReaper_ListedReasonWins: a worker still listed is named
// from its listed container status; the remembered reason is only read for a
// worker the LIST no longer returns (#1835).
func TestWarmWorkerLostReaper_ListedReasonWins(t *testing.T) {
	store := &fakeWarmBoundStore{bound: []WarmBoundTI{
		{TaskInstanceID: "listed", WarmWorkerID: "pod-error", TryNumber: 1},
		{TaskInstanceID: "gone", WarmWorkerID: "pod-gone", TryNumber: 1},
	}}
	lister := &recordingWarmLister{
		fakeWarmLister: fakeWarmLister{pods: []WarmPodInfo{{Name: "pod-error", Terminal: true, TerminationReason: "Error"}}},
		reasons:        map[string]string{"pod-error": "OOMKilled", "pod-gone": "OOMKilled"},
	}
	r := newWarmReaper(store, lister)
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !reflect.DeepEqual(store.oomMarked, []string{"gone"}) || !reflect.DeepEqual(store.marked, []string{"listed"}) {
		t.Errorf("oom marks = %v, plain marks = %v; want [gone] and [listed]", store.oomMarked, store.marked)
	}
}
