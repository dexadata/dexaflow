package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/dexadata/dexaflow/internal/agent"
	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// attemptTokenTTL is the short per-attempt agent-token lifetime minted at
// dispatch (ADR 0055 Fix #4), derived from the heartbeat interval so a live task
// refreshes its credential on every beat (see agentrpc.Server.Heartbeat) while a
// stolen or finished token lapses in minutes. It replaces the former flat 24h
// TTL at the dispatch site; the total renewed lifetime is separately capped by
// auth.max_attempt_credential_lifetime.
var attemptTokenTTL = agent.AttemptTokenTTL(agent.DefaultHeartbeatInterval)

// resilienceLadder assembles the effective timing knobs the control-plane
// restart recovery depends on, exactly as this binary wires them: the agent's
// heartbeat interval and derived token TTL, the reaper thresholds/graces, the
// reconciler sweep, the scheduler's longest infra re-place delay, and the
// operator's credential-lifetime ceiling, the one rung a deployment can move.
// validateStartup checks it before anything starts.
func resilienceLadder(cfg *config.ServerConfig) executor.ResilienceLadder {
	rc := executor.DefaultReaperConfig()
	return executor.ResilienceLadder{
		HeartbeatInterval:            agent.DefaultHeartbeatInterval,
		AgentLostThreshold:           rc.AgentLostThreshold,
		SettlingGrace:                rc.SettlingGrace,
		AttemptTokenTTL:              attemptTokenTTL,
		ReconcileInterval:            reconcileInterval,
		OrphanThreshold:              rc.OrphanThreshold,
		InfraReplaceMaxDelay:         scheduler.InfraReplaceMaxDelay(),
		MaxAttemptCredentialLifetime: cfg.Auth.MaxAttemptCredentialLifetime,
		TaskTerminationGrace:         time.Duration(corev1.DefaultTerminationGracePeriodSeconds) * time.Second,
		InfraConfirmMaxWait:          scheduler.InfraConfirmMaxWait,
		DispatchLostThreshold:        rc.DispatchLostThreshold,
	}
}

// redispatchStore is the seam the warm reclaim observer uses to re-place an
// attempt (ADR 0058 N1d-c, H2). *storage.ExecutionStore satisfies it; a fake
// records the calls in tests.
type redispatchStore interface {
	RequeueForRedispatch(ctx context.Context, runID, taskID string, tryNumber, attemptEpoch int) error
}

// reclaimShouldRequeue reports whether a reclaimed warm assignment may be
// immediately re-placed for fast recovery (ADR 0058 N1d-c, H2). ONLY the reasons
// where the worker demonstrably will NOT run the attempt qualify, so re-placing
// cannot double-run:
//   - ReclaimWorkerGone: the worker's stream ended holding an unacked lease; a
//     disconnected worker can never run it.
//   - ReclaimRefused: the worker acked started=false, it explicitly declined.
//
// ReclaimLeaseExpired is deliberately NOT re-placed: a lease-expired worker may
// be alive-but-slow and, by agent order, will still ack-then-run the attempt, so
// fast-re-placing it could double-run. That case is recovered double-run-free by
// the 3-minute dispatch-lost reaper + the H3 warm-defer already merged.
func reclaimShouldRequeue(reason agentrpc.ReclaimReason) bool {
	switch reason {
	case agentrpc.ReclaimWorkerGone, agentrpc.ReclaimRefused:
		return true
	default: // ReclaimLeaseExpired and any future reason: recover via the reaper.
		return false
	}
}

// handleReclaim is the warm reclaim observer wired into the registry (ADR 0058
// N1d-c, H2). It logs every reclaim, then re-places the attempt only for the
// double-run-safe reasons (reclaimShouldRequeue). Re-placement is best-effort:
// an error is logged, never fatal, so a transient DB blip cannot crash the
// registry's emit path.
func handleReclaim(ctx context.Context, store redispatchStore, logger *slog.Logger, ev agentrpc.ReclaimEvent) {
	logger.Debug("warm worker assignment reclaimed", "assignment", ev.AssignmentID, "dag_version", ev.DagVersionID, "reason", ev.Reason)
	if !reclaimShouldRequeue(ev.Reason) {
		return
	}
	if err := store.RequeueForRedispatch(ctx, ev.RunID, ev.TaskID, ev.TryNumber, ev.AttemptEpoch); err != nil {
		logger.Error("warm reclaim re-placement failed", "run", ev.RunID, "task", ev.TaskID, "try", ev.TryNumber, "err", err)
	}
}

