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
		{"many silent attempts but under half the fleet, list full", HeartbeatFleet{Total: 1000, Stale: 400}},
		{"many silent attempts but under half the fleet", HeartbeatFleet{Total: 100, Stale: 49}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fleetHeartbeatStore{fleet: tc.fleet}
			store.candidates = staleCandidates(20, now)
			rec := &capturingRecorder{}
			r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
			r.clock = func() time.Time { return now }
			if err := r.run(context.Background()); err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(store.failed) != 20 {
				t.Errorf("reaped %d, want 20", len(store.failed))
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
	// One pass a minute, as the leader's reaper runs, up to the valve.
	for now = start.Add(time.Minute); now.Before(start.Add(massSilenceValve)); now = now.Add(time.Minute) {
		if err := r.run(context.Background()); err != nil {
			t.Fatalf("run: %v", err)
		}
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
// when the silence clearly ends, so a later, separate outage gets a full hold
// instead of inheriting the first one's elapsed time.
func TestAgentLostMassSilenceResetsWhenFleetRecovers(t *testing.T) {
	start := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	now := start
	store := &fleetHeartbeatStore{fleet: HeartbeatFleet{Total: 20, Stale: 20}}
	store.candidates = staleCandidates(20, start)
	rec := &capturingRecorder{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
	r.clock = func() time.Time { return now }
	_ = r.run(context.Background())

	now = start.Add(massSilenceValve / 2)
	store.candidates = nil
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

// TestAgentLostMassSilenceHysteresis: once holding, a dip of the silent share
// just under one half does not reap; the hold ends only under a quarter.
func TestAgentLostMassSilenceHysteresis(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	store := &fleetHeartbeatStore{fleet: HeartbeatFleet{Total: 400, Stale: 212}}
	store.candidates = staleCandidates(100, now)
	rec := &capturingRecorder{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
	r.clock = func() time.Time { return now }
	_ = r.run(context.Background())

	now = now.Add(time.Second)
	store.fleet = HeartbeatFleet{Total: 400, Stale: 192}
	_ = r.run(context.Background())
	if len(store.failed) != 0 {
		t.Fatalf("a dip to 48%% while holding reaped %d", len(store.failed))
	}
	now = now.Add(time.Second)
	store.fleet = HeartbeatFleet{Total: 400, Stale: 99}
	_ = r.run(context.Background())
	if len(store.failed) == 0 {
		t.Error("under a quarter the hold must end and the silent attempts be reaped")
	}
}

// TestAgentLostMassSilenceGapRestartsValve is the regression test of the
// review of #1624: a hold seen in an earlier term must not count toward the
// valve of a new outage, or the valve opens on its first pass.
func TestAgentLostMassSilenceGapRestartsValve(t *testing.T) {
	start := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	now := start
	store := &fleetHeartbeatStore{fleet: HeartbeatFleet{Total: 20, Stale: 20}}
	store.candidates = staleCandidates(20, start)
	rec := &capturingRecorder{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
	r.clock = func() time.Time { return now }
	_ = r.run(context.Background())

	// No pass for an hour (another replica led), then a new mass silence.
	now = start.Add(time.Hour)
	store.candidates = staleCandidates(20, now)
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 0 {
		t.Errorf("the valve of an old hold opened on a new outage: reaped %d", len(store.failed))
	}
	if got := rec.count("agent_lost_mass_silence_valve_open"); got != 0 {
		t.Errorf("agent_lost_mass_silence_valve_open = %d, want 0", got)
	}
}

// TestAgentLostMassSilenceCountErrorHolds: when the list already shows a mass
// of silent attempts and the fleet cannot be counted, the database is
// degraded; the guard holds and meters the error.
func TestAgentLostMassSilenceCountErrorHolds(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	store := &fleetHeartbeatStore{countErr: errors.New("pool closed")}
	store.candidates = staleCandidates(massSilenceMinStale, now)
	rec := &capturingRecorder{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, rec)
	r.clock = func() time.Time { return now }
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 0 {
		t.Errorf("reaped %d on a count error, want a hold", len(store.failed))
	}
	if got := rec.count("agent_lost_fleet_count_error"); got != 1 {
		t.Errorf("agent_lost_fleet_count_error = %d, want 1", got)
	}
}

// TestAgentLostMassSilenceSkipsCountForAFewLosses: the fleet count is one more
// query, so it only runs when the list alone holds enough silent attempts
// for the guard to matter; a few real losses are reaped without it.
func TestAgentLostMassSilenceSkipsCountForAFewLosses(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	store := &fleetHeartbeatStore{fleet: HeartbeatFleet{Total: 10, Stale: 10}}
	store.candidates = staleCandidates(massSilenceMinStale-1, now)
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
	r.clock = func() time.Time { return now }
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if store.counted != 0 {
		t.Errorf("counted the fleet %d times for %d silent candidates", store.counted, massSilenceMinStale-1)
	}
	if len(store.failed) != massSilenceMinStale-1 {
		t.Errorf("reaped %d, want %d", len(store.failed), massSilenceMinStale-1)
	}
}

// TestAgentLostMassSilenceStillSettlesCeiling: an attempt that went silent
// after outliving the credential ceiling is settled under a hold, as
// credential_ceiling, and does not count toward the silence.
func TestAgentLostMassSilenceStillSettlesCeiling(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	store := &fleetHeartbeatStore{fleet: HeartbeatFleet{Total: 30, Stale: 30}}
	store.candidates = staleCandidates(20, now)
	store.candidates = append(store.candidates, AgentLostCandidate{
		TaskInstanceID: "ceiling", DagRunID: "run", TaskID: "c", TryNumber: 1,
		StartedAt: now.Add(-3 * time.Hour), LastHeartbeat: now.Add(-5 * time.Minute),
	})
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, &capturingRecorder{})
	r.ceiling = time.Hour
	r.clock = func() time.Time { return now }
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.failed) != 0 {
		t.Errorf("held attempts reaped: %v", store.failed)
	}
	if len(store.ceilingPins) != 1 {
		t.Errorf("credential ceiling settles under a hold = %v, want the one attempt", store.ceilingPins)
	}
}
