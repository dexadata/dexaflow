package executor

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/logs"
)

// fakeLogSink captures the final markers a reaper appends to a task's log
// stream (#861) via the MarkerSink seam, keyed by the same identity the disk
// sink uses. appendErr lets a test exercise the best-effort error path.
type fakeLogSink struct {
	appendErr error
	events    map[string][]logs.Event
}

func refKey(ref logs.Ref) string {
	return ref.TenantID + "/" + ref.DagID + "/" + ref.RunID + "/" + ref.TaskID + "/" + strconv.Itoa(ref.TryNumber)
}

func (f *fakeLogSink) AppendEvent(ref logs.Ref, ev logs.Event) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	if f.events == nil {
		f.events = map[string][]logs.Event{}
	}
	f.events[refKey(ref)] = append(f.events[refKey(ref)], ev)
	return nil
}

// TestIsAgentLost: the pure decision returns true iff the gap between the
// candidate's last heartbeat and now reaches the threshold. A zero
// LastHeartbeat (never reported) is treated as still alive — the TI may be
// inline (no agent) or fresh — so this reaper only fires on TIs that *did*
// heartbeat at least once and then went silent. The "do no harm" rule
// (ADR 0031): a positive observable signal is required.
func TestIsAgentLost(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	const threshold = 90 * time.Second

	tests := []struct {
		name string
		last time.Time
		want bool
	}{
		{"fresh heartbeat is alive", now.Add(-1 * time.Second), false},
		{"exactly at threshold is lost", now.Add(-90 * time.Second), true},
		{"well past threshold is lost", now.Add(-10 * time.Minute), true},
		{"future heartbeat (clock skew) is alive", now.Add(1 * time.Second), false},
		{"zero heartbeat is alive (never reported, out of scope)", time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsAgentLost(AgentLostCandidate{LastHeartbeat: tc.last}, threshold, now)
			if got != tc.want {
				t.Errorf("IsAgentLost(last=%v) = %v, want %v", tc.last, got, tc.want)
			}
		})
	}
}

// fakeHeartbeatStore is the minimal store the reaper needs in unit tests.
type fakeHeartbeatStore struct {
	candidates []AgentLostCandidate
	listErr    error
	failed     []string
	failErr    error
	markNoop   bool // when true, MarkTaskAgentLost reports 0 rows updated (a late terminal report won the race)
}

func (f *fakeHeartbeatStore) ListAgentLostCandidates(context.Context) ([]AgentLostCandidate, error) {
	return f.candidates, f.listErr
}

func (f *fakeHeartbeatStore) MarkTaskAgentLost(_ context.Context, tiID string) (bool, error) {
	if f.failErr != nil {
		return false, f.failErr
	}
	if f.markNoop {
		return false, nil
	}
	f.failed = append(f.failed, tiID)
	return true, nil
}

// TestReapAgentLost_MarksStaleTIs covers the success path: only candidates
// older than the threshold get failed; fresh ones are left alone. This is
// the contract that makes "stuck dag_run because one TI's agent died"
// recoverable without manual intervention.
func TestReapAgentLost_MarksStaleTIs(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{
		{TaskInstanceID: "fresh-ti", LastHeartbeat: now.Add(-1 * time.Second)},
		{TaskInstanceID: "stale-ti", LastHeartbeat: now.Add(-5 * time.Minute)},
	}}
	rec := &capturingRecorder{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	if len(store.failed) != 1 || store.failed[0] != "stale-ti" {
		t.Errorf("failed = %v, want [stale-ti]", store.failed)
	}
	if got := rec.count("agent_lost"); got != 1 {
		t.Errorf("agent_lost decisions = %d, want 1", got)
	}
}

// TestReapAgentLost_NoopWhenAlreadyTransitioned: if MarkTaskAgentLost updates 0
// rows (a late terminal report transitioned the TI between the list and the
// write, WHERE state='running'), the reaper must NOT log a false reap or delete
// the pod for the now-settled TI (audit finding: the rowcount was discarded).
func TestReapAgentLost_NoopWhenAlreadyTransitioned(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeHeartbeatStore{
		candidates: []AgentLostCandidate{
			{TaskInstanceID: "raced", DagRunID: "r", TaskID: "t", TryNumber: 1, LastHeartbeat: now.Add(-5 * time.Minute)},
		},
		markNoop: true,
	}
	rec := &capturingRecorder{}
	pods := &fakePodManager{active: map[string]bool{}}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
	r.pods = pods
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := rec.count("agent_lost"); got != 0 {
		t.Errorf("a no-op mark must not record a reap; agent_lost = %d, want 0", got)
	}
	if got := rec.count("agent_lost_noop"); got != 1 {
		t.Errorf("agent_lost_noop = %d, want 1", got)
	}
	if len(pods.deletedTasks) != 0 {
		t.Errorf("no pod should be deleted for a settled TI, got %v", pods.deletedTasks)
	}
}

