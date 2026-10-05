package executor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This file gives the Kubernetes executor the ability to tear down a reaped
// task's pod (#474). Before this, the three scheduler reapers only wrote DB
// state — a reaped TI's pod kept running to completion, doing at-most-once work
// twice. The reapers now call these methods after the DB transition so the pod
// is actually stopped.
//
// Every selector below reuses the exact label scheme BuildPod stamps
// (leoflow.io/run-id, /task-id, /try-number). The attempt epoch
// (leoflow.io/attempt-epoch, ADR 0051 amendment) is filtered in Go after the
// list, because a pod stamped before the epoch existed has no such label and is
// epoch 0, which a label selector cannot express. Deletion is by List-then-Delete,
// not DeleteCollection: a reap must judge each pod (see terminalForTeardown
// below), and a cluster whose Role predates the `deletecollection` grant would
// 403 it. The settled-run collection (run_settle_collect.go) is the one caller
// of DeleteCollection, and it falls back to per-pod deletes on a 403. Each
// per-pod delete names the pod it listed and pins that pod's UID as a
// precondition (#901), so the delete can never act on a pod the list did not
// return. NotFound is always tolerated (a pod may have been garbage-collected
// between the list and the delete), and so is a failed UID precondition, which
// means the pod listed is already gone.
//
// Both methods skip a pod that has already reached a terminal phase (#928) —
// see terminalForTeardown. That is enforced HERE, at the one delete site, rather
// than in each reaper's decision: agent-lost fires on heartbeat staleness alone
// at 90s and orphan-run abandons a whole run at 5m, neither reading pod presence
// at all, so a per-reaper guard would have to be written four times and kept
// right four times. No reaper's mark or decision changes.

// DeleteTaskPod deletes the pod(s) for exactly one reaped attempt: the
// (run, task, try, epoch) tuple. Pinning try-number and the attempt epoch is
// the invariant guard. A retry bumps try_number in place and dispatches a new
// pod with a new try-number label; every other rail that starts a new
// execution of the row (an infra re-place, a reschedule redispatch, a warm
// requeue) and every dispatch bumps attempt_epoch, and the new pod carries the
// new epoch label (ADR 0051 amendment). So a newer live attempt can never match
// and is never deleted (#901). A pod already in a terminal phase is left for
// the reconciler (#928). Tolerates NotFound.
func (e *KubernetesExecutor) DeleteTaskPod(ctx context.Context, a Attempt) error {
	return e.deletePodsBySelector(ctx, attemptSelector(a), func(pod *corev1.Pod) bool {
		return podMatchesEpoch(pod, a.AttemptEpoch)
	})
}

// attemptSelector is the server-side label selector for one attempt's pods:
// run, task and try. The epoch is not in it (see podMatchesEpoch).
func attemptSelector(a Attempt) string {
	return fmt.Sprintf("%s=%s,%s=%s,%s=%s",
		podLabelRunID, sanitizeLabel(a.RunID),
		podLabelTaskID, sanitizeLabel(a.TaskID),
		podLabelTryNumber, strconv.Itoa(a.TryNumber))
}