// executorDispatchEnabled decides what /api/v2/monitor/executor reports for
// pod_dispatch_enabled. The scheduler owns the executor, so on the scheduler/all
// role the accurate runtime signal (whether a K8s client actually wired up) is
// right. In the split api role (ADR 0049) the scheduler runs in another process,
// so the runtime bool is always false here, reporting that would tell an
// operator dispatch is off when it isn't (F1). Fall back to the configured
// capability (executor.type), the config-derived ExecutorInfo the ADR calls for.
func executorDispatchEnabled(cfg *config.ServerConfig, servesScheduler, runtimePodDispatch bool) bool {
	if servesScheduler {
		return runtimePodDispatch
	}
	return cfg.Executor.Type == "kubernetes"
}

// setupDispatch wires the pod-path executor selected by executor.type onto the
// scheduler and returns whether pod dispatch is active. "subprocess" runs the
// agent on the host (dev only); "kubernetes" (default) launches task pods. An
// error means the selected executor cannot be wired and the server must not
// boot.
func setupDispatch(ctx context.Context, cfg *config.ServerConfig, sched *scheduler.Scheduler, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, store *storage.SchedulerStore, warmPools *agentrpc.WorkerRegistry, logSink logs.Sink, logger *slog.Logger, metrics *observability.Metrics) (bool, io.Closer, error) {
	if cfg.Executor.Type == "subprocess" {
		on, closer := setupSubprocessDispatch(ctx, cfg, sched, execStore, authn, warmPools, store, logSink, logger, metrics) //nolint:contextcheck // buffered worker deliberately detaches from caller ctx
		return on, closer, nil
	}
	return setupK8sDispatch(ctx, cfg, sched, execStore, authn, store, warmPools, logSink, logger, metrics) //nolint:contextcheck // buffered worker deliberately detaches from caller ctx
}

// configureSubprocessDispatcher applies cfg to the dev-only subprocess
// dispatcher. warmPolicy describes what it makes of a task, for the register
// response.
func configureSubprocessDispatcher(dispatcher *dispatch.Dispatcher, cfg *config.ServerConfig, warmPools *agentrpc.WorkerRegistry) {
	dispatcher.SetPlatformDefaults(platformDefaults(cfg.Executor.Defaults))
	setWarmPlacer(dispatcher, warmPools)
}

