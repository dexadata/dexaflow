package scheduler

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

var allTaskStates = []domain.TaskState{
	domain.TaskStateNone,
	domain.TaskStateScheduled,
	domain.TaskStateQueued,
	domain.TaskStateRunning,
	domain.TaskStateSuccess,
	domain.TaskStateFailed,
	domain.TaskStateSkipped,
	domain.TaskStateUpstreamFailed,
	domain.TaskStateUpForRetry,
	domain.TaskStateUpForReschedule,
	domain.TaskStateRemoved,
}

// allowedTaskTransitions is the test's independent source of truth for the task
// state machine; the implementation must agree with it on all 121 pairs. It
// matches what the storage queries and the planner do (#2146): the agent can
// report before queued is recorded, a clear resets any state to none, and only
// a clear writes removed (ADR 0086), never on a queued or running task.
var allowedTaskTransitions = map[domain.TaskState][]domain.TaskState{
	domain.TaskStateNone:            {domain.TaskStateScheduled, domain.TaskStateSkipped, domain.TaskStateUpstreamFailed, domain.TaskStateRunning, domain.TaskStateSuccess, domain.TaskStateFailed, domain.TaskStateRemoved},
	domain.TaskStateScheduled:       {domain.TaskStateQueued, domain.TaskStateFailed, domain.TaskStateRunning, domain.TaskStateSuccess, domain.TaskStateSkipped, domain.TaskStateUpForReschedule, domain.TaskStateNone, domain.TaskStateRemoved},
	domain.TaskStateQueued:          {domain.TaskStateRunning, domain.TaskStateSuccess, domain.TaskStateFailed, domain.TaskStateSkipped, domain.TaskStateUpForReschedule, domain.TaskStateScheduled, domain.TaskStateNone},
	domain.TaskStateRunning:         {domain.TaskStateSuccess, domain.TaskStateFailed, domain.TaskStateSkipped, domain.TaskStateUpForReschedule, domain.TaskStateNone},
	domain.TaskStateUpForRetry:      {domain.TaskStateNone, domain.TaskStateRemoved},
	domain.TaskStateUpForReschedule: {domain.TaskStateNone, domain.TaskStateRemoved},
	domain.TaskStateSuccess:         {domain.TaskStateNone, domain.TaskStateRemoved},
	domain.TaskStateFailed:          {domain.TaskStateUpForRetry, domain.TaskStateNone, domain.TaskStateSuccess, domain.TaskStateRemoved},
	domain.TaskStateSkipped:         {domain.TaskStateNone, domain.TaskStateRemoved},
	domain.TaskStateUpstreamFailed:  {domain.TaskStateNone, domain.TaskStateRemoved},
	domain.TaskStateRemoved:         {domain.TaskStateNone},
}

