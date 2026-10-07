package executor

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestOutlivedCredentialCeiling pins the pure decision behind #1461: a silent
// attempt that has been running for longer than auth.max_attempt_credential_lifetime
// outlived the ceiling. A non-positive ceiling is "disabled" and never matches,
// and an attempt with no recorded start is never judged against it.
func TestOutlivedCredentialCeiling(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	const ceiling = time.Hour
	tests := []struct {
		name    string
		started time.Time
		ceiling time.Duration
		want    bool
	}{
		{"well past the ceiling", now.Add(-2 * time.Hour), ceiling, true},
		{"just past the ceiling", now.Add(-ceiling - time.Second), ceiling, true},
		{"exactly at the ceiling is not past it", now.Add(-ceiling), ceiling, false},
		{"inside the ceiling", now.Add(-30 * time.Minute), ceiling, false},
		{"zero ceiling is disabled", now.Add(-48 * time.Hour), 0, false},
		{"negative ceiling is disabled", now.Add(-48 * time.Hour), -time.Hour, false},
		{"unknown start is never judged", time.Time{}, ceiling, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := AgentLostCandidate{StartedAt: tc.started}
			if got := OutlivedCredentialCeiling(c, tc.ceiling, now); got != tc.want {
				t.Errorf("OutlivedCredentialCeiling(started=%v, ceiling=%v) = %v, want %v", tc.started, tc.ceiling, got, tc.want)
			}
		})
	}
}

// TestReapAgentLost_PastCeilingFailsAsTaskFailure is the #1461 fix: an attempt
// whose agent went silent after it outlived the credential ceiling is failed
// with the credential_ceiling reason (a task failure, subject to retries), not
// marked agent_lost (an infra loss that re-places it with a fresh credential).
func TestReapAgentLost_PastCeilingFailsAsTaskFailure(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{{
		TaskInstanceID: "runaway", TenantID: "ten", DagRunID: "run-a", DagID: "d", TaskID: "t",
		TryNumber: 1, AttemptEpoch: 3,
		StartedAt:     now.Add(-25 * time.Minute),
		LastHeartbeat: now.Add(-5 * time.Minute),
	}}}
	rec := &capturingRecorder{}
	sink := &fakeLogSink{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
	r.ceiling = 11 * time.Minute
	r.sink = sink

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(store.failed) != 0 {
		t.Errorf("a past-ceiling attempt must not be marked agent_lost (re-placed as infra), got %v", store.failed)
	}
	if len(store.ceilingPins) != 1 || store.ceilingPins[0] != "runaway/1/3" {
		t.Fatalf("the credential-ceiling mark must name the listed attempt (ti/try/epoch), got %v", store.ceilingPins)
	}
	if rec.count("agent_lost_credential_ceiling") != 1 {
		t.Errorf("want one agent_lost_credential_ceiling decision, got %v", rec.decisions)
	}
	if rec.count("agent_lost") != 0 {
		t.Errorf("a credential-ceiling failure must not also count as agent_lost, got %v", rec.decisions)
	}
	evs := sink.events["ten/d/run-a/t/1"]
	if len(evs) != 1 || !strings.Contains(evs[0].Message, "credential_ceiling") ||
		!strings.Contains(evs[0].Message, "auth.max_attempt_credential_lifetime") {
		t.Errorf("the log marker must name the credential ceiling, got %+v", evs)
	}
}

// TestReapAgentLost_InsideCeilingStaysAgentLost: an attempt that went silent
// before it reached the ceiling is a lost agent, exactly as before.
func TestReapAgentLost_InsideCeilingStaysAgentLost(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{{
		TaskInstanceID: "lost", StartedAt: now.Add(-10 * time.Minute), LastHeartbeat: now.Add(-5 * time.Minute),
	}}}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
	r.ceiling = time.Hour
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(store.failed) != 1 || len(store.ceilingPins) != 0 {
		t.Errorf("inside the ceiling: want agent_lost only, got agent_lost=%v ceiling=%v", store.failed, store.ceilingPins)
	}
}

