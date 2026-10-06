package scheduler

import "time"

// Dispatch-failure backoff (ADR 0031 Amendment A). A synchronous dispatch
// failure (kube-apiserver unreachable, RBAC denied, quota, admission reject) is
// not retried every tick: the task instance records a backed-off next attempt,
// and after dispatchMaxAttempts the planner fails it as dispatch_failed rather
// than looping forever.
const (
	// dispatchBackoffBase is the delay after the first failed dispatch.
	dispatchBackoffBase = 5 * time.Second
	// dispatchBackoffCap bounds the delay: a permanent misconfiguration is
	// re-attempted no less often than this while it is still within the budget.
	dispatchBackoffCap = 5 * time.Minute
	// dispatchMaxAttempts is how many failed dispatches are tolerated before the
	// task instance is failed as dispatch_failed. With the schedule above this is
	// a few minutes of retrying — long enough to ride out a transient blip, short
	// enough that a permanent failure surfaces rather than looping silently.
	dispatchMaxAttempts = 6
	// infraMaxAttempts bounds the try_number-free re-placement of a task that fails
	// for an infrastructure reason (agent/pod/dispatch lost) — the async analog of
	// dispatchMaxAttempts (ADR 0051 Phase 1). Past this, a task that keeps hitting
	// infra faults fails terminally instead of re-placing forever.
	infraMaxAttempts = 6
)

// dispatchBackoff returns the delay before the next dispatch attempt after
// `attempts` consecutive failures: exponential from the base, capped. It is
// deterministic (no jitter) so the planner's time gate is testable; attempts <= 1
// yields the base.
func dispatchBackoff(attempts int) time.Duration {
	d := dispatchBackoffBase
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= dispatchBackoffCap {
			return dispatchBackoffCap
		}
	}
	return d
}

// InfraConfirmMaxWait is the liveness valve of the infra confirmation gate
// (ADR 0052 amendment, part 2): how long after a provisional infra mark's
// ended_at the planner keeps the task active waiting for the pod reconciler to
// confirm the mark or settle a durable SUCCESS over it. Past it the planner
// acts on the guess as before. It must exceed a stopped task pod's termination
// grace plus two maintenance intervals (the reconciler needs the container to
// exit and then one sweep), and stay below the orphan-run threshold; both
// orderings are rungs of the boot-time resilience ladder. The store reads it
// too, to guard the re-place rail.
const InfraConfirmMaxWait = 2 * time.Minute

// InfraReplaceMaxDelay is the longest the planner may park an infra-failed task
// before re-placing it: the backoff before the last permitted re-place (attempt
// infraMaxAttempts) plus the full de-synchronizing jitter window. It is the
// upper bound on how long a run whose only live task is infra-parked shows no
// activity, so the executor's orphan-run threshold must sit above it or the
// orphan reaper fails a run that is still recovering. The two values live in
// packages that depend in one direction only (the scheduler imports the
// executor), so this is exported for the server to hand to the executor's
// boot-time resilience ladder rather than read from there.
func InfraReplaceMaxDelay() time.Duration {
	return dispatchBackoff(infraMaxAttempts) + infraReplaceJitterWindow
}