func TestCanTransitionExhaustive(t *testing.T) {
	allowed := func(from, to domain.TaskState) bool {
		for _, s := range allowedTaskTransitions[from] {
			if s == to {
				return true
			}
		}
		return false
	}
	for _, from := range allTaskStates {
		for _, to := range allTaskStates {
			want := allowed(from, to)
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

var allDagRunStates = []domain.DagRunState{
	domain.DagRunStateQueued,
	domain.DagRunStateRunning,
	domain.DagRunStateSuccess,
	domain.DagRunStateFailed,
}

var allowedDagRunTransitions = map[domain.DagRunState][]domain.DagRunState{
	domain.DagRunStateQueued:  {domain.DagRunStateRunning},                                                     // a queued run finalizes only after it starts
	domain.DagRunStateRunning: {domain.DagRunStateSuccess, domain.DagRunStateFailed, domain.DagRunStateQueued}, // a clear re-opens a running run
	domain.DagRunStateSuccess: {domain.DagRunStateQueued},
	domain.DagRunStateFailed:  {domain.DagRunStateQueued},
}

func TestCanTransitionDagRunExhaustive(t *testing.T) {
	allowed := func(from, to domain.DagRunState) bool {
		for _, s := range allowedDagRunTransitions[from] {
			if s == to {
				return true
			}
		}
		return false
	}
	for _, from := range allDagRunStates {
		for _, to := range allDagRunStates {
			want := allowed(from, to)
			if got := CanTransitionDagRun(from, to); got != want {
				t.Errorf("CanTransitionDagRun(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

// TestEvaluateTriggerRule holds the evaluator to the ADR 0064 D2 table: one
// row per cell, for every rule. The expected values restate Airflow 3.2.0
// trigger_rule_dep.py, whose flag_upstream_failed block (lines 389-427) gives
// the upstream_failed and skipped columns and whose readiness checks (lines
// 465-560) give the waits and runs columns.
func TestEvaluateTriggerRule(t *testing.T) {
	const (
		S = domain.TaskStateSuccess
		F = domain.TaskStateFailed
		K = domain.TaskStateSkipped
		U = domain.TaskStateUpstreamFailed
		R = domain.TaskStateRunning // an active (non-terminal) upstream
		Q = domain.TaskStateUpForRetry
	)
	type ups = []domain.TaskState
	cases := []struct {
		name      string
		rule      domain.TriggerRule
		upstreams []domain.TaskState
		want      TriggerDecision
	}{
		{"root task always schedules", domain.TriggerRuleAllSuccess, nil, DecisionSchedule},
		{"root task with no rule set schedules", domain.TriggerRuleNoneSkipped, nil, DecisionSchedule},

		{"all_success/all succeeded", domain.TriggerRuleAllSuccess, ups{S, S}, DecisionSchedule},
		{"all_success/one active waits", domain.TriggerRuleAllSuccess, ups{S, R}, DecisionWait},
		{"all_success/retrying upstream waits", domain.TriggerRuleAllSuccess, ups{S, Q}, DecisionWait},
		{"all_success/failed propagates while a sibling runs", domain.TriggerRuleAllSuccess, ups{R, F}, DecisionUpstreamFailed},
		{"all_success/upstream_failed propagates", domain.TriggerRuleAllSuccess, ups{S, U}, DecisionUpstreamFailed},
		{"all_success/failure wins over skip", domain.TriggerRuleAllSuccess, ups{K, F}, DecisionUpstreamFailed},
		{"all_success/skipped skips while a sibling runs", domain.TriggerRuleAllSuccess, ups{R, K}, DecisionSkip},

		{"all_failed/all failed", domain.TriggerRuleAllFailed, ups{F, F}, DecisionSchedule},
		{"all_failed/failed and upstream_failed runs", domain.TriggerRuleAllFailed, ups{F, U}, DecisionSchedule},
		{"all_failed/all upstream_failed runs", domain.TriggerRuleAllFailed, ups{U, U}, DecisionSchedule},
		{"all_failed/active waits", domain.TriggerRuleAllFailed, ups{F, R}, DecisionWait},
		{"all_failed/a success skips at once", domain.TriggerRuleAllFailed, ups{R, S}, DecisionSkip},
		{"all_failed/a skip skips at once", domain.TriggerRuleAllFailed, ups{R, K}, DecisionSkip},

		{"all_done/all terminal", domain.TriggerRuleAllDone, ups{S, F, K, U}, DecisionSchedule},
		{"all_done/active waits", domain.TriggerRuleAllDone, ups{F, R}, DecisionWait},
		{"all_done/all skipped runs", domain.TriggerRuleAllDone, ups{K, K}, DecisionSchedule},

		{"one_success/runs on one success while a sibling runs", domain.TriggerRuleOneSuccess, ups{S, R}, DecisionSchedule},
		{"one_success/runs on one success beside upstream_failed", domain.TriggerRuleOneSuccess, ups{S, U}, DecisionSchedule},
		{"one_success/no success yet waits", domain.TriggerRuleOneSuccess, ups{F, R}, DecisionWait},
		{"one_success/all done without success is upstream_failed", domain.TriggerRuleOneSuccess, ups{F, K}, DecisionUpstreamFailed},
		{"one_success/all upstream_failed is upstream_failed", domain.TriggerRuleOneSuccess, ups{U, U}, DecisionUpstreamFailed},
		{"one_success/all skipped skips", domain.TriggerRuleOneSuccess, ups{K, K}, DecisionSkip},

		{"one_failed/runs on one failure while a sibling runs", domain.TriggerRuleOneFailed, ups{F, R}, DecisionSchedule},
		{"one_failed/runs on upstream_failed", domain.TriggerRuleOneFailed, ups{U, R}, DecisionSchedule},
		{"one_failed/no failure yet waits", domain.TriggerRuleOneFailed, ups{S, R}, DecisionWait},
		{"one_failed/all done without failure skips", domain.TriggerRuleOneFailed, ups{S, K}, DecisionSkip},

		{"none_failed/all succeeded", domain.TriggerRuleNoneFailed, ups{S, S}, DecisionSchedule},
		{"none_failed/success and skip runs", domain.TriggerRuleNoneFailed, ups{S, K}, DecisionSchedule},
		{"none_failed/all skipped runs", domain.TriggerRuleNoneFailed, ups{K, K}, DecisionSchedule},
		{"none_failed/active waits", domain.TriggerRuleNoneFailed, ups{S, R}, DecisionWait},
		{"none_failed/failed propagates while a sibling runs", domain.TriggerRuleNoneFailed, ups{R, F}, DecisionUpstreamFailed},
		{"none_failed/upstream_failed propagates", domain.TriggerRuleNoneFailed, ups{K, U}, DecisionUpstreamFailed},

		{"none_failed_min_one_success/success and skip runs", domain.TriggerRuleNoneFailedMinOneSuccess, ups{S, K}, DecisionSchedule},
		{"none_failed_min_one_success/all skipped skips", domain.TriggerRuleNoneFailedMinOneSuccess, ups{K, K}, DecisionSkip},
		{"none_failed_min_one_success/active waits", domain.TriggerRuleNoneFailedMinOneSuccess, ups{K, R}, DecisionWait},
		{"none_failed_min_one_success/failed propagates", domain.TriggerRuleNoneFailedMinOneSuccess, ups{S, F}, DecisionUpstreamFailed},
		{"none_failed_min_one_success/upstream_failed propagates while a sibling runs", domain.TriggerRuleNoneFailedMinOneSuccess, ups{R, U}, DecisionUpstreamFailed},
		{"none_failed_min_one_success/failure wins over all-skipped", domain.TriggerRuleNoneFailedMinOneSuccess, ups{K, U}, DecisionUpstreamFailed},

		{"none_skipped/all succeeded", domain.TriggerRuleNoneSkipped, ups{S, S}, DecisionSchedule},
		{"none_skipped/a failed upstream still runs", domain.TriggerRuleNoneSkipped, ups{S, F}, DecisionSchedule},
		{"none_skipped/upstream_failed still runs", domain.TriggerRuleNoneSkipped, ups{U, S}, DecisionSchedule},
		{"none_skipped/active waits", domain.TriggerRuleNoneSkipped, ups{F, R}, DecisionWait},
		{"none_skipped/a skip skips at once", domain.TriggerRuleNoneSkipped, ups{R, K}, DecisionSkip},

		{"always/runs with active upstreams", domain.TriggerRuleAlways, ups{R, Q}, DecisionSchedule},
		{"always/runs after failures", domain.TriggerRuleAlways, ups{F, U, K}, DecisionSchedule},

		{"unknown rule waits", domain.TriggerRule("one_done"), ups{S}, DecisionWait},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateTriggerRule(tc.rule, tc.upstreams); got != tc.want {
				t.Errorf("EvaluateTriggerRule(%s, %v) = %d, want %d", tc.rule, tc.upstreams, got, tc.want)
			}
		})
	}
}
