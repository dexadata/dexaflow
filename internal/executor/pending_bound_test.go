package executor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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

func reconcileOnce(t *testing.T, pod *corev1.Pod, pendingTimeout time.Duration) (*pendingReporter, *fake.Clientset) {
	t.Helper()
	cs := fake.NewClientset(pod)
	reporter := &pendingReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	r.SetPendingTimeout(pendingTimeout)
	prime(t, r)
	r.now = func() time.Time { return pendingNow }
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return reporter, cs
}

// prime runs a sweep a day before pendingNow, before any bound is due, so the
// reconciler has seen every pod: the unschedulable clock starts no earlier
// than its first sight (unschedTracker), and a day back leaves the pod's own
// condition time the later one.
func prime(t *testing.T, r *Reconciler) {
	t.Helper()
	r.now = func() time.Time { return pendingNow.Add(-24 * time.Hour) }
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("priming Reconcile: %v", err)
	}
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

// pendingReporter is a reporter with the before-start guard: started marks the
// attempts that moved to running since the snapshot and gone the ones
// superseded or settled, failing the settle of every attempt when err is set.
type pendingReporter struct {
	fakeReporter
	started map[string]bool
	gone    map[string]bool
	err     error
	infra   map[string]bool
}

func (p *pendingReporter) FailPendingTask(_ context.Context, id string, try, _ int, reason string, infra bool) (PendingSettle, error) {
	if p.err != nil {
		return PendingGone, p.err
	}
	if p.started[id] {
		return PendingStarted, nil
	}
	if p.gone[id] {
		return PendingGone, nil
	}
	if p.infra == nil {
		p.infra = map[string]bool{}
	}
	p.infra[id] = infra
	kind := settleFailed
	if infra {
		kind = settleInfra
	}
	p.put(id, settledOutcome{kind: kind, tryNumber: try, reason: reason})
	return PendingSettled, nil
}

type decisionLog []string

func (d *decisionLog) RecordSchedulerDecision(s string) { *d = append(*d, s) }

func reconcilePending(t *testing.T, cs *fake.Clientset, reporter OutcomeReporter, pendingTimeout time.Duration) *decisionLog {
	t.Helper()
	r := NewReconciler(cs, "leoflow", reporter)
	r.SetPendingTimeout(pendingTimeout)
	prime(t, r)
	r.now = func() time.Time { return pendingNow }
	var d decisionLog
	r.SetDecisionRecorder(&d)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return &d
}

// TestPendingBoundUsesTheBeforeStartGuard: with a reporter that has the
// guard, both bounds settle through it, infra for the pull only, and count a
// decision.
func TestPendingBoundUsesTheBeforeStartGuard(t *testing.T) {
	cs := fake.NewClientset(
		unschedulablePod("p1", "ti1", pendingNow.Add(-16*time.Minute)),
		pullFailingPod("p2", "ti2", "ErrImagePull", pendingNow.Add(-imagePullGrace)),
	)
	reporter := &pendingReporter{}
	d := reconcilePending(t, cs, reporter, 15*time.Minute)
	if reporter.infra["ti1"] || !reporter.infra["ti2"] || len(reporter.infra) != 2 {
		t.Errorf("guarded settles = %v, want ti1 a task failure and ti2 infra", reporter.infra)
	}
	if !podGone(t, cs, "p1") || !podGone(t, cs, "p2") {
		t.Error("the settled pods must be deleted")
	}
	if got := strings.Join(*d, ","); got != "pending_bound_unschedulable,pending_bound_image_pull" && got != "pending_bound_image_pull,pending_bound_unschedulable" {
		t.Errorf("decisions = %v", *d)
	}
}

