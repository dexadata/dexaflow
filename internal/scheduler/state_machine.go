// Package scheduler implements the Leoflow scheduling state machine and loop.
package scheduler

import "github.com/dexadata/dexaflow/internal/domain"

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

// taskTransitions is the authoritative set of legal task state transitions.
var taskTransitions = map[domain.TaskState]map[domain.TaskState]bool{
	domain.TaskStateNone:            {domain.TaskStateScheduled: true, domain.TaskStateSkipped: true, domain.TaskStateUpstreamFailed: true},
	domain.TaskStateScheduled:       {domain.TaskStateQueued: true},
	domain.TaskStateQueued:          {domain.TaskStateRunning: true, domain.TaskStateFailed: true, domain.TaskStateUpForRetry: true},
	domain.TaskStateRunning:         {domain.TaskStateSuccess: true, domain.TaskStateFailed: true, domain.TaskStateUpForRetry: true, domain.TaskStateUpForReschedule: true},
	domain.TaskStateUpForRetry:      {domain.TaskStateScheduled: true, domain.TaskStateQueued: true, domain.TaskStateNone: true},
	domain.TaskStateUpForReschedule: {domain.TaskStateScheduled: true, domain.TaskStateQueued: true, domain.TaskStateNone: true},
	domain.TaskStateSuccess:         {domain.TaskStateNone: true},
	domain.TaskStateFailed:          {domain.TaskStateNone: true},
	domain.TaskStateSkipped:         {domain.TaskStateNone: true},
	domain.TaskStateUpstreamFailed:  {domain.TaskStateNone: true},
}

// dagRunTransitions is the authoritative set of legal dag run state transitions.
var dagRunTransitions = map[domain.DagRunState]map[domain.DagRunState]bool{
	domain.DagRunStateQueued:  {domain.DagRunStateRunning: true, domain.DagRunStateFailed: true},
	domain.DagRunStateRunning: {domain.DagRunStateSuccess: true, domain.DagRunStateFailed: true},
	domain.DagRunStateSuccess: {domain.DagRunStateQueued: true},
	domain.DagRunStateFailed:  {domain.DagRunStateQueued: true},
}

// CanTransition reports whether a task instance may move from one state to
// another under the Leoflow state machine.
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
func EvaluateTriggerRule(rule domain.TriggerRule, upstreams []domain.TaskState) TriggerDecision {
	if len(upstreams) == 0 {
		return DecisionSchedule
	}
	c := countUpstreams(upstreams)
	// An upstream_failed upstream propagates downstream for every rule except
	// all_done, which runs once everything is terminal regardless of outcome.
	if rule != domain.TriggerRuleAllDone && c.upstreamFailed > 0 {
		return DecisionUpstreamFailed
	}
	switch rule {
	case domain.TriggerRuleAllSuccess:
		return evalAllSuccess(c)
	case domain.TriggerRuleAllFailed:
		return evalAllFailed(c)
	case domain.TriggerRuleAllDone:
		return evalAllDone(c)
	case domain.TriggerRuleOneSuccess:
		return evalOneOf(c, c.success)
	case domain.TriggerRuleOneFailed:
		return evalOneOf(c, c.failed)
	default:
		return DecisionWait
	}
}

func evalAllSuccess(c upstreamCounts) TriggerDecision {
	switch {
	case c.failed > 0:
		return DecisionUpstreamFailed
	case c.skipped > 0:
		return DecisionSkip
	case c.active > 0:
		return DecisionWait
	default:
		return DecisionSchedule
	}
}

func evalAllFailed(c upstreamCounts) TriggerDecision {
	switch {
	case c.active > 0:
		return DecisionWait
	case c.failed == c.total:
		return DecisionSchedule
	default:
		return DecisionSkip
	}
}

func evalAllDone(c upstreamCounts) TriggerDecision {
	if c.active > 0 {
		return DecisionWait
	}
	return DecisionSchedule
}

// evalOneOf implements the one_success / one_failed rules: schedule once every
// upstream has settled and at least `hits` of the target state occurred,
// otherwise skip; wait while any upstream is still active.
func evalOneOf(c upstreamCounts, hits int) TriggerDecision {
	switch {
	case c.active > 0:
		return DecisionWait
	case hits > 0:
		return DecisionSchedule
	default:
		return DecisionSkip
	}
}