// configureK8sDispatcher applies cfg to the pod-path dispatcher. warmPolicy
// describes what it makes of a task, for the register response.
func configureK8sDispatcher(dispatcher *dispatch.Dispatcher, cfg *config.ServerConfig, warmPools *agentrpc.WorkerRegistry, metrics *observability.Metrics) {
	dispatcher.SetAgentTLSCAConfigMap(cfg.Executor.AgentTLSCAConfigMap)
	dispatcher.SetTaskSecret(cfg.Executor.TaskSecretName, cfg.Executor.TaskSecretMountPath)
	dispatcher.SetDefaultTaskServiceAccount(cfg.Executor.TaskServiceAccount)
	// Agent-token transport (ADR 0055 Fix #3). Under the exchange transport the
	// executor projects a ServiceAccount token instead of the plaintext one; the
	// default (envvar) leaves the pod spec unchanged. The audience is the shared
	// const the control-plane TokenReviewer validates against; expiration floors in
	// BuildPod.
	dispatcher.SetAgentTokenTransport(cfg.Auth.AgentTokenTransport, executor.DefaultAgentTokenAudience, 0)
	k8sDefaults := platformDefaults(cfg.Executor.Defaults)
	k8sDefaults.Unit = resourceUnit(cfg)
	dispatcher.SetPlatformDefaults(k8sDefaults)
	if metrics != nil {
		dispatcher.SetUnitMisfitRecorder(metrics)
	}
	// Deadline floor for task pods that declare no execution timeout: the agent's
	// reports retry for as long as the control plane is unreachable, so a pod
	// with no deadline of its own would outlive a total outage indefinitely. The
	// credential ceiling is the natural bound (past it the pod cannot renew its
	// bearer), and it is the same knob the warm worker's attempt watchdog uses.
	dispatcher.SetAttemptLifetimeCeiling(cfg.Auth.MaxAttemptCredentialLifetime)
	// External secrets backend (ADR 0060, Pro pod path): deliver the operator config
	// to task pods as LEOFLOW_SECRETS_* env. Empty backend → podEnv skips it (chain
	// stays vault-only). The D6 registration relaxation is wired separately in run()
	// where the Repository is in scope.
	dispatcher.SetSecretsBackend(cfg.Secrets.Backend, secretsKwargsJSON(cfg.Secrets))
	// Pro source mode (ADR 0067 §3): a version on the runtime image runs from
	// its registered dag.py. "" (the default) keeps it off.
	dispatcher.SetSourceModeImage(cfg.Execution.SourceMode.RuntimeImage())
	// Warm placement seam (ADR 0058 N1b1-place): the dispatcher Assign()s onto the
	// SAME registry the gRPC handler serves. nil when warm pools are off.
	setWarmPlacer(dispatcher, warmPools)
}

// taskPodSettings is how the dispatcher starts task pods, which the
// register dry run reports (#1889): podDispatch is
// ExecutorInfo.PodDispatchEnabled. The ServiceAccounts of the task namespace
// are read with a client newClient builds, only by a process that holds the
// executor's Role, which grants that read (ADR 0085): the all role. The api
// role of a split control plane has no access to the cluster (ADR 0049), so
// it reports every account unchecked, with the reason.
func taskPodSettings(cfg *config.ServerConfig, podDispatch bool, newClient func() (kubernetes.Interface, error), logger *slog.Logger) api.TaskPodSettings {
	s := api.TaskPodSettings{
		Enabled:               podDispatch,
		RunAsNonRoot:          cfg.Executor.Defaults.RunTasksAsNonRoot,
		DefaultServiceAccount: cfg.Executor.TaskServiceAccount,
	}
	switch {
	case !podDispatch:
		return s
	case !cfg.Server.ServesScheduler():
		s.Unread = "the API role of a split control plane has no access to the cluster (ADR 0049)"
		return s
	}
	cs, err := newClient()
	if err != nil {
		logger.Warn("the register dry run cannot look up task ServiceAccounts", "error", err)
		s.Unread = "the control plane has no Kubernetes client"
		return s
	}
	s.ServiceAccounts = executor.NewServiceAccounts(cs, cfg.Executor.TaskNamespace)
	return s
}

// resolveAgentControlAddr returns the address task agents dial back, defaulting
// to the server's own gRPC address.
func resolveAgentControlAddr(cfg *config.ServerConfig) string {
	if cfg.Executor.AgentControlPlaneAddr != "" {
		return cfg.Executor.AgentControlPlaneAddr
	}
	return cfg.Server.GRPCAddr
}

