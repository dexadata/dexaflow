package executor

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/dexadata/dexaflow/internal/logs"
)

// logSink is the slice of the log backend a reaper needs to append a final
// marker to a reaped attempt's log stream (#861). A reaper-killed pod stops
// mid-stream, so without this the task log ends with a silent truncation; the
// marker turns it into a diagnosable "killed: agent_lost …" line. It is
// logs.MarkerSink: AppendEvent preserves prior content on BOTH backends
// (O_APPEND on disk, read-modify-write on an object store), so the marker never
// clobbers the agent's streamed log. Nil disables the marker (Lite or an unwired
// sink) — the reaper's core work is unaffected.
type logSink interface {
	AppendEvent(ref logs.Ref, ev logs.Event) error
}

// AgentLostCandidate is one task instance in `running` whose agent may have
// gone silent, with the timestamp of its most recent heartbeat. The reaper
// compares the gap from this stamp to "now" against a stall threshold; a
// non-zero gap larger than the threshold means the agent is presumed gone
// and the TI is failed with reason "agent_lost".
type AgentLostCandidate struct {
	TaskInstanceID string
	TenantID       string
	DagRunID       string
	DagID          string
	TaskID         string
	// TryNumber is the attempt the candidate row is on, so the reaper can
	// tear down EXACTLY that attempt's pod after failing it (#474). A retry
	// bumps try_number in place and dispatches a new pod with a new
	// try-number label, so pinning it here means a newer live attempt's pod
	// can never be deleted by mistake.
	TryNumber     int
	LastHeartbeat time.Time
}

// IsAgentLost reports whether the agent has been silent long enough to be
// declared lost. A zero LastHeartbeat (never reported) is treated as alive,
// not lost — the TI may be inline (no agent ever exists), or simply has not
// completed its first interval yet. The reaper only fires on TIs that did
// heartbeat at least once and then went silent; this is the "do no harm"
// rule of ADR 0031. Future timestamps (clock skew) are treated as alive.
func IsAgentLost(c AgentLostCandidate, threshold time.Duration, now time.Time) bool {
	if c.LastHeartbeat.IsZero() {
		return false
	}
	return now.Sub(c.LastHeartbeat) >= threshold
}

// HeartbeatReapStore is the slice of scheduler.Store the TI heartbeat reaper
// needs. The full scheduler.Store embeds this interface so production wires
// through one type; unit tests fake just this surface.
type HeartbeatReapStore interface {
	// ListAgentLostCandidates returns every `running` TI whose last heartbeat
	// is non-null (it has heartbeated at least once). The reaper applies the
	// threshold per candidate so the SQL stays simple and the decision is
	// purely in Go.
	ListAgentLostCandidates(ctx context.Context) ([]AgentLostCandidate, error)
	// MarkTaskAgentLost transitions one TI to `failed` with
	// error_message='agent_lost'. The WHERE state='running' guard makes this
	// idempotent. It returns whether a row was actually updated: false means a
	// late terminal report transitioned the TI between the list and this write,
	// so the caller must NOT treat it as reaped (no false log, no pod delete).
	MarkTaskAgentLost(ctx context.Context, taskInstanceID string) (bool, error)
}

// agentLostReaper is the scheduler-internal worker that fails TIs whose agent
// went silent. Invoked once per scheduler tick, leader-only. Mirrors the
// shape of orphanReaper deliberately so the two reapers share the same
// resilience invariants: panic-safe, per-candidate isolated, metered.
type agentLostReaper struct {
	store     HeartbeatReapStore
	logger    *slog.Logger
	threshold time.Duration
	recorder  DecisionRecorder
	// pods tears down the reaped TI's pod after the DB transition (#474). A
	// silent agent may be a network-partitioned but still-running container;
	// deleting the pod is what actually stops the abandoned work. Nil in Lite.
	pods PodManager
	// sink appends a final "killed: agent_lost" marker to the reaped attempt's
	// log stream so a killed task's log does not end in a silent truncation
	// (#861). Nil disables the marker; the reap itself is unaffected.
	sink logSink
	// procs is the Lite liveness seam (see ProcessLiveness): a silent attempt
	// whose agent process is still alive is deferred, because Lite has no pod
	// delete to stop it and a re-placed attempt would run beside it. Nil on the
	// pod path, where the teardown above stops the abandoned container.
	procs ProcessLiveness
	// running lists running TIs with whether each has heartbeated. Lite only
	// (set with procs): it is how this reaper sees an agent that died before
	// its first heartbeat, which ListAgentLostCandidates never returns and
	// pod-lost cannot judge without pods (#916).
	running runningLister
	// gate is re-checked before every destructive call (see destructiveGate).
	gate destructiveGate
}

