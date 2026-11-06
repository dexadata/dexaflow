package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var pendingNow = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

// unschedulablePod is a managed task pod the scheduler could not place since
// `since`.
func unschedulablePod(name, tiID string, since time.Time) *corev1.Pod {
	p := managedPod(name, tiID, corev1.PodPending)
	p.CreationTimestamp = metav1.NewTime(since)
	p.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
		Message:            "0/3 nodes are available: 3 node(s) didn't match Pod's node affinity/selector.",
		LastTransitionTime: metav1.NewTime(since),
	}}
	return p
}

// pullFailingPod is a managed task pod scheduled at `since` whose image pull
// keeps failing with reason.
func pullFailingPod(name, tiID, reason string, since time.Time) *corev1.Pod {
	p := managedPod(name, tiID, corev1.PodPending)
	p.CreationTimestamp = metav1.NewTime(since)
	p.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(since),
	}}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: taskContainerName,
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
			Reason: reason, Message: "failed to pull image \"registry/x:1\": 429 Too Many Requests",
		}},
	}}
	return p
}

func reconcileOnce(t *testing.T, pod *corev1.Pod, pendingTimeout time.Duration) (*fakeReporter, *fake.Clientset) {
	t.Helper()
	cs := fake.NewClientset(pod)
	reporter := &fakeReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	r.now = func() time.Time { return pendingNow }
	r.SetPendingTimeout(pendingTimeout)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return reporter, cs
}

func podGone(t *testing.T, cs *fake.Clientset, name string) bool {
	t.Helper()
	_, err := cs.CoreV1().Pods("leoflow").Get(context.Background(), name, metav1.GetOptions{})
	return err != nil
}

// TestUnschedulablePodPastPendingTimeoutFails is the regression test of R-08
// (#1609): a pod the scheduler cannot place used to stay queued forever,
// holding its pool slot. Past executor.pending_timeout_seconds the task is
// failed with a reason that names scheduling, and the pod is deleted at once
// so a later placement cannot start the settled attempt.
func TestUnschedulablePodPastPendingTimeoutFails(t *testing.T) {
	pod := unschedulablePod("p1", "ti1", pendingNow.Add(-16*time.Minute))
	reporter, cs := reconcileOnce(t, pod, 15*time.Minute)
	got, ok := reporter.settled["ti1"]
	if !ok || got.kind != settleFailed {
		t.Fatalf("settled = %+v, want a task failure", reporter.settled)
	}
	if !strings.HasPrefix(got.reason, "unschedulable:") || !strings.Contains(got.reason, "node affinity") {
		t.Errorf("reason = %q, want it to start with unschedulable: and carry the scheduler's message", got.reason)
	}
	if !podGone(t, cs, "p1") {
		t.Error("the unschedulable pod must be deleted when its task is settled")
	}
}

// TestUnschedulablePodWithinPendingTimeoutWaits: inside the bound (a cluster
// autoscaler adding a node) and with the bound off (0, the default), the pod
// keeps waiting as before.
func TestUnschedulablePodWithinPendingTimeoutWaits(t *testing.T) {
	cases := map[string]struct {
		since   time.Time
		timeout time.Duration
	}{
		"within the bound": {pendingNow.Add(-14 * time.Minute), 15 * time.Minute},
		"bound off":        {pendingNow.Add(-6 * time.Hour), 0},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			reporter, cs := reconcileOnce(t, unschedulablePod("p1", "ti1", c.since), c.timeout)
			if len(reporter.settled) != 0 {
				t.Errorf("settled = %+v, want none", reporter.settled)
			}
			if podGone(t, cs, "p1") {
				t.Error("a waiting pod must not be deleted")
			}
		})
	}
}

// TestUnschedulableMeasuredFromTheCondition: the bound counts from when the
// pod became unschedulable, not from its creation (a pod placed, then its
// node lost and its PodScheduled condition reset, starts over).
func TestUnschedulableMeasuredFromTheCondition(t *testing.T) {
	pod := unschedulablePod("p1", "ti1", pendingNow.Add(-time.Minute))
	pod.CreationTimestamp = metav1.NewTime(pendingNow.Add(-time.Hour))
	reporter, _ := reconcileOnce(t, pod, 15*time.Minute)
	if len(reporter.settled) != 0 {
		t.Errorf("settled = %+v, want none: unschedulable for a minute only", reporter.settled)
	}
}

// TestImagePullFailureFirstSightWaits is the regression test of R-09 (#1609):
// a registry 429 or a short network blip used to fail the task, spending its
// retries, on the first observation. The kubelet retries the pull, so the
// reconciler waits.
func TestImagePullFailureFirstSightWaits(t *testing.T) {
	for _, reason := range []string{"ErrImagePull", "ImagePullBackOff"} {
		t.Run(reason, func(t *testing.T) {
			reporter, cs := reconcileOnce(t, pullFailingPod("p1", "ti1", reason, pendingNow.Add(-30*time.Second)), 0)
			if len(reporter.settled) != 0 {
				t.Errorf("settled = %+v, want none on the first back-off", reporter.settled)
			}
			if podGone(t, cs, "p1") {
				t.Error("a pod still pulling must not be deleted")
			}
		})
	}
}

// TestImagePullFailurePastGraceIsInfra: a pull still failing past
// imagePullGrace is settled on the infra rail (ADR 0051: a bad image is the
// substrate's failure), so the task is re-placed without spending a retry,
// and the pod is deleted.
func TestImagePullFailurePastGraceIsInfra(t *testing.T) {
	reporter, cs := reconcileOnce(t, pullFailingPod("p1", "ti1", "ImagePullBackOff", pendingNow.Add(-imagePullGrace)), 0)
	got, ok := reporter.settled["ti1"]
	if !ok || got.kind != settleInfra {
		t.Fatalf("settled = %+v, want an infra failure", reporter.settled)
	}
	if !strings.HasPrefix(got.reason, "image_pull_failed:") || !strings.Contains(got.reason, "ImagePullBackOff") {
		t.Errorf("reason = %q, want image_pull_failed: naming ImagePullBackOff", got.reason)
	}
	if !podGone(t, cs, "p1") {
		t.Error("the pod must be deleted when its task is settled")
	}
}

// TestInvalidImageNameStaysTerminal: a malformed reference never resolves, so
// it still fails the task on first sight.
func TestInvalidImageNameStaysTerminal(t *testing.T) {
	reporter, _ := reconcileOnce(t, pullFailingPod("p1", "ti1", "InvalidImageName", pendingNow.Add(-time.Second)), 0)
	if got := reporter.settled["ti1"]; got.kind != settleFailed {
		t.Errorf("settled = %+v, want a task failure", reporter.settled)
	}
}
