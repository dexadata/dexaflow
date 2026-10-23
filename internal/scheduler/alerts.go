package scheduler

import (
	"context"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// alertMaxAttempts bounds how many times one failure episode may be sent before
// Dexaflow gives up. A failed run stays failed forever, so without a ceiling a dead
// alert endpoint is retried once per scheduler tick for the life of the run, the
// alert path would DoS the very endpoint it is trying to reach. Five attempts
// spread over the backoff below survives a restart or a brief outage of the
// channel without becoming a hammer.
const alertMaxAttempts = 5

// alertRetryBackoff is the wait between attempts for one episode. Deliberately far
// longer than the scheduler tick (1s by default): the retry exists to ride out a
// transient channel outage, and an outage of the alert channel is frequently
// correlated with the incident being reported, so retrying hard makes both worse.
const alertRetryBackoff = 2 * time.Minute

// alertAttemptUnknown is the attempt number used when the claim itself errored
// and we alert anyway (fail open). It is deliberately outside the real attempt
// range so the delivery stamp, which is guarded on the attempt, matches no row,
// the page goes out, but a bookkeeping write derived from an unknown attempt
// never overwrites state we cannot reason about.
const alertAttemptUnknown = -1

// maybeAlertFailure fires the DAG's on-failure alert rules when a run finalizes
// failed. It runs in a detached goroutine (WithoutCancel) so a slow endpoint
// never stalls the tick, and the alerter's own best-effort contract means a
// delivery failure cannot fail the run. A nil alerter or a DAG without rules is
// a no-op.
func (s *Scheduler) maybeAlertFailure(ctx context.Context, state domain.DagRunState, run RunState) {
	if state != domain.DagRunStateFailed || s.alerter == nil {
		return
	}
	if run.Alerts == nil || len(run.Alerts.OnFailure) == 0 {
		return
	}
	// Claim one ATTEMPT for this failure episode. The claim no longer means
	// "paged", it means "we are about to try", so a send that fails leaves the
	// episode claimable and the next tick retries it after the backoff. Refused
	// when already delivered, out of budget, or still backing off. Fail OPEN on a
	// store error: a missed page is worse than a rare duplicate.
	attempt, err := s.store.ClaimAlertAttempt(ctx, run.RunID, alertMaxAttempts, alertRetryBackoff)
	if err != nil {
		s.logger.Error("claiming on-failure alert attempt (alerting anyway)",
			"dag", run.DagID, "run", run.RunID, "error", err)
		attempt = alertAttemptUnknown
	} else if attempt == 0 {
		return
	}
	// The last attempt is the one nobody hears about if it fails, so say so while
	// it is still happening. Without this, a run whose every attempt failed is
	// indistinguishable in the database from one that has not been tried, the
	// alerting system failing silently, which is the same shape as the failure it
	// exists to report.
	if attempt >= alertMaxAttempts {
		s.logger.Warn("final on-failure alert attempt for this run; no further retries after this one",
			"dag", run.DagID, "run", run.RunID, "attempt", attempt, "max", alertMaxAttempts)
	}
	// Acquire a dispatch slot without blocking the tick. A saturated semaphore
	// means a burst of failures is already sending; dropping this one (with a
	// warning) is the best-effort trade-off that protects the scheduler.
	select {
	case s.alertSem <- struct{}{}:
		go func() {
			defer func() { <-s.alertSem }()
			// Stamp delivery only when every rule landed. On anything less the
			// episode stays unstamped, so the next tick retries it once the
			// backoff elapses, until the attempt budget runs out.
			detached := context.WithoutCancel(ctx)
			if !s.alerter.AlertRunFailed(detached, run) {
				if attempt >= alertMaxAttempts {
					// Terminal: the budget is spent and nothing got through. This is
					// the line an operator needs when asking "why was I never paged",
					// so it says exactly that rather than reporting a failed send.
					s.logger.Error("on-failure alert GAVE UP: no attempt was delivered, nobody was paged",
						"dag", run.DagID, "run", run.RunID, "attempts", attempt)
					return
				}
				s.logger.Warn("on-failure alert not fully delivered; will retry after backoff",
					"dag", run.DagID, "run", run.RunID,
					"attempt", attempt, "max", alertMaxAttempts, "backoff", alertRetryBackoff)
				return
			}
			if err := s.store.MarkRunAlertDelivered(detached, run.RunID, attempt); err != nil {
				// The page went out; failing to record it costs a duplicate on the
				// next tick, which is the trade-off this path already prefers.
				s.logger.Error("recording on-failure alert delivery",
					"dag", run.DagID, "run", run.RunID, "error", err)
			}
		}()
	default:
		s.logger.Warn("dropping on-failure alert: dispatch saturated",
			"dag", run.DagID, "run", run.RunID, "limit", cap(s.alertSem))
		// Record the drop per rule (mirrors the dispatcher's per-rule sent/failed),
		// so a burst of saturation-drops is a metric operators can alert on, not
		// just a log line (#435). Best-effort: a nil recorder is a no-op.
		if s.recorder != nil {
			for _, rule := range run.Alerts.OnFailure {
				s.recorder.RecordAlert(run.DagID, rule.Type, alertResultDropped)
			}
		}
	}
}

// alertResultDropped is the RecordAlert result for an on-failure alert the
// scheduler drops because its dispatch semaphore is saturated (#435). It sits
// alongside the dispatcher's "sent"/"failed" on the same metric.
const alertResultDropped = "dropped"
