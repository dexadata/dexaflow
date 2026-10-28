package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// applyPlanned routes one planned transition. The queued rail launches the task
// (a dispatch side effect) and the none rail runs the guarded retry/reschedule/
// infra-replace resets, both stay per-task, applied in plan order. Every other
// transition is a plain state set with no side effect beyond the write and its
// metric, so it is collected into the batch and flushed grouped by source and
// target state after the loop.
func (s *Scheduler) applyPlanned(ctx context.Context, run RunState, t PlannedTransition, batch *transitionBatch) error {
	switch t.To {
	case domain.TaskStateQueued:
		return s.launchQueued(ctx, run, t)
	case domain.TaskStateNone:
		// A → none transition is a retry release (bump try_number), a reschedule
		// re-dispatch (preserve try_number), or an infra re-placement (preserve
		// try_number, bump infra_attempts), keyed on the from-state.
		if run.States[t.TaskID] == domain.TaskStateUpForReschedule {
			return s.redispatchReschedule(ctx, run, t.TaskID)
		}
		if run.States[t.TaskID] == domain.TaskStateFailed && run.InfraFailed[t.TaskID] {
			return s.resetForInfraReplace(ctx, run, t.TaskID)
		}
		return s.resetForRetry(ctx, run, t.TaskID)
	default:
		batch.add(run.States[t.TaskID], t.To, t.TaskID)
		return nil
	}
}

// transitionBatch groups a tick's plain state-set transitions by source and
// target state so each group can be flushed in one UPDATE
// (SchedulerStore.ApplyTransitions) rather than one per task. It carries only
// side-effect-free transitions; the queued (dispatch) and none (guarded reset)
// rails never enter it.
type transitionBatch struct {
	// order lists (from, to) groups in first-seen order so the flush (and the
	// metrics it emits) is deterministic regardless of Go's map iteration.
	order []transitionKey
	byKey map[transitionKey][]string
	// applied holds, per group, the task ids the flush actually moved; a row
	// the source-state guard left alone (#1605) is absent from it.
	applied map[transitionKey][]string
}

// transitionKey is one batched group: the state the tick's snapshot read for
// the task (the write's guard) and the state it moves to.
type transitionKey struct {
	from, to domain.TaskState
}

// newTransitionBatch builds an empty batch.
func newTransitionBatch() *transitionBatch {
	return &transitionBatch{
		byKey:   make(map[transitionKey][]string),
		applied: make(map[transitionKey][]string),
	}
}

// add records that taskID, read in state from, transitions to the given target
// state this tick.
func (b *transitionBatch) add(from, to domain.TaskState, taskID string) {
	k := transitionKey{from: from, to: to}
	if _, seen := b.byKey[k]; !seen {
		b.order = append(b.order, k)
	}
	b.byKey[k] = append(b.byKey[k], taskID)
}

// flushTransitions applies each collected (from, to) group in one guarded
// UPDATE, then records the per-task metrics for the rows that actually moved.
// A row a concurrent writer moved after the tick's snapshot is not touched
// (#1605) and is counted as a superseded decision instead, the same "record
// nothing we did not persist" contract the queued write follows.
func (s *Scheduler) flushTransitions(ctx context.Context, run RunState, b *transitionBatch) error {
	for _, k := range b.order {
		taskIDs := b.byKey[k]
		applied, err := s.store.ApplyTransitions(ctx, run.RunID, taskIDs, k.from, k.to)
		if err != nil {
			return fmt.Errorf("applying %s transitions: %w", k.to, err)
		}
		b.applied[k] = applied
		if skipped := len(taskIDs) - len(applied); skipped > 0 {
			s.logger.Debug("batched transition superseded; the task already moved on",
				"run", run.RunID, "from", k.from, "to", k.to, "superseded", skipped)
		}
		if s.recorder == nil {
			continue
		}
		for range applied {
			s.recorder.RecordSchedulerDecision(string(k.to))
			s.recorder.RecordTaskTransition(string(k.from), string(k.to), run.DagID)
		}
		for i := len(applied); i < len(taskIDs); i++ {
			s.recorder.RecordSchedulerDecision(string(k.to) + "_superseded")
		}
	}
	return nil
}

