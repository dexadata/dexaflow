package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/alerts"
	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/failurealert"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/xcom"
)

// startSchedulerSide starts the scheduler-role components (ADR 0049): the agent
// gRPC endpoint, the XCom/log janitors, and, when cfg.Scheduler.Enabled, the
// scheduler loop + dispatch pool. It returns the scheduler's health handle (nil
// when the loop is off; the API reports scheduler health as healthy on nil),
// whether pod dispatch is on, and a stop func to defer. When servesScheduler is
// false (the api role) it starts nothing and returns a nil handle + no-op stop, so
// the caller defers unconditionally. The stop func fires drain-then-gRPC-stop,
// matching the pre-0049 defer order (LIFO: drainDispatch before the bounded
// graceful stop, see stopGRPCWithin).
func startSchedulerSide(ctx context.Context, cfg *config.ServerConfig, pg *storage.Postgres, repo *storage.Repository, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, xcomSvc *xcom.Service, logSink logs.Sink, logTailer logs.Tailer, allowInsecureSecrets bool, servesScheduler bool, logger *slog.Logger, metrics *observability.Metrics) (health api.Heartbeater, podDispatch bool, stop func(), err error) {
	if !servesScheduler {
		return nil, false, func() {}, nil
	}
	// Warm-worker assignment registry (ADR 0058 N1b1-place). Built ONCE here, only
	// when warm pools are enabled, and shared between the gRPC handler (workers
	// register/ack on it via SetWarmPools) and the dispatcher's placer (Assign).
	// nil when warm pools are off => the handler stays inert (FailedPrecondition)
	// and the dispatcher's placer stays nil, so every path is byte-for-byte
	// today's dedicated pod-per-task; nothing reclaims and RequeueForRedispatch is
	// never called. The reclaim observer (ADR 0058 N1d-c, H2) re-places reclaimed
	// attempts through execStore for the double-run-safe reasons only.
	var warmReg *agentrpc.WorkerRegistry
	if cfg.Execution.WarmPoolsEnabled {
		warmReg = agentrpc.NewWorkerRegistry(func(ev agentrpc.ReclaimEvent) {
			handleReclaim(ctx, execStore, logger, ev)
		})
		logger.Warn("warm worker pools enabled (ADR 0058 N1b1-place); admitted tasks place on a free warm worker of their dag_version, else dispatch dedicated")
		requireWarmSweep(cfg, warmReg, metrics)
	}
	// Fail closed (#700): when the operator selected the exchange transport, the
	// Kubernetes client is a hard prerequisite. buildTokenExchange returns an
	// error here rather than leaving the exchange unwired, so the server refuses
	// to boot instead of advertising itself healthy while every task pod's
	// ExchangeToken bootstrap fails Unimplemented.
	xchg, xerr := buildTokenExchange(cfg, func() (kubernetes.Interface, error) {
		return buildK8sClient(cfg.Executor.KubeClient)
	})
	if xerr != nil {
		return nil, false, nil, xerr
	}
	// The bounded graceful stop reports how many handlers it left running, since
	// a log writer's final Put outlasts the post-force wait and an abandoned one
	// is otherwise silent. gRPC exposes no such number, so the counter rides on
	// the interceptor chain and the stop func reads it.
	inflight := agentrpc.NewInflightHandlers()
	grpcSrv, agentSrv, gerr := startAgentGRPC(ctx, cfg.Server.GRPCAddr, authn, execStore, repo, xcomSvc, logSink, logTailer, cfg.Logs.Tail.Publish == config.LogTailPublishOnDemand, allowInsecureSecrets, cfg.Auth.SecretScoping, cfg.Auth.SecretLivenessMode, cfg.Auth.MaxAttemptCredentialLifetime, xchg, cfg.Server.GRPCTLSCert, cfg.Server.GRPCTLSKey, warmReg, inflight, metrics, logger)
	if gerr != nil {
		return nil, false, nil, gerr
	}
	// Meter task tokens that predate the attempt_epoch claim (ADR 0051 amendment)
	// and refused agent bearers by reason (#1125).
	if metrics != nil {
		agentSrv.SetLegacyTokenRecorder(metrics)
		agentSrv.SetTokenRejectionRecorder(metrics)
	}
	// The scheduler loop, its reapers and the janitors run on their own pool
	// when database.scheduler_max_conns is set, so API traffic cannot starve
	// them; unset, schedPG is pg and nothing changes. The agent gRPC handlers
	// above stay on the main pool with the repository they share with the API.
	schedPG, releaseSchedPG, perr := pg.ForScheduler(ctx, schedulerDatabase(cfg))
	if perr != nil {
		stopGRPCWithin(grpcSrv, grpcStopTimeout, logger, inflight.Count)
		return nil, false, nil, fmt.Errorf("scheduler pool: %w", perr)
	}
	schedExec := execStore
	if schedPG != pg {
		schedExec = storage.NewExecutionStore(schedPG)
		logger.Info("scheduler uses a dedicated database pool", "max_conns", cfg.Database.SchedulerMaxConns)
	}
	// XCom-TTL and log-retention janitors are maintenance the scheduler owns; the
	// api role runs no background writers.
	startCleanup(ctx, storage.NewXComIndex(schedPG), logSink, cfg.Logs.Dir, logger)

	drain := func() {}
	if cfg.Scheduler.Enabled {
		sched, dispatchOn, dispatchCloser, serr := startScheduler(ctx, cfg, schedPG, repo, schedExec, authn, warmReg, logSink, logger, metrics)
		if serr != nil {
			releaseSchedPG()
			// Bounded, like every other stop of this server. At boot no stream is
			// open yet, so the unbounded form could not actually hang here, but a
			// second way to stop the same server is a way for the two to drift, and
			// the one that cannot exhaust the pod's grace is the one to keep.
			stopGRPCWithin(grpcSrv, grpcStopTimeout, logger, inflight.Count)
			return nil, false, nil, serr
		}
		// Warm-pool Hole B: gate the assignment stream to the scheduler LEADER. This
		// replica gates on ITS OWN leadership (sched.IsLeading), so behind one Service
		// exactly the leader, whose leader-only placer consults the SAME in-memory
		// registry workers register into here, serves AwaitAssignment; a follower
		// refuses so the worker reconnects toward the leader. Only with warm pools on;
		// off, the handler already refuses before the gate and nothing changes.
		if cfg.Execution.WarmPoolsEnabled {
			agentSrv.SetLeaderCheck(sched.IsLeading)
		}
		// scheduler.eager_promotion: a terminal report wakes this replica's
		// scheduler for an early tick. Only a leader acts on it; a woken follower
		// runs its usual empty tick.
		if cfg.Scheduler.EagerPromotion {
			agentSrv.SetSettledHook(sched.Wake)
		}
		// On shutdown, drain the buffered dispatch pool (if any) so in-flight
		// dispatches settle (success or failed via the sink) instead of leaking
		// workers and leaving TIs stuck `queued` (#133). nil in Lite/passthrough.
		drain = func() { drainDispatch(dispatchCloser, logger) }
		// On the scheduler pool, with the loop and the other janitors: off the
		// API pool and its statement_timeout (#1339).
		startRetention(ctx, cfg.Retention, storage.NewRetentionStore(schedPG), sched.IsLeading, metrics, logger)
		health = sched
		podDispatch = dispatchOn
	}
	stop = func() {
		drain()
		stopGRPCWithin(grpcSrv, grpcStopTimeout, logger, inflight.Count)
		releaseSchedPG()
	}
	return health, podDispatch, stop, nil
}

