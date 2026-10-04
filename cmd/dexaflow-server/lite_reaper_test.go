package main

import (
	"context"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/executor"
)

// liteReapStore is a ReaperStore fake holding one stale candidate per DB-only
// reaper: a quiet running run, a running TI whose heartbeat went silent, and a
// TI queued well past the dispatch-lost threshold.
type liteReapStore struct {
	reapedRuns, agentMarked, queuedMarked, podMarked []string
}

func (s *liteReapStore) ListReapCandidates(context.Context) ([]executor.ReapCandidate, error) {
	return []executor.ReapCandidate{{RunID: "quiet-run", DagID: "d", LastActivity: time.Now().Add(-time.Hour)}}, nil
}
func (s *liteReapStore) ReapRun(_ context.Context, id string) error {
	s.reapedRuns = append(s.reapedRuns, id)
	return nil
}
func (s *liteReapStore) ListAgentLostCandidates(context.Context) ([]executor.AgentLostCandidate, error) {
	return []executor.AgentLostCandidate{{TaskInstanceID: "dead-agent", DagRunID: "r1", TaskID: "t", TryNumber: 1, LastHeartbeat: time.Now().Add(-time.Hour)}}, nil
}
func (s *liteReapStore) MarkTaskAgentLost(_ context.Context, id string) (bool, error) {
	s.agentMarked = append(s.agentMarked, id)
	return true, nil
}
func (s *liteReapStore) ListStaleQueuedCandidates(context.Context) ([]executor.StaleQueuedCandidate, error) {
	return []executor.StaleQueuedCandidate{{TaskInstanceID: "live-queued", DagRunID: "r2", TaskID: "t", TryNumber: 1, QueuedAt: time.Now().Add(-time.Hour)}}, nil
}
func (s *liteReapStore) MarkTaskDispatchLost(_ context.Context, id string) error {
	s.queuedMarked = append(s.queuedMarked, id)
	return nil
}
func (s *liteReapStore) ListRunningTasks(context.Context, time.Duration) ([]executor.PodLostCandidate, error) {
	return []executor.PodLostCandidate{{TaskInstanceID: "no-pod", DagRunID: "r3", TaskID: "t", TryNumber: 1, RunningSince: time.Now().Add(-time.Hour)}}, nil
}
func (s *liteReapStore) MarkTaskPodLost(_ context.Context, id string) (bool, error) {
	s.podMarked = append(s.podMarked, id)
	return true, nil
}
func (s *liteReapStore) ListWarmBoundRunningTIs(context.Context) ([]executor.WarmBoundTI, error) {
	return nil, nil
}

// liteProcs reports the agent of r2/t/1 alive and every other attempt gone.
type liteProcs struct{}

func (liteProcs) AttemptProcessAlive(_ context.Context, runID, _ string, _ int) (bool, error) {
	return runID == "r2", nil
}

// fakeLeadership is the scheduler's leadership surface the Lite reaper reads.
type fakeLeadership struct {
	since   time.Time
	leading bool
}

func (f *fakeLeadership) LeaderSince() time.Time { return f.since }
func (f *fakeLeadership) IsLeading() bool        { return f.leading }
func (f *fakeLeadership) SteppingDown() bool     { return false }

// TestLiteReaperWiring pins how Lite builds its execution reaper (#916): it is
// driven by the scheduler's leadership (no reaping on a follower), it is held by
// the leader-settling gate after an election, and once settled it runs the
// reapers that mean something without pods (orphan-run, agent-lost on a dead
// agent process) while dispatch-lost defers on a live agent process (#911) and
// the pod-only reapers mark nothing.
func TestLiteReaperWiring(t *testing.T) {
	store := &liteReapStore{}
	lead := &fakeLeadership{since: time.Now(), leading: true}
	reaper := newLiteReaper(store, liteProcs{}, lead, nil, nil, discardLog())

	if err := reaper.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if n := len(store.reapedRuns) + len(store.agentMarked) + len(store.queuedMarked); n != 0 {
		t.Fatalf("a freshly elected Lite leader must hold every reaper until it settles; got %d writes", n)
	}

	lead.since = time.Now().Add(-time.Hour)
	lead.leading = false
	if err := reaper.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if n := len(store.reapedRuns) + len(store.agentMarked) + len(store.queuedMarked); n != 0 {
		t.Fatalf("a Lite instance that does not lead must not reap; got %d writes", n)
	}

	lead.leading = true
	if err := reaper.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	// agentMarked: the silent dead agent, and the dead agent that never
	// heartbeated (Lite-only never-heartbeated path).
	if len(store.reapedRuns) != 1 || len(store.agentMarked) != 2 {
		t.Errorf("settled Lite leader must reap the orphan run and both dead agents: reapedRuns=%v agentMarked=%v",
			store.reapedRuns, store.agentMarked)
	}
	if len(store.queuedMarked) != 0 {
		t.Errorf("dispatch-lost must defer on a live agent process (#911): queuedMarked=%v", store.queuedMarked)
	}
	if len(store.podMarked) != 0 {
		t.Errorf("pod-lost and warm-worker-lost have no signal in Lite: podMarked=%v", store.podMarked)
	}
}
