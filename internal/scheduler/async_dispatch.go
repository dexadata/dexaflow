package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/executor"
)

// AsyncDispatchStore is what AsyncDispatchFailures needs from storage.
type AsyncDispatchStore interface {
	// DispatchAttempts returns the task's consecutive dispatch failures while it
	// is still scheduled or queued; active is false once it has moved on.
	DispatchAttempts(ctx context.Context, runID, taskID string) (attempts int, active bool, err error)
	// RequeueDispatch moves a scheduled or queued task back to scheduled, held
	// until nextAt, adding one dispatch attempt when countAttempt, and clearing
	// the start of an outage window (ADR 0093) when clearUnavailable.
	RequeueDispatch(ctx context.Context, runID, taskID string, countAttempt, clearUnavailable bool, nextAt time.Time) (bool, error)
	MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error
	// FailDispatchRefused fails a refused task and spends its retry budget, as
	// on the sync path.
	FailDispatchRefused(ctx context.Context, runID, taskID, reason string) error
	// RecordDispatchUnavailable re-offers a task whose pod create met an
	// unavailable Kubernetes API without counting an attempt, as on the sync
	// path (ADR 0093).
	RecordDispatchUnavailable(ctx context.Context, runID, taskID string, now time.Time, minDelay, maxDelay time.Duration) (since, nextAt time.Time, applied bool, err error)
}

// AsyncDispatchFailures handles a dispatch that failed inside a buffered
// dispatch worker exactly as handleDispatchFailure handles a synchronous one:
// cluster backpressure (a quota 403, a 429) is re-offered without spending the
// dispatch budget, an unavailable Kubernetes API (a 503, a 504, a 500, a
// connection refused or reset, the client deadline) is re-offered without
// spending it until dispatchUnavailableWindow passes (ADR 0093), any other
// error (a rejection) is re-offered with a counted attempt and a growing
// backoff, and the task fails as dispatch_failed only once dispatchMaxAttempts
// is spent. It is the
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
		return a.store.FailDispatchRefused(ctx, runID, taskID, refusedReason(cause))
	}
	if disp == executor.Unavailable {
		out, uerr := holdUnavailable(ctx, a.store.RecordDispatchUnavailable, runID, taskID, a.now(), cause)
		if uerr != nil {
			return fmt.Errorf("re-offering %s: %w", taskID, uerr)
		}
		if out.expired {
			a.logger.Error("Kubernetes API unavailable past the dispatch window; failing task",
				"run", runID, "task", taskID, "unavailable_since", out.since, "error", cause)
			return a.store.MarkTaskDispatchFailed(ctx, runID, taskID, out.reason)
		}
		if out.applied {
			a.logger.Warn("buffered dispatch met an unavailable Kubernetes API; re-offering the task",
				"run", runID, "task", taskID, "unavailable_since", out.since, "next_dispatch_at", out.nextAt, "error", cause)
		}
		return nil
	}
	// Neither cluster backpressure nor a dispatch that never left the control
	// plane (Deferred: the client rate limiter refused it) is a failed attempt.
	counted := disp != executor.Backpressure && disp != executor.Deferred
	if counted && attempts+1 >= dispatchMaxAttempts {
		reason := fmt.Sprintf("dispatch_failed after %d attempts: %v", attempts+1, cause)
		a.logger.Error("dispatch attempts exhausted; failing task",
			"run", runID, "task", taskID, "attempts", attempts+1, "error", cause)
		return a.store.MarkTaskDispatchFailed(ctx, runID, taskID, reason)
	}
	// A rejection or cluster backpressure is an answer from the apiserver, so
	// it ends an outage window (ADR 0093). A Deferred refusal and a buffer wait
	// past its bound (reported as Backpressure) never reached the API and keep
	// it, or a saturated buffer during an outage would reset it forever.
	clearUnavailable := disp == executor.Rejected ||
		(disp == executor.Backpressure && !errors.Is(cause, dispatch.ErrBufferWaitExceeded))
	nextAt := a.now().Add(dispatchBackoff(attempts + 1))
	a.logger.Warn("buffered dispatch failed; re-offering the task after a backoff",
		"run", runID, "task", taskID, "disposition", disp.String(), "counted", counted,
		"next_dispatch_at", nextAt, "error", cause)
	if _, err := a.store.RequeueDispatch(ctx, runID, taskID, counted, clearUnavailable, nextAt); err != nil {
		return fmt.Errorf("re-offering %s: %w", taskID, err)
	}
	return nil
}

// refusedReason is the failure reason of a Refused dispatch: the refusal's own
// message, which already says what to change.
func refusedReason(cause error) string {
	return "dispatch refused: " + cause.Error()
}