// leaderTickPoll is how often a running leader-gated cycle re-checks leadership
// (runLeaderTick). The leadership watch polls every 5 seconds, so one second
// adds little to how long a step-down takes to stop the cycle.
const leaderTickPoll = time.Second

// runLeaderTick runs one cycle of a leader-gated loop that mutates cluster
// state (the warm-pool reconcile, the staging GC) under a context that ends at
// the cycle's budget or as soon as leading() reports false (#1602). The gated
// ticker only checks leadership when a tick starts, so without this a cycle in
// progress at a step-down kept creating and deleting pods next to the new
// leader's own loop, and one database call hung on a dead connection could hold
// it indefinitely. Both loops are level-triggered, so a cycle cut short is
// finished by the next one. A nil leading means always leading. An error is
// logged, except once the parent context is done (shutdown).
func runLeaderTick(ctx context.Context, name string, budget, poll time.Duration, leading func() bool, logger *slog.Logger, fn func(context.Context) error) {
	tctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	if leading != nil {
		go func() {
			t := time.NewTicker(poll)
			defer t.Stop()
			for {
				select {
				case <-tctx.Done():
					return
				case <-t.C:
					if !leading() {
						cancel()
						return
					}
				}
			}
		}()
	}
	if err := fn(tctx); err != nil && ctx.Err() == nil {
		logger.Error(name, "error", err)
	}
}

