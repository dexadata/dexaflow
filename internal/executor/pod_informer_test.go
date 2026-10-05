package executor

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// informerPod builds a managed task pod with the sanitized label scheme BuildPod
// stamps, so the informer's tweaked LIST selector and CachedPodActive lookups see
// the same keys.
func informerPod(name, runID, taskID string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "leoflow",
			Labels: map[string]string{
				"leoflow.io/run-id":     sanitizeLabel(runID),
				"leoflow.io/task-id":    sanitizeLabel(taskID),
				"leoflow.io/try-number": "1",
			},
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// waitFor polls cond every 10ms until it holds or the deadline elapses. The
// informer applies watch events asynchronously, so a cache read after a Create/
// Delete must be retried rather than read once.
func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// TestPodInformer_SeesAddAndDelete verifies the cache reflects the live pod set
// through the initial LIST and subsequent watch events, applies the same
// sanitizeLabel transform BuildPod/TaskPodPresence use (so a raw run-id lookup hits
// the sanitized label), and treats only Pending/Running as active — the identical
// predicate to TaskPodPresence. A Succeeded pod is not active.
func TestPodInformer_SeesAddAndDelete(t *testing.T) {
	// Uppercase/underscore run-id proves the lookup sanitizes before matching.
	cs := fake.NewClientset(
		informerPod("p-run", "Run_A", "extract", corev1.PodRunning),
		informerPod("p-done", "Run_A", "done-task", corev1.PodSucceeded),
	)
	pi := NewPodInformer(cs, "leoflow")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pi.Start(ctx)
	if !pi.WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}

	if !pi.CachedPodActive(Attempt{RunID: "Run_A", TaskID: "extract", TryNumber: 1}) {
		t.Error("a Running pod must read as active (with sanitized labels)")
	}
	if pi.CachedPodActive(Attempt{RunID: "Run_A", TaskID: "done-task", TryNumber: 1}) {
		t.Error("a Succeeded pod must not read as active")
	}
	if pi.CachedPodActive(Attempt{RunID: "Run_A", TaskID: "never-existed", TryNumber: 1}) {
		t.Error("an unknown task must read as not active")
	}

	// Watch-driven delete: the Running pod disappears; the cache must catch up.
	if err := cs.CoreV1().Pods("leoflow").Delete(ctx, "p-run", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !waitFor(t, func() bool { return !pi.CachedPodActive(Attempt{RunID: "Run_A", TaskID: "extract", TryNumber: 1}) }) {
		t.Error("cache did not observe the pod deletion")
	}

	// Watch-driven add: a new Pending pod appears; the cache must reflect it.
	if _, err := cs.CoreV1().Pods("leoflow").Create(ctx, informerPod("p-new", "Run_A", "load", corev1.PodPending), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if !waitFor(t, func() bool { return pi.CachedPodActive(Attempt{RunID: "Run_A", TaskID: "load", TryNumber: 1}) }) {
		t.Error("cache did not observe the added Pending pod")
	}
}

// TestPodInformer_BeforeSync_ReturnsFalse locks the safety gate: until the cache
// has synced, CachedPodActive returns false so every candidate falls through to
// the live TaskPodPresence read (today's behavior). A cold cache is never wrong,
// only "no speedup yet" — leader-failover safe.
func TestPodInformer_BeforeSync_ReturnsFalse(t *testing.T) {
	cs := fake.NewClientset(informerPod("p-run", "r1", "t1", corev1.PodRunning))
	pi := NewPodInformer(cs, "leoflow")
	// Deliberately NOT started / not synced.
	if pi.CachedPodActive(Attempt{RunID: "r1", TaskID: "t1", TryNumber: 1}) {
		t.Error("an unsynced cache must return false (fall through to the live read)")
	}
	if _, err := pi.SnapshotTaskPods(); err == nil {
		t.Error("an unsynced cache must not claim an authoritative empty snapshot")
	}
}

// TestPodInformer_SnapshotTaskPods returns the managed pod set from the cache once
// synced — the reconciler's read replacement for its 30s LIST.
func TestPodInformer_SnapshotTaskPods(t *testing.T) {
	cs := fake.NewClientset(
		informerPod("p-a", "r1", "t1", corev1.PodRunning),
		informerPod("p-b", "r1", "t2", corev1.PodSucceeded),
	)
	pi := NewPodInformer(cs, "leoflow")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pi.Start(ctx)
	if !pi.WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	pods, err := pi.SnapshotTaskPods()
	if err != nil {
		t.Fatalf("SnapshotTaskPods: %v", err)
	}
	if len(pods) != 2 {
		t.Errorf("snapshot should hold both managed pods, got %d", len(pods))
	}
}

// TestPodInformer_HasSynced exposes the cache's sync state as a predicate the
// reaper's leader-settling gate can poll: false until the initial LIST has
// populated the cache, true after. A gate that read a one-shot boot-time value
// would never see a late sync (a watch that recovered after RBAC drift was fixed).
func TestPodInformer_HasSynced(t *testing.T) {
	cs := fake.NewClientset()
	pi := NewPodInformer(cs, "leoflow")
	if pi.HasSynced() {
		t.Fatal("HasSynced must be false before the informer starts")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pi.Start(ctx)
	defer pi.Shutdown()
	if !pi.WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	if !pi.HasSynced() {
		t.Error("HasSynced must be true once the cache synced")
	}
}
