package executor

import (
	"context"
	"log/slog"
	"runtime/debug"
)

// WarmPodLister is the narrow read-only seam onto the live warm fleet the
// failover paths need: just the warm-pod LIST, no create/delete. Both the
// warm-worker-lost reaper and the dispatch-lost reaper's H3 defer depend on this
// capability rather than the full WarmPodClient, so a unit test fakes only the
// LIST. KubernetesWarmPods (via WarmPodClient) already satisfies it — production
// reuses that one type, it is not duplicated.
type WarmPodLister interface {
	ListWarmPods(ctx context.Context) ([]WarmPodInfo, error)
}

// liveWarmPodNames returns the set of LIVE (non-terminal) warm-pod names, read
// once per tick. A nil lister (warm pools off, or not wired) yields an empty
// set, which makes every warm consumer inert: the failover reaper marks nothing
// and the dispatch-lost warm defer never triggers. A LIST error is logged and
// also yields an empty set — "do no harm" for the reaper reads as "cannot prove
// a worker dead, so mark nothing this tick"; the dispatch-lost path likewise
// falls through to its existing pod-liveness gate. Terminal warm pods (a crashed
// worker that can never serve again) are excluded, so an attempt still bound to a
// terminal pod is correctly treated as lost. Θ(pools).
func liveWarmPodNames(ctx context.Context, lister WarmPodLister, logger *slog.Logger) map[string]bool {
	if lister == nil {
		return map[string]bool{}
	}
	pods, err := lister.ListWarmPods(ctx)
	if err != nil {
		if logger != nil {
			logger.Warn("warm failover: listing warm pods failed; treating live set as empty this tick", "error", err)
		}
		return map[string]bool{}
	}
	live := make(map[string]bool, len(pods))
	for _, p := range pods {
		if p.Terminal {
			continue
		}
		live[p.Name] = true
	}
	return live
}

// WarmBoundTI is one `running` task instance durably bound to a warm worker
// (ADR 0058 N1d-a2): WarmWorkerID is the warm pod that acked and is serving this
// attempt. The failover reaper matches WarmWorkerID against the live warm-pod set
// to find attempts a dead warm pod held.
type WarmBoundTI struct {
	TaskInstanceID string
	DagRunID       string
	TaskID         string
	TryNumber      int
	// AttemptEpoch pins the failover mark to the bound execution (ADR 0051
	// amendment).
	AttemptEpoch int
	WarmWorkerID string
}

