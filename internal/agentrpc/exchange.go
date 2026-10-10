package agentrpc

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dexadata/dexaflow/internal/auth"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// ReviewedPod is the pod a validated projected ServiceAccount token identifies.
// The concrete TokenReviewer fills it from the Kubernetes TokenReview response —
// the bound-token status carries the pod name/uid the token was issued for. The
// field names the control plane resolves a task instance from are
// apiserver-version-dependent (ADR 0055 D7): the primary key is PodName in the
// task namespace; PodUID guards against a name-reused stale pod.
type ReviewedPod struct {
	Namespace      string
	PodName        string
	PodUID         string
	ServiceAccount string
}

// TokenReviewer validates a projected ServiceAccount token against the control
// plane's audience via the Kubernetes TokenReview API and returns the pod it was
// issued for. It is the ONE apiserver call in the exchange, made once per pod at
// bootstrap (never on the secret hot path). It is an interface so it is MOCKED in
// unit tests — the concrete client needs a real apiserver and is exercised only
// by the owed real-cluster e2e. An error (bad signature, expired, wrong audience,
// or authenticated=false) means the token is not a valid bootstrap credential.
type TokenReviewer interface {
	ReviewProjectedToken(ctx context.Context, token string) (ReviewedPod, error)
}

// PodTaskResolver maps a reviewed pod to the agent identity it runs, so the
// minted JWT is scoped correctly. It is an interface so it is mocked in unit
// tests; the concrete resolver reads the pod the apiserver validated.
//
// ResolveAgent is the branch point the exchange uses (ADR 0058 D1): a pod carrying
// the warm-worker LABEL resolves to a warm-worker identity (Scope ==
// ScopeWarmWorker, naming its dag_version pool + worker id, no task claims); any
// other pod resolves to the task-instance identity the control plane stamped on it
// at dispatch — exactly what ResolveTaskInstance returns. ResolveTaskInstance is
// kept for the pure task path and callers that only ever expect a task instance.
type PodTaskResolver interface {
	ResolveTaskInstance(ctx context.Context, pod ReviewedPod) (auth.AgentIdentity, error)
	ResolveAgent(ctx context.Context, pod ReviewedPod) (auth.AgentIdentity, error)
}

// AgentTokenMinter issues a task-scoped agent JWT for a resolved identity. It is
// satisfied by *auth.JWTAuthenticator (IssueAgentToken) — the same minter used at
// dispatch and heartbeat renewal, so the exchanged token is indistinguishable
// from a dispatched one on every downstream path.
type AgentTokenMinter interface {
	IssueAgentToken(id auth.AgentIdentity, ttl time.Duration) (string, error)
}

// SetTokenExchange wires the projected-SA-token exchange (ADR 0055 Fix #3): the
// TokenReview client, the pod→task-instance resolver, the JWT minter, and the TTL
// of the minted task-scoped token. allowInsecure permits running the exchange
// over a non-TLS channel (dev only); production must use TLS (ExchangeToken fails
// closed otherwise, like the secret path). A nil reviewer leaves the exchange OFF
// — ExchangeToken then reports Unimplemented — which is the default (env-var)
// transport, so a deployment that does not opt in is byte-identical to today.
func (s *Server) SetTokenExchange(reviewer TokenReviewer, resolver PodTaskResolver, minter AgentTokenMinter, ttl time.Duration, allowInsecure bool) {
	s.reviewer = reviewer
	s.podResolver = resolver
	s.tokenMinter = minter
	s.exchangeTTL = ttl
	s.allowInsecureExchange = allowInsecure
}

// ExchangeToken validates the agent's projected ServiceAccount token (bootstrap
// bearer), resolves the pod to its task instance, and returns a freshly minted
// task-scoped agent JWT (ADR 0055 Fix #3). It is called ONCE at agent startup
// under the exchange transport; the default env-var transport never calls it.
//
// It fails closed at every step: Unimplemented when the exchange is not wired,
// PermissionDenied on an insecure channel, Unauthenticated on a missing or
// rejected projected token, and Internal when the reviewed pod cannot be resolved
// to an attempt (never mint an unscoped or misattributed token). The minted token
// and the presented projected token are never logged.
func (s *Server) ExchangeToken(ctx context.Context, _ *agentv1.ExchangeTokenRequest) (*agentv1.ExchangeTokenResponse, error) {
	if s.reviewer == nil || s.podResolver == nil || s.tokenMinter == nil {
		return nil, status.Error(codes.Unimplemented, "token exchange is not enabled")
	}
	if gerr := s.guardExchangeChannel(ctx); gerr != nil {
		return nil, gerr
	}
	token, ok := bearerFromContext(ctx)
	if !ok || token == "" {
		return nil, status.Error(codes.Unauthenticated, "missing projected token")
	}
	pod, err := s.reviewer.ReviewProjectedToken(ctx, token)
	if err != nil {
		// A rejected projected token is not a server fault: the credential is
		// invalid, expired, or carries the wrong audience. Surface Unauthenticated
		// without echoing the token or the review detail to the agent.
		slog.Warn("rejecting agent token exchange: projected token review failed", "error", err)
		return nil, status.Error(codes.Unauthenticated, "projected token is not valid")
	}
	// A warm-worker pod (by LABEL, DP1) resolves to a worker-scoped identity that
	// authorizes ONLY the control channel; any other pod resolves to its task
	// instance exactly as before. IssueAgentToken mints whichever flavor.
	id, err := s.podResolver.ResolveAgent(ctx, pod)
	if err != nil {
		// The resolver reads the control plane's own state, so its error can carry
		// SQLSTATE and schema names. The agent is told which step failed and the
		// cause stays in the control-plane log (#1068).
		return nil, internalStatus("resolving pod to agent identity", err,
			"namespace", pod.Namespace, "pod", pod.PodName, "pod_uid", pod.PodUID)
	}
	minted, err := s.tokenMinter.IssueAgentToken(id, s.exchangeTTL)
	if err != nil {
		return nil, internalStatus("minting agent token", err,
			"scope", id.Scope, "ti", id.TaskInstanceID, "worker", id.WorkerID)
	}
	if id.Scope == auth.ScopeWarmWorker {
		// Log the warm case distinctly (worker/dag_version, never the token): the
		// credential authorizes only Register + AwaitAssignment, no secrets or task.
		slog.Info("exchanged projected token for a WORKER-scoped agent JWT (control channel only: Register + AwaitAssignment)",
			"worker", id.WorkerID, "dag_version", id.DagVersionID, "tenant", id.TenantID,
			"namespace", pod.Namespace, "pod", pod.PodName)
	} else {
		slog.Info("exchanged projected token for a task-scoped agent JWT",
			"ti", id.TaskInstanceID, "tenant", id.TenantID, "dag", id.DagID,
			"run", id.RunID, "task", id.TaskID, "try", id.TryNumber, "namespace", pod.Namespace, "pod", pod.PodName)
	}
	return &agentv1.ExchangeTokenResponse{AgentToken: minted}, nil
}

// guardExchangeChannel refuses to run the exchange over a plaintext channel
// unless explicitly allowed for dev — the exchange returns a bearer credential,
// so it must not transit an insecure transport by default. Mirrors the secret
// channel guard.
func (s *Server) guardExchangeChannel(ctx context.Context) error {
	if s.allowInsecureExchange || channelIsSecure(ctx) {
		return nil
	}
	return status.Error(codes.PermissionDenied,
		"refusing to exchange a token over an insecure channel; enable gRPC TLS")
}