// setupSubprocessDispatch wires the dev-only subprocess executor (ADR 0023): it
// runs the agent on the host with no isolation, so it is gated to dev use. It
// also starts Lite's maintenance loop (#916): the reapers that mean something
// without pods, gated on the agent process's liveness (see newLiteReaper).
func setupSubprocessDispatch(ctx context.Context, cfg *config.ServerConfig, sched *scheduler.Scheduler, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, warmPools *agentrpc.WorkerRegistry, store *storage.SchedulerStore, logSink logs.Sink, logger *slog.Logger, metrics *observability.Metrics) (bool, io.Closer) {
	subExec := executor.NewSubprocessExecutor(cfg.Executor.AgentPath, logger)
	subExec.SetWorkDir(cfg.Executor.SubprocessWorkDir)
	dispatcher := dispatch.NewDispatcher(subExec, execStore, authn, resolveAgentControlAddr(cfg), attemptTokenTTL)
	configureSubprocessDispatcher(dispatcher, cfg, warmPools)
	disp, closer := wrapBuffered(dispatcher, store, logger, metrics, cfg.Scheduler.Dispatch) //nolint:contextcheck // buffered worker deliberately detaches from caller ctx
	sched.SetDispatcher(disp)
	var markers logs.MarkerSink
	if ms, ok := logSink.(logs.MarkerSink); ok {
		markers = ms
	}
	reaper := newLiteReaper(store, subExec, sched, markers, metrics, logger, cfg.Auth.MaxAttemptCredentialLifetime)
	startLiteMaintenance(ctx, reaper, sched.IsLeading, logger)
	logger.Warn("subprocess dispatch enabled (dev only; user code runs unsandboxed)")
	return true, closer
}