// WarmWorkerLostReapStore is the slice of the store the warm-worker-lost reaper
// needs: list the warm-bound running TIs, and reuse the pod-lost mark to route a
// lost attempt to infra (bumps infra_attempts, NOT try_number). The full
// scheduler store satisfies it; a unit test fakes just this surface.
type WarmWorkerLostReapStore interface {
	ListWarmBoundRunningTIs(ctx context.Context) ([]WarmBoundTI, error)
	// MarkTaskPodLost is reused verbatim from the pod-lost reaper: a warm worker
	// vanishing is the same infra failure as a task pod vanishing, so it routes
	// through the same infra path with the same idempotency guard (WHERE
	// state='running'). applied=false means a late settle raced the mark — a
	// benign skip, never a false reap.
	MarkTaskPodLost(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
}

// warmWorkerLostReaper recovers a dead warm worker's in-flight attempts (ADR 0058
// N1d-a2, the D7 failover). A warm worker serves MANY attempts per pod, so when a
// warm pod dies mid-attempt every attempt it was serving is stranded `running`
// with no backing pod — invisible to the pod-lost reaper (a warm attempt has no
// per-task pod) and, until its heartbeat lapses, to the agent-lost reaper. This
// reaper closes that gap: it lists the durably-bound running attempts, reads the
// live warm-pod set once per tick, and for every attempt whose worker is no
// longer live it routes the attempt to infra via MarkTaskPodLost.
//
// Fan-out: a dead worker holding N attempts gets all N marked in the tick,
// because every bound TI names the same (now-absent) worker. The set lookup is
// O(1) per TI, so the reaper is Θ(bound TIs + pools) overall.
//
// Warm-off inertness: with warm pools disabled the lister is nil (empty live
// set) AND no TI ever carries a warm_worker_id (ListWarmBoundRunningTIs returns
// empty), so the reaper is a double no-op — byte-for-byte today. Resilience
// mirrors the sibling reapers: panic-safe, per-TI isolated, metered.
type warmWorkerLostReaper struct {
	store    WarmWorkerLostReapStore
	logger   *slog.Logger
	recorder DecisionRecorder
	// warmPods is the live warm-pod seam. Nil (warm off / not wired) makes run a
	// no-op via the empty live set.
	warmPods WarmPodLister
	// gate is re-checked before every destructive call (see destructiveGate).
	gate destructiveGate
}

func newWarmWorkerLostReaper(store WarmWorkerLostReapStore, logger *slog.Logger, rec DecisionRecorder) *warmWorkerLostReaper {
	return &warmWorkerLostReaper{store: store, logger: logger, recorder: rec}
}

// run lists the warm-bound running TIs and marks pod_lost every one whose
// serving warm worker is no longer live. Per-TI mark failures are isolated; a
// panic anywhere is recovered so the scheduler tick stays alive.
func (r *warmWorkerLostReaper) run(ctx context.Context) error {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("warm-worker-lost reaper panic recovered", "panic", rec, "stack", string(debug.Stack()))
			r.record("warm_worker_lost_panic")
		}
	}()
	// Warm off / not wired: no lister means no warm fleet to consult, so nothing
	// can be proven dead. Skip before touching the store.
	if r.warmPods == nil {
		return nil
	}
	bound, err := r.store.ListWarmBoundRunningTIs(ctx)
	if err != nil {
		return err
	}
	if len(bound) == 0 {
		// No warm attempts in flight — nothing to recover.
		return nil
	}
	// Live warm-pod set, read ONCE per tick (Θ(pools)); the per-TI membership
	// check is then O(1), grouping the fan-out by worker implicitly.
	//
	// A LIST error is NOT an empty fleet: on this authoritative kill path,
	// treating "cannot list" as "everything is dead" would reap every live warm
	// attempt on a transient apiserver blip. So a LIST error aborts the tick with
	// no marks ("do no harm", ADR 0031) — the attempts are picked up next tick
	// once the fleet is readable again. (The dispatch-lost H3 defer differs: an
	// empty set there merely skips an extra defer, never causing a kill, so it
	// tolerates the empty-on-error fallback.)
	pods, perr := r.warmPods.ListWarmPods(ctx)
	if perr != nil {
		r.logger.Warn("warm-worker-lost: listing warm pods failed; deferring all this tick", "error", perr)
		r.record("warm_worker_lost_list_pods_error")
		return nil
	}
	live := make(map[string]bool, len(pods))
	for _, p := range pods {
		if p.Terminal {
			// A terminal warm pod can never serve again; it is not live, so an
			// attempt still bound to it is correctly reaped below.
			continue
		}
		live[p.Name] = true
	}
	for _, ti := range bound {
		if live[ti.WarmWorkerID] {
			// The serving warm worker is still alive; the attempt is healthy.
			continue
		}
		if !gateOpen(r.gate, ctx) {
			r.record("warm_worker_lost_gate_skip")
			continue
		}
		applied, ferr := r.store.MarkTaskPodLost(ctx, ti.TaskInstanceID, ti.TryNumber, ti.AttemptEpoch)
		if ferr != nil {
			r.logger.Error("marking warm-worker-lost task pod-lost",
				"ti", ti.TaskInstanceID, "run", ti.DagRunID, "task", ti.TaskID, "worker", ti.WarmWorkerID, "error", ferr)
			r.record("warm_worker_lost_error")
			continue
		}
		if !applied {
			// A late terminal report settled the TI between our list and our write
			// (WHERE state='running' matched 0 rows) — a benign race, not a reap.
			r.record("warm_worker_lost_noop")
			continue
		}
		r.logger.Warn("warm worker gone; failing its in-flight attempt as pod_lost",
			"ti", ti.TaskInstanceID, "run", ti.DagRunID, "task", ti.TaskID, "worker", ti.WarmWorkerID)
		r.record("warm_worker_lost")
	}
	return nil
}

func (r *warmWorkerLostReaper) record(decision string) {
	if r.recorder != nil {
		r.recorder.RecordSchedulerDecision(decision)
	}
}