// TestPendingBoundLeavesAStartedAttempt is the reviews' race (#1609): the
// snapshot shows the pod Pending past its bound, but its pull succeeded (or
// it was placed) since and its agent moved the attempt to running, possibly
// before the kubelet pushed the pod's new status, so the snapshot's
// resourceVersion may still be current. The guarded settle changes nothing
// and the pod is not deleted at all.
func TestPendingBoundLeavesAStartedAttempt(t *testing.T) {
	cs := fake.NewClientset(pullFailingPod("p1", "ti1", "ImagePullBackOff", pendingNow.Add(-imagePullGrace)))
	deletes := 0
	cs.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		deletes++
		return false, nil, nil
	})
	reporter := &pendingReporter{started: map[string]bool{"ti1": true}}
	d := reconcilePending(t, cs, reporter, 0)
	if len(reporter.settled) != 0 {
		t.Errorf("settled = %+v, want none for a started attempt", reporter.settled)
	}
	if strings.Join(*d, ",") != "pending_bound_image_pull_started" {
		t.Errorf("decisions = %v, want the started attempt counted", *d)
	}
	if deletes != 0 || podGone(t, cs, "p1") {
		t.Errorf("a started attempt's pod was deleted (%d delete calls)", deletes)
	}
}

// TestPendingBoundCollectsAGoneAttempt: an attempt superseded or settled
// already leaves its Pending pod a leftover, which the bound still collects,
// with the snapshot's UID and resourceVersion as preconditions.
func TestPendingBoundCollectsAGoneAttempt(t *testing.T) {
	pod := unschedulablePod("p1", "ti1", pendingNow.Add(-16*time.Minute))
	pod.UID = "uid-1"
	pod.ResourceVersion = "7"
	cs := fake.NewClientset(pod)
	var opts *metav1.DeleteOptions
	cs.PrependReactor("delete", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		o := a.(k8stesting.DeleteActionImpl).DeleteOptions
		opts = &o
		return false, nil, nil
	})
	reporter := &pendingReporter{gone: map[string]bool{"ti1": true}}
	d := reconcilePending(t, cs, reporter, 15*time.Minute)
	if strings.Join(*d, ",") != "pending_bound_unschedulable_gone" {
		t.Errorf("decisions = %v", *d)
	}
	if opts == nil || opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "uid-1" ||
		opts.Preconditions.ResourceVersion == nil || *opts.Preconditions.ResourceVersion != "7" {
		t.Errorf("delete options = %+v, want the snapshot's UID and resourceVersion as preconditions", opts)
	}
	if !podGone(t, cs, "p1") {
		t.Error("a gone attempt's leftover pod must be collected")
	}
}

// TestPendingBoundConflictKeepsThePod: a pod that changed since the snapshot
// fails the delete's precondition and is left to the next sweep.
func TestPendingBoundConflictKeepsThePod(t *testing.T) {
	cs := fake.NewClientset(unschedulablePod("p1", "ti1", pendingNow.Add(-16*time.Minute)))
	cs.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, "p1", errors.New("the resourceVersion changed"))
	})
	reconcilePending(t, cs, &pendingReporter{}, 15*time.Minute)
	if podGone(t, cs, "p1") {
		t.Error("a pod that changed since the snapshot must not be deleted")
	}
}

// TestPendingBoundRefusesAnUnguardedReporter: a reporter without the
// before-start guard would fail a running attempt, so the bound settles
// nothing, keeps the pod and says so.
func TestPendingBoundRefusesAnUnguardedReporter(t *testing.T) {
	cs := fake.NewClientset(unschedulablePod("p1", "ti1", pendingNow.Add(-16*time.Minute)))
	reporter := &fakeReporter{}
	d := reconcilePending(t, cs, reporter, 15*time.Minute)
	if len(reporter.settled) != 0 {
		t.Errorf("settled = %+v through an unguarded reporter", reporter.settled)
	}
	if strings.Join(*d, ",") != "pending_bound_unguarded" {
		t.Errorf("decisions = %v", *d)
	}
	if podGone(t, cs, "p1") {
		t.Error("the pod must be kept")
	}
}

