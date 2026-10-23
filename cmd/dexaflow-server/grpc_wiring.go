package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"k8s.io/client-go/kubernetes"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/kubeexchange"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// grpcStopTimeout bounds the agent gRPC server's graceful stop on shutdown.
// Open log streams end at SIGTERM (agentrpc.Server.SetShutdown), so the
// graceful path normally finishes in milliseconds and the whole shutdown in well
// under a second; the bound is the safety net for a stream or unary call that
// does not end. The bounded WORST case is not inside the kubelet's default 30 s
// grace: the HTTP shutdown (10 s) and the dispatch drain (15 s, configurable)
// run before it, and stopGRPCWithin spends up to 2 × this timeout (graceful,
// then the wait for handlers after the forced stop), so a shutdown that hits
// every bound takes ~35 s plus the telemetry flush. That is the PROCESS bound,
// measured from SIGTERM; the pod's termination clock can be longer, because the
// chart's optional preStop sleep (deployment.preStopSleepSeconds, off by
// default, 5 in the HA profile) runs inside the same grace but BEFORE the signal,
// so the grace an operator needs is that sleep plus this bound. The default
// grace covers the normal shutdown; an installation running the object log sink
// at scale should raise terminationGracePeriodSeconds (the HA profile ships 60).
const grpcStopTimeout = 5 * time.Second

// grpcStopper is the subset of *grpc.Server the bounded stop needs; a fake
// stands in for it in tests.
type grpcStopper interface {
	GracefulStop()
	Stop()
}

// stopGRPCWithin stops the agent gRPC server gracefully, but for at most timeout;
// past it, Stop force-closes the transports. The unbounded GracefulStop waited
// on every in-flight RPC, and a task's StreamLogs stays open for the whole
// task, so with any task running the process exhausted the pod's termination
// grace and died by SIGKILL, the normal terminal state under load. Forcing
// cancels the remaining handlers' stream contexts; GracefulStop is still
// waiting for those handlers to return, and they return after their deferred
// log flushes, so the wait for it is kept (bounded again) and forced reports
// whether the fallback was taken.
//
// inflight (optional; nil omits the field) reports how many agent RPC handlers
// are still executing, and is logged on both bounded paths. A log writer's
// final Put is bounded at objectPutTimeout, which is longer than this timeout,
// so the process can legitimately exit with handlers still running: abandoning
// one is SAFE, a log object is written by a single atomic Put, so the stored
// object stays at its previous flush rather than being truncated, but without
// the count it is also silent, and nothing says how much log tail that cost.
func stopGRPCWithin(srv grpcStopper, timeout time.Duration, logger *slog.Logger, inflight func() int) (forced bool) {
	// inflightArgs renders the count as log attributes, or nothing when no
	// counter is wired, so an unwired caller logs a shorter line instead of a
	// misleading zero.
	inflightArgs := func() []any {
		if inflight == nil {
			return nil
		}
		return []any{"in_flight_handlers", inflight()}
	}
	done := make(chan struct{})
	go func() { srv.GracefulStop(); close(done) }()
	select {
	case <-done:
		return false
	case <-time.After(timeout):
	}
	logger.Warn("agent grpc graceful stop exceeded its bound; forcing stop so shutdown completes within the pod's grace",
		append([]any{"timeout", timeout}, inflightArgs()...)...)
	srv.Stop()
	select {
	case <-done:
	case <-time.After(timeout):
		logger.Error("agent grpc handlers did not finish after the forced stop; exiting without them (an abandoned log flush leaves the object at its previous flush, not truncated)",
			append([]any{"timeout", timeout}, inflightArgs()...)...)
	}
	return true
}