// resetForRetry returns a task to 'none' with an incremented try number so the
// next tick re-evaluates and re-runs it.
func (s *Scheduler) resetForRetry(ctx context.Context, run RunState, taskID string) error {
	applied, err := s.store.ResetForRetry(ctx, run.RunID, taskID)
	if err != nil {
		return fmt.Errorf("resetting %s for retry: %w", taskID, err)
	}
	// The guarded reset no-ops when the TI is no longer up_for_retry, a stale
	// decision that raced a concurrent writer (e.g. a re-dispatch). Do not record
	// a retry we did not perform; surface the lost race instead.
	if !applied {
		if s.recorder != nil {
			s.recorder.RecordSchedulerDecision("retry_noop")
		}
		return nil
	}
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision("retry")
		s.recorder.RecordTaskRetry(run.DagID, taskID)
		s.recorder.RecordTaskTransition(string(run.States[taskID]), string(domain.TaskStateNone), run.DagID)
	}
	return nil
}

// resetForInfraReplace returns a task a reaper failed as infra to 'none' for a
// re-run, preserving try_number and bumping infra_attempts, an infrastructure
// fault (agent/pod/dispatch lost) is not a task failure and must not consume the
// user's retry budget (ADR 0051 Phase 1).
func (s *Scheduler) resetForInfraReplace(ctx context.Context, run RunState, taskID string) error {
	applied, err := s.store.ResetForInfraReplace(ctx, run.RunID, taskID)
	if err != nil {
		return fmt.Errorf("re-placing infra-failed %s: %w", taskID, err)
	}
	// The guarded reset no-ops when the TI is no longer a failed-infra candidate
	// (a late terminal report, or an app failure raced the reap). Do not record a
	// re-placement we did not perform; surface the lost race instead.
	if !applied {
		if s.recorder != nil {
			s.recorder.RecordSchedulerDecision("infra_replace_noop")
		}
		return nil
	}
	if s.recorder != nil {
		// A re-place of a mark the reconciler never confirmed means the
		// liveness valve opened (ADR 0052 amendment, part 2): the planner acted
		// on the guess without the record's evidence.
		if run.InfraProvisional[taskID] {
			s.recorder.RecordSchedulerDecision("infra_confirm_valve_open")
		}
		s.recorder.RecordSchedulerDecision("infra_replace")
		s.recorder.RecordTaskTransition(string(run.States[taskID]), string(domain.TaskStateNone), run.DagID)
	}
	return nil
}

// redispatchReschedule returns a reschedule-mode sensor to 'none' for re-dispatch
// once its reschedule_at has passed, preserving try_number (no attempt consumed).
func (s *Scheduler) redispatchReschedule(ctx context.Context, run RunState, taskID string) error {
	if err := s.store.RedispatchReschedule(ctx, run.RunID, taskID); err != nil {
		return fmt.Errorf("re-dispatching rescheduled %s: %w", taskID, err)
	}
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision("reschedule")
		s.recorder.RecordTaskTransition(string(run.States[taskID]), string(domain.TaskStateNone), run.DagID)
	}
	return nil
}

// launchQueued routes a queued task to the dispatcher (pod path), recording the
// appropriate transition. A transient failure leaves the task scheduled so the
// next tick retries.
func (s *Scheduler) launchQueued(ctx context.Context, run RunState, t PlannedTransition) error {
	task, ok := findTask(run, t.TaskID)
	if !ok {
		return fmt.Errorf("task %s not found in run %s", t.TaskID, run.RunID)
	}
	if s.dispatcher != nil {
		if s.holdDispatch(run) {
			// Leave the task scheduled, untouched, for the next tick.
			return nil
		}
		s.tenantDispatches[run.TenantID]++
		disp, err := s.dispatcher.Dispatch(ctx, run.RunID, run.DagID, run.DagVersionID, task)
		if err != nil {
			return s.handleDispatchFailure(ctx, run, t.TaskID, disp, err)
		}
		return s.recordQueued(ctx, run, t.TaskID)
	}
	return s.failUndispatchable(ctx, run, t.TaskID, task.Type)
}

