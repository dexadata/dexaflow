package executor

import (
	"context"
	"log/slog"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/taskoutcome"
	corev1 "k8s.io/api/core/v1"
)

// podLabelBranch marks the pod of a branch or short-circuit task (ADR 0064 D6).
const podLabelBranch = "leoflow.io/branch"

// reasonBranchDecisionMissing leads the failure of a branch task whose success
// record carries no decision. It is the reason the agent RPC settles the same
// report with (agentrpc.ReasonBranchDecisionMissing).
const reasonBranchDecisionMissing = "branch_decision_missing: the task's success record carries no branch decision"

// BranchOutcomeReporter settles a branch task's recovered success with its
// decision, guarded like SucceedTaskIfActive (ADR 0064 D6). A reporter that is
// not one leaves such a success to the reapers rather than settle it without
// its decision.
type BranchOutcomeReporter interface {
	SucceedBranchTaskIfActive(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, decision domain.BranchDecision) (bool, error)
}

// successVerdict reads a success record. A branch task's success carries its
// decision, and one without a decision is settled failed: read as no record
// instead, it would wait for the reapers, and read as a plain success it would
// run every branch. A decision on any other pod is ignored.
func successVerdict(pod *corev1.Pod, rec taskoutcome.Record) verdict {
	if pod.Labels[podLabelBranch] != "true" {
		return verdict{terminal: true, settle: settleSucceeded, fromRecord: true}
	}
	if rec.Branch == nil {
		return verdict{terminal: true, settle: settleFailed, reason: reasonBranchDecisionMissing, fromRecord: true}
	}
	return verdict{terminal: true, settle: settleSucceeded, fromRecord: true, branch: &domain.BranchDecision{
		FollowTaskIDs:     rec.Branch.FollowTaskIDs,
		ShortCircuitFalsy: rec.Branch.ShortCircuitFalsy,
	}}
}

// succeedBranch settles a branch task's recovered success with its decision.
func (r *Reconciler) succeedBranch(ctx context.Context, tiID string, tryNumber, epoch int, decision domain.BranchDecision) error {
	b, ok := r.reporter.(BranchOutcomeReporter)
	if !ok {
		slog.WarnContext(ctx, "cannot store a branch decision; leaving the recovered success to the reapers",
			"task_instance", tiID, "try_number", tryNumber)
		return nil
	}
	_, err := b.SucceedBranchTaskIfActive(ctx, tiID, tryNumber, epoch, decision)
	return err
}