// runningLister is the ListRunningTasks slice of PodLostReapStore.
type runningLister interface {
	ListRunningTasks(ctx context.Context, grace time.Duration) ([]PodLostCandidate, error)
}

func newAgentLostReaper(store HeartbeatReapStore, logger *slog.Logger, threshold time.Duration, rec DecisionRecorder) *agentLostReaper {
	return &agentLostReaper{store: store, logger: logger, threshold: threshold, recorder: rec}
}

// run lists every candidate, fails the stale ones, returns any infra-level
// list error so the caller can log it. Per-TI failures are isolated; a panic
// at any point is recovered so the scheduler tick stays alive.
func (r *agentLostReaper) run(ctx context.Context) error {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("agent-lost reaper panic recovered", "panic", rec, "stack", string(debug.Stack()))
			r.record("agent_lost_panic")
		}
	}()
	now := time.Now().UTC()
	// A control-plane restart manufactures the very silence this reaper punishes:
	// every in-flight TI's last heartbeat elapses during the outage, and the
	// heartbeat receiver is the process that just came back. Reaper.settling holds
	// the whole tick until the fleet has had a grace to re-heartbeat, so by the
	// time run is reached a stale heartbeat is a real one.
	candidates, err := r.store.ListAgentLostCandidates(ctx)
	if err != nil {
		return err
	}
	for _, c := range candidates {
		if !IsAgentLost(c, r.threshold, now) {
			continue
		}
		if processDefers(ctx, r.procs, r.logger, r.record, "agent_lost", c.TaskInstanceID, c.DagRunID, c.TaskID, c.TryNumber) {
			continue
		}
		r.reapOne(ctx, c, now)
	}
	return r.runNeverHeartbeated(ctx, now)
}

