package executor

import (
	"context"
	"log/slog"
)

// ProcessLiveness answers whether the local agent process of one attempt is
// still alive. It is the Lite (subprocess executor) counterpart of pod liveness:
// with no pods there is nothing for the reapers to read or delete, but the agent
// is a host process whose liveness can be checked by PID.
//
// The reapers consult it ONLY to defer. A Lite reaper does not stop a live
// agent the way a pod delete does (it only stops the orphaned task of a dead
// one, see OrphanStopper), so it must never fail an attempt whose agent
// is still alive: the infra re-place that follows keeps the try number, so a
// second agent would start on the same attempt while the first one could still
// get its RUNNING report accepted and run user code (#911).
type ProcessLiveness interface {
	// AttemptProcessAlive reports whether the agent process spawned for the
	// (run, task, try) attempt is alive. false with a nil error means no live
	// agent is known for the attempt (it exited, or was never spawned); a
	// non-nil error means liveness is unknown and the caller must defer.
	AttemptProcessAlive(ctx context.Context, runID, taskID string, tryNumber int) (bool, error)
}

// OrphanStopper is implemented by a ProcessLiveness that can stop a task left
// running by an agent that died (Lite: the task's process group outlives an
// agent killed outright, #916). The reapers call it for an attempt that reads
// alive, before deferring it, so the infra re-place that follows a reap never
// starts a second copy beside an orphan.
type OrphanStopper interface {
	// StopOrphanedTask stops the attempt's task when its agent is confirmed
	// dead and the task is verifiably the one that agent started, and reports
	// true once nothing of it is left. false with a nil error means nothing was
	// stopped (the agent is alive, or the task could not be verified) and the
	// attempt must still be treated as alive.
	StopOrphanedTask(ctx context.Context, runID, taskID string, tryNumber int) (bool, error)
}

// SetProcessLiveness wires the subprocess liveness seam into the two reapers
// that act on an attempt whose agent may still be running: agent-lost and
// dispatch-lost. Each defers while the attempt's agent process is alive or its
// liveness cannot be read. It also turns on agent-lost's Lite-only judgement
// of a TI that never heartbeated (see runNeverHeartbeated). Nil (the Kubernetes path, which gates on pods) leaves
// both reapers unchanged.
func (r *Reaper) SetProcessLiveness(p ProcessLiveness) {
	r.agentLost.procs = p
	r.dispatchLost.procs = p
	r.agentLost.running = nil
	if p != nil {
		r.agentLost.running = r.podLost.store
	}
}

// processDefers reports whether a reaper must defer an attempt because its
// local agent (or the task it started) is alive or its liveness is unknown,
// metering the reason as <prefix>_process_alive or <prefix>_process_query_error.
// When procs is an OrphanStopper, an alive attempt whose agent is dead gets its
// orphaned task stopped first: stopped proceeds (<prefix>_orphan_stopped), a
// failed stop defers (<prefix>_orphan_stop_error). A nil procs never defers.
func processDefers(ctx context.Context, procs ProcessLiveness, logger *slog.Logger, record func(string), prefix, tiID, runID, taskID string, try int) bool {
	if procs == nil {
		return false
	}
	alive, err := procs.AttemptProcessAlive(ctx, runID, taskID, try)
	if err != nil {
		logger.Warn(prefix+": agent process liveness unknown; deferring",
			"ti", tiID, "run", runID, "task", taskID, "try", try, "error", err)
		record(prefix + "_process_query_error")
		return true
	}
	if !alive {
		return false
	}
	if stopper, ok := procs.(OrphanStopper); ok {
		stopped, serr := stopper.StopOrphanedTask(ctx, runID, taskID, try)
		if serr != nil {
			logger.Warn(prefix+": could not stop the orphaned task of a dead agent; deferring",
				"ti", tiID, "run", runID, "task", taskID, "try", try, "error", serr)
			record(prefix + "_orphan_stop_error")
			return true
		}
		if stopped {
			logger.Warn(prefix+": stopped the orphaned task of a dead agent; reaping",
				"ti", tiID, "run", runID, "task", taskID, "try", try)
			record(prefix + "_orphan_stopped")
			return false
		}
	}
	logger.Info(prefix+": agent or task process is alive; deferring",
		"ti", tiID, "run", runID, "task", taskID, "try", try)
	record(prefix + "_process_alive")
	return true
}
