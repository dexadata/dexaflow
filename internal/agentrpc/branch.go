package agentrpc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// The classified reasons a branch task's success is settled failed with
// (ADR 0064 D1). They lead the task's error message.
const (
	// ReasonBranchDecisionMissing: a branch task reported success without a
	// decision, which would otherwise run every branch.
	ReasonBranchDecisionMissing = "branch_decision_missing"
	// ReasonInvalidBranchDecision: the decision names a task the DAG does not
	// declare, or is of the other kind than the task.
	ReasonInvalidBranchDecision = "invalid_branch_decision"
)

// branchVerdict is how a success report of a branch task is recorded.
type branchVerdict struct {
	// decision, when set, is persisted with the success.
	decision *domain.BranchDecision
	// failure, when set, settles the report failed with this message instead.
	failure string
}

// judgeBranch checks a success report against the task's branch spec, the
// server-side validation of ADR 0064 D1. The pod runs tenant code, so the
// decision is checked here whatever the runtime did: a branch task never
// reaches success through this path without a valid decision.
func judgeBranch(spec attemptSpec, reported *agentv1.BranchDecision, id *auth.AgentIdentity) branchVerdict {
	if spec.Branch == nil {
		if reported != nil {
			slog.Warn("ignoring a branch decision reported by a task that is not a branch task", attemptAttrs(id)...)
		}
		return branchVerdict{}
	}
	if reported == nil {
		return branchVerdict{failure: ReasonBranchDecisionMissing + ": the task reported success without its branch decision; " +
			"its image may predate branching, rebuild it on the current runtime"}
	}
	decision := domain.BranchDecision{FollowTaskIDs: reported.GetFollowTaskIds(), ShortCircuitFalsy: reported.GetShortCircuitFalsy()}
	switch spec.Branch.Kind {
	case domain.BranchKindBranch:
		if decision.ShortCircuitFalsy {
			return branchVerdict{failure: ReasonInvalidBranchDecision + ": a branch task reported a short-circuit decision"}
		}
		known := make(map[string]bool, len(spec.DagTaskIDs))
		for _, t := range spec.DagTaskIDs {
			known[t] = true
		}
		for _, t := range decision.FollowTaskIDs {
			if !known[t] {
				return branchVerdict{failure: fmt.Sprintf("%s: the branch chose %q, which is not a task of this DAG", ReasonInvalidBranchDecision, t)}
			}
		}
	case domain.BranchKindShortCircuit:
		if len(decision.FollowTaskIDs) > 0 {
			return branchVerdict{failure: ReasonInvalidBranchDecision + ": a short-circuit task reported task ids to follow"}
		}
	default:
		return branchVerdict{failure: fmt.Sprintf("%s: unknown branch kind %q", ReasonInvalidBranchDecision, spec.Branch.Kind)}
	}
	return branchVerdict{decision: &decision}
}

// recordReport writes a state report, with a branch task's decision when the
// verdict carries one and as a failure when the verdict refused the success.
func (s *Server) recordReport(ctx context.Context, id *auth.AgentIdentity, state domain.TaskState, req *agentv1.ReportStateRequest, v branchVerdict) error {
	switch {
	case v.failure != "":
		slog.Warn("settling a branch task's success as failed", append(attemptAttrs(id), "reason", v.failure)...)
		return s.store.ReportState(ctx, *id, domain.TaskStateFailed, int(req.GetExitCode()), v.failure)
	case v.decision != nil:
		br, ok := s.store.(BranchReporter)
		if !ok {
			// Every production store is a BranchReporter; a store that is not
			// cannot persist the decision, so the success must not land either.
			return s.store.ReportState(ctx, *id, domain.TaskStateFailed, int(req.GetExitCode()),
				ReasonBranchDecisionMissing+": this control plane cannot store branch decisions")
		}
		return br.ReportBranchSuccess(ctx, *id, *v.decision)
	default:
		return s.store.ReportState(ctx, *id, state, int(req.GetExitCode()), req.GetErrorMessage())
	}
}

// branchSpecProto renders a task's branch spec for the agent; nil stays nil.
func branchSpecProto(b *domain.BranchSpec) *agentv1.BranchSpec {
	if b == nil {
		return nil
	}
	return &agentv1.BranchSpec{Kind: string(b.Kind), IgnoreDownstreamTriggerRules: b.IgnoresDownstreamTriggerRules()}
}