// setupK8sDispatch wires the production pod-per-task executor.
//
// When no Kubernetes client can be built it FAILS CLOSED and returns an error,
// so the server refuses to boot (#704, the sibling of #700). It used to log a
// warning and leave pod dispatch unwired, so a scheduler on the Kubernetes
// executor reported itself healthy while every task it planned failed as
// undispatchable. A deployment without a cluster selects
// executor.type=subprocess, which needs no client.
func setupK8sDispatch(ctx context.Context, cfg *config.ServerConfig, sched *scheduler.Scheduler, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, store *storage.SchedulerStore, warmPools *agentrpc.WorkerRegistry, logSink logs.Sink, logger *slog.Logger, metrics *observability.Metrics) (bool, io.Closer, error) {
	clients, perr := buildK8sClients(cfg.Executor.KubeClient)
	if perr != nil {
		return false, nil, fmt.Errorf("executor type %q requires a Kubernetes client to dispatch task pods, but none is available (set executor.type=subprocess to run without a cluster): %w", cfg.Executor.Type, perr)
	}
	cs, mcs := clients.dispatch, clients.maintenance
	controlAddr := resolveAgentControlAddr(cfg)
	podExec := executor.NewKubernetesExecutor(cs, cfg.Executor.TaskNamespace)
	podExec.SetStagingStore(store) // record per-run staging volumes in the metadatabase (ADR 0022)
	// Meter a reap teardown that could not stop a started pod in place and
	// deleted it instead (ADR 0052 amendment).
	if metrics != nil {
		podExec.SetTeardownRecorder(metrics)
		// Count every task pod create and observe its API latency (#1186).
		podExec.SetPodMetrics(metrics)
	}
	dispatcher := dispatch.NewDispatcher(podExec, execStore, authn, controlAddr, attemptTokenTTL)
	configureK8sDispatcher(dispatcher, cfg, warmPools, metrics)
	disp, closer := wrapBuffered(dispatcher, store, logger, metrics, cfg.Scheduler.Dispatch) //nolint:contextcheck // buffered worker deliberately detaches from caller ctx
	sched.SetDispatcher(disp)
	// Shared informer read-path (PR-10): one long-lived watch replaces the
	// reapers' per-running-TI LIST storm and the reconciler's 30s LIST. Consulted
	// only to DEFER a reap; the live TaskPodPresence kill path is unchanged (#461).
	// A typed-nil pointer would defeat the reconciler's / reaper's nil check, so
	// the interfaces are only populated when the informer actually built.
	podInformer := buildPodInformer(ctx, cfg, mcs, logger)
	var (
		snapshotter executor.PodSnapshotter
		cache       executor.PodPresenceCache
	)
	if podInformer != nil {
		snapshotter = podInformer
		cache = podInformer
	}
	// Warm failover seam (ADR 0058 N1d-a2): one warm-pod client, built ONLY when
	// warm pools are enabled, is shared as the live-warm-pod lister for the reaper
	// (the warm-worker-lost recovery + the dispatch-lost H3 defer) AND as the
	// reconciler's create/delete/list client. With the flag off it stays a nil
	// interface, so both warm reaper paths are inert and no warm pod is ever
	// created, dispatch is byte-for-byte today's dedicated pod-per-task.
	var warmLister executor.WarmPodLister
	var warmPods *executor.KubernetesWarmPods
	if cfg.Execution.WarmPoolsEnabled {
		warmPods = executor.NewKubernetesWarmPods(mcs, cfg.Executor.TaskNamespace, warmPodSpecFunc(cfg, authn, controlAddr))
		warmLister = warmPods
	}
	// The execution reaper (#120/#128/#202/#527) fails stuck runs and TIs; it
	// tears down a reaped task's pod and gates the dispatch-lost decision on real
	// pod liveness (#474, #461). Lite builds its own reaper in
	// setupSubprocessDispatch, gated on agent process liveness instead (#916).
	// The reaper's live pod reads and deletes go through the maintenance client,
	// which is podExec's own client unless maintenance limits are set.
	var reapPods executor.PodManager = podExec
	if mcs != cs {
		reapPods = executor.NewKubernetesExecutor(mcs, cfg.Executor.TaskNamespace)
	}
	reaper := executor.NewReaper(store, reapPods, cache, warmLister, metrics, logger, executor.DefaultReaperConfig(), sched.SteppingDown)
	// The same ceiling that floors the task pod's deadline and sets the warm
	// attempt watchdog: a silent attempt older than it fails for the credential
	// ceiling, as a task failure, instead of being re-placed as agent_lost (#1461).
	reaper.SetAttemptLifetimeCeiling(cfg.Auth.MaxAttemptCredentialLifetime)
	// Under the env-var transport the attempt credential is minted at dispatch
	// and sits on the pod spec, so its TTL runs while the pod is still Pending.
	// A pod still Pending as it runs out is re-placed with a fresh one instead
	// of starting with a dead credential (#1126). The exchange transport mints
	// the credential when the agent starts, so nothing runs out while it waits.
	if cfg.Auth.AgentTokenTransport != config.AgentTokenTransportExchange {
		reaper.SetAttemptTokenTTL(attemptTokenTTL)
	}
	// Give the reaper an append-aware marker sink so a reaped attempt's log ends
	// with a "killed: agent_lost" or "killed: pod_lost" marker instead of a
	// silent truncation (#861).
	// Both DiskSink and ObjectSink implement MarkerSink (append preserves the
	// agent's streamed content on either backend); the assertion holds for every
	// sink NewDurableSink returns.
	var markers logs.MarkerSink
	if ms, ok := logSink.(logs.MarkerSink); ok {
		markers = ms
		reaper.SetLogSink(ms)
	}
	// Leader-settling gate: no reaper fires until this instance has led for the
	// settling grace, the pod informer has synced, and the reconciler has
	// completed a sweep under this leadership (wired in startMaintenance). A
	// control-plane restart makes heartbeats look stale and finished task pods
	// look lost; the gate holds every reaper until the true outcomes have been
	// recovered, with a liveness valve so a broken sweep cannot disable reaping.
	reaper.SetLeaderSince(sched.LeaderSince)
	if podInformer != nil {
		reaper.SetInformerSynced(podInformer.HasSynced)
	}
	// Close the reaper's destructive gate the moment this instance stops leading:
	// together with the step-down flag and the run-context cancel, this keeps a
	// draining or stepping-down leader from marking TIs failed or deleting pods
	// on its way out, the successor redoes the reap under its own settling gate.
	reaper.SetLeading(sched.IsLeading)
	// Infra marks are provisional here, because this reconciler confirms them
	// (ADR 0052 amendment, part 2). Lite never reaches this and keeps
	// confirming at mark time.
	store.SetProvisionalInfraMarks(true)
	startMaintenance(ctx, mcs, cfg.Executor.TaskNamespace, execStore, store, overrideObservers{metrics: metrics, sink: markers}, reaper, sched.IsLeading, logger, snapshotter, settledRunCollection(cfg.Executor, execStore))
	startStagingGC(ctx, mcs, cfg.Executor.TaskNamespace, store, sched.IsLeading, logger)
	// Warm-pool reconciler (ADR 0058 N1b2b, model A2): keeps min_idle warm workers
	// ready per active dag_version. Started ONLY when warm pools are enabled, with
	// them off it is never constructed, so no warm pod is ever created and dispatch
	// stays byte-for-byte today's dedicated pod-per-task.
	if cfg.Execution.WarmPoolsEnabled {
		store.SetWarmExecution(cfg.Execution, executionPolicy(cfg.Executor.Defaults), dispatcher.WarmPolicy())
		// The busy set (running attempts durably bound to a warm worker) makes the
		// reconciler busy-aware (ADR 0058 N1d-b): it scales the IDLE buffer and never
		// deletes a worker with an in-flight attempt. store implements it.
		var warmCache *executor.WarmPodInformer
		if cfg.Execution.WarmPoolEventRefill {
			// On the maintenance client, like the task-pod informer and the
			// warm-pod client the reconciler checks it against (#1315).
			warmCache = buildWarmPodInformer(ctx, mcs, cfg.Executor.TaskNamespace, warmPods, logger)
			if warmCache != nil && warmPools != nil {
				// A claimed worker leaves the idle buffer short: refill now.
				warmPools.SetOnClaim(warmCache.Kick)
			}
		}
		startWarmPoolReconciler(ctx, store, warmPods, store, cfg.Execution.MaxWarmPodsPerTenant, warmCache, sched.IsLeading, metrics, logger)
		logger.Warn("warm pool reconciler enabled (ADR 0058 N1b2b); maintaining min_idle warm workers per active dag_version", "namespace", cfg.Executor.TaskNamespace)
	}
	logger.Info("pod dispatch enabled", "namespace", cfg.Executor.TaskNamespace, "agent_control_plane_addr", controlAddr,
		"kube_qps", cfg.Executor.KubeClient.QPS, "kube_burst", cfg.Executor.KubeClient.Burst,
		"separate_maintenance_client", mcs != cs)
	return true, closer, nil
}

