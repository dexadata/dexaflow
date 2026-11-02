package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/logs"
)

// startExpiredStore is the reaper store for the start_expired tests: it records
// which mark each queued attempt received.
type startExpiredStore struct {
	fakeReaperStore
	startExpired []string
}

func (f *startExpiredStore) MarkTaskStartExpired(_ context.Context, id string, _, _ int) (bool, error) {
	f.startExpired = append(f.startExpired, id)
	return true, nil
}

// markerCapture records the log markers a reaper appends.
type markerCapture struct {
	refs   []logs.Ref
	events []logs.Event
}

func (m *markerCapture) AppendEvent(ref logs.Ref, ev logs.Event) error {
	m.refs = append(m.refs, ref)
	m.events = append(m.events, ev)
	return nil
}

const testAttemptTokenTTL = 10 * time.Minute

// newStartExpiredReaper wires a Reaper exactly as the Kubernetes server does:
// pods for liveness and teardown, the attempt token TTL and the marker sink.
func newStartExpiredReaper(store ReaperStore, pods PodManager, sink logSink) *Reaper {
	r := NewReaper(store, pods, nil, nil, nil, reapTestLogger(), DefaultReaperConfig(), ReaperGates{})
	r.SetAttemptTokenTTL(testAttemptTokenTTL)
	r.SetLogSink(sink)
	return r
}

// A pod still Pending (slow image pull, node provisioning, init containers) when
// the credential minted at dispatch is about to run out can only start with a
// dead credential: Register would be refused, no log would stream, and the
// attempt would fail against the user's retries (#1126). The reaper re-places it
// as an infra failure instead, says why in the attempt's log, and stops the
// Pending pod so it cannot start late.
func TestDispatchLostReaper_PendingPastStartDeadlineIsStartExpired(t *testing.T) {
	now := time.Now().UTC()
	store := &startExpiredStore{fakeReaperStore: fakeReaperStore{queuedCands: []StaleQueuedCandidate{{
		TaskInstanceID: "ti-slow", DagRunID: "run-a", TenantID: "acme", DagID: "dbt", TaskID: "build",
		TryNumber: 1, AttemptEpoch: 2,
		QueuedAt: now.Add(-(testAttemptTokenTTL - StartCredentialMargin) - time.Second),
	}}}}
	pods := &fakePodManager{active: map[string]bool{"run-a/build/1": true}, pending: map[string]bool{"run-a/build/1": true}}
	sink := &markerCapture{}

	if err := newStartExpiredReaper(store, pods, sink).ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.startExpired) != 1 || store.startExpired[0] != "ti-slow" {
		t.Fatalf("start_expired marks = %v, want [ti-slow]", store.startExpired)
	}
	if len(store.queuedMarked) != 0 {
		t.Errorf("a landed dispatch was marked dispatch_lost: %v", store.queuedMarked)
	}
	if len(pods.deletedTasks) != 1 || pods.deletedTasks[0] != (deletedTask{"run-a", "build", 1}) {
		t.Errorf("deleted pods = %v, want the Pending pod of run-a/build/1", pods.deletedTasks)
	}
	if len(sink.events) != 1 {
		t.Fatalf("got %d log markers, want 1", len(sink.events))
	}
	want := logs.Ref{TenantID: "acme", DagID: "dbt", RunID: "run-a", TaskID: "build", TryNumber: 1, AttemptEpoch: 2}
	if sink.refs[0] != want {
		t.Errorf("marker ref = %+v, want %+v", sink.refs[0], want)
	}
	if msg := sink.events[0].Message; !strings.HasPrefix(msg, "killed: start_expired") {
		t.Errorf("marker = %q, want a start_expired line", msg)
	}
}

// Before the start deadline a Pending pod is a slow start and still defers, as
// it always did (#461): its agent can still start with a live credential.
func TestDispatchLostReaper_PendingBeforeStartDeadlineDefers(t *testing.T) {
	now := time.Now().UTC()
	store := &startExpiredStore{fakeReaperStore: fakeReaperStore{queuedCands: []StaleQueuedCandidate{{
		TaskInstanceID: "ti-slow", DagRunID: "run-a", TaskID: "build", TryNumber: 1,
		QueuedAt: now.Add(-(testAttemptTokenTTL - StartCredentialMargin) + time.Minute),
	}}}}
	pods := &fakePodManager{active: map[string]bool{"run-a/build/1": true}, pending: map[string]bool{"run-a/build/1": true}}

	if err := newStartExpiredReaper(store, pods, &markerCapture{}).ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.startExpired)+len(store.queuedMarked)+len(pods.deletedTasks) != 0 {
		t.Errorf("a Pending pod inside its start deadline was reaped: start_expired=%v dispatch_lost=%v deleted=%v",
			store.startExpired, store.queuedMarked, pods.deletedTasks)
	}
}

