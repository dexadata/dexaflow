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
// Once holding, it keeps holding until the silence is clearly over (fewer than
// massSilenceExitMinStale silent attempts, or under a quarter of the fleet):
// writes that half fail make the silent share swing around one half from tick
// to tick, and a single threshold would reap a batch of healthy attempts on
// every dip.
//
// The hold is bounded like the settling hold: after massSilenceValve of
// continuous mass silence the reaper reaps anyway (the agents may really be
// gone, a lost node pool) and meters agent_lost_mass_silence_valve_open. The
// valve clock restarts when this reaper has not looked for massSilenceGap (a
// pass skipped while not leading or settling), so an old hold is never
// carried into a new term. When the fleet cannot be counted while the list
// already shows a mass of silent attempts, the database is degraded: the
// guard holds, metered as agent_lost_fleet_count_error, under the same valve.
//
// An attempt that went silent after outliving the credential ceiling
// (OutlivedCredentialCeiling) is settled even under a hold: its silence is
// the ceiling working, and a batch started together reaches it together.
// Such attempts are left out of the page floor that decides whether to count;
// the fleet count itself still includes them, so a ceiling batch can only
// widen a hold that ten other silent attempts already justify.
//
// A pass whose candidate list fails to load does not refresh silentSeen, so a
// list outage longer than massSilenceGap restarts the valve clock. Nothing is
// reaped while the list fails, so that only stretches a hold on a degraded
// database.
const (
	massSilenceMinStale     = 10
	massSilenceExitMinStale = 5
	massSilenceValve        = 10 * time.Minute
	massSilenceGap          = 2 * time.Minute
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
// writes rather than lost agents: the bar to enter a hold.
func massSilence(f HeartbeatFleet) bool {
	return f.Stale >= massSilenceMinStale && 2*f.Stale >= f.Total
}

// massSilenceLasts reports whether a hold already in place goes on: the lower
// bar to leave it.
func massSilenceLasts(f HeartbeatFleet) bool {
	return f.Stale >= massSilenceExitMinStale && 4*f.Stale >= f.Total
}

// resetSilence forgets a hold in progress.
func (r *agentLostReaper) resetSilence() {
	r.silentSince = time.Time{}
	r.silentSeen = time.Time{}
}

// massSilenceHold reports whether this pass must skip reaping silent agents.
// The candidate list is ordered by last heartbeat, so its silent attempts are
// the first min(silent, page) rows: the fleet is counted only when the list
// alone already shows enough of them for the guard to matter, and a tick with
// a few real losses pays no extra query.
func (r *agentLostReaper) massSilenceHold(ctx context.Context, candidates []AgentLostCandidate, now time.Time) bool {
	counter, ok := r.store.(HeartbeatFleetCounter)
	if !ok {
		return false
	}
	holding := !r.silentSince.IsZero()
	if holding && now.Sub(r.silentSeen) > massSilenceGap {
		r.resetSilence()
		holding = false
	}
	floor := massSilenceMinStale
	if holding {
		floor = massSilenceExitMinStale
	}
	if r.silentCandidates(candidates, now) < floor {
		r.resetSilence()
		return false
	}
	fleet, err := counter.CountHeartbeatFleet(ctx, now.Add(-r.threshold))
	var silent bool
	switch {
	case err != nil:
		r.logger.Warn("agent-lost reaper could not count the heartbeat fleet while many attempts look silent; holding", "error", err)
		r.record("agent_lost_fleet_count_error")
		silent = true
	case holding:
		silent = massSilenceLasts(fleet)
	default:
		silent = massSilence(fleet)
	}
	if !silent {
		r.resetSilence()
		return false
	}
	if r.silentSince.IsZero() {
		r.silentSince = now
	}
	r.silentSeen = now
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

// silentCandidates counts the candidates past the threshold that did not
// outlive the credential ceiling.
func (r *agentLostReaper) silentCandidates(candidates []AgentLostCandidate, now time.Time) int {
	n := 0
	for _, c := range candidates {
		if IsAgentLost(c, r.threshold, now) && !OutlivedCredentialCeiling(c, r.ceiling) {
			n++
		}
	}
	return n
}
