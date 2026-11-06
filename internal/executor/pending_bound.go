package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Bounds on a task pod that stays Pending (#1609, ADR 0053).
//
// A pod the scheduler cannot place (a node selector no node matches, a missing
// toleration, a request larger than any node, a volume that never binds) has
// no container status and no start time, so neither classifyPod nor
// activeDeadlineSeconds ever ends it: the task stayed queued indefinitely,
// holding its pool slot and its run's max_active_tasks. With
// executor.pending_timeout_seconds set, a pod unschedulable for longer than
// that is deleted and its task failed with a reason that names scheduling.
//
// An image pull that fails (ErrImagePull, ImagePullBackOff) used to fail the
// task on the first observation, spending its retries on a registry 429 or a
// network blip although ADR 0051 puts bad images on the infra rail. The
// kubelet retries the pull with a back-off, so the reconciler now waits
// imagePullGrace from the pod's placement; a pull still failing then is
// settled as infra (the task is re-placed on the infra budget) and the pod is
// deleted. InvalidImageName never resolves and stays a task failure at once.
//
// Both bounds decide from a snapshot of a Pending pod, which can be stale: the
// pod may have been placed or its pull may have succeeded since, and its agent
// registered, possibly before the kubelet pushed the pod's new status. So a
// bound fails the attempt only while it has not started
// (PendingOutcomeReporter) and leaves the pod of a started attempt alone; a
// pod it collects is deleted with its UID and resourceVersion as
// preconditions, so one that changed since the snapshot is left for the next
// sweep to judge.

// imagePullGrace is how long a failing image pull is left to the kubelet's
// back-off (10 s doubling to its 5 minute cap) before the attempt is settled
// as infra: a few pulls, past a short registry or network fault.
const imagePullGrace = 5 * time.Minute

// imagePullWaiting are the waiting reasons of a pull the kubelet retries.
var imagePullWaiting = map[string]bool{
	"ErrImagePull":     true,
	"ImagePullBackOff": true,
}

// SetPendingTimeout sets executor.pending_timeout_seconds: a task pod
// unschedulable for longer than d is deleted and its task failed. Zero or
// negative (the default) leaves such pods waiting, as before.
func (r *Reconciler) SetPendingTimeout(d time.Duration) { r.pendingTimeout = d }

// SetDecisionRecorder counts the Pending bounds' settles as scheduler
// decisions: pending_bound_unschedulable and pending_bound_image_pull, their
// _started forms for an attempt that is running since the snapshot (its pod
// is left alone) and _gone forms for one superseded or settled already (its
// pod is collected). Nil records nothing.
func (r *Reconciler) SetDecisionRecorder(d DecisionRecorder) { r.decisions = d }

// PendingSettle is what a Pending bound's settle found.
type PendingSettle int

const (
	// PendingSettled means the attempt had not started and is now failed.
	PendingSettled PendingSettle = iota
	// PendingStarted means the same attempt is running, or may be starting
	// (a start committed while the settle ran), so its pod may be its live
	// execution (placed, or its pull succeeded, since the snapshot) and must
	// be left alone.
	PendingStarted
	// PendingGone means the attempt was superseded or settled already; its pod is
	// a leftover the bound may still collect.
	PendingGone
)

// PendingOutcomeReporter fails an attempt only while it has not started
// (scheduled or queued), as infra when infra is set, and reports what it
// found. The reconciler uses it for the Pending bounds when its reporter
// implements it; storage.ExecutionStore does.
type PendingOutcomeReporter interface {
	FailPendingTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, reason string, infra bool) (PendingSettle, error)
}

// errAttemptStarted is settleBound's answer for an attempt that started since
// the snapshot: not a failure to log, but its pod must not be collected.
var errAttemptStarted = errors.New("the attempt started since the pod was read")

// Bound names, the suffix of their decisions.
const (
	boundUnschedulable = "unschedulable"
	boundImagePull     = "image_pull"
)

// settleBound records a Pending bound's verdict, before the attempt started
// only. It returns errAttemptStarted for an attempt that is running since the
// snapshot, so the caller leaves its pod alone. A reporter without the guard
// is refused, since the active-state settles of the other verdicts would fail
// a running attempt (#1609 review): the pod is left for the sweep and the
// pending_bound_unguarded decision says the wiring is wrong.
func (r *Reconciler) settleBound(ctx context.Context, tiID string, tryNumber, epoch int, v verdict) error {
	pr, ok := r.reporter.(PendingOutcomeReporter)
	if !ok {
		r.recordDecision("pending_bound_unguarded")
		return errAttemptStarted
	}
	got, err := pr.FailPendingTask(ctx, tiID, tryNumber, epoch, v.reason, v.settle == settleInfra)
	switch {
	case err != nil:
		return err
	case got == PendingStarted:
		r.recordDecision("pending_bound_" + v.bound + "_started")
		return errAttemptStarted
	case got == PendingGone:
		r.recordDecision("pending_bound_" + v.bound + "_gone")
		return nil
	default:
		r.recordDecision("pending_bound_" + v.bound)
		return nil
	}
}

func (r *Reconciler) recordDecision(d string) {
	if r.decisions != nil {
		r.decisions.RecordSchedulerDecision(d)
	}
}

// collectBound deletes a pod a Pending bound settled, only if it is still the
// object the verdict was read from: a pod placed or otherwise changed since
// fails the resourceVersion precondition (Conflict) and is left to the next
// sweep, which reads it afresh.
func (r *Reconciler) collectBound(ctx context.Context, pod *corev1.Pod) {
	var pre metav1.Preconditions
	if pod.UID != "" {
		uid := pod.UID
		pre.UID = &uid
	}
	if pod.ResourceVersion != "" {
		rv := pod.ResourceVersion
		pre.ResourceVersion = &rv
	}
	err := r.clientset.CoreV1().Pods(r.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &pre})
	switch {
	case err == nil, apierrors.IsNotFound(err):
	case apierrors.IsConflict(err):
		slog.Info("task pod changed since its Pending bound was read; leaving it to the next sweep", "pod", pod.Name)
	default:
		slog.Error("deleting a bounded Pending task pod", "pod", pod.Name, "error", err)
	}
}

