package executor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// syncReporter is a fakeReporter safe for the event worker's goroutine.
type syncReporter struct {
	mu  sync.Mutex
	rep fakeReporter
	// failures makes the next FailTask calls fail.
	failures int
	calls    int
}

func (s *syncReporter) FailTask(ctx context.Context, id string, try, epoch int, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failures > 0 {
		s.failures--
		return errors.New("database down")
	}
	return s.rep.FailTask(ctx, id, try, epoch, reason)
}

func (s *syncReporter) FailTaskInfra(ctx context.Context, id string, try, epoch int, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rep.FailTaskInfra(ctx, id, try, epoch, reason)
}

func (s *syncReporter) SucceedTask(ctx context.Context, id string, try, epoch int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rep.SucceedTask(ctx, id, try, epoch)
}

func (s *syncReporter) RescheduleTask(ctx context.Context, id string, try, epoch int, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.rep.RescheduleTask(ctx, id, try, epoch, at)
}

func (s *syncReporter) failNext(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = n
}

func (s *syncReporter) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// syncDecisions counts the recorded decisions.
type syncDecisions struct {
	mu   sync.Mutex
	seen map[string]int
}

func (d *syncDecisions) RecordSchedulerDecision(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil {
		d.seen = map[string]int{}
	}
	d.seen[name]++
}

func (d *syncDecisions) count(name string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.seen[name]
}

func (s *syncReporter) outcome(id string) (settledOutcome, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.rep.settled[id]
	return o, ok
}

// podEventsHarness starts an informer over one running task pod and the event
// settler over a reconciler that reads it, as the server wires them.
type podEventsHarness struct {
	cs      *fake.Clientset
	pi      *PodInformer
	rec     *Reconciler
	rep     *syncReporter
	dec     *syncDecisions
	leading atomic.Bool
	mu      sync.Mutex
}

