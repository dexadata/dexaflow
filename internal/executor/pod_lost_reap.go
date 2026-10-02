package executor

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// PodLostCandidate is one task instance in `running` whose backing pod may have
// vanished — deleted, evicted, OOM-killed, or lost with its node — before any
// other reaper could catch it. RunningSince is when the TI entered `running`;
// the reaper only checks pod liveness once the TI has been running past a grace
// period, so a just-dispatched TI whose pod is still materializing is never
// reaped on a transient "no pod yet".
type PodLostCandidate struct {
	TaskInstanceID string
	DagRunID       string
	DagID          string
	TaskID         string
	// TryNumber pins the best-effort pod delete to exactly this attempt, so a
	// retry's newer pod is never touched (same invariant as the #474 teardown).
	TryNumber    int
	RunningSince time.Time
}

// IsPodLostCandidate reports whether a running TI has been running long enough
// to warrant a pod-liveness check. A zero RunningSince is treated as alive (too
// poorly observed to reap — the "do no harm" rule of ADR 0031), and a future
// RunningSince (clock skew) is treated as alive. This gate is purely about
// elapsed time; the actual lost-vs-alive decision is the pod-liveness check.
func IsPodLostCandidate(c PodLostCandidate, grace time.Duration, now time.Time) bool {
	if c.RunningSince.IsZero() {
		return false
	}
	return now.Sub(c.RunningSince) >= grace
}

// PodLostReapStore is the slice of scheduler.Store the pod-lost reaper needs.
// The full scheduler.Store embeds this interface so production wires through one
// type; unit tests fake just this surface.
type PodLostReapStore interface {
	// ListRunningTasks returns the `running` TIs that have been running for at
	// least grace, with the timestamp each entered running. Warm-pool attempts
	// are excluded: they have no per-task pod, so a presence check would always
	// read them as lost, and the warm-worker-lost reaper owns them. The grace
	// filter is applied before the store's per-tick limit, so attempts still
	// inside it never take the slots of those the reaper can act on.
	ListRunningTasks(ctx context.Context, grace time.Duration) ([]PodLostCandidate, error)
	// MarkTaskPodLost transitions one TI to `failed` with
	// error_message='pod_lost'. The WHERE state='running' guard makes this
	// idempotent. It returns whether a row was actually updated: false means a
	// late terminal report transitioned the TI between the list and this write,
	// so the caller must NOT treat it as reaped (no false log, no pod delete).
	MarkTaskPodLost(ctx context.Context, taskInstanceID string) (bool, error)
}

// podLostReaper fails `running` TIs whose pod has vanished — the gap between the
// agent-lost reaper (which skips a TI that never heartbeated, the ADR 0031
// zero-guard) and the reconciler (which only sees pods that still exist), so a
// pod killed before its first heartbeat used to sit `running` until the 5-minute
// orphan reaper failed the whole run (#527). Invoked once per scheduler tick,
// leader-only. Mirrors the other reapers' resilience: panic-safe, per-candidate
// isolated, metered.
//
// It is Kubernetes-ONLY. With no PodManager (Lite/subprocess) there is no pod to
// lose and a subprocess task legitimately has none, so the reaper is a no-op —
// it must never reap live subprocess work on a "no pod" signal.
//
// Warm-pool attempts are out of this reaper's scope: a warm attempt has no
// per-task pod, so it never appears live here; the warm-worker-lost reaper
// recovers those from the warm pod's own liveness. That signal is one a
// control-plane restart cannot manufacture — ListWarmPods is a live apiserver
// LIST (not an informer-backed cache that could be cold or stale after a
// restart), and a LIST error aborts its tick with zero marks, so a fresh leader
// either sees the real warm fleet or marks nothing — yet the reaper is held by
// the same leader-settling gate as every other one: the delay is harmless and
// one uniform gate is the point.
type podLostReaper struct {
	store    PodLostReapStore
	logger   *slog.Logger
	grace    time.Duration
	recorder DecisionRecorder
	// pods is required for this reaper to do anything; nil (Lite) makes run a
	// no-op. See the type doc.
	pods PodManager
	// cache is an optional informer-backed presence cache (PR-10) consulted ONLY
	// to DEFER a reap: a cached Pending/Running pod skips the live LIST. A cache
	// miss is never authoritative — the reaper falls through to the live
	// TaskPodPresence read below, so cache lag can only delay a reap, never cause a
	// false-positive one (#461). Nil keeps every candidate on the live path.
	cache PodPresenceCache
	// gate is re-checked before every destructive call (see destructiveGate).
	gate destructiveGate
}

func newPodLostReaper(store PodLostReapStore, logger *slog.Logger, grace time.Duration, rec DecisionRecorder) *podLostReaper {
	return &podLostReaper{store: store, logger: logger, grace: grace, recorder: rec}
}

