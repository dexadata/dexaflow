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
}

// allowedTaskTransitions is the test's independent source of truth for the task
// state machine; the implementation must agree with it on all 81 pairs.
var allowedTaskTransitions = map[domain.TaskState][]domain.TaskState{
	domain.TaskStateNone:            {domain.TaskStateScheduled, domain.TaskStateSkipped, domain.TaskStateUpstreamFailed},
	domain.TaskStateScheduled:       {domain.TaskStateQueued},
	domain.TaskStateQueued:          {domain.TaskStateRunning, domain.TaskStateFailed, domain.TaskStateUpForRetry},
	domain.TaskStateRunning:         {domain.TaskStateSuccess, domain.TaskStateFailed, domain.TaskStateUpForRetry, domain.TaskStateUpForReschedule},
	domain.TaskStateUpForRetry:      {domain.TaskStateScheduled, domain.TaskStateQueued, domain.TaskStateNone},
	domain.TaskStateUpForReschedule: {domain.TaskStateScheduled, domain.TaskStateQueued, domain.TaskStateNone},
	domain.TaskStateSuccess:         {domain.TaskStateNone},
	domain.TaskStateFailed:          {domain.TaskStateNone},
	domain.TaskStateSkipped:         {domain.TaskStateNone},
	domain.TaskStateUpstreamFailed:  {domain.TaskStateNone},
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
	domain.DagRunStateQueued:  {domain.DagRunStateRunning, domain.DagRunStateFailed},
	domain.DagRunStateRunning: {domain.DagRunStateSuccess, domain.DagRunStateFailed},
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

func TestEvaluateTriggerRule(t *testing.T) {
	const (
		S = domain.TaskStateSuccess
		F = domain.TaskStateFailed
		K = domain.TaskStateSkipped
		U = domain.TaskStateUpstreamFailed
		R = domain.TaskStateRunning // an active (non-terminal) upstream
	)
	cases := []struct {
		name      string
		rule      domain.TriggerRule
		upstreams []domain.TaskState
		want      TriggerDecision
	}{
		{"root task always schedules", domain.TriggerRuleAllSuccess, nil, DecisionSchedule},

		{"all_success/all succeeded", domain.TriggerRuleAllSuccess, []domain.TaskState{S, S}, DecisionSchedule},
		{"all_success/one active waits", domain.TriggerRuleAllSuccess, []domain.TaskState{S, R}, DecisionWait},
		{"all_success/one failed propagates", domain.TriggerRuleAllSuccess, []domain.TaskState{S, F}, DecisionUpstreamFailed},
		{"all_success/one skipped skips", domain.TriggerRuleAllSuccess, []domain.TaskState{S, K}, DecisionSkip},
		{"all_success/upstream_failed propagates", domain.TriggerRuleAllSuccess, []domain.TaskState{S, U}, DecisionUpstreamFailed},

		{"all_failed/all failed", domain.TriggerRuleAllFailed, []domain.TaskState{F, F}, DecisionSchedule},
		{"all_failed/active waits", domain.TriggerRuleAllFailed, []domain.TaskState{F, R}, DecisionWait},
		{"all_failed/a success skips", domain.TriggerRuleAllFailed, []domain.TaskState{F, S}, DecisionSkip},

		{"all_done/all terminal", domain.TriggerRuleAllDone, []domain.TaskState{S, F, K}, DecisionSchedule},
		{"all_done/active waits", domain.TriggerRuleAllDone, []domain.TaskState{S, R}, DecisionWait},
		{"all_done/upstream_failed still schedules", domain.TriggerRuleAllDone, []domain.TaskState{U, S}, DecisionSchedule},

		{"one_success/has success", domain.TriggerRuleOneSuccess, []domain.TaskState{S, F}, DecisionSchedule},
		{"one_success/active waits", domain.TriggerRuleOneSuccess, []domain.TaskState{S, R}, DecisionWait},
		{"one_success/none success skips", domain.TriggerRuleOneSuccess, []domain.TaskState{F, K}, DecisionSkip},
		{"one_success/upstream_failed propagates", domain.TriggerRuleOneSuccess, []domain.TaskState{S, U}, DecisionUpstreamFailed},

		{"one_failed/has failed", domain.TriggerRuleOneFailed, []domain.TaskState{F, S}, DecisionSchedule},
		{"one_failed/active waits", domain.TriggerRuleOneFailed, []domain.TaskState{F, R}, DecisionWait},
		{"one_failed/none failed skips", domain.TriggerRuleOneFailed, []domain.TaskState{S, K}, DecisionSkip},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateTriggerRule(tc.rule, tc.upstreams); got != tc.want {
				t.Errorf("EvaluateTriggerRule(%s, %v) = %d, want %d", tc.rule, tc.upstreams, got, tc.want)
			}
		})
	}
}