// startAgentGRPC starts the AgentService gRPC server and returns it for graceful
// shutdown. TLS is enabled when tlsCert/tlsKey are set (issue #58); otherwise the
// channel is plaintext (dev). The pair is read at boot (a missing or invalid file
// is a boot error) and re-read on the next handshake whenever either file
// changes, so a renewed certificate is served without a restart (#637). The per-task bearer token in metadata authenticates
// each call regardless. inflight (required) is installed on the interceptor
// chain so the bounded stop can report the handlers it leaves running.
func startAgentGRPC(ctx context.Context, addr string, authn *auth.JWTAuthenticator, store *storage.ExecutionStore, secretsStore agentrpc.SecretsStore, xcomSvc agentrpc.XComService, logSink agentrpc.LogSink, logTailer agentrpc.LogPublisher, tailOnDemand bool, allowInsecureSecrets bool, secretScoping, secretLivenessMode string, maxAttemptLifetime time.Duration, exchange *tokenExchange, tlsCert, tlsKey string, warmPools *agentrpc.WorkerRegistry, inflight *agentrpc.InflightHandlers, metrics *observability.Metrics, logger *slog.Logger) (srv *grpc.Server, agentSrv *agentrpc.Server, err error) {
	secure := tlsCert != "" && tlsKey != ""
	var creds credentials.TransportCredentials
	if secure {
		r, cerr := newCertReloader(tlsListenerAgentGRPC, tlsCert, tlsKey, metrics)
		if cerr != nil {
			return nil, nil, fmt.Errorf("loading agent grpc TLS cert: %w", cerr)
		}
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: r.getCertificate})
	}
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listening for agent grpc on %s: %w", addr, err)
	}
	agentSrv = agentrpc.NewServer(authn, store, xcomSvc)
	// End open task log streams as soon as shutdown begins (ctx is the SIGTERM
	// context), so each stream's deferred log flush runs while the process is
	// alive and the bounded graceful stop below is not held open by running tasks.
	agentSrv.SetShutdown(ctx)
	agentSrv.SetLogSink(logSink)
	agentSrv.SetLogPublisher(logTailer)
	// logs.tail.publish: "always" (default) publishes every line; "on_demand"
	// publishes only while someone follows the attempt.
	agentSrv.SetTailPublishOnDemand(tailOnDemand)
	agentSrv.SetSecrets(secretsStore, allowInsecureSecrets)
	// Refresh a live attempt's bearer on every heartbeat (ADR 0055 Fix #4) with the
	// same short per-attempt TTL used at dispatch, so a long task keeps a working
	// credential while a stolen/finished token lapses in minutes. The renewal total
	// is capped by auth.max_attempt_credential_lifetime.
	agentSrv.SetTokenRenewal(authn, attemptTokenTTL, maxAttemptLifetime)
	// Scope-by-declaration policy (ADR 0055 D9). Default permissive: the whole
	// tenant vault, warning on a narrow or empty declaration but never denying.
	agentSrv.SetSecretScoping(secretScoping)
	// Bind secret delivery to task-instance liveness (ADR 0055 E2). The
	// ExecutionStore is the read-only liveness predicate; the mode defaults to
	// observe (log a would-have-denied, still deliver). Secret-path only.
	agentSrv.SetLivenessGate(store, secretLivenessMode)
	// Projected-SA-token exchange (ADR 0055 Fix #3). Wired only when the operator
	// selected the exchange transport on a Kubernetes executor; nil (the default)
	// leaves ExchangeToken Unimplemented, so the env-var transport is unchanged.
	if exchange != nil {
		agentSrv.SetTokenExchange(exchange.reviewer, exchange.resolver, authn, attemptTokenTTL, allowInsecureSecrets)
		logger.Warn("agent token exchange enabled (projected SA token → TokenReview → task-scoped JWT); real-cluster e2e owed before flipping this on in production")
	}
	// The secrets store is the Repository, which also records the warn-phase
	// secret-scope audit event (ADR 0045, ADR 0055); wire it as the auditor so a
	// narrowly-declared task's full-vault delivery is surfaced. Optional: without
	// it the WARN log still fires.
	if auditor, ok := secretsStore.(agentrpc.SecretScopeAuditor); ok {
		agentSrv.SetSecretScopeAuditor(auditor)
	}
	// The Repository also records the secret-path liveness would-have-denied /
	// denial audit event (ADR 0055 E2); wire it so the observe phase is
	// observable. Optional: without it the WARN log still fires.
	if auditor, ok := secretsStore.(agentrpc.SecretLivenessAuditor); ok {
		agentSrv.SetSecretLivenessAuditor(auditor)
	}
	// Warm worker assignment transport (ADR 0058 N1b1-place). The registry is built
	// once by startSchedulerSide and shared: the dispatcher's placer Assign()s onto
	// the SAME instance whose workers register/ack here. nil (warm pools off, the
	// default) leaves AwaitAssignment inert (FailedPrecondition) and no running path
	// changes.
	if warmPools != nil {
		agentSrv.SetWarmPools(warmPools)
	}

	// Recover panics in any agent RPC handler so a single malformed request from a
	// worker pod cannot crash the control plane (it returns Internal instead).
	// The in-flight counter sits INSIDE recovery so recovery stays outermost (it
	// must cover every later interceptor), and so a panicking handler is counted
	// out by the counter's own defer before recovery translates the panic.
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(agentrpc.RecoveryUnaryInterceptor(logger), inflight.UnaryInterceptor()),
		grpc.ChainStreamInterceptor(agentrpc.RecoveryStreamInterceptor(logger), inflight.StreamInterceptor()),
		// Tolerate the agents' keepalive pings, including on an otherwise idle
		// connection or assignment stream, so gRPC does not GOAWAY them as
		// abusive (ADR 0058 N1b, #946). This only RELAXES the server's default
		// client-ping enforcement (5m, streams-only). Server-initiated keepalive
		// (ServerParameters) is deliberately NOT added: the agent pings, and so
		// detects a dead control plane itself; a vanished agent is found by the
		// heartbeat reapers, and an assignment sent to a vanished warm worker is
		// reclaimed when its ack lease expires. Server pings on every idle agent
		// connection would cost something and detect nothing those miss.
		grpc.KeepaliveEnforcementPolicy(agentrpc.KeepaliveEnforcementPolicy()),
	}
	if secure {
		opts = append(opts, grpc.Creds(creds))
	}
	srv = grpc.NewServer(opts...)
	agentv1.RegisterAgentServiceServer(srv, agentSrv)
	go func() {
		if serr := srv.Serve(lis); serr != nil && !errors.Is(serr, grpc.ErrServerStopped) {
			logger.Error("agent grpc server", "error", serr)
		}
	}()
	logger.Info("agent grpc server started", "grpc_addr", addr, "tls", secure)
	return srv, agentSrv, nil
}

