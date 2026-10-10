package main

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/executor"
)

// liteReapStore is a ReaperStore fake holding one stale candidate per DB-only
// reaper: a quiet running run, a running TI whose heartbeat went silent, and a
// TI queued well past the dispatch-lost threshold.
type liteReapStore struct {
	reapedRuns, agentMarked, queuedMarked, podMarked []string
	// agentPins records each agent-lost mark as "ti/try/epoch".
	agentPins []string
	// ceilingPins records each credential-ceiling mark as "ti/try/epoch" (#1461).
	ceilingPins []string
	// agentStartedAt is the silent agent's running-since stamp; zero leaves it
	// unknown, as an older row would.
	agentStartedAt time.Time
	// agentLastHeartbeat is the silent agent's last beat; zero means an hour ago.
	agentLastHeartbeat time.Time
}

func (s *liteReapStore) ListReapCandidates(context.Context) ([]executor.ReapCandidate, error) {
	return []executor.ReapCandidate{{RunID: "quiet-run", DagID: "d", LastActivity: time.Now().Add(-time.Hour)}}, nil
}
func (s *liteReapStore) ReapRun(_ context.Context, id string, _ time.Time) (bool, error) {
	s.reapedRuns = append(s.reapedRuns, id)
	return true, nil
}
func (s *liteReapStore) ListAgentLostCandidates(context.Context) ([]executor.AgentLostCandidate, error) {
	beat := s.agentLastHeartbeat
	if beat.IsZero() {
		beat = time.Now().Add(-time.Hour)
	}
	return []executor.AgentLostCandidate{{TaskInstanceID: "dead-agent", DagRunID: "r1", TaskID: "t", TryNumber: 1, AttemptEpoch: 2, StartedAt: s.agentStartedAt, LastHeartbeat: beat}}, nil
}
func (s *liteReapStore) MarkTaskAgentLost(_ context.Context, id string, try, epoch int) (bool, error) {
	s.agentMarked = append(s.agentMarked, id)
	s.agentPins = append(s.agentPins, fmt.Sprintf("%s/%d/%d", id, try, epoch))
	return true, nil
}
func (s *liteReapStore) MarkTaskCredentialCeiling(_ context.Context, id string, try, epoch int) (bool, error) {
	s.ceilingPins = append(s.ceilingPins, fmt.Sprintf("%s/%d/%d", id, try, epoch))
	return true, nil
}
func (s *liteReapStore) ListStaleQueuedCandidates(context.Context) ([]executor.StaleQueuedCandidate, error) {
	return []executor.StaleQueuedCandidate{{TaskInstanceID: "live-queued", DagRunID: "r2", TaskID: "t", TryNumber: 1, QueuedAt: time.Now().Add(-time.Hour)}}, nil
}
func (s *liteReapStore) MarkTaskDispatchLost(_ context.Context, id string, _, _ int) (bool, error) {
	s.queuedMarked = append(s.queuedMarked, id)
	return true, nil
}
func (s *liteReapStore) ListRunningTasks(context.Context, time.Duration) ([]executor.PodLostCandidate, error) {
	return []executor.PodLostCandidate{{TaskInstanceID: "no-pod", DagRunID: "r3", TaskID: "t", TryNumber: 1, AttemptEpoch: 3, RunningSince: time.Now().Add(-time.Hour)}}, nil
}
func (s *liteReapStore) MarkTaskPodLost(_ context.Context, id string, _, _ int) (bool, error) {
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
	reaper := newLiteReaper(store, liteProcs{}, lead, nil, nil, discardLog(), 24*time.Hour)

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
	// Both agent-lost marks are pinned to the listed attempt (ADR 0051 A4), so
	// a row re-placed between the list and the write is left alone.
	if want := []string{"dead-agent/1/2", "no-pod/1/3"}; !slices.Equal(store.agentPins, want) {
		t.Errorf("agent-lost marks must carry the listed try and epoch: got %v, want %v", store.agentPins, want)
	}
	if len(store.queuedMarked) != 0 {
		t.Errorf("dispatch-lost must defer on a live agent process (#911): queuedMarked=%v", store.queuedMarked)
	}
	if len(store.podMarked) != 0 {
		t.Errorf("pod-lost and warm-worker-lost have no signal in Lite: podMarked=%v", store.podMarked)
	}
}