// wrapBuffered returns the dispatcher to plug into the scheduler. When
// BufferSize > 0 the inner dispatcher is fronted by the worker pool (#127);
// when BufferSize == 0 the inner dispatcher is used directly (Lite). The
// caller passes a FailureSink (typically the SchedulerStore) so worker-side
// dispatch failures are re-offered or fail the TI with a clear reason instead
// of leaving it stuck `queued`.
// The io.Closer is non-nil only in buffered mode; the caller defers Close() on
// shutdown so in-flight dispatches drain (workers finish or fail via the sink)
// instead of leaking goroutines and leaving TIs stuck `queued` (#133).
func wrapBuffered(inner dispatch.Inner, sink dispatch.FailureSink, logger *slog.Logger, metrics *observability.Metrics, cfg config.DispatchSection) (scheduler.Dispatcher, io.Closer) {
	if cfg.BufferSize <= 0 {
		// Passthrough: keep the inner dispatcher exposed verbatim so the
		// scheduler sees the same surface it always did in Lite. No pool to close.
		return inner, nil
	}
	// A store that can re-offer (the SchedulerStore) gets the scheduler's
	// failure policy, so a worker-side dispatch failure is retried like a
	// synchronous one instead of failing the task at once.
	if st, ok := sink.(scheduler.AsyncDispatchStore); ok {
		sink = scheduler.NewAsyncDispatchFailures(st, logger)
	}
	bd := dispatch.NewBuffered(inner, sink, logger, metrics, dispatch.BufferConfig{
		BufferSize: cfg.BufferSize,
		Workers:    cfg.Workers,
	})
	logger.Info("buffered dispatch enabled (async pool)",
		"buffer_size", cfg.BufferSize, "workers", cfg.Workers)
	return bd, bd
}

// The SchedulerStore passed to wrapBuffered must keep satisfying
// AsyncDispatchStore: the type assertion there would otherwise fall back to
// failing every worker-side dispatch error at once, silently.
var _ scheduler.AsyncDispatchStore = (*storage.SchedulerStore)(nil)
