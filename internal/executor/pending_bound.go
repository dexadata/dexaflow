package executor

import (
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
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
		return verdict{terminal: true, settle: settleInfra, collect: true, reason: imagePullReason(w)}
	}
	if r.pendingTimeout > 0 {
		if c := unschedulable(pod); c != nil && now.Sub(unschedulableSince(pod, c)) > r.pendingTimeout {
			return verdict{terminal: true, settle: settleFailed, collect: true, reason: unschedulableReason(c, r.pendingTimeout)}
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
