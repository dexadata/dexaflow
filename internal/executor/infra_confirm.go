package executor

import (
	"context"
	"log/slog"

	corev1 "k8s.io/api/core/v1"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// ProvisionalInfraFailure is one infra mark (agent_lost, pod_lost,
// dispatch_lost) the reconciler has not confirmed yet (ADR 0052 amendment,
// part 2), with the attempt it names.
type ProvisionalInfraFailure struct {
	TaskInstanceID string
	DagRunID       string
	TaskID         string
	TryNumber      int
	AttemptEpoch   int
}

// attempt is the execution the mark names, for matching its pods.
func (p ProvisionalInfraFailure) attempt() Attempt {
	return Attempt{RunID: p.DagRunID, TaskID: p.TaskID, TryNumber: p.TryNumber, AttemptEpoch: p.AttemptEpoch}
}

// InfraConfirmer is the store surface of the confirmation pass: list the
// provisional marks, and confirm one, guarded on its exact attempt. The
// scheduler store implements it.
type InfraConfirmer interface {
	ListProvisionalInfraFailures(ctx context.Context) ([]ProvisionalInfraFailure, error)
	ConfirmInfraFailure(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
}

// SetInfraConfirmer wires the confirmation pass (ADR 0052 amendment, part 2).
// With it set, every sweep looks at each provisional infra mark's pods and
// confirms the mark when no evidence can still surface. Left unset, no mark is
// confirmed here, so the store must then confirm marks itself (Lite).
func (r *Reconciler) SetInfraConfirmer(c InfraConfirmer) { r.confirmer = c }

// infraEvidence is what an attempt's pods say about a provisional infra mark.
type infraEvidence int

const (
	// evidenceNone: no pod of the attempt, or only pods whose task container
	// terminated without a SUCCESS record. The guess stands.
	evidenceNone infraEvidence = iota
	// evidencePending: a task container of the attempt has not terminated yet
	// (a pod stopped in place is still inside its termination grace). Wait.
	evidencePending
	// evidenceSuccess: the attempt wrote a durable SUCCESS record.
	evidenceSuccess
)

// attemptEvidence classifies the pods of exactly one attempt. It reads the task
// container's terminated state, never the pod phase: a pod stopped in place can
// report phase Failed (DeadlineExceeded) while its container still runs.
func attemptEvidence(pods []*corev1.Pod, a Attempt) infraEvidence {
	out := evidenceNone
	for _, pod := range pods {
		if !podIsAttempt(pod, a) {
			continue
		}
		if rec, ok := outcomeRecord(pod); ok && rec.Outcome == taskoutcome.Success {
			return evidenceSuccess
		}
		if !taskContainerTerminated(pod) {
			out = evidencePending
		}
	}
	return out
}

// podIsAttempt reports whether the pod carries exactly the attempt's labels.
func podIsAttempt(pod *corev1.Pod, a Attempt) bool {
	try, epoch, ok := attemptOf(pod)
	return ok && try == a.TryNumber && epoch == a.AttemptEpoch &&
		pod.Labels[podLabelRunID] == labelValue(a.RunID) &&
		pod.Labels[podLabelTaskID] == labelValue(a.TaskID)
}

// taskContainerTerminated reports whether the pod's task container has
// terminated. A pod with no status for it (never started) has not.
func taskContainerTerminated(pod *corev1.Pod) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == taskContainerName {
			return cs.State.Terminated != nil
		}
	}
	return false
}

// confirmInfraMarks is the confirmation pass of one sweep, over the pod set the
// sweep already listed. A mark whose attempt can no longer produce a record is
// confirmed, so the planner may re-place it; a mark whose attempt is still
// terminating, or that has a SUCCESS record, is left provisional. Errors are
// logged and retried next sweep; the planner's liveness valve bounds the wait.
func (r *Reconciler) confirmInfraMarks(ctx context.Context, pods []*corev1.Pod) {
	if r.confirmer == nil {
		return
	}
	marks, err := r.confirmer.ListProvisionalInfraFailures(ctx)
	if err != nil {
		slog.WarnContext(ctx, "listing provisional infra failures; retrying next sweep", "error", err)
		return
	}
	for _, m := range marks {
		if attemptEvidence(pods, m.attempt()) != evidenceNone {
			continue
		}
		if _, cerr := r.confirmer.ConfirmInfraFailure(ctx, m.TaskInstanceID, m.TryNumber, m.AttemptEpoch); cerr != nil {
			slog.WarnContext(ctx, "confirming infra failure; retrying next sweep",
				"task_instance", m.TaskInstanceID, "try", m.TryNumber, "attempt_epoch", m.AttemptEpoch, "error", cerr)
		}
	}
}
