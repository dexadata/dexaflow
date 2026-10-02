package executor

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// warmInformerPod builds a warm worker pod with the labels BuildWarmPod stamps.
func warmInformerPod(name, dagVersionID string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "leoflow",
			Labels: map[string]string{
				warmWorkerLabelKey:     warmWorkerLabelVal,
				warmDagVersionLabelKey: sanitizeLabel(dagVersionID),
				warmTenantLabelKey:     "acme",
			},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func startWarmInformer(t *testing.T, cs *fake.Clientset) *WarmPodInformer {
	t.Helper()
	wi, err := NewWarmPodInformer(cs, "leoflow")
	if err != nil {
		t.Fatalf("NewWarmPodInformer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	wi.Start(ctx)
	if !wi.WaitForCacheSync(ctx) {
		t.Fatal("warm pod cache did not sync")
	}
	return wi
}

// drained reports whether a change signal is pending, consuming it.
func drained(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// TestWarmPodInformerServesTheFleet: the cache holds exactly the warm workers
// (never task pods), mapped like ListWarmPods maps them, and reads as not synced
// until its first LIST lands so a cold cache never looks like an empty fleet.
func TestWarmPodInformerServesTheFleet(t *testing.T) {
	task := informerPod("task-pod", "run-1", "extract", corev1.PodRunning)
	cs := fake.NewClientset(
		warmInformerPod("warm-a", "dv-1", corev1.PodRunning),
		warmInformerPod("warm-b", "dv-1", corev1.PodFailed),
		task,
	)
	cold, err := NewWarmPodInformer(cs, "leoflow")
	if err != nil {
		t.Fatalf("NewWarmPodInformer: %v", err)
	}
	if _, synced := cold.CachedWarmPods(); synced {
		t.Fatal("an unstarted informer must not report a synced fleet")
	}

	wi := startWarmInformer(t, cs)
	pods, synced := wi.CachedWarmPods()
	if !synced {
		t.Fatal("synced informer reported not synced")
	}
	got := map[string]WarmPodInfo{}
	for _, p := range pods {
		got[p.Name] = p
	}
	if len(got) != 2 {
		t.Fatalf("cached fleet = %+v, want only the two warm workers", pods)
	}
	if a := got["warm-a"]; a.DagVersionID != "dv-1" || a.TenantID != "acme" || a.Terminal {
		t.Errorf("warm-a = %+v, want dv-1/acme live", a)
	}
	if !got["warm-b"].Terminal {
		t.Error("a Failed warm pod must read as terminal")
	}
}

// TestWarmPodInformerSignalsLossOfAWorker is event-driven refill: deleting a warm
// worker, or one reaching a terminal phase, signals the reconciler at once.
func TestWarmPodInformerSignalsLossOfAWorker(t *testing.T) {
	cs := fake.NewClientset(
		warmInformerPod("warm-a", "dv-1", corev1.PodRunning),
		warmInformerPod("warm-b", "dv-1", corev1.PodRunning),
	)
	wi := startWarmInformer(t, cs)
	drained(wi.Changes())
	ctx := context.Background()

	if err := cs.CoreV1().Pods("leoflow").Delete(ctx, "warm-a", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !waitFor(t, func() bool { return drained(wi.Changes()) }) {
		t.Error("deleting a warm worker did not signal a refill")
	}

	failed := warmInformerPod("warm-b", "dv-1", corev1.PodFailed)
	if _, err := cs.CoreV1().Pods("leoflow").UpdateStatus(ctx, failed, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !waitFor(t, func() bool { return drained(wi.Changes()) }) {
		t.Error("a warm worker turning Failed did not signal a refill")
	}
}

// TestWarmPodInformerCreateExpectations: a create the cache has not observed yet
// is pending, so the reconciler does not create the same worker twice off a
// lagging cache. It clears when the pod is observed, when the create fails, or
// after a bounded wait, so a missed event cannot stall refill.
func TestWarmPodInformerCreateExpectations(t *testing.T) {
	cs := fake.NewClientset()
	wi := startWarmInformer(t, cs)

	wi.ExpectCreate("dv-1")
	if !wi.CreatesPending("dv-1") {
		t.Fatal("an expected create must be pending until observed")
	}
	if wi.CreatesPending("dv-2") {
		t.Error("expectations must be per dag_version")
	}
	if _, err := cs.CoreV1().Pods("leoflow").Create(context.Background(), warmInformerPod("warm-new", "dv-1", corev1.PodPending), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !waitFor(t, func() bool { return !wi.CreatesPending("dv-1") }) {
		t.Error("observing the created pod must clear the expectation")
	}

	wi.ExpectCreate("dv-1")
	wi.CreateFailed("dv-1")
	if wi.CreatesPending("dv-1") {
		t.Error("a failed create must clear its expectation")
	}

	now := time.Now()
	wi.now = func() time.Time { return now }
	wi.ExpectCreate("dv-1")
	wi.now = func() time.Time { return now.Add(warmCreateExpectationTTL + time.Second) }
	if wi.CreatesPending("dv-1") {
		t.Error("an expectation older than its TTL must expire")
	}
}