// TestReapAgentLost_DisabledCeilingKeepsAgentLost: a non-positive ceiling is
// the operator's "no ceiling", so even a very old silent attempt keeps today's
// agent_lost outcome.
func TestReapAgentLost_DisabledCeilingKeepsAgentLost(t *testing.T) {
	for _, ceiling := range []time.Duration{0, -time.Hour} {
		now := time.Now().UTC()
		store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{{
			TaskInstanceID: "old", StartedAt: now.Add(-72 * time.Hour), LastHeartbeat: now.Add(-5 * time.Minute),
		}}}
		r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
		r.ceiling = ceiling
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run err = %v", err)
		}
		if len(store.failed) != 1 || len(store.ceilingPins) != 0 {
			t.Errorf("ceiling %v: want agent_lost only, got agent_lost=%v ceiling=%v", ceiling, store.failed, store.ceilingPins)
		}
	}
}

// TestReapAgentLost_PastCeilingRespectsGateAndFence: the credential-ceiling
// mark goes through the same destructive gate and the same "a late report won"
// no-op as agent_lost, and the pod teardown follows only an applied mark.
func TestReapAgentLost_PastCeilingRespectsGateAndFence(t *testing.T) {
	now := time.Now().UTC()
	cand := AgentLostCandidate{
		TaskInstanceID: "runaway", DagRunID: "run-a", TaskID: "t", TryNumber: 2, AttemptEpoch: 1,
		StartedAt: now.Add(-3 * time.Hour), LastHeartbeat: now.Add(-5 * time.Minute),
	}

	t.Run("closed gate marks nothing", func(t *testing.T) {
		store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{cand}}
		pods := &fakePodManager{}
		r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
		r.ceiling = time.Hour
		r.pods = pods
		r.gate = func(context.Context) bool { return false }
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run err = %v", err)
		}
		if len(store.ceilingPins) != 0 || len(store.failed) != 0 || len(pods.deletedTasks) != 0 {
			t.Errorf("a closed gate must stop every write, got ceiling=%v lost=%v deleted=%v", store.ceilingPins, store.failed, pods.deletedTasks)
		}
	})

	t.Run("applied mark tears down the attempt's pod", func(t *testing.T) {
		store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{cand}}
		pods := &fakePodManager{}
		r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
		r.ceiling = time.Hour
		r.pods = pods
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run err = %v", err)
		}
		if len(pods.deletedTasks) != 1 || pods.deletedTasks[0] != (deletedTask{"run-a", "t", 2}) {
			t.Errorf("want the reaped attempt's pod torn down, got %v", pods.deletedTasks)
		}
	})

	t.Run("noop mark leaves the pod alone", func(t *testing.T) {
		store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{cand}, markNoop: true}
		pods := &fakePodManager{}
		rec := &capturingRecorder{}
		r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
		r.ceiling = time.Hour
		r.pods = pods
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run err = %v", err)
		}
		if len(pods.deletedTasks) != 0 {
			t.Errorf("a mark that matched no row must not delete a pod, got %v", pods.deletedTasks)
		}
		if rec.count("agent_lost_noop") != 1 {
			t.Errorf("want agent_lost_noop, got %v", rec.decisions)
		}
	})
}

// TestReaperSetAttemptLifetimeCeiling: the Reaper hands the operator's ceiling
// to the agent-lost reaper, which is the only reaper that judges by it.
func TestReaperSetAttemptLifetimeCeiling(t *testing.T) {
	r := NewReaper(&fakeReaperStore{}, nil, nil, nil, nil, reapTestLogger(), DefaultReaperConfig(), nil)
	r.SetAttemptLifetimeCeiling(11 * time.Minute)
	if r.agentLost.ceiling != 11*time.Minute {
		t.Errorf("agent-lost ceiling = %v, want 11m", r.agentLost.ceiling)
	}
}
