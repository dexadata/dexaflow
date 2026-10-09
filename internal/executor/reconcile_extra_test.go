package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// TestPodFailureReasonSurfacesCause asserts the reconciler surfaces a precise,
// actionable failure reason — what an operator sees on a pod the agent never
// reported (OOMKilled, Evicted, image-pull failures), with a generic fallback.
func TestPodFailureReasonSurfacesCause(t *testing.T) {
	withReason := func(r string) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodFailed, Reason: r}}
	}
	// The reason must NAME the Kubernetes cause — the token an operator greps for
	// and sees in `kubectl describe`. It may carry additional operator-facing
	// guidance around that token, so the assertion is containment, not equality.
	cases := map[string]struct {
		pod  *corev1.Pod
		want string
	}{
		"evicted":          {withReason("Evicted"), "Evicted"},
		"oom":              {withReason("OOMKilled"), "OOMKilled"},
		"failed no reason": {podPhase(corev1.PodFailed), "pod failed"},
		"image pull":       {podWaiting("ImagePullBackOff"), "ImagePullBackOff"},
		"invalid image":    {podWaiting("InvalidImageName"), "InvalidImageName"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := classifyPod(c.pod).reason; !strings.Contains(got, c.want) {
				t.Errorf("reason = %q, want it to name %q", got, c.want)
			}
		})
	}
}

// TestReconcileSkipsPodWithoutTaskInstance: a failed pod with no
// task-instance-id annotation cannot be mapped to a task, so it must be skipped
// (no FailTask, no panic) rather than reported against an empty id.
func TestReconcileSkipsPodWithoutTaskInstance(t *testing.T) {
	cs := fake.NewClientset(managedPod("p-fail", "", corev1.PodFailed)) // empty tiID
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(reporter.settled) != 0 {
		t.Errorf("a pod without a task-instance annotation must not be reported, got %v", reporter.settled)
	}
}

type errReporter struct{}

func (errReporter) FailTask(context.Context, string, int, int, string) error {
	return errors.New("metadatabase unavailable")
}

func (errReporter) SucceedTask(context.Context, string, int, int) error {
	return errors.New("metadatabase unavailable")
}

func (errReporter) RescheduleTask(context.Context, string, int, int, time.Time) error {
	return errors.New("metadatabase unavailable")
}

// TestReconcileToleratesReporterError: a failure to record one pod's failure must
// not abort the reconcile (the next tick retries; other pods still process).
func TestReconcileToleratesReporterError(t *testing.T) {
	cs := fake.NewClientset(managedPod("p-fail", "ti1", corev1.PodFailed))
	r := NewReconciler(cs, "leoflow", errReporter{})
	if err := r.Reconcile(context.Background()); err != nil {
		t.Errorf("a reporter error must not fail the reconcile, got %v", err)
	}
}

// TestReconcileToleratesGCDeleteError: a failure to garbage-collect a finished
// pod must be logged, not fatal — the reconcile still completes.
func TestReconcileToleratesGCDeleteError(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	cs := fake.NewClientset(agedPod("old-success", corev1.PodSucceeded, now.Add(-30*time.Minute)))
	cs.PrependReactor("delete", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("delete forbidden")
	})
	r := NewReconciler(cs, "leoflow", &fakeReporter{})
	r.now = func() time.Time { return now }
	r.ttl = 10 * time.Minute
	if err := r.Reconcile(context.Background()); err != nil {
		t.Errorf("a GC delete error must not fail the reconcile, got %v", err)
	}
}

// TestReconcileKeepsFailedPodWhenReportErrors: a failed pod must not be
// garbage-collected until its failure has been durably recorded. The pod is the
// only signal that lets the next tick retry the report; deleting it after a
// failed FailTask strands the task instance in `running` until the heartbeat
// reaper (the slower backstop) catches it. So a failed report on an aged pod
// skips collection and the next tick tries again. Never let one component be
// both the state-recorder and the garbage-collector without ordering the two.
func TestReconcileKeepsFailedPodWhenReportErrors(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	cs := fake.NewClientset(agedPod("old-failed", corev1.PodFailed, now.Add(-30*time.Minute)))
	r := NewReconciler(cs, "leoflow", errReporter{})
	r.now = func() time.Time { return now }
	r.ttl = 10 * time.Minute

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	pods, _ := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{})
	found := false
	for _, p := range pods.Items {
		if p.Name == "old-failed" {
			found = true
		}
	}
	if !found {
		t.Error("a failed pod whose report errored was garbage-collected; it must be kept so the next tick can retry the report")
	}
}

// TestReconcileCollectsAgedOrphanFailedPod: a failed pod with no task-instance
// annotation has no terminal state to preserve, so reportFailure reports "nothing
// to do" (nil) and the aged orphan is still collected — the report-before-collect
// ordering must not strand orphan garbage.
func TestReconcileCollectsAgedOrphanFailedPod(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	orphan := managedPod("orphan", "", corev1.PodFailed) // empty tiID
	orphan.CreationTimestamp = metav1.NewTime(now.Add(-30 * time.Minute))
	cs := fake.NewClientset(orphan)
	r := NewReconciler(cs, "leoflow", &fakeReporter{})
	r.now = func() time.Time { return now }
	r.ttl = 10 * time.Minute

	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	pods, _ := cs.CoreV1().Pods("leoflow").List(context.Background(), metav1.ListOptions{})
	for _, p := range pods.Items {
		if p.Name == "orphan" {
			t.Error("an aged orphan failed pod (no task instance) should still be garbage-collected")
		}
	}
}
