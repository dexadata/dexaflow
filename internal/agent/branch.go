package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxBranchDecisionBytes bounds the decision file the runtime writes. A real
// decision is a list of task ids; the bound only keeps a runaway file out of
// the agent's memory.
const maxBranchDecisionBytes = 1 << 20

// branchEnv tells the runtime of a branch or short-circuit task (ADR 0064 D1)
// where to write its decision and which task ids the DAG declares. It is
// empty for every other task, so their environment is unchanged.
func branchEnv(spec *agentv1.TaskSpec, path string) ([]string, error) {
	b := spec.GetBranch()
	if b == nil || path == "" {
		return nil, nil
	}
	ids, err := json.Marshal(nonNil(spec.GetDagTaskIds()))
	if err != nil {
		return nil, err
	}
	children, err := json.Marshal(nonNil(spec.GetDownstreamTaskIds()))
	if err != nil {
		return nil, err
	}
	// A file left by an earlier attempt on this worker must never stand for
	// this attempt's decision.
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return []string{
		"LEOFLOW_BRANCH_KIND=" + b.GetKind(),
		"LEOFLOW_BRANCH_PATH=" + path,
		"LEOFLOW_BRANCH_TASK_IDS=" + string(ids),
		"LEOFLOW_BRANCH_DOWNSTREAM=" + string(children),
	}, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// branchFile is the decision file the runtime writes.
type branchFile struct {
	FollowTaskIDs     []string `json:"follow_task_ids"`
	ShortCircuitFalsy bool     `json:"short_circuit_falsy"`
}

// readBranch reads the decision the runtime wrote. ok is false when the file is
// absent or does not decode; the success is then reported without a decision
// and the control plane settles it failed (branch_decision_missing).
func (r *Runner) readBranch() (*agentv1.BranchDecision, bool) {
	if r.BranchPath == "" {
		return nil, false
	}
	info, err := os.Stat(r.BranchPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("reading the branch decision", "path", r.BranchPath, "error", err)
		}
		return nil, false
	}
	if info.Size() > maxBranchDecisionBytes {
		slog.Warn("the branch decision is too large", "path", r.BranchPath, "bytes", info.Size())
		return nil, false
	}
	raw, err := os.ReadFile(r.BranchPath)
	if err != nil {
		slog.Warn("reading the branch decision", "path", r.BranchPath, "error", err)
		return nil, false
	}
	var d branchFile
	if err := json.Unmarshal(raw, &d); err != nil {
		slog.Warn("the branch decision does not decode", "path", r.BranchPath, "error", err)
		return nil, false
	}
	return &agentv1.BranchDecision{FollowTaskIds: d.FollowTaskIDs, ShortCircuitFalsy: d.ShortCircuitFalsy}, true
}

// reportSuccess reports a success, carrying a branch task's decision in the
// report and in the durable record (ADR 0064 D1, D6). A record whose decision
// does not fit the termination message is not written at all, so a lost
// report degrades to the no-record path and never to a success without its
// decision.
func (r *Runner) reportSuccess(ctx context.Context) error {
	if !r.branchTask {
		return r.report(ctx, agentv1.TaskState_TASK_STATE_SUCCESS, 0, "")
	}
	decision, ok := r.readBranch()
	rec := taskoutcome.Succeeded()
	if ok {
		rec = rec.WithBranch(taskoutcome.BranchDecision{
			FollowTaskIDs:     decision.GetFollowTaskIds(),
			ShortCircuitFalsy: decision.GetShortCircuitFalsy(),
		})
	}
	r.writeOutcome(rec)
	if r.BeforeReport != nil {
		r.BeforeReport(agentv1.TaskState_TASK_STATE_SUCCESS)
	}
	return r.reportRequest(ctx, &agentv1.ReportStateRequest{
		State:      agentv1.TaskState_TASK_STATE_SUCCESS,
		OccurredAt: timestamppb.Now(),
		Branch:     decision,
	})
}