// classify is classifyPod plus the time-based bounds on a Pending pod. A
// verdict from a bound sets collect, so the pod is deleted right after its
// settle instead of after the grace period: an unschedulable pod could still
// be placed later and start a settled attempt, and a pulling one keeps
// pulling.
func (r *Reconciler) classify(pod *corev1.Pod, now time.Time) verdict {
	v := classifyPod(pod)
	if v.terminal || pod.Status.Phase != corev1.PodPending {
		return v
	}
	if w := pullWaiting(pod); w != nil && now.Sub(placedAt(pod)) >= imagePullGrace {
		return verdict{terminal: true, settle: settleInfra, collect: true, bound: boundImagePull, reason: imagePullReason(w)}
	}
	if r.pendingTimeout > 0 {
		if c := unschedulable(pod); c != nil && now.Sub(r.unsched.since(pod, unschedulableSince(pod, c), now)) > r.pendingTimeout {
			return verdict{terminal: true, settle: settleFailed, collect: true, bound: boundUnschedulable, reason: unschedulableReason(c, r.pendingTimeout)}
		}
	}
	return v
}

// pullWaiting returns the task container's waiting state when it is a pull
// the kubelet retries, or nil.
func pullWaiting(pod *corev1.Pod) *corev1.ContainerStateWaiting {
	for _, cs := range pod.Status.ContainerStatuses {
		// The task container only: a sidecar an admission webhook injected
		// may fail its pull while the task runs.
		if cs.Name != taskContainerName {
			continue
		}
		if w := cs.State.Waiting; w != nil && imagePullWaiting[w.Reason] {
			return w
		}
	}
	return nil
}

// placedAt is when the pod was scheduled onto its node, or its creation time
// when the condition is missing.
func placedAt(pod *corev1.Pod) time.Time {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionTrue && !c.LastTransitionTime.IsZero() {
			return c.LastTransitionTime.Time
		}
	}
	return pod.CreationTimestamp.Time
}

// unschedulable returns the pod's PodScheduled=False condition with reason
// Unschedulable, or nil.
func unschedulable(pod *corev1.Pod) *corev1.PodCondition {
	for i := range pod.Status.Conditions {
		c := &pod.Status.Conditions[i]
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonUnschedulable {
			return c
		}
	}
	return nil
}

// unschedulableSince is when the condition turned unschedulable, or the pod's
// creation when the scheduler did not stamp it.
func unschedulableSince(pod *corev1.Pod, c *corev1.PodCondition) time.Time {
	if !c.LastTransitionTime.IsZero() {
		return c.LastTransitionTime.Time
	}
	return pod.CreationTimestamp.Time
}

// unschedulableReason names the scheduling failure with the scheduler's own
// message, which says which constraint no node met. Bounded, since the
// message is a cluster-supplied field.
func unschedulableReason(c *corev1.PodCondition, timeout time.Duration) string {
	return boundReason(fmt.Sprintf("unschedulable: the task pod could not be placed on any node for %s (executor.pending_timeout_seconds): %s; check the task's node_selector, tolerations, resource requests and volumes.",
		timeout, strings.TrimSpace(c.Message)))
}

// imagePullReason names the failing pull with the kubelet's message. It
// starts with the image_pull_failed mark, the "<mark>: " shape of the other
// infra settles.
func imagePullReason(w *corev1.ContainerStateWaiting) string {
	return boundReason(fmt.Sprintf("image_pull_failed: %s for %s: %s; check the image reference, the pull secret and the registry.",
		w.Reason, imagePullGrace, strings.TrimSpace(w.Message)))
}

// unschedTracker starts the unschedulable clock no earlier than when this
// reconciler first saw the pod unschedulable. The condition's
// lastTransitionTime alone over-counts a pod that waited behind scheduling
// gates (Kueue, a gate webhook): such a pod is PodScheduled=False with reason
// SchedulingGated first, and the scheduler keeps the time when the reason
// turns Unschedulable, since the status stays False, so the time spent gated
// would count against executor.pending_timeout_seconds and fail the task as
// soon as its gate lifts. Taking the later of the two never over-counts. The
// cost is that the clock starts over when leadership moves or the server
// restarts, which delays a bound, the safe direction. The map holds the
// unschedulable pods seen recently and is pruned every sweep.
type unschedTracker struct {
	mu    sync.Mutex
	first map[types.UID]unschedSeen
}

type unschedSeen struct {
	first, last time.Time
}

// since returns the later of from and when this reconciler first saw the pod
// unschedulable, now when this is the first time.
func (u *unschedTracker) since(pod *corev1.Pod, from, now time.Time) time.Time {
	if pod.UID == "" {
		return from
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.first == nil {
		u.first = map[types.UID]unschedSeen{}
	}
	e, ok := u.first[pod.UID]
	if !ok {
		e.first = now
	}
	e.last = now
	u.first[pod.UID] = e
	if e.first.After(from) {
		return e.first
	}
	return from
}

// prune forgets every pod not seen unschedulable since before (the start of
// the sweep that calls it), so a pod placed, settled or deleted is dropped
// while one an event handler saw during the sweep is kept.
func (u *unschedTracker) prune(before time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for uid, e := range u.first {
		if e.last.Before(before) {
			delete(u.first, uid)
		}
	}
}