// run lists every running TI, checks pod presence for the ones past the grace
// period, and fails as pod_lost only those whose attempt has no pod at all. A
// pod that is present — live or finished — is somebody else's to resolve.
// Per-TI failures are isolated; a panic anywhere is recovered so the scheduler
// tick stays alive.
func (r *podLostReaper) run(ctx context.Context) error {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("pod-lost reaper panic recovered", "panic", rec, "stack", string(debug.Stack()))
			r.record("pod_lost_panic")
		}
	}()
	// K8s-only: without pods there is nothing to lose, and reaping a Lite
	// subprocess task on "no pod" would kill live work. Skip entirely.
	if r.pods == nil {
		return nil
	}
	now := time.Now().UTC()
	// A control-plane restart makes a task pod that finished during the outage
	// look lost: its container exited, yet its TI is still `running` because the
	// terminal report found no server. Only the reconciler's sweep can recover
	// that pod's durable outcome; Reaper.settling holds the whole tick until one
	// has completed under this leadership.
	//
	// That gate is a timing argument, so it cannot be the only defense: it holds
	// on facts about the leader, and its liveness valve deliberately opens after
	// 2 × grace when those facts never arrive. The reap authorization below is
	// therefore narrowed to a state no timing can fake — no pod object for the
	// attempt at all. A finished pod is still a present pod, and stays the
	// reconciler's, valve open or shut.
	candidates, err := r.store.ListRunningTasks(ctx, r.grace)
	if err != nil {
		return err
	}
	for _, c := range candidates {
		if !IsPodLostCandidate(c, r.grace, now) {
			continue
		}
		// Cache fast-path (PR-10), safe direction only: a cached Pending/Running
		// pod defers the reap without an apiserver read. A cache MISS is NOT
		// trusted — fall through to the live read below, preserving the #461 fix.
		if r.cache != nil && r.cache.CachedPodActive(c.DagRunID, c.TaskID, c.TryNumber) {
			r.record("pod_lost_cache_active")
			continue
		}
		// Consult the pod before failing:
		//   * query failed        -> liveness unknown; DEFER ("do no harm"). The
		//                            live read is the only authorization there is,
		//                            so an unreachable or forbidden apiserver
		//                            fails this reaper closed (see Reaper.settling).
		//   * pod Pending/Running -> a pod exists; not lost. Silence, if any, is
		//                            the agent-lost reaper's job.
		//   * pod present but done -> the attempt's outcome is on that pod object;
		//                            settling it is the reconciler's job. DEFER.
		//   * no pod at all       -> the pod is genuinely gone; fail as pod_lost.
		presence, perr := r.pods.TaskPodPresence(ctx, c.DagRunID, c.TaskID, c.TryNumber)
		if perr != nil {
			r.logger.Warn("pod-lost: pod liveness unknown; deferring",
				"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "error", perr)
			r.record("pod_lost_pod_query_error")
			continue
		}
		switch presence {
		case PodPresenceLive:
			continue
		case PodPresenceTerminal:
			// A terminal-but-present pod is NOT a lost pod, and treating it as one
			// is destructive twice over: the mark makes a finished attempt read as
			// pod_lost, and the teardown then deletes the pod whose termination log
			// is the durable evidence of what the attempt actually did. Only the
			// reconciler may settle it. Deferring costs at most one reconcile
			// interval; reaping costs the outcome permanently.
			r.logger.Info("pod-lost: pod is present in a terminal phase; leaving it for the reconciler",
				"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID, "try", c.TryNumber)
			r.record("pod_lost_terminal_pod_defer")
			continue
		case PodPresenceAbsent:
			r.reapOne(ctx, c)
		}
	}
	return nil
}

// reapOne fails one TI whose pod is gone and sweeps any pod that appears for the
// attempt after all, re-checking the destructive gate immediately before each
// write. It must only ever be called for PodPresenceAbsent.
func (r *podLostReaper) reapOne(ctx context.Context, c PodLostCandidate) {
	if !gateOpen(r.gate, ctx) {
		r.record("pod_lost_gate_skip")
		return
	}
	applied, ferr := r.store.MarkTaskPodLost(ctx, c.TaskInstanceID)
	if ferr != nil {
		r.logger.Error("marking task pod-lost",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID, "error", ferr)
		r.record("pod_lost_error")
		return
	}
	if !applied {
		// A late terminal report transitioned the TI between our list and our
		// write (WHERE state='running' matched 0 rows) — it settled on its own,
		// so do not log a false reap or run the teardown.
		r.record("pod_lost_noop")
		return
	}
	r.logger.Warn("running task has no live pod past grace; failing as pod_lost",
		"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID, "running_since", c.RunningSince)
	r.record("pod_lost")
	// Best-effort teardown pinned to (run, task, try). The presence read said
	// there is no pod for this attempt at all, so this normally deletes nothing
	// and exists to catch a pod that materialized between the read and here; a
	// retry's newer pod has a different try-number label and is never touched.
	if !gateOpen(r.gate, ctx) {
		r.record("pod_lost_teardown_gate_skip")
		return
	}
	if derr := r.pods.DeleteTaskPod(ctx, c.DagRunID, c.TaskID, c.TryNumber); derr != nil {
		r.logger.Error("deleting pod-lost task pod",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "try", c.TryNumber, "error", derr)
		r.record("pod_lost_pod_delete_error")
	}
}

func (r *podLostReaper) record(decision string) {
	if r.recorder != nil {
		r.recorder.RecordSchedulerDecision(decision)
	}
}