// A pod that is already Running past the deadline has an agent that started
// with a live credential (it renews on its heartbeats) and is only slow to
// report RUNNING: it is left alone.
func TestDispatchLostReaper_RunningPastStartDeadlineDefers(t *testing.T) {
	now := time.Now().UTC()
	store := &startExpiredStore{fakeReaperStore: fakeReaperStore{queuedCands: []StaleQueuedCandidate{{
		TaskInstanceID: "ti-started", DagRunID: "run-a", TaskID: "build", TryNumber: 1,
		QueuedAt: now.Add(-testAttemptTokenTTL),
	}}}}
	pods := &fakePodManager{active: map[string]bool{"run-a/build/1": true}}

	if err := newStartExpiredReaper(store, pods, &markerCapture{}).ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.startExpired)+len(store.queuedMarked)+len(pods.deletedTasks) != 0 {
		t.Errorf("a started pod was reaped: start_expired=%v dispatch_lost=%v deleted=%v",
			store.startExpired, store.queuedMarked, pods.deletedTasks)
	}
}

// The presence cache only ever defers. Past the start deadline it must not:
// the reaper needs the live read to tell Pending from Running.
func TestDispatchLostReaper_StartDeadlineBypassesPresenceCache(t *testing.T) {
	now := time.Now().UTC()
	store := &startExpiredStore{fakeReaperStore: fakeReaperStore{queuedCands: []StaleQueuedCandidate{{
		TaskInstanceID: "ti-slow", DagRunID: "run-a", TaskID: "build", TryNumber: 1,
		QueuedAt: now.Add(-testAttemptTokenTTL),
	}}}}
	pods := &fakePodManager{active: map[string]bool{"run-a/build/1": true}, pending: map[string]bool{"run-a/build/1": true}}
	r := NewReaper(store, pods, alwaysActiveCache{}, nil, nil, reapTestLogger(), DefaultReaperConfig(), ReaperGates{})
	r.SetAttemptTokenTTL(testAttemptTokenTTL)

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.startExpired) != 1 {
		t.Errorf("start_expired marks = %v, want the cached-active Pending attempt reaped", store.startExpired)
	}
}

type alwaysActiveCache struct{}

func (alwaysActiveCache) CachedPodActive(Attempt) bool { return true }

// Without the TTL wired (Lite, or a caller that never set it) nothing changes:
// a live pod defers however long it has been queued.
func TestDispatchLostReaper_NoTTLKeepsDeferring(t *testing.T) {
	now := time.Now().UTC()
	store := &startExpiredStore{fakeReaperStore: fakeReaperStore{queuedCands: []StaleQueuedCandidate{{
		TaskInstanceID: "ti-slow", DagRunID: "run-a", TaskID: "build", TryNumber: 1,
		QueuedAt: now.Add(-time.Hour),
	}}}}
	pods := &fakePodManager{active: map[string]bool{"run-a/build/1": true}, pending: map[string]bool{"run-a/build/1": true}}
	r := NewReaper(store, pods, nil, nil, nil, reapTestLogger(), DefaultReaperConfig(), ReaperGates{})

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.startExpired)+len(store.queuedMarked) != 0 {
		t.Errorf("reaped without a TTL: start_expired=%v dispatch_lost=%v", store.startExpired, store.queuedMarked)
	}
}

// TaskPodPending answers about exactly the attempt: true only when its pod is
// Pending and no pod of it is Running.
func TestTaskPodPending(t *testing.T) {
	cases := []struct {
		name string
		pods []*corev1.Pod
		want bool
	}{
		{"pending pod", []*corev1.Pod{taskPod("p", "run-a", "build", 1, corev1.PodPending)}, true},
		{"running pod", []*corev1.Pod{taskPod("p", "run-a", "build", 1, corev1.PodRunning)}, false},
		{"running beside pending", []*corev1.Pod{
			taskPod("p", "run-a", "build", 1, corev1.PodPending),
			taskPod("r", "run-a", "build", 1, corev1.PodRunning),
		}, false},
		{"another try pending", []*corev1.Pod{taskPod("p", "run-a", "build", 2, corev1.PodPending)}, false},
		{"no pod", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewSimpleClientset()
			for _, p := range tc.pods {
				if _, err := cs.CoreV1().Pods("leoflow").Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
					t.Fatalf("create pod: %v", err)
				}
			}
			e := NewKubernetesExecutor(cs, "leoflow")
			got, err := e.TaskPodPending(context.Background(), Attempt{RunID: "run-a", TaskID: "build", TryNumber: 1})
			if err != nil {
				t.Fatalf("TaskPodPending: %v", err)
			}
			if got != tc.want {
				t.Errorf("TaskPodPending = %v, want %v", got, tc.want)
			}
		})
	}
}
