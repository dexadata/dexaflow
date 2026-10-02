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
// (leoflow.io/run-id, /task-id, /try-number). Deletion is by List-then-Delete,
// not DeleteCollection: the executor Role grants the `list` and `delete` verbs
// but NOT `deletecollection` (helm/dexaflow/templates/rbac.yaml), so a
// DeleteCollection call would 403 in production. NotFound is always tolerated —
// a pod may have been garbage-collected between the list and the delete.
//
// Both methods skip a pod that has already reached a terminal phase (#928) —
// see terminalForTeardown. That is enforced HERE, at the one delete site, rather
// than in each reaper's decision: agent-lost fires on heartbeat staleness alone
// at 90s and orphan-run abandons a whole run at 5m, neither reading pod presence
// at all, so a per-reaper guard would have to be written four times and kept
// right four times. No reaper's mark or decision changes.

// DeleteTaskPod deletes the pod(s) for exactly one reaped task instance: the
// (run, task, try) tuple. Pinning try-number is the invariant guard — a retry
// bumps try_number in place and dispatches a new pod with a new try-number
// label, so a newer live attempt can never match this selector and is never
// deleted. A pod already in a terminal phase is left for the reconciler (#928).
// Tolerates NotFound.
func (e *KubernetesExecutor) DeleteTaskPod(ctx context.Context, runID, taskID string, tryNumber int) error {
	selector := fmt.Sprintf("leoflow.io/run-id=%s,leoflow.io/task-id=%s,leoflow.io/try-number=%s",
		labelValue(runID), labelValue(taskID), strconv.Itoa(tryNumber))
	return e.deletePodsBySelector(ctx, selector)
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
	selector := fmt.Sprintf("leoflow.io/run-id=%s", labelValue(runID))
	return e.deletePodsBySelector(ctx, selector)
}

// deletePodsBySelector lists the pods matching selector and deletes each one
// that still has a container to stop, skipping those already in a terminal
// phase (#928). It uses only the `list` and `delete` verbs the executor Role
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
func (e *KubernetesExecutor) deletePodsBySelector(ctx context.Context, selector string) error {
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
		if terminalForTeardown(pod) {
			slog.InfoContext(ctx, "reap teardown: task pod is already in a terminal phase; leaving it for the reconciler",
				"pod", pod.Name, "phase", pod.Status.Phase, "selector", selector)
			continue
		}
		if derr := e.clientset.CoreV1().Pods(e.namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); derr != nil && !apierrors.IsNotFound(derr) {
			errs = append(errs, fmt.Errorf("deleting pod %s: %w", pod.Name, derr))
		}
	}
	return errors.Join(errs...)
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
// (run, task, try) attempt: a live pod (Pending/Running), a present-but-finished
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
// attempt the reaper is about to fail is the correct liveness question.
func (e *KubernetesExecutor) TaskPodPresence(ctx context.Context, runID, taskID string, tryNumber int) (PodPresence, error) {
	selector := fmt.Sprintf("leoflow.io/run-id=%s,leoflow.io/task-id=%s,leoflow.io/try-number=%s",
		labelValue(runID), labelValue(taskID), strconv.Itoa(tryNumber))
	pods, err := e.clientset.CoreV1().Pods(e.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		// PodPresenceLive is the zero value on purpose: a caller that drops the
		// error still holds the presence that authorizes nothing.
		return PodPresenceLive, fmt.Errorf("listing pods for liveness (%s): %w", selector, err)
	}
	if len(pods.Items) == 0 {
		return PodPresenceAbsent, nil
	}
	for i := range pods.Items {
		if phase := pods.Items[i].Status.Phase; phase == corev1.PodPending || phase == corev1.PodRunning {
			return PodPresenceLive, nil
		}
	}
	return PodPresenceTerminal, nil
}
