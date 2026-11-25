package agent

import "time"

// DefaultHeartbeatInterval is how often the in-pod agent pings the control plane
// while a task runs. Token renewal rides this signal (ADR 0055 Fix #4): every
// live heartbeat refreshes the bearer, so the per-attempt token TTL is derived
// from this interval rather than set to a flat day.
const DefaultHeartbeatInterval = 15 * time.Second

// attemptTokenTTLFloor is the minimum per-attempt token lifetime. It must
// comfortably exceed the heartbeat interval so a single missed beat (a slow tick,
// a brief partition) does not lapse a live task's credential before the next
// beat renews it, while still bounding a stolen or finished token to minutes
// rather than a day. Verified against the ADR's ~10 min projected-token floor.
const attemptTokenTTLFloor = 10 * time.Minute

// attemptTokenTTLBeats is the missed-beat tolerance: the token stays valid for
// this many heartbeat intervals even before the floor applies, so several
// consecutive missed beats are survivable.
const attemptTokenTTLBeats = 4

// AttemptTokenTTL derives the short per-attempt agent-token TTL from the
// heartbeat interval: max(floor, beats × interval). The result always exceeds a
// single interval, so one missed beat never lapses a live credential, while the
// floor keeps the TTL short enough to bound an exfiltrated token. A non-positive
// interval (heartbeats disabled) yields the floor.
func AttemptTokenTTL(interval time.Duration) time.Duration {
	ttl := attemptTokenTTLFloor
	if scaled := time.Duration(attemptTokenTTLBeats) * interval; scaled > ttl {
		ttl = scaled
	}
	return ttl
}

// DefaultMaxFailedHeartbeats is how many heartbeats in a row may go unanswered
// before the agent stops its task (Runner.MaxFailedHeartbeats, #2430): as many
// as span the attempt token TTL. By then the bearer has lapsed unrenewed, so
// the attempt can never report again, and the agent-lost reaper failed it long
// before. A control plane outage shorter than that, a restart or a failover
// that the reapers' settling grace forgives, stops nothing.
var DefaultMaxFailedHeartbeats = int((AttemptTokenTTL(DefaultHeartbeatInterval) + DefaultHeartbeatInterval - 1) / DefaultHeartbeatInterval)