func newPodEventsHarness(t *testing.T, pods ...*corev1.Pod) *podEventsHarness {
	t.Helper()
	h := &podEventsHarness{rep: &syncReporter{}, dec: &syncDecisions{}}
	h.cs = fake.NewClientset()
	for _, p := range pods {
		if _, err := h.cs.CoreV1().Pods(p.Namespace).Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	h.leading.Store(true)
	h.pi = NewPodInformer(h.cs, "leoflow")
	h.rec = NewReconciler(h.cs, "leoflow", h.rep)
	h.rec.SetPodSnapshotter(h.pi)
	h.rec.SetDecisionRecorder(h.dec)
	settler := NewPodEventSettler(h.rec, h.pi, h.leading.Load, &h.mu)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.pi.Start(ctx)
	if !h.pi.WaitForCacheSync(ctx) {
		t.Fatal("informer never synced")
	}
	go settler.Run(ctx)
	return h
}

// runningTaskPod is a managed task pod whose task container is running.
func runningTaskPod(name, tiID string) *corev1.Pod {
	pod := managedPod(name, tiID, corev1.PodRunning)
	pod.Labels["leoflow.io/task-id"] = "t1"
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name:  taskContainerName,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	return pod
}

// oomKill marks the pod Failed with its task container OOMKilled, as the
// kubelet reports it.
func (h *podEventsHarness) oomKill(t *testing.T, name string) {
	t.Helper()
	pod, err := h.cs.CoreV1().Pods("leoflow").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	taskExit(pod, 137, "OOMKilled")
	pod.Status.Phase = corev1.PodFailed
	if _, err := h.cs.CoreV1().Pods("leoflow").UpdateStatus(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// podReads counts the get and list calls on pods the fake apiserver served,
// leaving out the informer's own initial LIST and watch.
func podReads(cs *fake.Clientset) int {
	n := 0
	for _, a := range cs.Actions() {
		if a.GetResource().Resource != "pods" {
			continue
		}
		if a.GetVerb() == "get" || a.GetVerb() == "list" {
			n++
		}
	}
	return n
}

// TestPodEventSettlesAnOOMKilledPodWithinSeconds is #2330: an OOMKilled task
// pod is settled from its informer update event, without waiting for the 30 s
// sweep, and without one more read on the apiserver: the watch is already open.
func TestPodEventSettlesAnOOMKilledPodWithinSeconds(t *testing.T) {
	h := newPodEventsHarness(t, runningTaskPod("p-oom", "ti-oom"))
	readsBefore := podReads(h.cs)
	h.oomKill(t, "p-oom")
	// The test's own Get above is one read; anything past it is the settler's.
	readsBefore++

	if !waitFor(t, func() bool { _, ok := h.rep.outcome("ti-oom"); return ok }) {
		t.Fatal("the OOMKilled pod was not settled from its update event")
	}
	got, _ := h.rep.outcome("ti-oom")
	if got.kind != settleFailed || got.tryNumber != 1 {
		t.Fatalf("settled %+v, want a task failure of try 1", got)
	}
	if reads := podReads(h.cs); reads != readsBefore {
		t.Fatalf("the event path read the apiserver %d more times, want 0", reads-readsBefore)
	}
	if n := h.dec.count("pod_event_settled"); n != 1 {
		t.Fatalf("pod_event_settled recorded %d times, want 1", n)
	}
}

// TestPodEventWaitsForLeadershipAndTheSweepRepairsIt: a replica that does not
// lead never settles from an event, and the sweep still settles the attempt
// whose event was missed.
func TestPodEventWaitsForLeadershipAndTheSweepRepairsIt(t *testing.T) {
	h := newPodEventsHarness(t, runningTaskPod("p-oom", "ti-oom"))
	h.leading.Store(false)
	h.oomKill(t, "p-oom")
	// Wait until the cache holds the Failed pod, then a little longer for the
	// worker to have seen the event.
	if !waitFor(t, func() bool {
		pods, err := h.pi.SnapshotTaskPods()
		return err == nil && len(pods) == 1 && pods[0].Status.Phase == corev1.PodFailed
	}) {
		t.Fatal("the informer never saw the update")
	}
	time.Sleep(200 * time.Millisecond)
	if _, ok := h.rep.outcome("ti-oom"); ok {
		t.Fatal("a replica that does not lead settled from an event")
	}

	h.leading.Store(true)
	h.mu.Lock()
	err := h.rec.Reconcile(context.Background())
	h.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := h.rep.outcome("ti-oom"); !ok || got.kind != settleFailed {
		t.Fatalf("the sweep did not repair the missed event: %+v %v", got, ok)
	}
}

// TestPodEventSettlesADeletedTerminalPod: a terminal pod deleted before the
// worker reads it (a node's pod garbage collector) is settled from the final
// state the delete event carried; a pod deleted while running is left to the
// reaper, which reads liveness.
func TestPodEventSettlesADeletedTerminalPod(t *testing.T) {
	failed := taskExit(managedPod("p-gone", "ti-gone", corev1.PodFailed), 137, "OOMKilled")
	failed.Labels["leoflow.io/task-id"] = "t1"
	h := newPodEventsHarness(t, failed, runningTaskPod("p-live", "ti-live"))
	for _, name := range []string{"p-gone", "p-live"} {
		if err := h.cs.CoreV1().Pods("leoflow").Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if !waitFor(t, func() bool { _, ok := h.rep.outcome("ti-gone"); return ok }) {
		t.Fatal("the deleted terminal pod was not settled from its delete event")
	}
	time.Sleep(100 * time.Millisecond)
	if _, ok := h.rep.outcome("ti-live"); ok {
		t.Fatal("a pod deleted while running was settled")
	}
}

// TestPodEventHoldsTheMaintenanceLock: the worker settles only while it holds
// the lock the maintenance cycle holds, so an event settle never interleaves
// with a sweep or a reap.
func TestPodEventHoldsTheMaintenanceLock(t *testing.T) {
	h := newPodEventsHarness(t, runningTaskPod("p-oom", "ti-oom"))
	h.mu.Lock()
	h.oomKill(t, "p-oom")
	time.Sleep(200 * time.Millisecond)
	if _, ok := h.rep.outcome("ti-oom"); ok {
		h.mu.Unlock()
		t.Fatal("settled while the maintenance cycle held the lock")
	}
	h.mu.Unlock()
	if !waitFor(t, func() bool { _, ok := h.rep.outcome("ti-oom"); return ok }) {
		t.Fatal("not settled once the lock was released")
	}
}

// TestPodEventLeavesARescheduleToTheSweep: a reschedule is applied once, by the
// sweep, which collects the pod at once (ADR 0052). Neither the update that
// ended the pod nor the delete of that collection applies it again: a redispatch
// keeps the try number, and a second apply would re-park the live attempt.
func TestPodEventLeavesARescheduleToTheSweep(t *testing.T) {
	h := newPodEventsHarness(t, runningTaskPod("p-poke", "ti-poke"))
	pod, err := h.cs.CoreV1().Pods("leoflow").Get(context.Background(), "p-poke", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	withRecord(pod, taskoutcome.RescheduledAt(time.Now().Add(time.Minute)))
	pod.Status.Phase = corev1.PodSucceeded
	if _, err := h.cs.CoreV1().Pods("leoflow").UpdateStatus(context.Background(), pod, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := h.cs.CoreV1().Pods("leoflow").Delete(context.Background(), "p-poke", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if n := h.rep.callCount(); n != 0 {
		t.Fatalf("the event path applied a reschedule %d times, want 0", n)
	}
}

// TestPodEventRetriesAFailedSettleOfADeletedPod: a deleted pod is not listed
// by the sweep any more, so its last state is kept for the retry of a write
// that failed.
func TestPodEventRetriesAFailedSettleOfADeletedPod(t *testing.T) {
	h := newPodEventsHarness(t, runningTaskPod("p-gone", "ti-gone"))
	h.rep.failNext(1)
	// Hold the cycle lock so the first settle runs on the deleted pod's last
	// state, not on the cached one.
	h.mu.Lock()
	h.oomKill(t, "p-gone")
	if err := h.cs.CoreV1().Pods("leoflow").Delete(context.Background(), "p-gone", metav1.DeleteOptions{}); err != nil {
		h.mu.Unlock()
		t.Fatal(err)
	}
	if !waitFor(t, func() bool { _, ok := h.pi.cachedPod("leoflow/p-gone"); return !ok }) {
		h.mu.Unlock()
		t.Fatal("the informer never saw the delete")
	}
	h.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := h.rep.outcome("ti-gone"); ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the deleted pod's failed settle was not retried (calls %d)", h.rep.callCount())
}
