package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/logs"
)

// ceilingRunning is a runningLister fake: it returns its candidates and
// records the grace each list asked for.
type ceilingRunning struct {
	cands  []PodLostCandidate
	err    error
	graces []time.Duration
}

func (f *ceilingRunning) ListRunningTasks(_ context.Context, grace time.Duration) ([]PodLostCandidate, error) {
	f.graces = append(f.graces, grace)
	return f.cands, f.err
}

// ceilingTimeline is one ordered record of what the store, the stopper and the
// log sink saw, so a test can assert the order of the mark, the stop and the
// marker across the reaper's background stop.
type ceilingTimeline struct {
	mu     sync.Mutex
	events []string
}

func (l *ceilingTimeline) add(ev string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
}

func (l *ceilingTimeline) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

// ceilingStore is the HeartbeatReapStore fake of the ceiling tests: it lists
// no silent agent and writes every mark into the timeline.
type ceilingStore struct {
	tl       *ceilingTimeline
	markNoop bool
	markErr  error
}

func (s *ceilingStore) ListAgentLostCandidates(context.Context) ([]AgentLostCandidate, error) {
	return nil, nil
}

func (s *ceilingStore) MarkTaskAgentLost(_ context.Context, id string, try, epoch int) (bool, error) {
	s.tl.add(fmt.Sprintf("agent_lost %s/%d/%d", id, try, epoch))
	return true, nil
}

func (s *ceilingStore) MarkTaskCredentialCeiling(_ context.Context, id string, try, epoch int) (bool, error) {
	if s.markErr != nil {
		return false, s.markErr
	}
	if s.markNoop {
		return false, nil
	}
	s.tl.add(fmt.Sprintf("mark %s/%d/%d", id, try, epoch))
	return true, nil
}

// ceilingProcs is a ProcessLiveness that can also stop an attempt, the way the
// subprocess executor can (AttemptStopper).
type ceilingProcs struct {
	tl       *ceilingTimeline
	alive    map[string]bool
	aliveErr error
	stopOK   bool
	stopErr  error
}

func (p *ceilingProcs) AttemptProcessAlive(_ context.Context, runID, taskID string, try int) (bool, error) {
	return p.alive[fmt.Sprintf("%s/%s/%d", runID, taskID, try)], p.aliveErr
}

func (p *ceilingProcs) StopAttempt(_ context.Context, runID, taskID string, try int) (bool, error) {
	p.tl.add(fmt.Sprintf("stop %s/%s/%d", runID, taskID, try))
	return p.stopOK, p.stopErr
}

// ceilingSink writes every marker into the timeline.
type ceilingSink struct {
	tl   *ceilingTimeline
	mu   sync.Mutex
	refs []logs.Ref
	evs  []logs.Event
}

func (s *ceilingSink) AppendEvent(ref logs.Ref, ev logs.Event) error {
	s.mu.Lock()
	s.refs = append(s.refs, ref)
	s.evs = append(s.evs, ev)
	s.mu.Unlock()
	s.tl.add("marker " + ev.Message)
	return nil
}

// pastCeilingFixture is one running attempt that entered running two hours
// ago under a one hour ceiling, with a live agent, wired into a Lite-shaped
// agent-lost reaper.
type pastCeilingFixture struct {
	tl      *ceilingTimeline
	store   *ceilingStore
	procs   *ceilingProcs
	running *ceilingRunning
	sink    *ceilingSink
	rec     *capturingRecorder
	r       *agentLostReaper
}

