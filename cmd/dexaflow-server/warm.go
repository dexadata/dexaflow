package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"k8s.io/client-go/kubernetes"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/gatedtick"
	"github.com/dexadata/dexaflow/internal/observability"
)

// warmSweepPolicy is how the warm registry treats a worker that does not
// confirm the /tmp and /dev/shm sweep (#1678). The sweep is done by the agent in
// the task image, so an image built on an older runtime runs with the read-only
// root while nothing empties those paths between attempts. With the read-only
// root on, such a worker is logged and metered, and refused only when
// execution.warm_require_attempt_home_sweep is also on: agents from v0.5.1 up to
// the release that added the report do sweep but cannot confirm it, so refusing
// by default would take warm pools away from images that are fine.
func warmSweepPolicy(cfg *config.ServerConfig) agentrpc.SweepPolicy {
	switch {
	case !cfg.Execution.WarmReadOnlyRootFilesystem:
		return agentrpc.SweepIgnore
	case cfg.Execution.WarmRequireAttemptHomeSweep:
		return agentrpc.SweepRefuse
	default:
		return agentrpc.SweepWarn
	}
}

// requireWarmSweep sets the warm registry's sweep policy from the config.
func requireWarmSweep(cfg *config.ServerConfig, reg *agentrpc.WorkerRegistry, metrics *observability.Metrics) {
	// A nil *Metrics boxed into the interface would be non-nil, so pass a nil
	// interface when metrics are off.
	var rec agentrpc.DecisionRecorder
	if metrics != nil {
		rec = metrics
	}
	reg.SetSweepPolicy(warmSweepPolicy(cfg), rec)
}

// startWarmPoolReconciler runs the leader-gated warm-pool reconciler (ADR 0058
// N1b2b + N1d-b, model A2): each tick it keeps EffectiveMinIdle IDLE warm workers
// ready per active dag_version, growing the pool past min_idle under load up to
// MaxPoolSize and deleting only idle/terminal workers (never a busy one). It reads
// the busy set (busy) once per tick to classify workers. Like the pod reconciler
// it mutates cluster state, so it runs on the leader alone via the gated ticker.
// It is only called when warm pools are enabled.
func startWarmPoolReconciler(ctx context.Context, targets executor.WarmTargetSource, pods executor.WarmPodClient, busy executor.BusyWarmWorkerSource, maxWarmPodsPerTenant int, warmCache *executor.WarmPodInformer, leading func() bool, rec executor.DecisionRecorder, logger *slog.Logger) {
	rc := executor.NewWarmPoolReconciler(targets, pods, busy, maxWarmPodsPerTenant, logger, rec)
	// Event-driven refill (execution.warm_pool_event_refill): read the fleet from
	// the warm-pod informer, reconcile on its change signals as well as on the
	// tick, and create in parallel. Without it, kicks stays nil and this is the
	// polling reconciler it always was.
	var kicks <-chan struct{}
	if warmCache != nil {
		rc.SetCache(warmCache, warmCreateParallelism)
		kicks = warmCache.Changes()
	}
	t := time.NewTicker(reconcileInterval)
	go func() {
		defer t.Stop()
		gatedtick.RunOrKick(ctx, "warm-pool-reconcile", t.C, kicks, warmKickMinGap, leading, logger, func() {
			gatedtick.RunLeaderTick(ctx, "warm pool reconcile", reconcileInterval, gatedtick.LeaderTickPoll, leading, logger, rc.Reconcile)
		})
	}()
}

// warmCreateParallelism bounds how many warm pod creates one event-driven
// reconcile issues at once: enough to refill a burst of claims in one or two
// apiserver round trips, few enough not to stampede the apiserver or the image
// registry when a large pool refills.
const warmCreateParallelism = 4

// warmKickMinGap is the least time between two event-driven warm-pool
// reconciles. Each one reads the busy set from the database and may read the
// fleet live, so a storm of pod events must not turn into a storm of those.
const warmKickMinGap = time.Second

