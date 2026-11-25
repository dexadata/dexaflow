package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// fakeAttempts serves task instance rows by id.
type fakeAttempts struct {
	rows  map[string]AttemptState
	err   error
	reads int
}

func (f *fakeAttempts) TaskAttempts(_ context.Context, ids []string) (map[string]AttemptState, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]AttemptState{}
	for _, id := range ids {
		if row, ok := f.rows[id]; ok {
			out[id] = row
		}
	}
	return out, nil
}

// recordingStopper records the pods it was asked to stop.
type recordingStopper struct {
	stopped []string
	err     error
}

func (s *recordingStopper) StopTaskPod(_ context.Context, pod *corev1.Pod) error {
	s.stopped = append(s.stopped, pod.Name)
	return s.err
}

type countingRecorder map[string]int

func (c countingRecorder) RecordSchedulerDecision(d string) { c[d]++ }

// runningPod is a started task pod of the attempt (try, epoch) of task
// instance tiID, its task container running.
func runningPod(name, tiID string, try, epoch int) *corev1.Pod {
	pod := withTaskContainer(epochPod(name, "run-a", "extract", try, epoch, corev1.PodRunning), false, 0)
	pod.Annotations = map[string]string{"leoflow.io/task-instance-id": tiID}
	now := metav1.Now()
	pod.Status.StartTime = &now
	return pod
}

// teardownHarness is a reconciler over pods with the teardown wired, on a
// clock the test moves.
type teardownHarness struct {
	r       *Reconciler
	store   *fakeAttempts
	stopper *recordingStopper
	rec     countingRecorder
	clock   time.Time
	gate    bool
}

func newTeardownHarness(t *testing.T, rows map[string]AttemptState, pods ...*corev1.Pod) *teardownHarness {
	t.Helper()
	cs := fake.NewClientset()
	for _, p := range pods {
		if _, err := cs.CoreV1().Pods("leoflow").Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create pod: %v", err)
		}
	}
	h := &teardownHarness{
		store: &fakeAttempts{rows: rows}, stopper: &recordingStopper{}, rec: countingRecorder{},
		clock: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC), gate: true,
	}
	h.r = NewReconciler(cs, "leoflow", &fakeReporter{})
	h.r.now = func() time.Time { return h.clock }
	h.r.SetAbandonedPodTeardown(h.store, h.stopper, func(context.Context) bool { return h.gate }, 90*time.Second, h.rec)
	return h
}

