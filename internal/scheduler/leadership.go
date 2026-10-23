package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// leaderCheckInterval is how often we both poll for leadership (as a follower)
// and revalidate it (as a leader).
const leaderCheckInterval = 5 * time.Second

// maxLeaderCheckFailures is how many consecutive leadership-check errors are
// tolerated before stepping down, so a single transient error (e.g. the leader
// connection being recycled) does not cause needless leadership churn.
const maxLeaderCheckFailures = 3

// leadershipChecker reports whether this instance still holds leadership.
type leadershipChecker interface {
	HoldsLock(ctx context.Context) (bool, error)
}

// leadershipAcquirer tries to take leadership without blocking.
type leadershipAcquirer interface {
	TryAcquire(ctx context.Context) (bool, error)
}

// tryLeadership runs one campaign attempt under a deadline. The leader pool
// has no statement timeout, so on a black-holed session (the database host
// gone without a TCP reset) an attempt with no deadline would block until the
// kernel gives up on the socket, about 15 minutes by default (#1599). When the
// deadline cuts the query, pgx closes that connection, so a lock the server
// may have granted meanwhile goes with the session.
func tryLeadership(ctx context.Context, acq leadershipAcquirer, timeout time.Duration) (bool, error) {
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return acq.TryAcquire(actx)
}

// checkLeadership is one watchdog check under a deadline: a check that cannot
// confirm the lock within it fails, and counts toward stepping down, instead
// of blocking the watchdog on a dead session (#1599).
func checkLeadership(ctx context.Context, chk leadershipChecker, timeout time.Duration) (bool, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return chk.HoldsLock(cctx)
}

// CampaignAndRun acquires scheduler leadership (polling every leaderCheckInterval)
// and runs the loop only while leader, so a single replica schedules at a time
// (ADR 0009). If leadership is lost mid-run (the advisory lock dropped because the
// connection blipped, was idle-reaped, or recycled), it steps down and
// re-campaigns rather than scheduling alongside the new leader. It returns when
// ctx is done.
func CampaignAndRun(ctx context.Context, leader *Leader, sched *Scheduler, logger *slog.Logger) {
	ticker := time.NewTicker(leaderCheckInterval)
	defer ticker.Stop()
	// stepDownAt is the wall-clock the previous runAsLeader returned at (i.e.
	// when we transitioned out of leadership). Zero means "no prior step-down
	// in this process", the first acquisition at boot doesn't record a
	// re-acquire latency (no churn happened yet).
	var stepDownAt time.Time
	for {
		acquired, err := tryLeadership(ctx, leader, leaderCheckInterval)
		switch {
		case err != nil:
			logger.Error("acquiring leadership", "error", err)
		case acquired:
			// Record the time we spent stepped down (#311 observability). A
			// growing P99 here surfaces leader churn that affects scheduling
			// latency, operators alert on the histogram, not log content.
			sched.RecordReacquireSince(stepDownAt)
			sched.ClearSteppingDown()
			runAsLeader(ctx, leader, sched, logger)
			if ctx.Err() != nil {
				return // shutting down, not a leadership loss
			}
			stepDownAt = time.Now()
			logger.Warn("stepped down as scheduler leader; re-campaigning")
		default:
			logger.Info("scheduler follower; retrying for leadership")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runAsLeader runs the scheduling loop while leadership is held. A watchdog
// revalidates the advisory lock and cancels the loop the moment leadership is
// lost, so a stale leader (whose lock-holding session died) steps down instead
// of double-scheduling against the new leader. It signals leadership to the
// scheduler so the heartbeat/health reflects whether this instance is the
// active scheduler.
func runAsLeader(ctx context.Context, leader *Leader, sched *Scheduler, logger *slog.Logger) {
	logger.Info("became scheduler leader")
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sched.SetLeading(true)
	defer sched.SetLeading(false)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The watch passes the step-down reason to the scheduler BEFORE
		// canceling the run-context, so the reapers/Step that are about to
		// return ctx-canceled log at WARN ("expected during step-down") not
		// ERROR (#311). A nil scheduler is tolerated by tests.
		watchLeadership(runCtx, leader, leaderCheckInterval, cancel, logger,
			func(reason string) { sched.MarkSteppingDown(reason) })
	}()

	if runErr := sched.Run(runCtx); runErr != nil && !errors.Is(runErr, context.Canceled) {
		logger.Error("scheduler stopped", "error", runErr)
	}
	cancel()
	<-done
	releaseLeader(leader, logger) //nolint:contextcheck // release uses a fresh bounded context
}

// watchLeadership cancels the run when leadership is lost: a definitive "not
// held" steps down immediately, while transient check errors are tolerated up to
// maxLeaderCheckFailures (a connection blip should not churn leadership). Each
// check runs under a deadline of one interval, so a check that hangs on a
// black-holed session is a failure too: the leader steps down within about
// maxLeaderCheckFailures intervals of losing its database (#1599). It returns
// when the run context is canceled. The onStepDown callback fires
// once, BEFORE cancel(), with the step-down reason so the scheduler can
// classify the cancel fanout (#311) and increment the per-reason counter.
func watchLeadership(ctx context.Context, chk leadershipChecker, interval time.Duration, cancel context.CancelFunc, logger *slog.Logger, onStepDown func(reason string)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failures := 0
	stepDown := func(reason string) {
		if onStepDown != nil {
			onStepDown(reason)
		}
		cancel()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			held, err := checkLeadership(ctx, chk, interval)
			switch {
			case err != nil:
				if ctx.Err() != nil {
					return // canceled mid-check (shutdown/stepdown)
				}
				failures++
				logger.Warn("leadership check failed", "error", err, "consecutive", failures)
				if failures >= maxLeaderCheckFailures {
					logger.Warn("too many failed leadership checks; stepping down",
						"reason", "check_timeout", "consecutive_failures", failures)
					stepDown("check_timeout")
					return
				}
			case !held:
				logger.Warn("lost scheduler advisory lock; stepping down",
					"reason", "lock_released")
				stepDown("lock_released")
				return
			default:
				failures = 0
			}
		}
	}
}

func releaseLeader(leader *Leader, logger *slog.Logger) {
	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := leader.Release(rctx); err != nil { //nolint:contextcheck // fresh context to release after shutdown
		logger.Error("releasing leadership", "error", err)
	}
}
