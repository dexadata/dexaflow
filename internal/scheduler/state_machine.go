// Package scheduler implements the Dexaflow scheduling state machine and loop.
package scheduler

import (
	"slices"
	"strings"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TriggerDecision is the scheduler's decision for a task given its trigger rule
// and the states of its upstream tasks.
type TriggerDecision int

// Possible scheduler decisions for a task.
const (
	// DecisionWait means the upstreams are not yet settled; re-evaluate later.
	DecisionWait TriggerDecision = iota
	// DecisionSchedule means dependencies are satisfied; move the task to scheduled.
	DecisionSchedule
	// DecisionSkip means the trigger rule can no longer be satisfied; skip the task.
	DecisionSkip
	// DecisionUpstreamFailed means a required upstream failed; propagate the failure.
	DecisionUpstreamFailed
)

// taskTransitions is the authoritative set of legal task state transitions:
// every state change a storage query or the planner makes is in it, and every
// entry is made by one of them. The writes happen in the storage SQL (UPDATE
// ... WHERE state IN (...)), so two tests hold the table to them (#2146):
// internal/storage/state_transitions_integration_test.go records each state
// change the storage queries make, and TestPlannedTransitionsAreInTheTable
// checks the planner's choices, which the scheduler writes through
// ApplyTransition and ApplyTransitions.
//
// The agent reports from the moment its process starts, which can be before
// the scheduler recorded queued (launchQueued dispatches first), so running,
// success, failed and skipped are reachable from none and scheduled too
// (ReportTaskResult). A clear resets a task to none from any state (Airflow
// clears running tasks as well), and only a clear writes removed (ADR 0086):
// when it moves a run to a version that dropped a task, the task instance goes
// to removed from any state but queued and running, and a removed one whose
// task the version declares again goes back to none.
//
// A user's mark (SetTaskInstanceStateByUser) is a verdict, not a transition of
// this machine: it sets whatever state the API accepts, from any state.
var taskTransitions = map[domain.TaskState]map[domain.TaskState]bool{
	domain.TaskStateNone: {
		domain.TaskStateScheduled: true, domain.TaskStateSkipped: true, domain.TaskStateUpstreamFailed: true, // planner
		domain.TaskStateRunning: true, domain.TaskStateSuccess: true, domain.TaskStateFailed: true, // agent report
		domain.TaskStateRemoved: true,
	},
	domain.TaskStateScheduled: {
		domain.TaskStateQueued:  true,
		domain.TaskStateFailed:  true,                                                                                                      // dispatch failed, refused or exhausted; orphaned run
		domain.TaskStateRunning: true, domain.TaskStateSuccess: true, domain.TaskStateSkipped: true, domain.TaskStateUpForReschedule: true, // agent report
		domain.TaskStateNone: true, domain.TaskStateRemoved: true, // clear
	},
	domain.TaskStateQueued: {
		domain.TaskStateRunning: true, domain.TaskStateSuccess: true, domain.TaskStateFailed: true, domain.TaskStateSkipped: true, domain.TaskStateUpForReschedule: true,
		domain.TaskStateScheduled: true, // dispatch requeued
		domain.TaskStateNone:      true, // clear
	},
	domain.TaskStateRunning: {
		domain.TaskStateSuccess: true, domain.TaskStateFailed: true, domain.TaskStateSkipped: true, domain.TaskStateUpForReschedule: true,
		domain.TaskStateNone: true, // clear
	},
	domain.TaskStateUpForRetry:      {domain.TaskStateNone: true, domain.TaskStateRemoved: true},
	domain.TaskStateUpForReschedule: {domain.TaskStateNone: true, domain.TaskStateRemoved: true},
	domain.TaskStateSuccess:         {domain.TaskStateNone: true, domain.TaskStateRemoved: true},
	domain.TaskStateFailed: {
		domain.TaskStateUpForRetry: true, // retry budget left
		domain.TaskStateNone:       true, // infra re-place, clear
		domain.TaskStateSuccess:    true, // a durable SUCCESS over a provisional infra mark (ADR 0052)
		domain.TaskStateRemoved:    true,
	},
	domain.TaskStateSkipped:        {domain.TaskStateNone: true, domain.TaskStateRemoved: true},
	domain.TaskStateUpstreamFailed: {domain.TaskStateNone: true, domain.TaskStateRemoved: true},
	domain.TaskStateRemoved:        {domain.TaskStateNone: true},
}

// dagRunTransitions is the authoritative set of legal dag run state
// transitions, held to the storage queries the same way. A clear re-opens a
// run to queued from any state, a running one included. A user's mark
// (UpdateDagRunState) is a verdict outside this table.
var dagRunTransitions = map[domain.DagRunState]map[domain.DagRunState]bool{
	domain.DagRunStateQueued:  {domain.DagRunStateRunning: true},
	domain.DagRunStateRunning: {domain.DagRunStateSuccess: true, domain.DagRunStateFailed: true, domain.DagRunStateQueued: true},
	domain.DagRunStateSuccess: {domain.DagRunStateQueued: true},
	domain.DagRunStateFailed:  {domain.DagRunStateQueued: true},
}

// CanTransition reports whether a task instance may move from one state to
// another under the Dexaflow state machine.
func CanTransition(from, to domain.TaskState) bool {
	return taskTransitions[from][to]
}

// CanTransitionDagRun reports whether a dag run may move from one state to another.
func CanTransitionDagRun(from, to domain.DagRunState) bool {
	return dagRunTransitions[from][to]
}

// upstreamCounts tallies upstream task states for trigger-rule evaluation.
type upstreamCounts struct {
	total          int
	success        int
	failed         int
	skipped        int
	upstreamFailed int
	active         int // non-terminal: none, scheduled, queued, running, up_for_retry, up_for_reschedule
}

func countUpstreams(upstreams []domain.TaskState) upstreamCounts {
	c := upstreamCounts{total: len(upstreams)}
	for _, s := range upstreams {
		switch s {
		case domain.TaskStateSuccess:
			c.success++
		case domain.TaskStateFailed:
			c.failed++
		case domain.TaskStateSkipped:
			c.skipped++
		case domain.TaskStateUpstreamFailed:
			c.upstreamFailed++
		default:
			c.active++
		}
	}
	return c
}

// EvaluateTriggerRule decides what to do with a task given its trigger rule and
// the current states of its upstream tasks. A task with no upstreams (a root
// task) is always scheduled.
//
// Each rule follows Airflow 3.2 (ADR 0064 D2): every evaluator owns its
// handling of upstream_failed, so an upstream_failed upstream counts as a
// failure for the rules that look at failures (all_failed and one_failed run
// on it) instead of condemning every rule. An unknown rule waits, so a DAG the
// schema let through with a rule this build does not know never runs a task
// it was not meant to.
func EvaluateTriggerRule(rule domain.TriggerRule, upstreams []domain.TaskState) TriggerDecision {
	if len(upstreams) == 0 {
		return DecisionSchedule
	}
	c := countUpstreams(upstreams)
	switch rule {
	case domain.TriggerRuleAllSuccess:
		return evalAllSuccess(c)
	case domain.TriggerRuleAllFailed:
		return evalAllFailed(c)
	case domain.TriggerRuleAllDone:
		return evalAllDone(c)
	case domain.TriggerRuleOneSuccess:
		return evalOneSuccess(c)
	case domain.TriggerRuleOneFailed:
		return evalOneFailed(c)
	case domain.TriggerRuleNoneFailed:
		return evalNoneFailed(c)
	case domain.TriggerRuleNoneFailedMinOneSuccess:
		return evalNoneFailedMinOneSuccess(c)
	case domain.TriggerRuleNoneSkipped:
		return evalNoneSkipped(c)
	case domain.TriggerRuleAlways:
		return DecisionSchedule
	default:
		return DecisionWait
	}
}

// failures counts the upstreams that failed or were marked upstream_failed.
func (c upstreamCounts) failures() int { return c.failed + c.upstreamFailed }

// evalAllSuccess: a failure propagates and a skip skips at once; otherwise the
// task waits for every upstream to succeed.
func evalAllSuccess(c upstreamCounts) TriggerDecision {
	switch {
	case c.failures() > 0:
		return DecisionUpstreamFailed
	case c.skipped > 0:
		return DecisionSkip
	case c.active > 0:
		return DecisionWait
	default:
		return DecisionSchedule
	}
}

// evalAllFailed: a success or a skip skips at once; otherwise the task runs
// once every upstream failed or was marked upstream_failed.
func evalAllFailed(c upstreamCounts) TriggerDecision {
	switch {
	case c.success+c.skipped > 0:
		return DecisionSkip
	case c.active > 0:
		return DecisionWait
	default:
		return DecisionSchedule
	}
}

func evalAllDone(c upstreamCounts) TriggerDecision {
	if c.active > 0 {
		return DecisionWait
	}
	return DecisionSchedule
}

// evalOneSuccess runs as soon as one upstream succeeds. When every upstream
// finished without a success it skips if all were skipped and is
// upstream_failed otherwise.
func evalOneSuccess(c upstreamCounts) TriggerDecision {
	switch {
	case c.success > 0:
		return DecisionSchedule
	case c.active > 0:
		return DecisionWait
	case c.skipped == c.total:
		return DecisionSkip
	default:
		return DecisionUpstreamFailed
	}
}

// evalOneFailed runs as soon as one upstream failed or was marked
// upstream_failed, and skips once every upstream finished without one.
func evalOneFailed(c upstreamCounts) TriggerDecision {
	switch {
	case c.failures() > 0:
		return DecisionSchedule
	case c.active > 0:
		return DecisionWait
	default:
		return DecisionSkip
	}
}

// evalNoneFailed: a failure propagates at once; otherwise the task runs once
// every upstream succeeded or was skipped, all skipped included.
func evalNoneFailed(c upstreamCounts) TriggerDecision {
	switch {
	case c.failures() > 0:
		return DecisionUpstreamFailed
	case c.active > 0:
		return DecisionWait
	default:
		return DecisionSchedule
	}
}

// evalNoneFailedMinOneSuccess is none_failed, except that a task whose
// upstreams were all skipped is skipped too.
func evalNoneFailedMinOneSuccess(c upstreamCounts) TriggerDecision {
	switch {
	case c.failures() > 0:
		return DecisionUpstreamFailed
	case c.skipped == c.total:
		return DecisionSkip
	case c.active > 0:
		return DecisionWait
	default:
		return DecisionSchedule
	}
}

// evalNoneSkipped: a skip skips at once; otherwise the task runs once every
// upstream finished, failures included.
func evalNoneSkipped(c upstreamCounts) TriggerDecision {
	switch {
	case c.skipped > 0:
		return DecisionSkip
	case c.active > 0:
		return DecisionWait
	default:
		return DecisionSchedule
	}
}

// plannerTransitions is every transition the planner can ask for. The
// scheduler writes them through ApplyTransition and ApplyTransitions, or the
// retry and infra re-place resets; TestPlannedTransitionsAreInTheTable checks
// that the planner makes exactly these, each legal in taskTransitions.
var plannerTransitions = [][2]domain.TaskState{
	{domain.TaskStateFailed, domain.TaskStateNone},
	{domain.TaskStateFailed, domain.TaskStateUpForRetry},
	{domain.TaskStateNone, domain.TaskStateScheduled},
	{domain.TaskStateNone, domain.TaskStateSkipped},
	{domain.TaskStateNone, domain.TaskStateUpstreamFailed},
	{domain.TaskStateScheduled, domain.TaskStateQueued},
	{domain.TaskStateUpForReschedule, domain.TaskStateNone},
	{domain.TaskStateUpForRetry, domain.TaskStateNone},
}

// PlannerTransitions lists the task state transitions the planner can ask for,
// as (from, to) pairs, sorted.
func PlannerTransitions() [][2]domain.TaskState {
	return slices.Clone(plannerTransitions)
}

// TaskTransitions lists every legal task state transition as (from, to)
// pairs, sorted, for tests that check the storage queries against the table.
func TaskTransitions() [][2]domain.TaskState {
	return sortedPairs(taskTransitions)
}

// DagRunTransitions lists every legal dag run state transition as (from, to)
// pairs, sorted.
func DagRunTransitions() [][2]domain.DagRunState {
	return sortedPairs(dagRunTransitions)
}

func sortedPairs[S ~string](table map[S]map[S]bool) [][2]S {
	var out [][2]S
	for from, tos := range table {
		for to, ok := range tos {
			if ok {
				out = append(out, [2]S{from, to})
			}
		}
	}
	slices.SortFunc(out, func(a, b [2]S) int {
		if c := strings.Compare(string(a[0]), string(b[0])); c != 0 {
			return c
		}
		return strings.Compare(string(a[1]), string(b[1]))
	})
	return out
}
