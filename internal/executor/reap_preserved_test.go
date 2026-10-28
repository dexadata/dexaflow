package executor

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestReapersMeterTheTerminalPodsTheirTeardownPreserved pins #933: the
// teardown skips a pod already in a terminal phase (#928) and reports how many
// it skipped, and each reaper records that as its own
// <reaper>_terminal_pod_preserved decision, so the preserve rate is a series
// per reaper and not a log grep. It is recorded even when another pod of the
// same teardown failed to delete.
func TestReapersMeterTheTerminalPodsTheirTeardownPreserved(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-10 * time.Minute)
	cases := []struct {
		decision string
		run      func(rec DecisionRecorder, pods *fakePodManager) error
	}{
		{"agent_lost_terminal_pod_preserved", func(rec DecisionRecorder, pods *fakePodManager) error {
			r := newAgentLostReaper(&fakeHeartbeatStore{candidates: []AgentLostCandidate{
				{TaskInstanceID: "ti", DagRunID: "run-a", TaskID: "extract", TryNumber: 1, LastHeartbeat: old},
			}}, reapTestLogger(), 90*time.Second, rec)
			r.pods = pods
			return r.run(context.Background())
		}},
		{"orphan_terminal_pod_preserved", func(rec DecisionRecorder, pods *fakePodManager) error {
			r := newOrphanReaper(&fakeReapStore{candidates: []ReapCandidate{
				{RunID: "run-a", DagID: "etl", LastActivity: old},
			}}, reapTestLogger(), 5*time.Minute, rec)
			r.pods = pods
			return r.run(context.Background())
		}},
		{"dispatch_lost_terminal_pod_preserved", func(rec DecisionRecorder, pods *fakePodManager) error {
			r := newDispatchLostReaper(&fakeStaleQueuedStore{candidates: []StaleQueuedCandidate{
				{TaskInstanceID: "ti", DagRunID: "run-a", TaskID: "extract", TryNumber: 1, QueuedAt: old},
			}}, reapTestLogger(), 3*time.Minute, rec)
			r.pods = pods
			return r.run(context.Background())
		}},
		{"pod_lost_terminal_pod_preserved", func(rec DecisionRecorder, pods *fakePodManager) error {
			r := newPodLostReaper(&fakePodLostStore{candidates: []PodLostCandidate{
				{TaskInstanceID: "ti", DagRunID: "run-a", TaskID: "work", TryNumber: 1, RunningSince: old},
			}}, reapTestLogger(), 60*time.Second, rec)
			r.pods = pods
			return r.run(context.Background())
		}},
	}
	for _, tc := range cases {
		t.Run(tc.decision, func(t *testing.T) {
			for _, fail := range []bool{false, true} {
				rec := &capturingRecorder{}
				pods := &fakePodManager{preserved: 2}
				if fail {
					pods.deleteErr = errors.New("apiserver 500")
					pods.deleteRunE = pods.deleteErr
				}
				if err := tc.run(rec, pods); err != nil {
					t.Fatalf("run: %v", err)
				}
				if got := rec.count(tc.decision); got != 2 {
					t.Errorf("delete failing=%v: %s recorded %d times, want once per preserved pod (2); decisions %v",
						fail, tc.decision, got, rec.decisions)
				}
			}

			rec := &capturingRecorder{}
			if err := tc.run(rec, &fakePodManager{}); err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := rec.count(tc.decision); got != 0 {
				t.Errorf("a teardown that preserved nothing recorded %s %d times", tc.decision, got)
			}
		})
	}
}
