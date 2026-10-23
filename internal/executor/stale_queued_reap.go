package executor

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/dexadata/dexaflow/internal/logs"
)

// StaleQueuedCandidate is one task instance in `queued` whose dispatch may
// have been lost — typically because the scheduler crashed mid-tick between
// committing the scheduled→queued transition and actually dispatching the TI
// to an executor. The reaper compares the gap from QueuedAt to "now" against
// a dispatch-lost threshold; a non-zero gap larger than the threshold means
// the dispatch is presumed gone and the TI is failed with reason
// `dispatch_lost`. This unblocks the orphan-run reaper, which keeps stuck
// runs out of its candidate set as long as any TI looks active (#202).
type StaleQueuedCandidate struct {
	TaskInstanceID string
	DagRunID       string
	// TenantID names the attempt's log stream for a start_expired marker.
	TenantID string
	DagID    string
	TaskID   string
	// TryNumber is the attempt the queued row is on, so a best-effort pod
	// delete after the mark targets exactly that attempt's pod (#474).
	TryNumber int
	// AttemptEpoch is the epoch the row's current execution was dispatched
	// with (ADR 0051 amendment). The mark and the pod teardown are pinned to it
	// as well as to TryNumber, so a row re-placed or re-dispatched between the
	// list and the write is a different attempt and is left alone.
	AttemptEpoch int
	QueuedAt     time.Time
	// WarmWorkerID is the warm pod durably bound to this attempt (ADR 0058
	// N1d-a2), or "" for a dedicated task or a warm attempt not yet acked. When
	// set AND the worker is in the live warm-pod set, the dispatch-lost reaper
	// DEFERS: the warm worker holds this attempt and is merely slow to transition
	// queued->running, so failing it would double-run the task (review finding
	// H3). A warm attempt has no task pod, so the existing pod-presence gate
	// cannot protect it — this warm check is what does.
	WarmWorkerID string
}

// attempt is the execution this candidate names, for the presence reads and
// the pod teardown.
func (c StaleQueuedCandidate) attempt() Attempt {
	return Attempt{RunID: c.DagRunID, TaskID: c.TaskID, TryNumber: c.TryNumber, AttemptEpoch: c.AttemptEpoch}
}

// IsDispatchLost reports whether a queued TI has been waiting long enough to
// be declared dispatch-lost. A zero QueuedAt is treated as alive — a TI
// without that stamp is too poorly observed to reap defensively. Future
// timestamps (clock skew) are treated as alive. Mirrors IsAgentLost's
// "do no harm" rule (ADR 0031).
func IsDispatchLost(c StaleQueuedCandidate, threshold time.Duration, now time.Time) bool {
	if c.QueuedAt.IsZero() {
		return false
	}
	return now.Sub(c.QueuedAt) >= threshold
}

