package main

import (
	"context"
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
		scheduler.CampaignAndRun(ctx, leader, sched, logger)
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