func startScheduler(ctx context.Context, cfg *config.ServerConfig, pg *storage.Postgres, repo *storage.Repository, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, warmPools *agentrpc.WorkerRegistry, logSink logs.Sink, logger *slog.Logger, metrics *observability.Metrics) (*scheduler.Scheduler, bool, io.Closer, error) {
	// Built before the leader pool so a bad alert egress config cannot leak it.
	alertClient, err := alertHTTPClient(cfg.Scheduler.Alerts)
	if err != nil {
		return nil, false, nil, err
	}
	leaderPool, err := storage.NewLeaderPool(ctx, cfg.Database)
	if err != nil {
		return nil, false, nil, fmt.Errorf("leader pool: %w", err)
	}
	store := storage.NewSchedulerStore(pg)
	sched := scheduler.NewScheduler(store, logger,
		time.Duration(cfg.Scheduler.LoopIntervalMS)*time.Millisecond)
	sched.SetRecorder(metrics)
	// Bound the dispatch work of one tick, and of one tenant in a tick, so a
	// wide fan-out cannot stall every other run and cron creation (#1596).
	sched.SetDispatchTickBudget(cfg.Scheduler.Dispatch.TickBudget)
	sched.SetDispatchTenantLimit(cfg.Scheduler.Dispatch.MaxPerTenantPerTick)
	// Named pools are Pro-only (ADR 0053): enable the cross-DAG slot gate only on
	// the Pro edition. Left off on Lite/non-Pro, the tick never queries pool
	// budgets and planning is byte-identical to the max_active_tasks-only path.
	if cfg.UI.Edition == "pro" {
		sched.EnablePools()
		// A task held by its pool past the threshold reserves the pool, so
		// smaller tasks cannot starve it (ADR 0066 §4).
		sched.SetPoolStarvationThreshold(cfg.Scheduler.PoolStarvationThreshold)
		if cfg.Server.PoolsReadOnly {
			// Tenants cannot create pools, so an undefined pool name must not be
			// a way around default_pool (#646).
			sched.ConfineUndefinedPools()
			logger.Info("pools read-only: tasks naming an undefined pool draw on default_pool")
		}
	} else if cfg.Server.PoolsReadOnly {
		logger.Warn("server.pools_read_only has no effect: pools are a Pro edition feature")
	}
	// Off by default (ADR 0062): see config.SchedulerSection.EagerPromotion.
	if cfg.Scheduler.EagerPromotion {
		sched.EnableEagerPromotion()
		logger.Info("scheduler eager promotion enabled: same-tick promotion and early ticks on task completion")
	}
	// Native on-failure alerting (#424): the scheduler fires Slack/webhook rules
	// declared in dexaflow.yaml when a run finalizes failed, resolving each rule's
	// managed connection to its endpoint URL. Best-effort, off the tick path.
	sched.SetAlerter(failurealert.New(
		alerts.NewNotifier(alertClient),
		connEndpointResolver{repo},
		metrics,
		logger,
	))
	podDispatch, dispatchCloser, err := setupDispatch(ctx, cfg, sched, execStore, authn, store, warmPools, logSink, logger, metrics)
	if err != nil {
		leaderPool.Close()
		return nil, false, nil, err
	}
	leader := scheduler.NewLeader(leaderPool)
	go func() {
		defer leaderPool.Close()
		campaignAndRun(ctx, leader, sched, logger)
	}()
	return sched, podDispatch, dispatchCloser, nil
}

// drainDispatch closes the buffered dispatch pool on shutdown so in-flight
// dispatches drain instead of leaking workers / leaving TIs stuck `queued`
// (#133). Nil closer (Lite/passthrough) is a no-op; a close error is logged.
func drainDispatch(closer io.Closer, logger *slog.Logger) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		logger.Error("draining dispatch pool on shutdown", "error", err)
	}
}

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

// campaignAndRun acquires scheduler leadership (polling every leaderCheckInterval)
// and runs the loop only while leader, so a single replica schedules at a time
// (ADR 0009). If leadership is lost mid-run (the advisory lock dropped because the
// connection blipped, was idle-reaped, or recycled), it steps down and
// re-campaigns rather than scheduling alongside the new leader.
func campaignAndRun(ctx context.Context, leader *scheduler.Leader, sched *scheduler.Scheduler, logger *slog.Logger) {
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
func runAsLeader(ctx context.Context, leader *scheduler.Leader, sched *scheduler.Scheduler, logger *slog.Logger) {
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

func releaseLeader(leader *scheduler.Leader, logger *slog.Logger) {
	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := leader.Release(rctx); err != nil { //nolint:contextcheck // fresh context to release after shutdown
		logger.Error("releasing leadership", "error", err)
	}
}
