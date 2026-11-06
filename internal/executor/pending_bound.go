package executor

import (
	"context"
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
// pod may have been placed since, and its agent registered. So a bound fails
// the attempt only while it has not started (PendingOutcomeReporter), and the
// pod is deleted with its UID and resourceVersion as preconditions: a pod that
// changed since the snapshot (bound to a node, its pull succeeded) is left for
// the next sweep to judge.

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
// decisions: pending_bound_unschedulable, pending_bound_image_pull, and their
// _skipped forms for an attempt that started or was settled since the
// snapshot. Nil records nothing.
func (r *Reconciler) SetDecisionRecorder(d DecisionRecorder) { r.decisions = d }

// PendingOutcomeReporter fails an attempt only while it has not started
// (scheduled or queued), as infra when infra is set, and reports whether the
// row changed. The reconciler uses it for the Pending bounds when its
// reporter implements it; storage.ExecutionStore does.
type PendingOutcomeReporter interface {
	FailPendingTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, reason string, infra bool) (bool, error)
}

// Bound names, the suffix of their decisions.
const (
	boundUnschedulable = "unschedulable"
	boundImagePull     = "image_pull"
)

// settleBound records a Pending bound's verdict: before the attempt started
// only, when the reporter can guard on that, or with the active-state settle
// of the other verdicts otherwise (a reporter without the guard, in tests).
func (r *Reconciler) settleBound(ctx context.Context, tiID string, tryNumber, epoch int, v verdict) error {
	infra := v.settle == settleInfra
	pr, ok := r.reporter.(PendingOutcomeReporter)
	if !ok {
		if infra {
			return r.reporter.FailTaskInfra(ctx, tiID, tryNumber, epoch, v.reason)
		}
		return r.reporter.FailTask(ctx, tiID, tryNumber, epoch, v.reason)
	}
	applied, err := pr.FailPendingTask(ctx, tiID, tryNumber, epoch, v.reason, infra)
	if err != nil {
		return err
	}
	if !applied {
		r.recordDecision("pending_bound_" + v.bound + "_skipped")
		return nil
	}
	r.recordDecision("pending_bound_" + v.bound)
	return nil
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
		r.gates.seeGated(pod)
		if c := unschedulable(pod); c != nil && now.Sub(r.gates.since(pod, unschedulableSince(pod, c), now)) > r.pendingTimeout {
			return verdict{terminal: true, settle: settleFailed, collect: true, bound: boundUnschedulable, reason: unschedulableReason(c, r.pendingTimeout)}
		}
	}
	return v
}

// pullWaiting returns the task container's waiting state when it is a pull
// the kubelet retries, or nil.
func pullWaiting(pod *corev1.Pod) *corev1.ContainerStateWaiting {
	for _, cs := range pod.Status.ContainerStatuses {
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

// gateTracker corrects the unschedulable clock of a pod that waited behind
// scheduling gates (Kueue, a gate webhook). Such a pod is PodScheduled=False
// with reason SchedulingGated first, and the scheduler keeps the condition's
// lastTransitionTime when the reason turns Unschedulable, since the status
// stays False: the time spent gated would count against
// executor.pending_timeout_seconds. A pod this reconciler saw gated has its
// clock start when it first saw it Unschedulable instead. A pod whose gate
// was lifted before this process watched it keeps the condition's time. The
// map holds Pending pods only and is pruned to the swept set every sweep.
type gateTracker struct {
	mu sync.Mutex
	// ungated maps a pod seen gated to when it was first seen Unschedulable
	// after that, zero while still gated.
	ungated map[types.UID]time.Time
}

// seeGated records a pod waiting behind its scheduling gates.
func (g *gateTracker) seeGated(pod *corev1.Pod) {
	if pod.UID == "" || !schedulingGated(pod) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ungated == nil {
		g.ungated = map[types.UID]time.Time{}
	}
	g.ungated[pod.UID] = time.Time{}
}

// since returns the later of from and when the pod, once seen gated, was
// first seen Unschedulable.
func (g *gateTracker) since(pod *corev1.Pod, from, now time.Time) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	at, ok := g.ungated[pod.UID]
	if !ok {
		return from
	}
	if at.IsZero() {
		at = now
		g.ungated[pod.UID] = at
	}
	if at.After(from) {
		return at
	}
	return from
}

// prune forgets every pod not in pods.
func (g *gateTracker) prune(pods []*corev1.Pod) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.ungated) == 0 {
		return
	}
	keep := make(map[types.UID]bool, len(pods))
	for _, p := range pods {
		keep[p.UID] = true
	}
	for uid := range g.ungated {
		if !keep[uid] {
			delete(g.ungated, uid)
		}
	}
}

// schedulingGated reports whether the pod waits behind scheduling gates.
func schedulingGated(pod *corev1.Pod) bool {
	if len(pod.Spec.SchedulingGates) > 0 {
		return true
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason == corev1.PodReasonSchedulingGated {
			return true
		}
	}
	return false
}