// buildWarmPodInformer starts the warm-pod informer for event-driven refill and
// waits for its first sync within the same boot budget as the task-pod informer.
// A nil return (it could not be built) leaves the reconciler polling with a live
// LIST; an informer that has not synced is read live until it does. It records
// the final state of every warm worker it sees terminate or get deleted on
// warmPods, the client the warm-worker-lost reaper lists through, so the reaper
// names an OOM kill of a worker already deleted (#1835).
func buildWarmPodInformer(ctx context.Context, cs kubernetes.Interface, namespace string, warmPods *executor.KubernetesWarmPods, logger *slog.Logger) *executor.WarmPodInformer {
	wi, err := executor.NewWarmPodInformer(cs, namespace)
	if err != nil {
		logger.Warn("warm pod informer unavailable; warm pools keep polling refill", "error", err)
		return nil
	}
	wi.RecordTerminationsTo(warmPods)
	wi.Start(ctx)
	syncCtx, cancel := context.WithTimeout(ctx, podInformerSyncTimeout)
	defer cancel()
	if !wi.WaitForCacheSync(syncCtx) {
		logger.Warn("warm pod informer did not sync within the boot budget; the warm-pool reconciler reads pods live until it does",
			"namespace", namespace, "budget", podInformerSyncTimeout)
	}
	return wi
}

// warmPodSpecFunc returns the per-target warm-pod spec builder the warm-pod client
// calls on each create. It mints a fresh, UNIQUE bootstrap credential per pod
// (each worker registers as a distinct member of its dag_version pool) and stamps
// the connection + hardening fields.
//
// The bootstrap credential authorizes ONLY Register + AwaitAssignment (the warm
// control channel) and resolves NO secrets or task, a warm-worker-scoped token
// (ADR 0058 D2). It reaches the pod by the operator's auth.agent_token_transport:
//
//   - exchange (the transport the N1a boot guard forces on when warm pools are
//     enabled): the pod projects a ServiceAccount token and NO plaintext token; the
//     warm agent exchanges it once per connect, and the control plane, recognizing
//     the warm-worker LABEL, mints the worker-scoped JWT. This is the default and
//     the whole point of this change: the bootstrap credential is never a plaintext
//     env var on the pod.
//   - env-var (dev / explicit fallback): a worker-scoped JWT is minted here and
//     ridden in as a plaintext env var. Even this fallback is control-channel-only,
//     so a leaked warm bootstrap token can never read the vault.
//
// The per-attempt token, the one that resolves secrets, arrives in-band over
// AwaitAssignment and is exchange/liveness-gated upstream (N1b1/N1b2a).
func warmPodSpecFunc(cfg *config.ServerConfig, authn *auth.JWTAuthenticator, controlAddr string) executor.WarmPodSpecFunc {
	defaults := platformDefaults(cfg.Executor.Defaults)
	warmResources := resourceUnit(cfg).WarmResources()
	if warmResources == nil {
		// Without a unit, the default task limits (#537) size the warm pod, so a
		// task placed on a warm worker runs under the same cap as on a dedicated
		// pod. Nil (nothing configured) leaves warm pods unsized, as before.
		warmResources = defaults.Limits.WarmResources()
	}
	useExchange := cfg.Auth.AgentTokenTransport == config.AgentTokenTransportExchange
	return func(t executor.WarmTarget) (executor.WarmPodSpec, error) {
		// ActiveWarmTargets already drops a version whose image the execution
		// allowlist refuses (issue #537 C2); refusing here too means no warm pod
		// is built for such an image whatever the target source.
		if err := defaults.ExecutionPolicy.CheckImage(t.Image); err != nil {
			return executor.WarmPodSpec{}, err
		}
		spec := executor.WarmPodSpec{
			DagVersionID:        t.DagVersionID,
			Image:               t.Image,
			TenantID:            t.TenantID,
			ControlPlaneAddr:    controlAddr,
			AgentTLSCAConfigMap: cfg.Executor.AgentTLSCAConfigMap,
			ServiceAccount:      cfg.Executor.TaskServiceAccount,
			PodSecurity:         defaults.PodSecurity,
			// With executor.unit a warm pod is one unit, and the dispatcher only
			// places size-1 tasks without resources on it (ADR 0066 §3). Without
			// one it carries the default task limits, if any.
			Resources: warmResources,
			// Self-lifecycle caps (ADR 0058 D9/D10/D6/H3). The attempt watchdog is
			// anchored to the credential ceiling: an attempt can never validly outlive
			// its per-attempt credential, so max_attempt_credential_lifetime is the
			// natural hard upper bound and needs no separate operator knob.
			MaxAttemptsPerWorker:     cfg.Execution.MaxAttemptsPerWorker,
			MaxWorkerLifetimeSeconds: int64(cfg.Execution.MaxWorkerLifetime.Seconds()),
			WorkerIdleTTLSeconds:     int64(cfg.Execution.WorkerIdleTTL.Seconds()),
			AttemptWatchdogSeconds:   int64(cfg.Auth.MaxAttemptCredentialLifetime.Seconds()),
			// X3.2: a read-only root plus a per-attempt HOME, so nothing one attempt
			// writes to the image survives into the next attempt on this worker.
			ReadOnlyRootFilesystem: cfg.Execution.WarmReadOnlyRootFilesystem,
		}
		if useExchange {
			// Exchange transport: project an SA token, no plaintext bootstrap token.
			// BuildWarmPod (identity nil) projects the token and stamps the warm-worker
			// LABEL the exchange resolver keys on; the control plane mints the
			// worker-scoped JWT. Audience/expiration mirror the task-pod projected token
			// (executor floors the expiration).
			spec.AgentTokenTransport = config.AgentTokenTransportExchange
			spec.AgentTokenAudience = executor.DefaultAgentTokenAudience
			return spec, nil
		}
		// Env-var fallback: mint a WORKER-scoped (control-channel-only) bootstrap JWT
		// and ride it in as a plaintext env var. The unique worker id is the token
		// Subject; the scope claim makes AwaitAssignment accept it and every secret/
		// task RPC reject it.
		token, err := authn.IssueAgentToken(auth.AgentIdentity{
			Scope:        auth.ScopeWarmWorker,
			WorkerID:     "warm-" + t.DagVersionID + "-" + uuid.NewString(),
			DagVersionID: t.DagVersionID,
			TenantID:     t.TenantID,
		}, warmBootstrapTokenTTL(cfg))
		if err != nil {
			return executor.WarmPodSpec{}, fmt.Errorf("minting warm bootstrap token: %w", err)
		}
		spec.BootstrapToken = token
		return spec, nil
	}
}