// TestReapAgentLost_WritesLogMarker: when a TI is reaped, a final marker is
// appended to that attempt's log stream (#861), at the exact Ref, so a killed
// task's log ends with "agent_lost" instead of a silent truncation.
func TestReapAgentLost_WritesLogMarker(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{
		{TaskInstanceID: "ti1", TenantID: "tnt", DagID: "dag", DagRunID: "run", TaskID: "task", TryNumber: 2, LastHeartbeat: now.Add(-5 * time.Minute)},
	}}
	sink := &fakeLogSink{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
	r.sink = sink

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	evs := sink.events[refKey(logs.Ref{TenantID: "tnt", DagID: "dag", RunID: "run", TaskID: "task", TryNumber: 2})]
	if len(evs) != 1 {
		t.Fatalf("want exactly 1 marker at the reaped attempt's Ref, got %d (%v)", len(evs), sink.events)
	}
	if !strings.Contains(evs[0].Message, "agent_lost") {
		t.Errorf("marker msg = %q, want it to mention agent_lost", evs[0].Message)
	}
}

// TestReapAgentLost_NoMarkerOnNoop: a raced no-op mark must not write a log
// marker (the TI was settled by a late terminal report; its log is not ours).
func TestReapAgentLost_NoMarkerOnNoop(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeHeartbeatStore{
		candidates: []AgentLostCandidate{
			{TaskInstanceID: "raced", TenantID: "tnt", DagID: "dag", DagRunID: "run", TaskID: "task", TryNumber: 1, LastHeartbeat: now.Add(-5 * time.Minute)},
		},
		markNoop: true,
	}
	sink := &fakeLogSink{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
	r.sink = sink
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(sink.events) != 0 {
		t.Errorf("no marker should be written for a settled TI, got %v", sink.events)
	}
}

// TestReapAgentLost_MarkerErrorDoesNotBlockReap: a marker append failure is
// best-effort — it records a metric but must NOT stop the TI being failed or its
// pod being torn down. The log marker is a diagnostic nicety, never a gate.
func TestReapAgentLost_MarkerErrorDoesNotBlockReap(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{
		{TaskInstanceID: "ti1", TenantID: "tnt", DagID: "dag", DagRunID: "run", TaskID: "task", TryNumber: 1, LastHeartbeat: now.Add(-5 * time.Minute)},
	}}
	rec := &capturingRecorder{}
	pods := &fakePodManager{active: map[string]bool{}}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
	r.pods = pods
	r.sink = &fakeLogSink{appendErr: errors.New("sink down")}

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 1 {
		t.Errorf("TI must still be failed despite a marker error, failed=%v", store.failed)
	}
	if len(pods.deletedTasks) != 1 {
		t.Errorf("pod must still be deleted despite a marker error, deleted=%v", pods.deletedTasks)
	}
	if got := rec.count("agent_lost_log_marker_error"); got != 1 {
		t.Errorf("agent_lost_log_marker_error = %d, want 1", got)
	}
	if got := rec.count("agent_lost"); got != 1 {
		t.Errorf("agent_lost = %d, want 1 (reap still counts)", got)
	}
}

// TestReapAgentLost_ListErrorSurfaces: a list failure is returned so the
// caller can log it (the scheduler's reapHeartbeatLossesIfLeader treats this
// as a "next tick will try again" condition). It must NEVER panic.
func TestReapAgentLost_ListErrorSurfaces(t *testing.T) {
	r := newAgentLostReaper(&fakeHeartbeatStore{listErr: errors.New("db down")},
		reapTestLogger(), 90*time.Second, nil)
	if err := r.run(context.Background()); err == nil {
		t.Error("expected list error to be returned")
	}
}

// TestReapAgentLost_PerTIErrorIsolated: a failure on one TI does not stall
// the rest of the candidate set — same isolation pattern as the run reaper
// and advanceSafely.
func TestReapAgentLost_PerTIErrorIsolated(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeHeartbeatStore{
		candidates: []AgentLostCandidate{
			{TaskInstanceID: "a", LastHeartbeat: now.Add(-5 * time.Minute)},
			{TaskInstanceID: "b", LastHeartbeat: now.Add(-5 * time.Minute)},
		},
		failErr: errors.New("write failed"),
	}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
	if err := r.run(context.Background()); err != nil {
		t.Errorf("run err = %v, want nil (per-TI errors isolated)", err)
	}
}

// panicHeartbeatStore is the resilience pin — any panic in the store layer
// must be recovered. The scheduler must never die.
type panicHeartbeatStore struct {
	panicOnList bool
	panicOnFail bool
}

func (p *panicHeartbeatStore) ListAgentLostCandidates(context.Context) ([]AgentLostCandidate, error) {
	if p.panicOnList {
		panic("boom: ListAgentLostCandidates")
	}
	return []AgentLostCandidate{{TaskInstanceID: "doomed", LastHeartbeat: time.Now().Add(-1 * time.Hour)}}, nil
}
func (p *panicHeartbeatStore) MarkTaskAgentLost(context.Context, string) (bool, error) {
	if p.panicOnFail {
		panic("boom: MarkTaskAgentLost")
	}
	return true, nil
}

func TestReapAgentLost_PanicInListDoesNotCrash(t *testing.T) {
	r := newAgentLostReaper(&panicHeartbeatStore{panicOnList: true}, reapTestLogger(), 90*time.Second, nil)
	if err := r.run(context.Background()); err != nil {
		t.Errorf("panic in list must be recovered; got error %v", err)
	}
}

func TestReapAgentLost_PanicInMarkDoesNotCrash(t *testing.T) {
	r := newAgentLostReaper(&panicHeartbeatStore{panicOnFail: true}, reapTestLogger(), 90*time.Second, nil)
	if err := r.run(context.Background()); err != nil {
		t.Errorf("panic in MarkTaskAgentLost must be recovered; got error %v", err)
	}
}