// runNeverHeartbeated is the Lite-only half of agent-lost: a TI that reported
// RUNNING but never heartbeated, running longer than the agent-lost threshold,
// is failed as agent_lost when its agent AND its task process group both read
// dead. Without it such a TI stays running forever in Lite: this reaper's list
// skips it, pod-lost needs pods, and orphan-run skips a run with a running TI.
// Unlike the heartbeated path it never stops an orphaned task group: anything
// alive defers, so the reap needs both dead outright. Nil procs or running
// (the pod path, where pod-lost owns this window) makes it a no-op.
func (r *agentLostReaper) runNeverHeartbeated(ctx context.Context, now time.Time) error {
	if r.procs == nil || r.running == nil {
		return nil
	}
	candidates, err := r.running.ListRunningTasks(ctx, r.threshold)
	if err != nil {
		return err
	}
	for _, c := range candidates {
		if c.Heartbeated || !IsPodLostCandidate(c, r.threshold, now) {
			continue
		}
		alive, perr := r.procs.AttemptProcessAlive(ctx, c.DagRunID, c.TaskID, c.TryNumber)
		if perr != nil {
			r.logger.Warn("agent-lost: process liveness of a never-heartbeated task unknown; deferring",
				"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "try", c.TryNumber, "error", perr)
			r.record("agent_lost_never_heartbeated_process_query_error")
			continue
		}
		if alive {
			r.record("agent_lost_never_heartbeated_process_alive")
			continue
		}
		r.reapNeverHeartbeated(ctx, c, now)
	}
	return nil
}

// reapNeverHeartbeated fails one never-heartbeated TI whose agent and task
// group are both dead, through the same guarded write and log marker as a
// silent agent.
func (r *agentLostReaper) reapNeverHeartbeated(ctx context.Context, c PodLostCandidate, now time.Time) {
	if !gateOpen(r.gate, ctx) {
		r.record("agent_lost_gate_skip")
		return
	}
	applied, err := r.store.MarkTaskAgentLost(ctx, c.TaskInstanceID)
	if err != nil {
		r.logger.Error("marking never-heartbeated task agent-lost",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID, "error", err)
		r.record("agent_lost_error")
		return
	}
	if !applied {
		r.record("agent_lost_noop")
		return
	}
	r.logger.Warn("task agent died before its first heartbeat; failing as agent_lost",
		"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID, "running_since", c.RunningSince)
	r.record("agent_lost_never_heartbeated")
	r.writeAgentLostMarker(AgentLostCandidate{
		TaskInstanceID: c.TaskInstanceID, TenantID: c.TenantID, DagRunID: c.DagRunID, DagID: c.DagID,
		TaskID: c.TaskID, TryNumber: c.TryNumber,
	}, now)
}

// reapOne fails one silent TI, writes its log marker and tears down its pod,
// re-checking the destructive gate immediately before each write.
func (r *agentLostReaper) reapOne(ctx context.Context, c AgentLostCandidate, now time.Time) {
	if !gateOpen(r.gate, ctx) {
		r.record("agent_lost_gate_skip")
		return
	}
	applied, ferr := r.store.MarkTaskAgentLost(ctx, c.TaskInstanceID)
	if ferr != nil {
		r.logger.Error("marking task agent-lost",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID, "error", ferr)
		r.record("agent_lost_error")
		return
	}
	if !applied {
		// A late terminal report transitioned the TI between our list and our
		// write (WHERE state='running' matched 0 rows). It is no longer ours
		// to reap — do not log a false reap or delete a pod for a settled TI.
		r.record("agent_lost_noop")
		return
	}
	r.logger.Warn("task agent silent past threshold; failing as agent_lost",
		"ti", c.TaskInstanceID, "run", c.DagRunID, "dag", c.DagID, "task", c.TaskID,
		"last_heartbeat", c.LastHeartbeat)
	r.record("agent_lost")
	// Append a final marker to the attempt's log BEFORE deleting the pod, so a
	// killed task's log ends with the reason instead of a silent truncation
	// (#861) — the log stream stops the moment the pod is gone.
	r.writeAgentLostMarker(c, now)
	// The TI is now durably failed; delete its pod so a partitioned-but-alive
	// container stops (#474). Pinned to (run, task, try) so a retry's newer
	// pod is never touched. Only reached after the DB mark, so we never delete
	// a pod for a TI we did not settle. Best-effort: a delete error is logged.
	//
	// This reaper reads no pod presence at all — it fires on heartbeat staleness
	// alone at a 90s threshold, and a task that finished and stopped
	// heartbeating is precisely its candidate. The evidence guard is therefore in
	// the teardown, not here: DeleteTaskPod skips a pod in a terminal phase and
	// leaves its outcome record to the reconciler (#928).
	if r.pods == nil {
		return
	}
	if !gateOpen(r.gate, ctx) {
		r.record("agent_lost_teardown_gate_skip")
		return
	}
	if derr := r.pods.DeleteTaskPod(ctx, c.DagRunID, c.TaskID, c.TryNumber); derr != nil {
		r.logger.Error("deleting agent-lost task pod",
			"ti", c.TaskInstanceID, "run", c.DagRunID, "task", c.TaskID, "try", c.TryNumber, "error", derr)
		r.record("agent_lost_pod_delete_error")
	}
}

// writeAgentLostMarker appends one terminal line to the reaped attempt's log
// stream so the user tailing it sees why it stopped instead of a truncation
// (#861). Best-effort: a nil sink or an open/write error never blocks the reap —
// the DB state and server slog remain the source of truth.
func (r *agentLostReaper) writeAgentLostMarker(c AgentLostCandidate, now time.Time) {
	if r.sink == nil {
		return
	}
	msg := fmt.Sprintf("killed: agent_lost (last heartbeat %s, silent past %s threshold)", c.LastHeartbeat.UTC().Format(time.RFC3339), r.threshold)
	if c.LastHeartbeat.IsZero() {
		msg = fmt.Sprintf("killed: agent_lost (no heartbeat ever, agent and task processes gone, running past %s threshold)", r.threshold)
	}
	ref := logs.Ref{TenantID: c.TenantID, DagID: c.DagID, RunID: c.DagRunID, TaskID: c.TaskID, TryNumber: c.TryNumber}
	ev := logs.Event{
		Time:    now,
		Level:   "error",
		Stream:  "system",
		Message: msg,
	}
	if err := r.sink.AppendEvent(ref, ev); err != nil {
		r.record("agent_lost_log_marker_error")
	}
}

func (r *agentLostReaper) record(decision string) {
	if r.recorder != nil {
		r.recorder.RecordSchedulerDecision(decision)
	}
}