// warmBootstrapTokenTTL sizes the bootstrap credential to outlive the worker's
// whole life (the stream bearer is validated once at open), anchored to the worker
// lifetime cap so it cannot lapse while the worker is still valid.
func warmBootstrapTokenTTL(cfg *config.ServerConfig) time.Duration {
	if cfg.Execution.MaxWorkerLifetime > 0 {
		return cfg.Execution.MaxWorkerLifetime + time.Hour
	}
	return 2 * time.Hour
}

// setWarmPlacer attaches the warm-worker placement seam to the dispatcher, but
// only when the registry is actually present (ADR 0058 N1b1-place). The nil check
// is on the concrete *agentrpc.WorkerRegistry BEFORE it is boxed into the
// dispatch.WarmPlacer interface: passing a typed-nil pointer would make the
// interface non-nil, so the dispatcher would try to place onto a nil registry
// instead of taking the dedicated path. Warm pools off => reg nil => no placer.
func setWarmPlacer(d *dispatch.Dispatcher, reg *agentrpc.WorkerRegistry) {
	if reg != nil {
		d.SetWarmPlacer(reg)
	}
}

// warmPolicy is the warm placement decision the dispatcher that cfg selects
// applies (configureK8sDispatcher or configureSubprocessDispatcher), built
// from the same settings: the register response reports each task's
// execution mode with it (#1707), and warm_policy_test.go holds the two
// together. The warm registry, and so the placer, exists exactly when
// execution.warm_pools_enabled is on.
func warmPolicy(cfg *config.ServerConfig) domain.WarmPolicy {
	policy := domain.WarmPolicy{Enabled: cfg.Execution.WarmPoolsEnabled, Limits: defaultLimits(cfg.Executor.Defaults)}
	if cfg.Executor.Type == "subprocess" {
		return policy
	}
	policy.ServiceAccount = cfg.Executor.TaskServiceAccount
	policy.Unit = resourceUnit(cfg)
	policy.SourceModeImage = cfg.Execution.SourceMode.RuntimeImage()
	policy.SourceModeWarm = cfg.Execution.SourceMode.WarmPoolsOn()
	return policy
}

// warmWorkersAbsent says why no warm worker runs although warm pools are on,
// "" when they run or warm pools are off. Only the pod path starts the warm
// pool reconciler (setupK8sDispatch): the subprocess executor never does, and
// a server whose Kubernetes client failed has pod dispatch disabled. There the
// dispatcher's warm offer always misses, so the register response reports
// every task on a dedicated pod. podDispatch is ExecutorInfo.PodDispatchEnabled.
func warmWorkersAbsent(cfg *config.ServerConfig, podDispatch bool) string {
	switch {
	case !cfg.Execution.WarmPoolsEnabled:
		return ""
	case cfg.Executor.Type == "subprocess":
		return "the subprocess executor runs no warm workers"
	case !podDispatch:
		return "pod dispatch is disabled on this server"
	}
	return ""
}