// tokenExchange bundles the Kubernetes-backed dependencies of the agent
// token-exchange transport (ADR 0055 Fix #3): the TokenReview client and the
// pod→task-instance resolver. nil means the exchange is not wired (the default
// env-var transport), in which case ExchangeToken reports Unimplemented.
type tokenExchange struct {
	reviewer agentrpc.TokenReviewer
	resolver agentrpc.PodTaskResolver
}

// buildTokenExchange constructs the exchange dependencies when the operator
// selected the exchange transport on a Kubernetes executor; it returns (nil,
// nil) otherwise (env-var default, or the subprocess executor, which has no
// pod/SA/TokenReview and needs no client). newK8sClient is the seam that
// supplies the Kubernetes client (buildK8sClient in production); it is consulted
// only when the exchange transport is actually selected.
//
// When the exchange transport IS selected on a Kubernetes executor and no client
// can be built, this FAILS CLOSED and returns an error so the server refuses to
// boot. An unwired exchange makes ExchangeToken report Unimplemented, so every
// task pod fails at bootstrap while the control plane reports itself healthy,
// a fail-open on a security-critical transport the operator explicitly opted
// into. The transport flip is a deliberate opt-in, which is an argument for
// honoring it strictly, not for silently ignoring it.
func buildTokenExchange(cfg *config.ServerConfig, newK8sClient func() (kubernetes.Interface, error)) (*tokenExchange, error) {
	if cfg.Auth.AgentTokenTransport != config.AgentTokenTransportExchange || cfg.Executor.Type != "kubernetes" {
		return nil, nil //nolint:nilnil // env-var transport or non-Kubernetes executor needs no exchange; a nil exchange is the correct wiring, not an error
	}
	cs, err := newK8sClient()
	if err != nil {
		return nil, fmt.Errorf("agent token transport %q requires a Kubernetes client, but none is available (no in-cluster config or kubeconfig): %w", config.AgentTokenTransportExchange, err)
	}
	return &tokenExchange{
		reviewer: kubeexchange.NewTokenReviewer(cs, executor.DefaultAgentTokenAudience),
		resolver: kubeexchange.NewPodResolver(cs, cfg.Executor.TaskNamespace),
	}, nil
}