// failUndispatchable fails a task that has no executor to run it (pod dispatch
// disabled). The condition is deterministic for the process lifetime, so failing
// fast, with the reason on the task note for the UI, beats leaving the run
// "running" forever (#46, #50).
// handleDispatchFailure records a synchronous dispatch failure: it backs the task
// off for a growing interval, or fails it as dispatch_failed once the attempt
// budget is spent (ADR 0031 Amendment A). It returns nil in both cases, the
// failure is recorded in the DB, not propagated, so one task's dispatch trouble
// does not abort the tick. A dispatch failure is infrastructure, not a task
// failure, so it never consumes the task's try_number.
//
// A Deferred dispatch (the buffered dispatch queue was full) is split out first:
// nothing was attempted, so nothing is recorded and the next tick re-offers it.
// Cluster backpressure (a ResourceQuota 403 or an APF 429) is split out next
// (ADR 0053): it is retriable-forever, so it is backed off WITHOUT touching the
// dispatch-attempt counter and can never reach the dispatch_failed give-up below.
// Dexaflow holds the task and re-offers it until the cluster has room, rather than
// failing the user's task because the cluster asked it to slow down. The
// disposition that split is decided on is classified on the execution layer and
// arrives typed over the seam (ADR 0051 Phase 4), so the scheduler never inspects
// Kubernetes error types itself. An unavailable Kubernetes API (a 503, a 504,
// a 500, a connection refused or reset, the client deadline) is split out
// too: it is re-offered off the budget as well, but bounded in time by
// dispatchUnavailableWindow (ADR 0093, handleUnavailable).
func (s *Scheduler) handleDispatchFailure(ctx context.Context, run RunState, taskID string, disp executor.Disposition, cause error) error {
	if disp == executor.Deferred {
		// The dispatch never left the control plane (the buffered dispatch queue
		// is full or draining). Leave the task scheduled and write nothing: the
		// next tick re-offers it, and local backpressure must never spend the
		// dispatch-attempt budget or drive the task to dispatch_failed.
		s.logger.Debug("dispatch deferred; buffer at capacity, re-offering next tick",
			"run", run.RunID, "task", taskID, "error", cause)
		s.deferredRun = run.RunID
		return nil
	}
	if disp == executor.Backpressure {
		return s.backoffBackpressure(ctx, run, taskID, cause)
	}
	if disp == executor.Unavailable {
		return s.handleUnavailable(ctx, run, taskID, cause)
	}
	if disp == executor.Refused {
		// A permanent verdict (ADR 0066 §3): retrying cannot change it, so the
		// task fails now and spends neither a dispatch retry nor its own retries.
		s.logger.Error("dispatch refused; failing task",
			"run", run.RunID, "task", taskID, "error", cause)
		if err := s.store.FailDispatchRefused(ctx, run.RunID, taskID, refusedReason(cause)); err != nil {
			s.logger.Error("failing refused task", "run", run.RunID, "task", taskID, "error", err)
		}
		return nil
	}
	attempts := run.DispatchAttempts[taskID] + 1
	if attempts >= dispatchMaxAttempts {
		reason := fmt.Sprintf("dispatch_failed after %d attempts: %v", attempts, cause)
		s.logger.Error("dispatch attempts exhausted; failing task",
			"run", run.RunID, "task", taskID, "attempts", attempts, "error", cause)
		if err := s.store.FailDispatchExhausted(ctx, run.RunID, taskID, reason); err != nil {
			s.logger.Error("failing dispatch-exhausted task", "run", run.RunID, "task", taskID, "error", err)
		}
		return nil
	}
	now := run.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nextAt := now.Add(dispatchBackoff(attempts))
	s.logger.Warn("dispatch failed; backing off before re-attempt",
		"run", run.RunID, "task", taskID, "attempt", attempts, "next_dispatch_at", nextAt, "error", cause)
	if err := s.store.RecordDispatchFailure(ctx, run.RunID, taskID, nextAt); err != nil {
		s.logger.Error("recording dispatch failure", "run", run.RunID, "task", taskID, "error", err)
	}
	return nil
}