func newPastCeilingFixture() *pastCeilingFixture {
	tl := &ceilingTimeline{}
	f := &pastCeilingFixture{
		tl:    tl,
		store: &ceilingStore{tl: tl},
		procs: &ceilingProcs{tl: tl, alive: map[string]bool{"run-a/t/2": true}, stopOK: true},
		running: &ceilingRunning{cands: []PodLostCandidate{{
			TaskInstanceID: "runaway", TenantID: "ten", DagRunID: "run-a", DagID: "d", TaskID: "t",
			TryNumber: 2, AttemptEpoch: 4, RunningSince: time.Now().UTC().Add(-2 * time.Hour), Heartbeated: true,
		}}},
		rec: &capturingRecorder{},
	}
	f.sink = &ceilingSink{tl: tl}
	f.r = newAgentLostReaper(f.store, reapTestLogger(), 90*time.Second, f.rec)
	f.r.procs = f.procs
	f.r.running = f.running
	f.r.sink = f.sink
	f.r.ceiling = time.Hour
	return f
}

// runAndWait runs one agent-lost pass and waits for the stops it started.
func (f *pastCeilingFixture) runAndWait(t *testing.T) {
	t.Helper()
	if err := f.r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	f.r.waitCeilingStops()
}

// TestCeilingStopsALiveLiteAttempt is #1511: in Lite an attempt still running
// past the credential ceiling is failed as credential_ceiling and its task is
// stopped, instead of running on with a lapsed credential until it exits on
// its own. The mark comes first, pinned to the listed attempt, so the agent's
// own report can no longer settle it as something else. The stop follows, and
// the log marker comes last, once the processes are gone, so it is the last
// line of the try log.
func TestCeilingStopsALiveLiteAttempt(t *testing.T) {
	f := newPastCeilingFixture()
	f.runAndWait(t)

	got := f.tl.snapshot()
	if len(got) != 3 || got[0] != "mark runaway/2/4" || got[1] != "stop run-a/t/2" || !strings.HasPrefix(got[2], "marker ") {
		t.Fatalf("want mark, then stop, then marker; got %q", got)
	}
	msg := got[2]
	for _, want := range []string{"killed: credential_ceiling", "auth.max_attempt_credential_lifetime", "1h0m0s"} {
		if !strings.Contains(msg, want) {
			t.Errorf("marker %q must contain %q", msg, want)
		}
	}
	if ref := f.sink.refs[0]; ref != (logs.Ref{TenantID: "ten", DagID: "d", RunID: "run-a", TaskID: "t", TryNumber: 2, AttemptEpoch: 4}) {
		t.Errorf("the marker must go to the stopped attempt's log, got %+v", ref)
	}
	if want := []time.Duration{time.Hour}; len(f.running.graces) == 0 || f.running.graces[0] != want[0] {
		t.Errorf("the past-ceiling list must ask for attempts running longer than the ceiling, got graces %v", f.running.graces)
	}
	if f.rec.count("credential_ceiling_stopped") != 1 {
		t.Errorf("want one credential_ceiling_stopped decision, got %v", f.rec.decisions)
	}
}

// TestCeilingMarkerIsStampedAfterTheStop: the log is read back in time order,
// so the marker's own timestamp must not predate the end of the stop.
func TestCeilingMarkerIsStampedAfterTheStop(t *testing.T) {
	f := newPastCeilingFixture()
	before := time.Now()
	f.runAndWait(t)
	if len(f.sink.evs) != 1 || f.sink.evs[0].Time.Before(before) {
		t.Fatalf("want one marker stamped at write time, got %+v", f.sink.evs)
	}
}

