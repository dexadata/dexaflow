package executor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// fleetHeartbeatStore is fakeHeartbeatStore plus the fleet count the
// mass-silence guard reads (#1624).
type fleetHeartbeatStore struct {
	fakeHeartbeatStore
	fleet    HeartbeatFleet
	countErr error
	counted  int
	// staleBefore records the cut-off the reaper asked about.
	staleBefore time.Time
}

func (f *fleetHeartbeatStore) CountHeartbeatFleet(_ context.Context, staleBefore time.Time) (HeartbeatFleet, error) {
	f.counted++
	f.staleBefore = staleBefore
	return f.fleet, f.countErr
}

// staleCandidates builds n running attempts whose last heartbeat is well past
// the 90 s threshold.
func staleCandidates(n int, now time.Time) []AgentLostCandidate {
	out := make([]AgentLostCandidate, 0, n)
	for i := range n {
		out = append(out, AgentLostCandidate{
			TaskInstanceID: fmt.Sprintf("ti-%d", i), DagRunID: "run", TaskID: fmt.Sprintf("t%d", i),
			TryNumber: 1, LastHeartbeat: now.Add(-5 * time.Minute),
		})
	}
	return out
}

// TestAgentLostMassSilenceHoldsReaping is the regression test of #1624: when
// most of the fleet went silent in the same window (the heartbeat writes are
// what failed, not the agents), the reaper reaps none of them, meters the hold
// and asks the store about the threshold's cut-off.
func TestAgentLostMassSilenceHoldsReaping(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	store := &fleetHeartbeatStore{fleet: HeartbeatFleet{Total: 40, Stale: 30}}
	store.candidates = staleCandidates(30, now)
	rec := &capturingRecorder{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
	r.clock = func() time.Time { return now }

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 0 {
		t.Fatalf("a mass silence must hold agent-lost reaping, reaped %d", len(store.failed))
	}
	if got := rec.count("agent_lost_mass_silence_hold"); got != 1 {
		t.Errorf("agent_lost_mass_silence_hold = %d, want 1", got)
	}
	if want := now.Add(-90 * time.Second); !store.staleBefore.Equal(want) {
		t.Errorf("staleBefore = %v, want %v (now less the threshold)", store.staleBefore, want)
	}
}

// TestAgentLostMassSilenceBelowBoundsReaps: the guard only holds when both the
// count and the share of silent attempts are past their floors, so an ordinary
// agent loss (a node or a few pods) is reaped as before.
func TestAgentLostMassSilenceBelowBoundsReaps(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		fleet HeartbeatFleet
	}{
		{"few silent attempts in a small fleet", HeartbeatFleet{Total: massSilenceMinStale - 1, Stale: massSilenceMinStale - 1}},
		{"many silent attempts but under half the fleet", HeartbeatFleet{Total: 100, Stale: 49}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fleetHeartbeatStore{fleet: tc.fleet}
			store.candidates = staleCandidates(3, now)
			rec := &capturingRecorder{}
			r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
			r.clock = func() time.Time { return now }
			if err := r.run(context.Background()); err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(store.failed) != 3 {
				t.Errorf("reaped %d, want 3", len(store.failed))
			}
			if got := rec.count("agent_lost_mass_silence_hold"); got != 0 {
				t.Errorf("agent_lost_mass_silence_hold = %d, want 0", got)
			}
		})
	}
}

// TestAgentLostMassSilenceAtBoundsHolds pins the inclusive edges: exactly the
// minimum count at exactly half the fleet holds.
func TestAgentLostMassSilenceAtBoundsHolds(t *testing.T) {
	if !massSilence(HeartbeatFleet{Total: 2 * massSilenceMinStale, Stale: massSilenceMinStale}) {
		t.Error("the minimum count at half the fleet must hold")
	}
	if massSilence(HeartbeatFleet{Total: 0, Stale: 0}) {
		t.Error("an empty fleet must not hold")
	}
}

// TestAgentLostMassSilenceValveOpens: a hold is bounded. Once the fleet has
// been silent for massSilenceValve the reaper reaps anyway (the agents may
// really be gone, a lost node pool), and meters that it did.
func TestAgentLostMassSilenceValveOpens(t *testing.T) {
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	now := start
	store := &fleetHeartbeatStore{fleet: HeartbeatFleet{Total: 20, Stale: 20}}
	store.candidates = staleCandidates(20, start)
	rec := &capturingRecorder{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
	r.clock = func() time.Time { return now }

	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	now = start.Add(massSilenceValve - time.Second)
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 0 {
		t.Fatalf("held before the valve, reaped %d", len(store.failed))
	}
	now = start.Add(massSilenceValve)
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 20 {
		t.Errorf("valve open: reaped %d, want 20", len(store.failed))
	}
	if got := rec.count("agent_lost_mass_silence_valve_open"); got != 1 {
		t.Errorf("agent_lost_mass_silence_valve_open = %d, want 1", got)
	}
}

// TestAgentLostMassSilenceResetsWhenFleetRecovers: the valve clock restarts
// when the fleet heartbeats again, so a later, separate outage gets a full
// hold instead of inheriting the first one's elapsed time.
func TestAgentLostMassSilenceResetsWhenFleetRecovers(t *testing.T) {
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	now := start
	store := &fleetHeartbeatStore{fleet: HeartbeatFleet{Total: 20, Stale: 20}}
	rec := &capturingRecorder{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
	r.clock = func() time.Time { return now }
	_ = r.run(context.Background())

	now = start.Add(massSilenceValve / 2)
	store.fleet = HeartbeatFleet{Total: 20, Stale: 0}
	_ = r.run(context.Background())

	now = start.Add(massSilenceValve)
	store.fleet = HeartbeatFleet{Total: 20, Stale: 20}
	store.candidates = staleCandidates(20, now)
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 0 {
		t.Errorf("a new outage must get a full hold, reaped %d", len(store.failed))
	}
	if got := rec.count("agent_lost_mass_silence_valve_open"); got != 0 {
		t.Errorf("valve opened on a fresh outage: %d", got)
	}
}

// TestAgentLostMassSilenceCountErrorReaps: the guard fails open. A store that
// cannot count the fleet keeps the reaper's behavior from before the guard,
// and the error is metered.
func TestAgentLostMassSilenceCountErrorReaps(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	store := &fleetHeartbeatStore{countErr: errors.New("pool closed")}
	store.candidates = staleCandidates(2, now)
	rec := &capturingRecorder{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
	r.clock = func() time.Time { return now }
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 2 {
		t.Errorf("reaped %d, want 2", len(store.failed))
	}
	if got := rec.count("agent_lost_fleet_count_error"); got != 1 {
		t.Errorf("agent_lost_fleet_count_error = %d, want 1", got)
	}
}

// TestAgentLostMassSilenceSkipsCountWithoutStaleCandidates: the fleet count is
// one more query per tick, so it only runs when the candidate list holds a
// silent attempt.
func TestAgentLostMassSilenceSkipsCountWithoutStaleCandidates(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	store := &fleetHeartbeatStore{}
	store.candidates = []AgentLostCandidate{{TaskInstanceID: "fresh", LastHeartbeat: now.Add(-time.Second)}}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
	r.clock = func() time.Time { return now }
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if store.counted != 0 {
		t.Errorf("counted the fleet %d times with no silent candidate", store.counted)
	}
}
