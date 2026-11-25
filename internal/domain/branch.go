package domain

import "slices"

// BranchKind names how a branch task's decision is read (ADR 0064 D3).
type BranchKind string

const (
	// BranchKindBranch is @task.branch and BranchPythonOperator: the task
	// returns the task ids to follow, and its direct children outside them
	// are skipped.
	BranchKindBranch BranchKind = "branch"
	// BranchKindShortCircuit is @task.short_circuit and ShortCircuitOperator:
	// a falsy result skips its downstream.
	BranchKindShortCircuit BranchKind = "short_circuit"
)

// BranchSpec marks a python task whose successful result decides which of its
// downstream tasks run (ADR 0064 D3). It is a property of the task, not a task
// type: the task runs like any python task.
//
// The schema does not accept it yet (ADR 0064 Phased path, step 3), so a
// registered dag.json cannot carry it; the planner already applies it, so
// every binary a rollback can reach does once the schema does.
type BranchSpec struct {
	Kind BranchKind `json:"kind"`
	// IgnoreDownstreamTriggerRules is ShortCircuitOperator's argument of the
	// same name. Unset means true, Airflow's default: a falsy result skips
	// every task downstream. False skips only the direct children and lets the
	// trigger rules decide below them.
	IgnoreDownstreamTriggerRules *bool `json:"ignore_downstream_trigger_rules,omitempty"`
}

// IgnoresDownstreamTriggerRules reports whether a falsy short-circuit skips
// every downstream task rather than only its direct children.
func (b BranchSpec) IgnoresDownstreamTriggerRules() bool {
	return b.IgnoreDownstreamTriggerRules == nil || *b.IgnoreDownstreamTriggerRules
}

// BranchDecision is what a branch task decided, reported with its success and
// stored on its task instance (ADR 0064 D1). The task's BranchSpec says which
// field applies: FollowTaskIDs for a branch task (empty when the callable
// returned None, which follows nothing), ShortCircuitFalsy for a
// short-circuit one.
type BranchDecision struct {
	FollowTaskIDs     []string `json:"follow_task_ids,omitempty"`
	ShortCircuitFalsy bool     `json:"short_circuit_falsy,omitempty"`
}

// Follows reports whether the decision chose taskID.
func (d BranchDecision) Follows(taskID string) bool {
	return slices.Contains(d.FollowTaskIDs, taskID)
}