// podEpoch reads the attempt epoch a pod was stamped with. A pod without the
// label was created before the epoch existed and is epoch 0. A label that is
// present but not a number names no attempt (ok is false).
func podEpoch(pod *corev1.Pod) (int, bool) {
	s, present := pod.Labels[podLabelAttemptEpoch]
	if !present {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}

// podMatchesEpoch reports whether a pod belongs to the given attempt epoch. A
// pod whose epoch label cannot be parsed matches no epoch, so it is neither
// deleted nor counted as present for any attempt.
func podMatchesEpoch(pod *corev1.Pod, epoch int) bool {
	n, ok := podEpoch(pod)
	return ok && n == epoch
}

// DeleteRunPods deletes every task pod belonging to a single reaped run. The
// orphan-run reaper abandons a whole run (failing all its still-active TIs), so
// every pod of that run must be torn down. The run-id is a unique per-run UUID,
// so this selector can only ever match pods of the one abandoned run — never a
// different run's live pod. The terminal-phase skip applies per pod inside the
// run, not just to the per-attempt delete above (#928): this reaper reads no
// presence at all, so a run abandoned at the 5-minute threshold would otherwise
// take every finished task's outcome record with it. A mixed set is safe because
// each settle is guarded on the pod's own try-number, ReapRun has already flipped
// the run and every still-active task instance in one transaction before this
// runs, and pod names carry a random suffix so a preserved pod can never collide
// with a redispatch. The subtlest cell is a reschedule poke pod, which the
// reconciler collects immediately rather than on age because a reschedule reuses
// the same try-number: preserving one delays that collect by up to a cycle, which
// is harmless because up_for_reschedule is not an active state for any reaper, so
// a reaper only ever preserves a poke pod for an attempt it has just made
// terminal. Tolerates NotFound.
func (e *KubernetesExecutor) DeleteRunPods(ctx context.Context, runID string) error {
	selector := fmt.Sprintf("%s=%s", podLabelRunID, sanitizeLabel(runID))
	return e.deletePodsBySelector(ctx, selector, nil)
}

// deletePodsBySelector lists the pods matching selector and deletes each one
// that match accepts (every pod when match is nil) and that still has a
// container to stop, skipping those already in a terminal phase (#928). Each
// delete is by name with the listed pod's UID as a precondition (#901). It uses only the `list` and `delete` verbs the executor Role
// grants; a NotFound on either the list target or an individual delete is
// treated as success (the pod is already gone). Per-pod delete errors are
// collected so one failure does not skip the rest.
//
// The skip is logged per pod at INFO, by name and phase, so an operator can tell
// "left for the reconciler" from "failed to delete" — the latter is the
// *_pod_delete_error the reaper meters at its own call site. It is deliberately
// NOT metered as a scheduler decision: those labels are recorded by a
// DecisionRecorder the reapers hold, this layer has none, and a label here could
// not say which reaper's teardown it came from. The reaper-level defer
// (pod_lost_terminal_pod_defer) remains the metered signal for the same class.
func (e *KubernetesExecutor) deletePodsBySelector(ctx context.Context, selector string, match func(*corev1.Pod) bool) error {
	pods, err := e.clientset.CoreV1().Pods(e.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("listing pods to delete (%s): %w", selector, err)
	}
	var errs []error
	for i := range pods.Items {
		pod := &pods.Items[i]
		if match != nil && !match(pod) {
			continue
		}
		if terminalForTeardown(pod) {
			slog.InfoContext(ctx, "reap teardown: task pod is already in a terminal phase; leaving it for the reconciler",
				"pod", pod.Name, "phase", pod.Status.Phase, "selector", selector)
			continue
		}
		derr := e.clientset.CoreV1().Pods(e.namespace).Delete(ctx, pod.Name, deleteListedPod(pod))
		if derr != nil && !apierrors.IsNotFound(derr) && !apierrors.IsConflict(derr) {
			errs = append(errs, fmt.Errorf("deleting pod %s: %w", pod.Name, derr))
		}
	}
	return errors.Join(errs...)
}

// deleteListedPod is the delete options for a pod a teardown listed: its UID is
// a precondition, so a pod recreated under the same name between the list and
// the delete is never acted on (#901). The apiserver answers a failed
// precondition with Conflict, which the caller treats like NotFound: the pod it
// listed is gone. A pod without a UID (only a hand-built fixture) gets no
// precondition.
func deleteListedPod(pod *corev1.Pod) metav1.DeleteOptions {
	if pod.UID == "" {
		return metav1.DeleteOptions{}
	}
	uid := pod.UID
	return metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
}

// terminalForTeardown reports whether a task pod has reached a phase where a
// reaper's teardown has nothing left to stop and an outcome record to lose
// (#928). A reap deletes a pod for exactly one reason — to stop a container that
// is still running user code and would otherwise break at-most-once execution
// (#474). Succeeded and Failed have no such container, and their pod object
// carries the attempt's durable outcome record (ADR 0052); the reconciler is the
// designated deleter of those — it settles each one and then garbage-collects it
// on age, which is the "reconciler-as-deleter" the RBAC comment names
// (helm/dexaflow/templates/rbac.yaml).
//
// The invariant is ONE-DIRECTIONAL, and stating it as an equality would be
// false: everything this preserves, classifyPod will settle and collect, so
// nothing preserved can leak. The converse does not hold and does not need to —
// classifyPod also treats a Pending or Running pod with an unrecoverable
// waiting reason (an unpullable image, a missing config) as terminal, and the
// teardown still deletes those. That costs nothing, because such a container
// never started and so left no record behind, and it is REQUIRED, because a
// Pending pod can still start and run the task, which is #474's exact chain.
//
// The record test comes first on purpose, and it is not redundant with the
// phase test. Phase is only a sound proxy for "the record is safe" while a task
// pod has one container and RestartPolicy Never, which is what makes "the task
// container terminated" imply "the phase is terminal" — a property of BuildPod,
// not of this function. Add a second container and the implication breaks: a
// service mesh injects a sidecar (an author can ask for that through the
// execution annotations BuildPod merges), the task container terminates and
// writes its record, the sidecar keeps running, and the phase stays Running for
// good. That is the one case where the record is the ONLY settle path, so it is
// the worst possible pod to delete.
//
// Hence Unknown is deleted, even though TaskPodPresence classifies it as
// present-but-terminal — those two answer different questions. Presence asks
// "is this an absence?", where Unknown is conservatively a presence; teardown
// asks "is there a container to stop, and will anything else collect this?",
// and for Unknown the answers are "possibly yes" and "no" — classifyPod groups
// Unknown with Pending/Running, so the reconciler neither settles nor collects
// it. Skipping it would leave a pod that may still be running the very work
// #474 exists to stop, with nothing to stop it.
func terminalForTeardown(pod *corev1.Pod) bool {
	if _, ok := outcomeRecord(pod); ok {
		return true
	}
	return pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed
}

// TaskPodPresence reports what the apiserver holds for exactly the
// (run, task, try, epoch) attempt: a live pod (Pending/Running), a present-but-finished
// pod, or no pod at all. The dispatch-lost and pod-lost reapers consult this
// before failing a TI: a live pod means the dispatch actually landed and the node
// is merely slow to pull the image (#461), so the reaper must DEFER.
//
// A present-but-finished pod is reported apart from an absence on purpose. The
// pod object still carries the attempt's outcome (the termination log the
// reconciler recovers a durable result from), so it is the reconciler's to settle
// and no reaper may delete it; only a genuine absence means the attempt is lost
// with nothing left to recover. Any live pod for the attempt wins over a
// lingering finished sibling, since work may still be running.
//
// Try-number is pinned — the same invariant guard as DeleteTaskPod above. The
// retry rail resets up_for_retry -> none with try_number+1 and the planner
// re-queues the TI (storage/queries/runs.sql), so a `queued`/`running` TI can be
// on try 2 while try 1's pod still lingers Pending after a failed best-effort
// delete. Selecting on (run, task) alone would match that stale older pod and
// false-defer the reap of the current attempt forever (#723). Asking about the
// attempt the reaper is about to fail is the correct liveness question. The
// epoch is pinned for the same reason (ADR 0051 amendment): an infra re-place
// keeps the try, so a lingering pod of the superseded execution must not defer
// the reap of its replacement.
func (e *KubernetesExecutor) TaskPodPresence(ctx context.Context, a Attempt) (PodPresence, error) {
	selector := attemptSelector(a)
	pods, err := e.clientset.CoreV1().Pods(e.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		// PodPresenceLive is the zero value on purpose: a caller that drops the
		// error still holds the presence that authorizes nothing.
		return PodPresenceLive, fmt.Errorf("listing pods for liveness (%s): %w", selector, err)
	}
	presence := PodPresenceAbsent
	for i := range pods.Items {
		if !podMatchesEpoch(&pods.Items[i], a.AttemptEpoch) {
			continue
		}
		if phase := pods.Items[i].Status.Phase; phase == corev1.PodPending || phase == corev1.PodRunning {
			return PodPresenceLive, nil
		}
		presence = PodPresenceTerminal
	}
	return presence, nil
}