// TestPendingBoundFailedSettleKeepsThePod: a settle that fails (a database
// error) leaves the pod, the only signal, for the next sweep, although the
// bound's verdict collects at once.
func TestPendingBoundFailedSettleKeepsThePod(t *testing.T) {
	cs := fake.NewClientset(unschedulablePod("p1", "ti1", pendingNow.Add(-16*time.Minute)))
	reconcilePending(t, cs, &pendingReporter{err: errors.New("database down")}, 15*time.Minute)
	if podGone(t, cs, "p1") {
		t.Error("a pod whose settle failed must be kept")
	}
}

// TestPendingTimeoutSkipsTheTimeGated: a pod that waited behind scheduling
// gates (Kueue) keeps the condition's lastTransitionTime when the scheduler
// turns the reason to Unschedulable, so the gated time used to count against
// the bound and fail the task as soon as its gate lifted. The clock starts no
// earlier than the reconciler's first sight of the pod unschedulable.
func TestPendingTimeoutSkipsTheTimeGated(t *testing.T) {
	gated := unschedulablePod("p1", "ti1", pendingNow.Add(-20*time.Minute))
	gated.UID = "uid-1"
	gated.Status.Conditions[0].Reason = corev1.PodReasonSchedulingGated
	gated.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "kueue.x-k8s.io/admission"}}
	cs := fake.NewClientset(gated)
	reporter := &pendingReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	now := pendingNow
	r.now = func() time.Time { return now }
	r.SetPendingTimeout(15 * time.Minute)
	sweep := func() {
		t.Helper()
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	sweep()

	// The gate is lifted; the condition's time stays 20 minutes back.
	ungated := gated.DeepCopy()
	ungated.Spec.SchedulingGates = nil
	ungated.Status.Conditions[0].Reason = corev1.PodReasonUnschedulable
	if _, err := cs.CoreV1().Pods("leoflow").Update(context.Background(), ungated, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	now = pendingNow.Add(time.Minute)
	sweep()
	if len(reporter.settled) != 0 {
		t.Fatalf("settled = %+v: the time behind the gate counted against the bound", reporter.settled)
	}
	now = pendingNow.Add(17 * time.Minute)
	sweep()
	if got := reporter.settled["ti1"]; got.kind != settleFailed {
		t.Errorf("settled = %+v, want the bound once unschedulable for 16 minutes", reporter.settled)
	}
}

// TestPendingTimeoutStartsOverOnANewReconciler: a new leader (or a restart)
// has not seen the pod, so its clock starts at its first sight, delaying the
// bound rather than firing it for time it cannot account for.
func TestPendingTimeoutStartsOverOnANewReconciler(t *testing.T) {
	pod := unschedulablePod("p1", "ti1", pendingNow.Add(-16*time.Minute))
	pod.UID = "uid-1"
	cs := fake.NewClientset(pod)
	reporter := &pendingReporter{}
	r := NewReconciler(cs, "leoflow", reporter)
	r.now = func() time.Time { return pendingNow }
	r.SetPendingTimeout(15 * time.Minute)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(reporter.settled) != 0 {
		t.Errorf("settled = %+v on the reconciler's first sight", reporter.settled)
	}
}

// TestUnschedTrackerPrunes: a pod not seen unschedulable during the sweep is
// forgotten; one seen during it (by the sweep or an event) is kept.
func TestUnschedTrackerPrunes(t *testing.T) {
	var u unschedTracker
	p := unschedulablePod("p1", "ti1", pendingNow)
	p.UID = "uid-1"
	u.since(p, pendingNow, pendingNow)
	u.prune(pendingNow)
	if len(u.first) != 1 {
		t.Fatal("a pod seen during the sweep was forgotten")
	}
	u.prune(pendingNow.Add(time.Second))
	if len(u.first) != 0 {
		t.Errorf("a pod not seen since the sweep started is kept: %v", u.first)
	}
}
