package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/dexadata/dexaflow/internal/executor"
)

// AsyncDispatchStore is what AsyncDispatchFailures needs from storage.
type AsyncDispatchStore interface {
	// DispatchAttempts returns the task's consecutive dispatch failures while it
	// is still scheduled or queued; active is false once it has moved on.
	DispatchAttempts(ctx context.Context, runID, taskID string) (attempts int, active bool, err error)
	// RequeueDispatch moves a scheduled or queued task back to scheduled, held
	// until nextAt, adding one dispatch attempt when countAttempt.
	RequeueDispatch(ctx context.Context, runID, taskID string, countAttempt bool, nextAt time.Time) (bool, error)
	MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error
}

// AsyncDispatchFailures handles a dispatch that failed inside a buffered
// dispatch worker exactly as handleDispatchFailure handles a synchronous one:
// cluster backpressure (a quota 403, a 429) is re-offered without spending the
// dispatch budget, any other error (a 5xx, a timeout, a rejection) is re-offered
// with a counted attempt and a growing backoff, and the task fails as
// dispatch_failed only once dispatchMaxAttempts is spent. It is the
// dispatch.FailureSink the buffered dispatcher reports to.
type AsyncDispatchFailures struct {
	store  AsyncDispatchStore
	logger *slog.Logger
	now    func() time.Time
}

// NewAsyncDispatchFailures builds the handler over store.
func NewAsyncDispatchFailures(store AsyncDispatchStore, logger *slog.Logger) *AsyncDispatchFailures {
	if logger == nil {
		logger = slog.Default()
	}
	return &AsyncDispatchFailures{store: store, logger: logger, now: func() time.Time { return time.Now().UTC() }}
}

// MarkTaskDispatchFailed fails the task outright; the buffered dispatcher uses
// it for a failure that is not a dispatch error (a worker panic).
func (a *AsyncDispatchFailures) MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error {
	return a.store.MarkTaskDispatchFailed(ctx, runID, taskID, reason)
}

// HandleDispatchFailure re-offers or fails a task whose buffered dispatch
// failed with the given disposition. A task that is no longer scheduled or
// queued (the agent reported, an operator acted) is left alone.
func (a *AsyncDispatchFailures) HandleDispatchFailure(ctx context.Context, runID, taskID string, disp executor.Disposition, cause error) error {
	attempts, active, err := a.store.DispatchAttempts(ctx, runID, taskID)
	if err != nil {
		return fmt.Errorf("reading dispatch attempts for %s: %w", taskID, err)
	}
	if !active {
		return nil
	}
	if disp == executor.Refused {
		a.logger.Error("buffered dispatch refused; failing task", "run", runID, "task", taskID, "error", cause)
		return a.store.MarkTaskDispatchFailed(ctx, runID, taskID, refusedReason(cause))
	}
	counted := disp != executor.Backpressure
	if counted && attempts+1 >= dispatchMaxAttempts {
		reason := fmt.Sprintf("dispatch_failed after %d attempts: %v", attempts+1, cause)
		a.logger.Error("dispatch attempts exhausted; failing task",
			"run", runID, "task", taskID, "attempts", attempts+1, "error", cause)
		return a.store.MarkTaskDispatchFailed(ctx, runID, taskID, reason)
	}
	nextAt := a.now().Add(dispatchBackoff(attempts + 1))
	a.logger.Warn("buffered dispatch failed; re-offering the task after a backoff",
		"run", runID, "task", taskID, "disposition", disp.String(), "counted", counted,
		"next_dispatch_at", nextAt, "error", cause)
	if _, err := a.store.RequeueDispatch(ctx, runID, taskID, counted, nextAt); err != nil {
		return fmt.Errorf("re-offering %s: %w", taskID, err)
	}
	return nil
}

// refusedReason is the failure reason of a Refused dispatch: the refusal's own
// message, which already says what to change.
func refusedReason(cause error) string {
	return "dispatch refused: " + cause.Error()
}