// TestLiteReaperFailsPastCeilingAttempt pins #1461 on the Lite wiring: the
// operator's auth.max_attempt_credential_lifetime reaches the agent-lost reaper,
// so a silent attempt that ran past it is failed for the credential ceiling (a
// task failure) instead of being marked agent_lost and re-placed.
func TestLiteReaperFailsPastCeilingAttempt(t *testing.T) {
	// The agent beat for 20 minutes, past the 11 minute ceiling, then went silent.
	store := &liteReapStore{agentStartedAt: time.Now().Add(-25 * time.Minute), agentLastHeartbeat: time.Now().Add(-5 * time.Minute)}
	lead := &fakeLeadership{since: time.Now().Add(-time.Hour), leading: true}
	reaper := newLiteReaper(store, liteProcs{}, lead, nil, nil, discardLog(), 11*time.Minute)
	if err := reaper.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if want := []string{"dead-agent/1/2"}; !slices.Equal(store.ceilingPins, want) {
		t.Errorf("the past-ceiling attempt must be failed for the credential ceiling: got %v, want %v", store.ceilingPins, want)
	}
	// The never-heartbeated dead agent is not a ceiling case and stays agent_lost.
	if want := []string{"no-pod/1/3"}; !slices.Equal(store.agentPins, want) {
		t.Errorf("only the never-heartbeated agent stays agent_lost: got %v, want %v", store.agentPins, want)
	}
}

// stoppingLiteProcs reports the agent of r3/t/1 alive and records each
// StopAttempt, the way the subprocess executor can stop a live attempt.
type stoppingLiteProcs struct {
	mu      sync.Mutex
	stopped []string
}

func (*stoppingLiteProcs) AttemptProcessAlive(_ context.Context, runID, _ string, _ int) (bool, error) {
	return runID == "r3", nil
}

func (p *stoppingLiteProcs) StopAttempt(_ context.Context, runID, taskID string, try int) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = append(p.stopped, fmt.Sprintf("%s/%s/%d", runID, taskID, try))
	return true, nil
}

func (p *stoppingLiteProcs) stops() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.stopped)
}

// The Lite server hands its subprocess executor to the reaper as the process
// liveness seam; it must be able to stop a live attempt for #1511 to apply.
var _ executor.AttemptStopper = (*executor.SubprocessExecutor)(nil)

// TestLiteReaperStopsALiveAttemptPastCeiling pins #1511 on the Lite wiring: an
// attempt still running past auth.max_attempt_credential_lifetime, with its
// agent alive, is failed as credential_ceiling and its task is stopped, the
// Lite counterpart of a task pod's activeDeadlineSeconds.
func TestLiteReaperStopsALiveAttemptPastCeiling(t *testing.T) {
	store := &liteReapStore{agentLastHeartbeat: time.Now()}
	procs := &stoppingLiteProcs{}
	lead := &fakeLeadership{since: time.Now().Add(-time.Hour), leading: true}
	reaper := newLiteReaper(store, procs, lead, nil, nil, discardLog(), 11*time.Minute)
	if err := reaper.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if want := []string{"no-pod/1/3"}; !slices.Equal(store.ceilingPins, want) {
		t.Errorf("the live past-ceiling attempt must be failed for the credential ceiling: got %v, want %v", store.ceilingPins, want)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(procs.stops()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if want := []string{"r3/t/1"}; !slices.Equal(procs.stops(), want) {
		t.Errorf("the live past-ceiling attempt's task must be stopped: got %v, want %v", procs.stops(), want)
	}
	if len(store.agentPins) != 0 {
		t.Errorf("a live attempt must never be marked agent_lost: got %v", store.agentPins)
	}
}