// backoffBackpressure handles a retriable-forever dispatch failure caused by
// cluster backpressure (ADR 0053). It sets next_dispatch_at so the planner holds
// the task `scheduled` and re-offers it once the backoff elapses, but it does NOT
// increment dispatch_attempts, backpressure is a temporary "no room," not a
// poison placement, so it must never accumulate toward the dispatch_failed cap.
//
// The backoff reuses dispatchBackoff over the task's EXISTING (un-incremented)
// attempt count: a task hitting only backpressure keeps a steady base-interval
// re-offer cadence rather than a growing one, the right trade-off for a
// condition expected to clear, since it recovers fast when the cluster frees room
// without hammering the apiserver every tick, and without inflating a counter
// that could later fail the task. Returns nil: like every dispatch outcome, the
// result is recorded, never propagated, so one task's backpressure cannot abort
// the tick.
func (s *Scheduler) backoffBackpressure(ctx context.Context, run RunState, taskID string, cause error) error {
	now := run.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nextAt := now.Add(dispatchBackoff(run.DispatchAttempts[taskID] + 1))
	s.logger.Warn("dispatch hit cluster backpressure; backing off and re-offering (not counted against dispatch budget)",
		"run", run.RunID, "task", taskID, "next_dispatch_at", nextAt, "error", cause)
	if err := s.store.RecordDispatchBackpressure(ctx, run.RunID, taskID, nextAt); err != nil {
		s.logger.Error("recording dispatch backpressure", "run", run.RunID, "task", taskID, "error", err)
	}
	return nil
}

func (s *Scheduler) failUndispatchable(ctx context.Context, run RunState, taskID string, taskType domain.TaskType) error {
	s.logger.Warn("task has no available executor; failing it (it can never run)",
		"run", run.RunID, "dag", run.DagID, "task", taskID, "task_type", taskType,
		"reason", "no_executor",
		"hint", "pod dispatch disabled (no kubeconfig) or no executor handles this task type")
	if s.recorder != nil {
		s.recorder.RecordUndispatchable("no_executor")
	}
	note := fmt.Sprintf("Failed: no executor available for a %q task. "+
		"Pod dispatch is disabled (no Kubernetes config).", taskType)
	if nerr := s.store.SetTaskNote(ctx, run.RunID, taskID, note); nerr != nil {
		s.logger.Warn("setting task note", "run", run.RunID, "task", taskID, "error", nerr)
	}
	return s.recordTransition(ctx, run, taskID, domain.TaskStateFailed)
}

// recordQueued writes scheduled -> queued after an accepted dispatch, guarded to
// the slot this tick planned. With buffered dispatch the worker may already have
// failed or re-offered the task, or the agent reported on it; the write then
// touches nothing and no transition is recorded.
func (s *Scheduler) recordQueued(ctx context.Context, run RunState, taskID string) error {
	applied, err := s.store.MarkQueued(ctx, run.RunID, taskID, run.NextDispatchAt[taskID])
	if err != nil {
		return fmt.Errorf("applying transition for %s: %w", taskID, err)
	}
	if !applied {
		s.logger.Debug("queued write superseded; the task already moved on", "run", run.RunID, "task", taskID)
		if s.recorder != nil {
			s.recorder.RecordSchedulerDecision("queued_superseded")
		}
		return nil
	}
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision(string(domain.TaskStateQueued))
		s.recorder.RecordTaskTransition(string(run.States[taskID]), string(domain.TaskStateQueued), run.DagID)
	}
	return nil
}

// recordTransition persists a task transition and records its metrics.
func (s *Scheduler) recordTransition(ctx context.Context, run RunState, taskID string, to domain.TaskState) error {
	from := run.States[taskID]
	if err := s.store.ApplyTransition(ctx, run.RunID, taskID, to); err != nil {
		return fmt.Errorf("applying transition for %s: %w", taskID, err)
	}
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision(string(to))
		s.recorder.RecordTaskTransition(string(from), string(to), run.DagID)
	}
	return nil
}