func (h *teardownHarness) sweep(t *testing.T, after time.Duration) {
	t.Helper()
	h.clock = h.clock.Add(after)
	if err := h.r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

// #2430: a run marked failed leaves its task instance failed while the pod,
// whose agent cannot reach the control plane, keeps running. The leader stops
// it once the attempt has stayed ended for the grace, and only once.
func TestAbandonedPodIsStoppedAfterTheGrace(t *testing.T) {
	h := newTeardownHarness(t, map[string]AttemptState{"00000000-0000-0000-0000-000000000001": {TryNumber: 1, AttemptEpoch: 2, Live: false}},
		runningPod("p", "00000000-0000-0000-0000-000000000001", 1, 2))

	h.sweep(t, 0)
	h.sweep(t, 60*time.Second)
	if len(h.stopper.stopped) != 0 {
		t.Fatalf("stopped %v inside the grace", h.stopper.stopped)
	}
	h.sweep(t, 30*time.Second)
	h.sweep(t, 30*time.Second)

	if len(h.stopper.stopped) != 1 || h.stopper.stopped[0] != "p" {
		t.Fatalf("stopped = %v, want p once", h.stopper.stopped)
	}
	if h.rec["abandoned_pod_teardown"] != 1 {
		t.Errorf("decisions = %v, want one abandoned_pod_teardown", h.rec)
	}
}

// A live attempt's pod is never touched, whatever its age.
func TestLiveAttemptPodIsNeverStopped(t *testing.T) {
	h := newTeardownHarness(t, map[string]AttemptState{
		"00000000-0000-0000-0000-000000000001": {TryNumber: 1, AttemptEpoch: 2, Live: true},
		// A row behind the pod (cannot happen) is left alone too.
		"00000000-0000-0000-0000-000000000002": {TryNumber: 1, AttemptEpoch: 1, Live: false},
		"00000000-0000-0000-0000-000000000003": {TryNumber: 1, AttemptEpoch: 5, Live: false},
	}, runningPod("live", "00000000-0000-0000-0000-000000000001", 1, 2), runningPod("ahead-try", "00000000-0000-0000-0000-000000000002", 2, 1), runningPod("ahead-epoch", "00000000-0000-0000-0000-000000000003", 1, 6))

	for range 5 {
		h.sweep(t, time.Minute)
	}

	if len(h.stopper.stopped) != 0 {
		t.Fatalf("stopped %v, want none", h.stopper.stopped)
	}
}

// The other ways an attempt ends: a later try or epoch took the row, or the
// row is gone (its run deleted).
func TestSupersededAndOrphanedPodsAreStopped(t *testing.T) {
	h := newTeardownHarness(t, map[string]AttemptState{
		"00000000-0000-0000-0000-000000000001": {TryNumber: 2, AttemptEpoch: 1, Live: true},
		"00000000-0000-0000-0000-000000000002": {TryNumber: 1, AttemptEpoch: 3, Live: true},
	}, runningPod("old-try", "00000000-0000-0000-0000-000000000001", 1, 1), runningPod("old-epoch", "00000000-0000-0000-0000-000000000002", 1, 2), runningPod("orphan", "00000000-0000-0000-0000-00000000dead", 1, 1))

	h.sweep(t, 0)
	h.sweep(t, 2*time.Minute)

	if len(h.stopper.stopped) != 3 {
		t.Fatalf("stopped = %v, want the three pods", h.stopper.stopped)
	}
}

// A finished pod has nothing to stop and carries the record the reconciler
// settles from; it is never handed to the stopper.
func TestFinishedPodIsNotStopped(t *testing.T) {
	done := withTaskContainer(epochPod("done", "run-a", "extract", 1, 2, corev1.PodRunning), true, 0)
	done.Annotations = map[string]string{"leoflow.io/task-instance-id": "00000000-0000-0000-0000-000000000001"}
	h := newTeardownHarness(t, map[string]AttemptState{"00000000-0000-0000-0000-000000000001": {TryNumber: 1, AttemptEpoch: 2}}, done)

	h.sweep(t, 0)
	h.sweep(t, 5*time.Minute)

	if len(h.stopper.stopped) != 0 || h.store.reads != 0 {
		t.Fatalf("stopped %v after %d reads, want nothing read or stopped", h.stopper.stopped, h.store.reads)
	}
}

// A pod that turns live again in the database (it cannot, but a read that
// lags could say so) restarts its grace.
func TestGraceRestartsWhenThePodIsSeenLive(t *testing.T) {
	rows := map[string]AttemptState{"00000000-0000-0000-0000-000000000001": {TryNumber: 1, AttemptEpoch: 2, Live: false}}
	h := newTeardownHarness(t, rows, runningPod("p", "00000000-0000-0000-0000-000000000001", 1, 2))

	h.sweep(t, 0)
	rows["00000000-0000-0000-0000-000000000001"] = AttemptState{TryNumber: 1, AttemptEpoch: 2, Live: true}
	h.sweep(t, 60*time.Second)
	rows["00000000-0000-0000-0000-000000000001"] = AttemptState{TryNumber: 1, AttemptEpoch: 2, Live: false}
	h.sweep(t, 60*time.Second)

	if len(h.stopper.stopped) != 0 {
		t.Fatalf("stopped %v, want the grace to restart", h.stopper.stopped)
	}
}

// The destructive gate holds the stop on a leader stepping down, and a failed
// read stops nothing.
func TestTeardownRespectsTheGateAndReadErrors(t *testing.T) {
	h := newTeardownHarness(t, map[string]AttemptState{"00000000-0000-0000-0000-000000000001": {TryNumber: 1, AttemptEpoch: 2}}, runningPod("p", "00000000-0000-0000-0000-000000000001", 1, 2))
	h.gate = false
	h.sweep(t, 0)
	h.sweep(t, 5*time.Minute)
	if len(h.stopper.stopped) != 0 {
		t.Fatalf("stopped %v with the gate closed", h.stopper.stopped)
	}

	h.store.err = errors.New("db down")
	h.gate = true
	h.sweep(t, time.Minute)
	if len(h.stopper.stopped) != 0 || h.rec["abandoned_pod_read_error"] != 1 {
		t.Fatalf("stopped %v, decisions %v: a failed read must stop nothing", h.stopper.stopped, h.rec)
	}

	h.store.err = nil
	h.sweep(t, time.Minute)
	if len(h.stopper.stopped) != 1 {
		t.Fatalf("stopped %v once the gate opened and the read worked", h.stopper.stopped)
	}
}

// A refused stop is metered and tried again on the next sweep.
func TestFailedStopIsRetried(t *testing.T) {
	h := newTeardownHarness(t, map[string]AttemptState{"00000000-0000-0000-0000-000000000001": {TryNumber: 1, AttemptEpoch: 2}}, runningPod("p", "00000000-0000-0000-0000-000000000001", 1, 2))
	h.stopper.err = errors.New("forbidden")
	h.sweep(t, 0)
	h.sweep(t, 2*time.Minute)
	h.stopper.err = nil
	h.sweep(t, 30*time.Second)

	if len(h.stopper.stopped) != 2 || h.rec["abandoned_pod_teardown_error"] != 1 || h.rec["abandoned_pod_teardown"] != 1 {
		t.Fatalf("stopped %v, decisions %v: want a metered failure, then the stop", h.stopper.stopped, h.rec)
	}
}

// Without the teardown wired (Lite, or before this change) the sweep reads
// nothing and stops nothing.
func TestTeardownOffByDefault(t *testing.T) {
	cs := fake.NewClientset()
	if _, err := cs.CoreV1().Pods("leoflow").Create(context.Background(), runningPod("p", "00000000-0000-0000-0000-000000000001", 1, 2), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	r := NewReconciler(cs, "leoflow", &fakeReporter{})
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// StopTaskPod stops a started pod in place, so its outcome record survives,
// and leaves a finished one alone.
func TestStopTaskPodStopsInPlace(t *testing.T) {
	pod := runningPod("p", "00000000-0000-0000-0000-000000000001", 1, 2)
	finished := epochPod("f", "run-a", "extract", 1, 1, corev1.PodSucceeded)
	cs := fake.NewClientset(pod, finished)
	e := NewKubernetesExecutor(cs, "leoflow")

	if err := e.StopTaskPod(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	if err := e.StopTaskPod(context.Background(), finished); err != nil {
		t.Fatal(err)
	}

	got, err := cs.CoreV1().Pods("leoflow").Get(context.Background(), "p", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the started pod was deleted, not stopped in place: %v", err)
	}
	if got.Spec.ActiveDeadlineSeconds == nil || *got.Spec.ActiveDeadlineSeconds != 1 {
		t.Errorf("activeDeadlineSeconds = %v, want 1", got.Spec.ActiveDeadlineSeconds)
	}
	if _, err := cs.CoreV1().Pods("leoflow").Get(context.Background(), "f", metav1.GetOptions{}); err != nil {
		t.Errorf("the finished pod was touched: %v", err)
	}
}

// A pod whose task instance annotation is not a UUID was not made by BuildPod;
// it is never read as abandoned.
func TestPodWithoutAValidTaskInstanceIsLeftAlone(t *testing.T) {
	h := newTeardownHarness(t, nil, runningPod("hand-made", "not-a-uuid", 1, 1))
	h.sweep(t, 0)
	h.sweep(t, 5*time.Minute)
	if len(h.stopper.stopped) != 0 || h.store.reads != 0 {
		t.Fatalf("stopped %v after %d reads, want nothing", h.stopper.stopped, h.store.reads)
	}
}
