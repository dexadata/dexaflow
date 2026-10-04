package executor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeProcessLiveness answers AttemptProcessAlive from a fixed map keyed by
// (run, task, try); err, when set, is returned for every query.
type fakeProcessLiveness struct {
	alive map[string]bool
	err   error
	asked []string
}

func (f *fakeProcessLiveness) AttemptProcessAlive(_ context.Context, runID, taskID string, tryNumber int) (bool, error) {
	key := fmt.Sprintf("%s/%s/%d", runID, taskID, tryNumber)
	f.asked = append(f.asked, key)
	if f.err != nil {
		return false, f.err
	}
	return f.alive[key], nil
}

// newLiteTestReaper builds the reaper exactly as Lite wires it: no pod manager,
// no presence cache, no warm lister, and the subprocess liveness seam in place
// of pod liveness.
func newLiteTestReaper(store *fakeReaperStore, procs ProcessLiveness, rec DecisionRecorder) *Reaper {
	r := NewReaper(store, nil, nil, nil, rec, reapTestLogger(), DefaultReaperConfig(), nil)
	r.SetProcessLiveness(procs)
	return r
}

// TestLiteReaperReapsDeadSubprocessWork: with no pods (Lite) the reapers that
// are meaningful without Kubernetes still run. A run nothing touches is failed
// as orphaned, a running TI whose agent process is gone and whose heartbeat went
// silent is failed as agent_lost, and a queued TI with no agent process is failed
// as dispatch_lost. The pod-only reapers stay no-ops: there is no pod to lose.
func TestLiteReaperReapsDeadSubprocessWork(t *testing.T) {
	store := staleEverythingStore()
	procs := &fakeProcessLiveness{alive: map[string]bool{}}
	r := newLiteTestReaper(store, procs, &capturingRecorder{})

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.reapedRuns) != 1 {
		t.Errorf("orphan-run must reap in Lite: reapedRuns=%v", store.reapedRuns)
	}
	if len(store.agentMarked) != 1 {
		t.Errorf("agent-lost must reap a dead subprocess agent in Lite: agentMarked=%v", store.agentMarked)
	}
	if len(store.queuedMarked) != 1 {
		t.Errorf("dispatch-lost must reap a queued TI with no agent process in Lite: queuedMarked=%v", store.queuedMarked)
	}
	if len(store.podMarked) != 0 {
		t.Errorf("pod-lost and warm-worker-lost have no signal in Lite and must not mark: podMarked=%v", store.podMarked)
	}
}

// TestLiteReaperDefersOnLiveAgentProcess: the subprocess liveness gate (#911).
// A queued TI whose agent process is alive (retrying its RUNNING report) must
// not be failed as dispatch_lost: the infra re-place that follows would start a
// second agent on the same try_number while the first one is still alive, and
// both could run user code. A running TI whose agent is alive but silent (a
// laptop resuming from sleep) is deferred for the same reason: Lite cannot
// stop the process the way a pod delete does, so it only reaps a dead one.
func TestLiteReaperDefersOnLiveAgentProcess(t *testing.T) {
	store := staleEverythingStore()
	procs := &fakeProcessLiveness{alive: map[string]bool{"r1/t/1": true, "r2/t/1": true}}
	rec := &capturingRecorder{}
	r := newLiteTestReaper(store, procs, rec)

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.agentMarked) != 0 {
		t.Errorf("a live agent process must defer agent-lost: agentMarked=%v", store.agentMarked)
	}
	if len(store.queuedMarked) != 0 {
		t.Errorf("a live agent process must defer dispatch-lost (#911): queuedMarked=%v", store.queuedMarked)
	}
	for _, want := range []string{"agent_lost_process_alive", "dispatch_lost_process_alive"} {
		if rec.count(want) == 0 {
			t.Errorf("deferral must be metered as %q; got %v", want, rec.decisions)
		}
	}
}

// TestLiteReaperDefersWhenLivenessUnknown: a liveness read that fails is "do no
// harm": the reaper defers rather than guess, exactly as the pod path defers on
// a failed pod LIST.
func TestLiteReaperDefersWhenLivenessUnknown(t *testing.T) {
	store := staleEverythingStore()
	procs := &fakeProcessLiveness{err: errors.New("pidfile unreadable")}
	rec := &capturingRecorder{}
	r := newLiteTestReaper(store, procs, rec)

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.agentMarked)+len(store.queuedMarked) != 0 {
		t.Errorf("unknown liveness must defer: agentMarked=%v queuedMarked=%v", store.agentMarked, store.queuedMarked)
	}
	for _, want := range []string{"agent_lost_process_query_error", "dispatch_lost_process_query_error"} {
		if rec.count(want) == 0 {
			t.Errorf("deferral must be metered as %q; got %v", want, rec.decisions)
		}
	}
}

// TestLiteReaperHeldBySettlingGate: Lite runs under the same leader-settling
// gate as the pod path. A Lite restart leaves detached agents alive with a stale
// heartbeat; until the grace has elapsed nothing is reaped.
func TestLiteReaperHeldBySettlingGate(t *testing.T) {
	store := staleEverythingStore()
	r := newLiteTestReaper(store, &fakeProcessLiveness{alive: map[string]bool{}}, &capturingRecorder{})
	now := time.Now()
	r.now = func() time.Time { return now }
	r.SetLeaderSince(func() time.Time { return now.Add(-time.Second) })

	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.reapedRuns)+len(store.agentMarked)+len(store.queuedMarked) != 0 {
		t.Errorf("nothing may be reaped while settling: reapedRuns=%v agentMarked=%v queuedMarked=%v",
			store.reapedRuns, store.agentMarked, store.queuedMarked)
	}

	r.SetLeaderSince(func() time.Time { return now.Add(-DefaultReaperConfig().SettlingGrace) })
	if err := r.ReapOnce(context.Background()); err != nil {
		t.Fatalf("ReapOnce: %v", err)
	}
	if len(store.reapedRuns) != 1 || len(store.agentMarked) != 1 || len(store.queuedMarked) != 1 {
		t.Errorf("once settled Lite must reap: reapedRuns=%v agentMarked=%v queuedMarked=%v",
			store.reapedRuns, store.agentMarked, store.queuedMarked)
	}
}