// DispatchLostReapStore is the slice of scheduler.Store the dispatch-lost
// reaper needs. The full scheduler.Store embeds this interface so production
// wires through one type; unit tests fake just this surface.
type DispatchLostReapStore interface {
	// ListStaleQueuedCandidates returns one page of the `queued` TIs that
	// entered the queue at least threshold ago, with that timestamp, ordered
	// by (queued_at, id) and strictly after the cursor (the zero cursor is the
	// start), at most ReapPageSize rows. The threshold is applied before the
	// page limit, so attempts inside it never take the slots of those past it;
	// the reaper still re-checks it in Go.
	ListStaleQueuedCandidates(ctx context.Context, threshold time.Duration, after ReapCursor) ([]StaleQueuedCandidate, error)
	// MarkTaskDispatchLost transitions one TI to `failed` with
	// error_message='dispatch_lost'. The WHERE state='queued' guard makes
	// this idempotent: a second call on a now-non-queued TI is a no-op. It is
	// pinned to the listed (tryNumber, attemptEpoch) and returns whether a row
	// was actually updated: false means the row moved on between the list and
	// this write (its agent reported RUNNING, or a dispatch claimed a new
	// epoch), so the caller must NOT treat it as reaped (no false log, no pod
	// delete).
	MarkTaskDispatchLost(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
	// MarkTaskStartExpired fails one queued TI as an infra failure because its
	// pod never started before the attempt credential would run out (#1126).
	// Same guards and return as MarkTaskDispatchLost.
	MarkTaskStartExpired(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
}

// StartCredentialMargin is how long before the attempt credential minted at
// dispatch runs out the dispatch-lost reaper stops waiting for a Pending pod
// (#1126). An agent that starts inside the margin still registers and reaches
// its first heartbeat, which renews the credential, before it lapses; a pod
// still Pending at the deadline is re-placed instead. The resilience ladder
// checks it against the heartbeat and maintenance intervals.
const StartCredentialMargin = 2 * time.Minute

// dispatchLostReaper is the scheduler-internal worker that fails TIs whose
// dispatch was lost. Invoked once per scheduler tick, leader-only. Mirrors
// the shape of agentLostReaper deliberately so the three reapers share the
// same resilience invariants: panic-safe, per-candidate isolated, metered.
type dispatchLostReaper struct {
	store     DispatchLostReapStore
	logger    *slog.Logger
	threshold time.Duration
	recorder  DecisionRecorder
	// pods makes the reaper K8s-aware (#461): before failing a past-threshold
	// queued TI, it checks whether the TI's pod is actually live (Pending/
	// Running) — a slow image pull on a cold node means the dispatch DID land,
	// so the reaper must DEFER. Nil in Lite, which gates on the agent process
	// instead (procs).
	pods PodManager
	// cache is an optional informer-backed presence cache (PR-10) consulted ONLY
	// to DEFER a reap: a cached Pending/Running pod skips the live LIST. A cache
	// miss is never authoritative — the reaper falls through to the live
	// TaskPodPresence read, so cache lag can only delay a reap, never cause a
	// false-positive one (#461, the queued path). Nil keeps the live path.
	cache PodPresenceCache
	// warmPods is the live warm-pod seam (ADR 0058 N1d-a2), consulted ONLY to
	// DEFER a reap of a warm-bound queued TI: a warm attempt has no task pod, so
	// TaskPodPresence cannot tell a slow queued->running transition from a lost
	// dispatch. Before failing a candidate whose WarmWorkerID is set, the reaper
	// checks whether that worker is still live; if so it defers (the worker holds
	// the attempt — failing it would double-run, review finding H3). Nil (warm
	// off / not wired) yields an empty live set, so the warm check never defers
	// and the dedicated pod-liveness path is byte-for-byte unchanged.
	warmPods WarmPodLister
	// procs is the Lite liveness seam (see ProcessLiveness), consulted ONLY to
	// DEFER: a queued attempt whose agent process is alive (still retrying its
	// RUNNING report) is not lost, and failing it would re-place the attempt on
	// the same try number beside a live agent (#911). Nil on the pod path, which
	// gates on pod presence instead.
	procs ProcessLiveness
	// startDeadline is how long after queued_at a Pending pod may wait before
	// the credential minted for it at dispatch is about to run out: the attempt
	// token TTL less StartCredentialMargin (#1126). The token is minted after
	// the row is queued, so measuring from queued_at errs early, never late.
	// Zero (Lite, or the TTL not wired) keeps deferring on a live pod however
	// long it waits.
	startDeadline time.Duration
	// sink appends the start_expired marker to the attempt's log; nil leaves
	// markers off.
	sink logSink
	// gate is re-checked before every destructive call (see destructiveGate).
	gate destructiveGate
	// pager walks the queued list across sweeps, so attempts whose pods are
	// Pending cannot hide a lost dispatch behind a full page (R-07, #1597).
	pager reapPager
}

func newDispatchLostReaper(store DispatchLostReapStore, logger *slog.Logger, threshold time.Duration, rec DecisionRecorder) *dispatchLostReaper {
	return &dispatchLostReaper{store: store, logger: logger, threshold: threshold, recorder: rec}
}

// run lists every candidate, fails the stale ones, returns any infra-level
// list error so the caller can log it. Per-TI failures are isolated; a panic
// at any point is recovered so the scheduler tick stays alive.
func (r *dispatchLostReaper) run(ctx context.Context) error {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("dispatch-lost reaper panic recovered", "panic", rec, "stack", string(debug.Stack()))
			r.record("dispatch_lost_panic")
		}
	}()
	// One page per sweep, after the cursor the last sweep left (R-07).
	candidates, err := r.store.ListStaleQueuedCandidates(ctx, r.threshold, r.pager.after())
	if err != nil {
		return err
	}
	advancePager(&r.pager, candidates)
	// Live warm-pod set for the H3 defer, read ONCE per tick (Θ(pools)). A nil
	// lister (warm off / not wired) yields an empty set, so the warm defer below
	// never triggers and the dedicated path is unchanged.
	liveWarm := liveWarmPodNames(ctx, r.warmPods, r.logger)
	now := time.Now().UTC()
	for _, c := range candidates {
		if !IsDispatchLost(c, r.threshold, now) {
			continue
		}
		// Warm-liveness gate (ADR 0058 N1d-a2, review finding H3): a warm attempt
		// can sit in `queued` past the threshold while its serving warm worker is
		// alive and merely slow to transition queued->running. A warm attempt has
		// no task pod, so the pod-presence gate below cannot protect it; failing
		// it would double-run the task once the live worker does transition it.
		// If this candidate is bound to a warm worker that is still live, DEFER.
		if c.WarmWorkerID != "" && liveWarm[c.WarmWorkerID] {
			r.logger.Info("dispatch-lost: warm worker live; deferring",
				"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "worker", c.WarmWorkerID)
			r.record("dispatch_lost_warm_deferred")
			continue
		}
		// K8s-aware liveness gate (#461): a TI can sit in `queued` past the
		// threshold while its pod is alive and just slow to pull the image on a
		// cold node. The pure time threshold cannot tell that apart from a truly
		// lost dispatch, so before failing, consult the pod:
		//   * pod Pending/Running  -> the dispatch landed; DEFER (do not reap).
		//   * pod query failed      -> liveness unknown; DEFER ("do no harm").
		//   * no/terminal pod       -> dispatch is genuinely lost; proceed.
		// Nil pods (Lite) has no pod concept; there the subprocess liveness seam
		// below plays the same role, deferring on a live agent process.
		//
		// Cache fast-path (PR-10), safe direction only: a cached Pending/Running
		// pod defers without an apiserver read. A cache MISS is NOT trusted — fall
		// through to the live read below, preserving the #461 fix.
		//
		// Past the start deadline the cache cannot defer: whether the live pod is
		// still Pending decides between deferring and start_expired (#1126).
		startDue := r.startDue(c, now)
		if !startDue && r.cache != nil && r.cache.CachedPodActive(c.attempt()) {
			r.record("dispatch_lost_cache_active")
			continue
		}
		if processDefers(ctx, r.procs, r.logger, r.record, "dispatch_lost", c.TaskInstanceID, c.DagRunID, c.TaskID, c.TryNumber) {
			continue
		}
		if r.podDefers(ctx, c, startDue) {
			continue
		}
		r.reapOne(ctx, c)
	}
	return nil
}

// podDefers is the live pod read behind the K8s-aware gate: it reports whether
// the candidate was handled here (deferred on a live pod or an unknown read,
// or failed as start_expired) so the caller must not mark it dispatch_lost.
// Nil pods (Lite) handles nothing.
func (r *dispatchLostReaper) podDefers(ctx context.Context, c StaleQueuedCandidate, startDue bool) bool {
	if r.pods == nil {
		return false
	}
	presence, perr := r.pods.TaskPodPresence(ctx, c.attempt())
	if perr != nil {
		r.logger.Warn("dispatch-lost: pod liveness unknown; deferring",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "error", perr)
		r.record("dispatch_lost_pod_query_error")
		return true
	}
	// Only a LIVE pod defers here, which is NOT because a finished pod is
	// worthless: SucceedTaskInstanceIfActive and its siblings admit
	// `queued`, so the reconciler settles queued attempts by design and
	// "a queued attempt never ran" is unsound. (The scheduler's queued
	// stamp is guarded to the scheduled slot it planned, so it never moves
	// a row that reached `running` back to `queued`, #929.) The
	// finished pod's outcome record is nonetheless safe: the teardown below
	// skips a pod in a terminal phase and leaves it to the reconciler
	// (#928), so this reaper's decision can stay a pure liveness question.
	if presence != PodPresenceLive {
		return false
	}
	if startDue && r.startExpired(ctx, c) {
		return true
	}
	r.logger.Info("dispatch-lost: pod is live (slow start); deferring",
		"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID)
	r.record("dispatch_lost_deferred")
	return true
}

// reapOne fails one dispatch-lost TI and tears down any lingering pod,
// re-checking the destructive gate immediately before each write.
func (r *dispatchLostReaper) reapOne(ctx context.Context, c StaleQueuedCandidate) {
	if !gateOpen(r.gate, ctx) {
		r.record("dispatch_lost_gate_skip")
		return
	}
	applied, ferr := r.store.MarkTaskDispatchLost(ctx, c.TaskInstanceID, c.TryNumber, c.AttemptEpoch)
	if ferr != nil {
		r.logger.Error("marking task dispatch-lost",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID, "error", ferr)
		r.record("dispatch_lost_error")
		return
	}
	if !applied {
		// The row left the listed attempt between our list and our write: its
		// agent reported RUNNING, or a dispatch claimed a new epoch. It is not
		// ours to reap, and the attempt's pod may now be the row's live
		// execution, so neither log a reap nor tear the pod down.
		r.record("dispatch_lost_noop")
		return
	}
	r.logger.Warn("task queued past dispatch threshold; failing as dispatch_lost",
		"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID,
		"queued_at", c.QueuedAt)
	r.record("dispatch_lost")
	// Best-effort teardown of any lingering pod for this attempt (#474), pinned
	// to (run, task, try, epoch). By here the presence read said no live pod exists (or
	// pods is nil), so this normally deletes nothing: a pod that materialized
	// since the read is stopped, and a pod in a terminal phase is skipped by the
	// teardown itself and left for the reconciler to settle (#928).
	if r.pods == nil {
		return
	}
	if !gateOpen(r.gate, ctx) {
		r.record("dispatch_lost_teardown_gate_skip")
		return
	}
	td, derr := r.pods.DeleteTaskPod(ctx, c.attempt())
	recordPreserved(r.record, "dispatch_lost_terminal_pod_preserved", td)
	if derr != nil {
		r.logger.Error("deleting dispatch-lost task pod",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "try", c.TryNumber, "error", derr)
		r.record("dispatch_lost_pod_delete_error")
	}
}

// startDue reports whether c has waited past the start deadline: its pod, if
// still Pending, can no longer start with a live credential.
func (r *dispatchLostReaper) startDue(c StaleQueuedCandidate, now time.Time) bool {
	return r.startDeadline > 0 && r.pods != nil && !c.QueuedAt.IsZero() && now.Sub(c.QueuedAt) >= r.startDeadline
}

// startExpired handles a live pod past the start deadline (#1126). A pod whose
// agent has started (Running) keeps deferring: it registered with a live
// credential and renews it on every heartbeat. A pod still Pending would start
// with a credential about to lapse, be refused at Register, stream no log and
// fail against the user's retries; it is failed as start_expired instead, an
// infra mark the planner re-places off the retry budget with a fresh
// credential, and its pod is stopped so it cannot start late. It reports
// whether it took the candidate (reaped it, or deferred on an unknown read);
// false means the pod has started and the ordinary live-pod defer applies.
func (r *dispatchLostReaper) startExpired(ctx context.Context, c StaleQueuedCandidate) bool {
	pending, err := r.pods.TaskPodPending(ctx, c.attempt())
	if err != nil {
		r.logger.Warn("dispatch-lost: pod start unknown; deferring",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "error", err)
		r.record("start_expired_pod_query_error")
		return true
	}
	if !pending {
		return false
	}
	if !gateOpen(r.gate, ctx) {
		r.record("start_expired_gate_skip")
		return true
	}
	applied, ferr := r.store.MarkTaskStartExpired(ctx, c.TaskInstanceID, c.TryNumber, c.AttemptEpoch)
	if ferr != nil {
		r.logger.Error("marking task start-expired",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID, "error", ferr)
		r.record("start_expired_error")
		return true
	}
	if !applied {
		// The agent reported RUNNING, or a dispatch claimed a new epoch, since
		// the list: not ours to reap, and its pod is not ours to stop.
		r.record("start_expired_noop")
		return true
	}
	r.logger.Warn("task pod still pending as its attempt credential runs out; failing as start_expired for an infra re-place",
		"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID,
		"try", c.TryNumber, "epoch", c.AttemptEpoch, "queued_at", c.QueuedAt, "start_deadline", r.startDeadline)
	r.record("start_expired")
	r.writeStartExpiredMarker(c)
	if !gateOpen(r.gate, ctx) {
		r.record("start_expired_teardown_gate_skip")
		return true
	}
	td, derr := r.pods.DeleteTaskPod(ctx, c.attempt())
	recordPreserved(r.record, "start_expired_terminal_pod_preserved", td)
	if derr != nil {
		r.logger.Error("deleting start-expired task pod",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "try", c.TryNumber, "error", derr)
		r.record("start_expired_pod_delete_error")
	}
	return true
}

// writeStartExpiredMarker appends the reason to the attempt's log, which is
// otherwise empty: its agent never started. Best-effort, like the agent-lost
// marker: a nil sink or a write error never blocks the reap.
func (r *dispatchLostReaper) writeStartExpiredMarker(c StaleQueuedCandidate) {
	if r.sink == nil {
		return
	}
	ref := logs.Ref{
		TenantID: c.TenantID, DagID: c.DagID, RunID: c.DagRunID, TaskID: c.TaskID,
		TryNumber: c.TryNumber, AttemptEpoch: c.AttemptEpoch,
	}
	ev := logs.Event{
		Time:   time.Now().UTC(),
		Level:  "error",
		Stream: "system",
		Message: fmt.Sprintf("killed: start_expired (task pod still pending %s after it was queued, too close to the end of its attempt credential; "+
			"re-placed with a fresh credential without using a retry)", r.startDeadline),
	}
	if err := r.sink.AppendEvent(ref, ev); err != nil {
		r.record("start_expired_log_marker_error")
	}
}

func (r *dispatchLostReaper) record(decision string) {
	if r.recorder != nil {
		r.recorder.RecordSchedulerDecision(decision)
	}
}
