package executor

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/dexadata/dexaflow/internal/logs"
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

// WarmTerminationLookup is the optional seam a WarmPodLister offers to name the
// cause of a worker the LIST no longer returns: the warm-worker container's
// terminated reason it last observed for that pod, or "" (#1835).
// KubernetesWarmPods implements it.
type WarmTerminationLookup interface {
	LastTerminationReason(podName string) string
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
	// TenantID and DagID locate the attempt's log stream for the killed marker
	// (#861).
	TenantID string
	DagID    string
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
	// MarkTaskPodLostOOMKilled is MarkTaskPodLost, the same infra mark with the
	// same guards, for an attempt whose warm worker's container the kubelet
	// reported OOMKilled (#1218): only the failure message differs, naming the
	// OOM kill. The attribution stays infra (ADR 0051, ADR 0058 D7).
	MarkTaskPodLostOOMKilled(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
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
	// sink appends a final "killed: pod_lost" marker to each reaped attempt's
	// log stream (#861). Nil disables the marker; the reap is unaffected.
	sink logSink
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
	fleet := viewWarmFleet(pods)
	for _, ti := range bound {
		if fleet.live[ti.WarmWorkerID] {
			// The serving warm worker is still alive; the attempt is healthy.
			continue
		}
		if !gateOpen(r.gate, ctx) {
			r.record("warm_worker_lost_gate_skip")
			continue
		}
		oom := r.oomKilled(fleet, ti.WarmWorkerID)
		applied, ferr := r.mark(ctx, ti, oom)
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
			"ti", ti.TaskInstanceID, "run", ti.DagRunID, "task", ti.TaskID, "worker", ti.WarmWorkerID, "oom_killed", oom)
		r.record("warm_worker_lost")
		if oom {
			r.record("warm_worker_lost_oomkilled")
		}
		r.writeMarker(ti, oom)
	}
	return nil
}

// warmFleetView is one tick's reading of the listed warm fleet: the live
// workers, every listed one, and the terminal ones whose container the kubelet
// reported OOMKilled.
type warmFleetView struct {
	live, listed, oomKilled map[string]bool
}

// viewWarmFleet classifies the listed warm pods once per tick. A terminal warm
// pod can never serve again, so it is not live and an attempt still bound to
// it is reaped. On cgroup v2 the kubelet sets memory.oom.group, so an OOM in a
// task kills the whole warm-worker container and the warm-worker-lost reaper
// is the one that settles the attempt (#1218).
func viewWarmFleet(pods []WarmPodInfo) warmFleetView {
	v := warmFleetView{
		live:      make(map[string]bool, len(pods)),
		listed:    make(map[string]bool, len(pods)),
		oomKilled: map[string]bool{},
	}
	for _, p := range pods {
		v.listed[p.Name] = true
		switch {
		case !p.Terminal:
			v.live[p.Name] = true
		case p.TerminationReason == "OOMKilled":
			v.oomKilled[p.Name] = true
		}
	}
	return v
}

// oomKilled reports whether the named worker's container was OOMKilled: from
// its listed status while the LIST still returns it, else from the lister's
// remembered terminated reason, since after an OOM the pod is usually deleted
// before this reaper runs (#1835). A lister without that memory, or no
// remembered reason, reports false, which keeps the plain pod_lost text.
func (r *warmWorkerLostReaper) oomKilled(fleet warmFleetView, worker string) bool {
	if fleet.listed[worker] {
		return fleet.oomKilled[worker]
	}
	tl, ok := r.warmPods.(WarmTerminationLookup)
	return ok && tl.LastTerminationReason(worker) == "OOMKilled"
}

// mark fails the attempt as pod_lost, through the OOMKilled variant when its
// worker's container was reported OOMKilled. Both are the same infra mark.
func (r *warmWorkerLostReaper) mark(ctx context.Context, ti WarmBoundTI, oom bool) (bool, error) {
	if oom {
		return r.store.MarkTaskPodLostOOMKilled(ctx, ti.TaskInstanceID, ti.TryNumber, ti.AttemptEpoch)
	}
	return r.store.MarkTaskPodLost(ctx, ti.TaskInstanceID, ti.TryNumber, ti.AttemptEpoch)
}

// writeMarker ends the reaped attempt's log with the reason, since the stream
// stopped wherever its warm worker died (#861). Best-effort, like the other
// reapers' markers. oom names the OOM kill when the worker's container was
// reported OOMKilled (#1218).
func (r *warmWorkerLostReaper) writeMarker(ti WarmBoundTI, oom bool) {
	ref := logs.Ref{
		TenantID: ti.TenantID, DagID: ti.DagID, RunID: ti.DagRunID, TaskID: ti.TaskID,
		TryNumber: ti.TryNumber, AttemptEpoch: ti.AttemptEpoch,
	}
	msg := fmt.Sprintf("killed: pod_lost (warm worker %s is gone or terminated)", ti.WarmWorkerID)
	if oom {
		msg = fmt.Sprintf("killed: pod_lost (warm worker %s was OOMKilled; raise the task's memory limit)", ti.WarmWorkerID)
	}
	if err := appendKilledMarker(r.sink, ref, time.Now().UTC(), msg); err != nil {
		r.record("warm_worker_lost_log_marker_error")
	}
}

func (r *warmWorkerLostReaper) record(decision string) {
	if r.recorder != nil {
		r.recorder.RecordSchedulerDecision(decision)
	}
}
