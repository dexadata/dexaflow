package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// unavailableHold is the store write both dispatch paths make when the
// Kubernetes API was unavailable to a pod create (ADR 0093): re-offer the
// task without spending a dispatch attempt, and return when the task's outage
// window started and when it is re-offered. applied is false when the task is
// no longer scheduled or queued.
type unavailableHold func(ctx context.Context, runID, taskID string, now time.Time, minDelay, maxDelay time.Duration) (since, nextAt time.Time, applied bool, err error)

// unavailableOutcome is what holdUnavailable decided.
type unavailableOutcome struct {
	// applied is false when the task had moved on and nothing was written.
	applied bool
	// expired reports that the task's outage window passed
	// dispatchUnavailableWindow: the caller fails it as dispatch_failed with
	// reason.
	expired bool
	reason  string
	since   time.Time
	nextAt  time.Time
}

// holdUnavailable records an unavailable-API dispatch failure through hold and
// decides whether the task's outage window is spent. The window is measured
// from the first unavailable failure of the dispatch episode, which the store
// keeps on the row, so it survives a restart or a leader change (the DB-as-
// truth rule of ADR 0031).
func holdUnavailable(ctx context.Context, hold unavailableHold, runID, taskID string, now time.Time, cause error) (unavailableOutcome, error) {
	since, nextAt, applied, err := hold(ctx, runID, taskID, now, dispatchBackoffBase, dispatchUnavailableMaxDelay)
	if err != nil {
		return unavailableOutcome{}, err
	}
	out := unavailableOutcome{applied: applied, since: since, nextAt: nextAt}
	if !applied {
		return out, nil
	}
	if outage := now.Sub(since); outage >= dispatchUnavailableWindow {
		out.expired = true
		subject := "Kubernetes API unavailable"
		var s interface{ UnavailableSubject() string }
		if errors.As(cause, &s) {
			subject = s.UnavailableSubject()
		}
		out.reason = fmt.Sprintf("dispatch_failed: %s for %s: %v", subject, outage.Round(time.Second), cause)
	}
	return out, nil
}

// handleUnavailable is the synchronous path's answer to an Unavailable
// dispatch (#1606): the task is held and re-offered off the dispatch-attempt
// budget, and fails as dispatch_failed only once its outage window is spent.
// Like every dispatch outcome it is recorded, never propagated.
func (s *Scheduler) handleUnavailable(ctx context.Context, run RunState, taskID string, cause error) error {
	now := run.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	out, err := holdUnavailable(ctx, s.store.RecordDispatchUnavailable, run.RunID, taskID, now, cause)
	if err != nil {
		s.logger.Error("recording unavailable dispatch", "run", run.RunID, "task", taskID, "error", err)
		return nil
	}
	if !out.applied {
		return nil
	}
	if out.expired {
		s.logger.Error("Kubernetes API unavailable past the dispatch window; failing task",
			"run", run.RunID, "task", taskID, "unavailable_since", out.since, "window", dispatchUnavailableWindow, "error", cause)
		s.recordDecision("dispatch_unavailable_exhausted")
		if ferr := s.store.FailDispatchExhausted(ctx, run.RunID, taskID, out.reason); ferr != nil {
			s.logger.Error("failing dispatch-unavailable task", "run", run.RunID, "task", taskID, "error", ferr)
		}
		return nil
	}
	s.logger.Warn("Kubernetes API unavailable; re-offering the task (not counted against dispatch budget)",
		"run", run.RunID, "task", taskID, "unavailable_since", out.since, "next_dispatch_at", out.nextAt, "error", cause)
	s.recordDecision("dispatch_unavailable")
	return nil
}

// recordDecision counts a scheduler decision when a recorder is set.
func (s *Scheduler) recordDecision(decision string) {
	if s.recorder != nil {
		s.recorder.RecordSchedulerDecision(decision)
	}
}
