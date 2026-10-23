package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/executor"
)

// loadK8sRESTConfig loads the in-cluster config or, failing that, the local
// kubeconfig. It returns an error when neither is available, in which case a
// server on the Kubernetes executor refuses to boot (#704).
func loadK8sRESTConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("no in-cluster config or kubeconfig: %w", err)
		}
	}
	// Bound every API call. Workers dispatch with a detached context (they
	// already accepted responsibility for the task), so without a client-side
	// deadline an apiserver that accepts the connection and never answers hangs
	// a worker for good, and with it the shutdown drain (#463).
	cfg.Timeout = k8sClientTimeout
	return cfg, nil
}

// buildK8sClient builds one Kubernetes client with the dispatch limits. The agent
// token exchange uses it, so its TokenReviews and pod reads keep their own token
// bucket, as before.
func buildK8sClient(limits config.KubeClientSection) (kubernetes.Interface, error) {
	base, err := loadK8sRESTConfig()
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(k8sClientConfig(base, limits.QPS, limits.Burst))
	if err != nil {
		return nil, fmt.Errorf("building kubernetes client: %w", err)
	}
	return cs, nil
}

// k8sClients holds the pod-path Kubernetes clients. dispatch creates task pods;
// maintenance serves the pod informer, reconciler, reapers, staging GC and warm
// pool reconciler. They are the same client unless maintenance limits are set.
type k8sClients struct {
	dispatch    kubernetes.Interface
	maintenance kubernetes.Interface
}

// buildK8sClients builds the pod-path clients from the in-cluster config or the
// local kubeconfig.
func buildK8sClients(limits config.KubeClientSection) (k8sClients, error) {
	base, err := loadK8sRESTConfig()
	if err != nil {
		return k8sClients{}, err
	}
	return newK8sClients(base, limits)
}

// newK8sClients builds the dispatch client and, when MaintenanceQPS is set, a
// separate maintenance client. client-go gives every clientset its own token
// bucket, so the split keeps a maintenance burst (a reconciler sweep, a GC pass)
// from spending the budget pod creation depends on. Unset, both roles share one
// client and one budget, exactly as before the split.
func newK8sClients(base *rest.Config, limits config.KubeClientSection) (k8sClients, error) {
	dispatchCS, err := kubernetes.NewForConfig(k8sClientConfig(base, limits.QPS, limits.Burst))
	if err != nil {
		return k8sClients{}, fmt.Errorf("building kubernetes dispatch client: %w", err)
	}
	if limits.MaintenanceQPS <= 0 {
		return k8sClients{dispatch: dispatchCS, maintenance: dispatchCS}, nil
	}
	maintCS, err := kubernetes.NewForConfig(k8sClientConfig(base, limits.MaintenanceQPS, limits.MaintenanceBurst))
	if err != nil {
		return k8sClients{}, fmt.Errorf("building kubernetes maintenance client: %w", err)
	}
	return k8sClients{dispatch: dispatchCS, maintenance: maintCS}, nil
}

// k8sClientConfig copies base with the given client-side limits. A non-positive
// QPS or burst falls back to client-go's default, never to an unthrottled client
// or one that can never send.
func k8sClientConfig(base *rest.Config, qps float64, burst int) *rest.Config {
	cfg := rest.CopyConfig(base)
	cfg.QPS = rest.DefaultQPS
	if qps > 0 {
		cfg.QPS = float32(qps)
	}
	cfg.Burst = rest.DefaultBurst
	if burst > 0 {
		cfg.Burst = burst
	}
	return cfg
}

// k8sClientTimeout caps a single Kubernetes API call. Generous enough for a pod
// create against a loaded apiserver, short enough that a hung call is cut well
// inside the dispatch drain timeout.
const k8sClientTimeout = 10 * time.Second

// buildPodInformer constructs and starts the shared pod informer for the
// scheduler read-path (PR-10), returning nil when this process must NOT watch
// pods: the api-only role (ADR 0049, the sibling scheduler process owns the
// read-path) or a non-Kubernetes executor (no pods to watch). A nil return leaves
// the reapers and reconciler on their live read paths, today's behavior. The
// informer is always-on for the process lifetime, warming the cache before
// leadership; a not-yet-synced cache is safe because its readings only ever DEFER
// a reap (#461), never authorize one.
func buildPodInformer(ctx context.Context, cfg *config.ServerConfig, cs kubernetes.Interface, logger *slog.Logger) *executor.PodInformer {
	if !cfg.Server.ServesScheduler() || cfg.Executor.Type != "kubernetes" {
		return nil
	}
	pi := executor.NewPodInformer(cs, cfg.Executor.TaskNamespace)
	pi.Start(ctx)
	// Bound the warm-up. This wait is on the boot path, ahead of startAPISide and
	// therefore ahead of the HTTP and metrics listeners, and cache.WaitForCacheSync
	// returns only on sync or on its context being canceled. Handed the process
	// context, canceled at shutdown, a cache that cannot sync held boot forever:
	// gRPC served, /readyz never bound, the liveness probe on that same port killed
	// the container about every 70s, and each cycle was recorded as
	// `Completed exit=0` because SIGTERM is handled cleanly. A dependency failure
	// that reports itself as a success (#1083). The usual cause is a ServiceAccount
	// without list/watch on pods in the task namespace, which the reflector retries
	// forever.
	//
	// Not syncing is already survivable by design: CachedPodActive gates on
	// HasSynced and consumers keep their live read paths, and HasSynced stays live
	// so a cache that warms later (an RBAC fix) is picked up. That fallback was
	// correct and unreachable; the deadline is what makes it reachable.
	syncCtx, cancel := context.WithTimeout(ctx, podInformerSyncTimeout)
	defer cancel()
	switch {
	case pi.WaitForCacheSync(syncCtx):
		logger.Info("pod informer cache synced; reaper/reconciler read-path warm", "namespace", cfg.Executor.TaskNamespace)
	case errors.Is(syncCtx.Err(), context.DeadlineExceeded):
		// Say the cause, not just the symptom: without this line the operator has a
		// readiness failure and a klog line from a vendored library to correlate.
		logger.Warn("pod informer cache did not sync within the boot budget; continuing with live reads",
			"namespace", cfg.Executor.TaskNamespace,
			"budget", podInformerSyncTimeout,
			"likely_cause", "the ServiceAccount cannot list/watch pods in this namespace, or the Kubernetes API is unreachable",
			"effect", "reapers and the reconciler read pods live; no reap is authorized from a cold cache")
	default:
		logger.Warn("pod informer cache did not sync before shutdown; reapers use live reads until it warms")
	}
	return pi
}