// TestCeilingLeavesOtherAttemptsAlone: the pass judges only a live attempt
// past the ceiling. An attempt inside the ceiling runs on, an attempt whose
// processes are gone is the agent-lost paths' to settle, an unknown liveness
// defers, and a non-positive ceiling or a ProcessLiveness that cannot stop
// anything (the pod path) turns the pass off.
func TestCeilingLeavesOtherAttemptsAlone(t *testing.T) {
	tests := []struct {
		name  string
		tweak func(f *pastCeilingFixture)
	}{
		{"inside the ceiling", func(f *pastCeilingFixture) {
			f.running.cands[0].RunningSince = time.Now().UTC().Add(-30 * time.Minute)
		}},
		{"unknown start", func(f *pastCeilingFixture) { f.running.cands[0].RunningSince = time.Time{} }},
		{"processes gone", func(f *pastCeilingFixture) { f.procs.alive = map[string]bool{} }},
		{"liveness unknown", func(f *pastCeilingFixture) { f.procs.aliveErr = errors.New("pid dir unreadable") }},
		{"zero ceiling", func(f *pastCeilingFixture) { f.r.ceiling = 0 }},
		{"negative ceiling", func(f *pastCeilingFixture) { f.r.ceiling = -time.Hour }},
		{"closed gate", func(f *pastCeilingFixture) { f.r.gate = func(context.Context) bool { return false } }},
		{"liveness that cannot stop", func(f *pastCeilingFixture) {
			f.r.procs = &fakeProcessLiveness{alive: map[string]bool{"run-a/t/2": true}}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newPastCeilingFixture()
			tc.tweak(f)
			f.runAndWait(t)
			if got := f.tl.snapshot(); len(got) != 0 {
				t.Errorf("nothing may be marked, stopped or logged, got %q", got)
			}
		})
	}
}

// TestCeilingNoopMarkStopsNothing: when the agent's own report settled the
// attempt between the list and the mark, the guarded mark matches no row and
// the attempt is no longer the reaper's: nothing is stopped and no marker is
// written, so a natural exit and the ceiling never both finalize it.
func TestCeilingNoopMarkStopsNothing(t *testing.T) {
	f := newPastCeilingFixture()
	f.store.markNoop = true
	f.runAndWait(t)
	if got := f.tl.snapshot(); len(got) != 0 {
		t.Errorf("a mark that matched no row must stop and log nothing, got %q", got)
	}
	if f.rec.count("credential_ceiling_noop") != 1 {
		t.Errorf("want credential_ceiling_noop, got %v", f.rec.decisions)
	}
}

// TestCeilingMarkErrorStopsNothing: a failed mark leaves the attempt running
// for the next pass; nothing is stopped while its state is unknown.
func TestCeilingMarkErrorStopsNothing(t *testing.T) {
	f := newPastCeilingFixture()
	f.store.markErr = errors.New("db down")
	f.runAndWait(t)
	if got := f.tl.snapshot(); len(got) != 0 {
		t.Errorf("a failed mark must stop and log nothing, got %q", got)
	}
	if f.rec.count("credential_ceiling_error") != 1 {
		t.Errorf("want credential_ceiling_error, got %v", f.rec.decisions)
	}
}

// TestCeilingStopFailureStillLogsTheReason: the attempt is already failed when
// the stop runs, so a stop that fails is metered, and the marker still says why
// the attempt ended.
func TestCeilingStopFailureStillLogsTheReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
		err  error
	}{
		{"not stopped", false, nil},
		{"stop error", false, errors.New("signal refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPastCeilingFixture()
			f.procs.stopOK, f.procs.stopErr = tc.ok, tc.err
			f.runAndWait(t)
			got := f.tl.snapshot()
			if len(got) != 3 || !strings.Contains(got[2], "credential_ceiling") {
				t.Fatalf("want mark, stop, marker; got %q", got)
			}
			if f.rec.count("credential_ceiling_stop_error") != 1 {
				t.Errorf("want credential_ceiling_stop_error, got %v", f.rec.decisions)
			}
		})
	}
}

// TestCeilingStoppedAttemptIsNotAlsoAgentLost: the ceiling pass runs before the
// silent-agent pass, so an attempt it settled is never also judged agent_lost
// in the same pass (the guarded mark would no-op, but the order keeps the
// metrics and the log honest).
func TestCeilingStoppedAttemptIsNotAlsoAgentLost(t *testing.T) {
	f := newPastCeilingFixture()
	f.runAndWait(t)
	for _, ev := range f.tl.snapshot() {
		if strings.HasPrefix(ev, "agent_lost") {
			t.Errorf("the attempt must not also be marked agent_lost, got %q", f.tl.snapshot())
		}
	}
}
