package executor

import (
	"context"
	"time"
)

// The mass-silence guard of the agent-lost reaper (#1624).
//
// The reaper reads last_heartbeat_at and fails an attempt whose agent has been
// silent past the threshold. That reading is only as good as the heartbeat
// writes behind it: when the control plane cannot write heartbeats (the pool
// the agent RPCs use is exhausted, the database is slow for that path only),
// every running attempt looks silent at once although every agent is alive.
// A real agent loss is local: a pod, a node, a few attempts. So when at least
// massSilenceMinStale attempts and at least half of the attempts that
// heartbeat are silent in the same window, the reaper holds agent-lost reaping
// and meters agent_lost_mass_silence_hold, which the chart alerts on.
//
// The hold is bounded like the settling hold: after massSilenceValve of
// continuous mass silence the reaper reaps anyway (the agents may really be
// gone, a lost node pool) and meters agent_lost_mass_silence_valve_open. The
// guard fails open: a store that cannot count the fleet reaps as before.
const (
	massSilenceMinStale = 10
	massSilenceValve    = 10 * time.Minute
)

// HeartbeatFleet counts the running attempts that have heartbeated at least
// once (Total) and, of those, the ones whose last heartbeat is older than the
// cut-off the reaper asked about (Stale).
type HeartbeatFleet struct {
	Total int
	Stale int
}

// HeartbeatFleetCounter is the optional store seam of the mass-silence guard.
// A HeartbeatReapStore that also implements it gets the guard; one that does
// not keeps the reaper's behavior from before the guard.
type HeartbeatFleetCounter interface {
	// CountHeartbeatFleet counts the running attempts with a heartbeat, and
	// those whose last heartbeat is before staleBefore.
	CountHeartbeatFleet(ctx context.Context, staleBefore time.Time) (HeartbeatFleet, error)
}

// massSilence reports whether a fleet count looks like failed heartbeat
// writes rather than lost agents.
func massSilence(f HeartbeatFleet) bool {
	return f.Stale >= massSilenceMinStale && 2*f.Stale >= f.Total
}

// massSilenceHold reports whether this pass must skip reaping silent agents.
// It counts the fleet only when the candidate list holds a silent attempt, so
// a healthy tick pays no extra query.
func (r *agentLostReaper) massSilenceHold(ctx context.Context, candidates []AgentLostCandidate, now time.Time) bool {
	counter, ok := r.store.(HeartbeatFleetCounter)
	if !ok || !anyAgentLost(candidates, r.threshold, now) {
		r.silentSince = time.Time{}
		return false
	}
	fleet, err := counter.CountHeartbeatFleet(ctx, now.Add(-r.threshold))
	if err != nil {
		r.logger.Warn("agent-lost reaper could not count the heartbeat fleet; reaping without the mass-silence guard", "error", err)
		r.record("agent_lost_fleet_count_error")
		return false
	}
	if !massSilence(fleet) {
		r.silentSince = time.Time{}
		return false
	}
	if r.silentSince.IsZero() {
		r.silentSince = now
	}
	elapsed := now.Sub(r.silentSince)
	if elapsed >= massSilenceValve {
		r.record("agent_lost_mass_silence_valve_open")
		r.logger.Warn("agent-lost mass-silence valve open: the fleet stayed silent, reaping anyway",
			"silent", fleet.Stale, "heartbeating", fleet.Total, "since", elapsed, "valve_after", massSilenceValve)
		return false
	}
	r.record("agent_lost_mass_silence_hold")
	r.logger.Warn("agent-lost reaping held: most running attempts went silent at once, which points at heartbeat writes failing on the control plane, not at lost agents",
		"silent", fleet.Stale, "heartbeating", fleet.Total, "since", elapsed, "valve_after", massSilenceValve)
	return true
}

func anyAgentLost(candidates []AgentLostCandidate, threshold time.Duration, now time.Time) bool {
	for _, c := range candidates {
		if IsAgentLost(c, threshold, now) {
			return true
		}
	}
	return false
}
