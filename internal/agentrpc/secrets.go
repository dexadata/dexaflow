package agentrpc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/dexadata/dexaflow/internal/auth"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// SecretsStore returns a tenant's Variables and Connections for delivery to a
// task pod (ADR 0021). Connection URIs carry decrypted credentials, so this is
// only ever served over the authenticated agent channel — never to the UI/API.
//
// The unscoped methods return the whole tenant vault (permissive / off). The
// scoped methods return ONLY the named subset, filtered IN THE QUERY (ADR 0055
// D1: never post-filter the decrypted whole vault in the handler); they back
// secret_scoping: enforce, where a task receives only what it declared. An empty
// name set returns nothing — enforce's load-bearing [] case.
type SecretsStore interface {
	SecretVariables(ctx context.Context, tenant string) (map[string]string, error)
	SecretConnectionURIs(ctx context.Context, tenant string) (map[string]string, error)
	SecretVariablesScoped(ctx context.Context, tenant string, names []string) (map[string]string, error)
	SecretConnectionURIsScoped(ctx context.Context, tenant string, names []string) (map[string]string, error)
}

// Secret scoping policy (ADR 0055 D9), operator-set, NEVER author-settable.
const (
	// ScopingPermissive delivers the whole tenant vault when a DAG declares
	// nothing (today's behavior) and warns — but still delivers the whole vault —
	// when a DAG declares a narrower set. Subsetting is reserved for enforce, so no
	// already-declaring pipeline loses access. It is the default for this shipment.
	ScopingPermissive = "permissive"
	// ScopingEnforce delivers ONLY the declared subset (empty declaration →
	// nothing), resolved server-side and filtered in the query.
	ScopingEnforce = "enforce"
	// ScopingOff disables scoping entirely: the whole tenant vault, no warn.
	ScopingOff = "off"
)

// SecretScopeAuditor records a structured audit event when a task receives the
// full tenant secret set despite declaring only a subset of it — the visibility
// half of the warn-before-enforce arc (ADR 0045 §Settled #3, ADR 0055). It
// carries counts only, never secret names or values. It is optional and
// best-effort: a nil auditor or a write error only skips the audit row; it never
// changes what is delivered.
type SecretScopeAuditor interface {
	// tenantID is the tenant UUID the agent token carries (AgentIdentity.TenantID),
	// not the tenant name.
	RecordSecretScopeWarning(ctx context.Context, tenantID, dagID, runID, taskID, kind string, declared, total int) error
}

// Secret-path liveness gate modes (ADR 0055 E2). The gate consults the
// read-only task-instance liveness predicate before serving secrets so a token
// whose task instance is no longer live stops resolving them.
const (
	// LivenessObserve logs a structured warn + records a would-have-denied audit
	// event when the caller's TI is not live, but STILL delivers. It is the
	// default: no behavior change, the observe half of the warn→enforce arc.
	LivenessObserve = "observe"
	// LivenessEnforce denies with codes.PermissionDenied when the caller's TI is
	// not live. It is the operator's later flip, after an observe period.
	LivenessEnforce = "enforce"
)

// TaskLivenessChecker reports whether a task-instance attempt is still live —
// present and in an active (non-terminal) state for the given (run, task, try).
// It is the read-only revocation signal the secret path consults (ADR 0055 D3):
// a terminal, superseded, or reaped attempt is not live, so its token stops
// resolving secrets. The predicate derives ONLY from (run, task, try) + active
// state — never run recency — so a clear-and-rerun of an old run stays live.
type TaskLivenessChecker interface {
	IsTaskInstanceLive(ctx context.Context, runID, taskID string, tryNumber int) (bool, error)
}

// SecretLivenessAuditor records a structured audit event when the secret-path
// liveness gate fires: a would-have-denied in observe mode, or a denial in
// enforce mode (ADR 0055). It carries identity + kind + mode only, never secret
// names or values. Optional and best-effort: a nil auditor or a write error only
// skips the row; it never changes the gate's decision.
type SecretLivenessAuditor interface {
	// tenantID is the tenant UUID the agent token carries (AgentIdentity.TenantID),
	// not the tenant name.
	RecordSecretLivenessDenial(ctx context.Context, tenantID, dagID, runID, taskID string, tryNumber int, kind, mode string) error
}

// SetSecrets attaches the secrets store. allowInsecure permits serving secrets
// over a non-TLS channel — for local/dev only; production must use TLS (the
// handlers fail closed otherwise). See ADR 0021 / issue #58.
func (s *Server) SetSecrets(store SecretsStore, allowInsecure bool) {
	s.secrets, s.allowInsecureSecrets = store, allowInsecure
}

// SetSecretScopeAuditor attaches the sink for secret-scope warning events
// (optional). Without it, a narrowing declaration still produces the WARN log
// but no audit row.
func (s *Server) SetSecretScopeAuditor(a SecretScopeAuditor) { s.secretAudit = a }

// SetLivenessGate attaches the read-only task-instance liveness predicate the
// secret path consults, in the given mode ("observe" | "enforce", ADR 0055 E2).
// An unrecognized mode falls back to observe — the safe, non-denying default.
// A nil checker leaves the gate off (delivery unchanged), so the gate is opt-in.
func (s *Server) SetLivenessGate(checker TaskLivenessChecker, mode string) {
	s.liveness = checker
	if mode == LivenessEnforce {
		s.livenessMode = LivenessEnforce
	} else {
		s.livenessMode = LivenessObserve
	}
}

// SetSecretLivenessAuditor attaches the sink for secret-path liveness events
// (optional). Without it, a would-have-denied / denial still produces the WARN
// log but no audit row.
func (s *Server) SetSecretLivenessAuditor(a SecretLivenessAuditor) { s.livenessAudit = a }

// SetSecretScoping sets the operator scope-by-declaration policy (ADR 0055 D9):
// "enforce" | "permissive" | "off". An unrecognized value falls back to
// permissive — the safe, non-denying default — so a misconfiguration never
// silently denies. The policy is operator-scoped, never author-settable.
func (s *Server) SetSecretScoping(policy string) { s.scoping = policy }

// scopingPolicy normalises the configured policy to one of the three known
// values, defaulting (and failing safe on an unknown value) to permissive.
func (s *Server) scopingPolicy() string {
	switch s.scoping {
	case ScopingEnforce, ScopingOff:
		return s.scoping
	default:
		return ScopingPermissive
	}
}

// guardSecretChannel refuses to serve secrets when the store is unconfigured or
// the transport is not TLS (unless explicitly allowed for dev). This is the
// fail-closed gate: secrets never transit a plaintext channel by default.
func (s *Server) guardSecretChannel(ctx context.Context) error {
	if s.secrets == nil {
		return status.Error(codes.Unavailable, "secrets delivery is not configured")
	}
	if s.allowInsecureSecrets || channelIsSecure(ctx) {
		return nil
	}
	return status.Error(codes.PermissionDenied,
		"refusing to send secrets over an insecure channel; enable gRPC TLS (see issue #58)")
}

// channelIsSecure reports whether the incoming gRPC call arrived over a secure
// (TLS) transport — the peer carries AuthInfo only when the channel is
// encrypted. It is the shared secure-transport check used by both the secret and
// the token-exchange channel guards.
func channelIsSecure(ctx context.Context) bool {
	p, ok := peer.FromContext(ctx)
	return ok && p.AuthInfo != nil
}

// checkLiveness consults the read-only task-instance liveness predicate on the
// secret path (ADR 0055 D2/D3, E2). It is called ONLY by GetVariables and
// GetConnections — never by the shared identify(), because Heartbeat/ReportState
// are designed to run for a superseded TI so they can return the terminate
// signal, and gating them on liveness would break it.
//
// It returns a PermissionDenied error ONLY in enforce mode on a POSITIVE
// not-live result. In observe mode a not-live result logs + audits a
// would-have-denied and returns nil (delivery proceeds — no behavior change).
//
// Transient-error rule (both modes): an inconclusive liveness read (a nil
// checker, or a DB error) NEVER denies and NEVER warns-as-not-live — an errored
// check cannot conclude the TI is dead, and the short token TTL bounds a real
// blip. Failing closed on a transient read would break a live pipeline.
func (s *Server) checkLiveness(ctx context.Context, id *auth.AgentIdentity, kind string) error {
	if s.liveness == nil {
		return nil // gate not configured — delivery unchanged
	}
	live, err := s.liveness.IsTaskInstanceLive(ctx, id.RunID, id.TaskID, id.TryNumber)
	if err != nil {
		// Inconclusive: cannot conclude not-live. Never deny, never warn-as-not-live.
		slog.Warn("secret-path liveness check inconclusive; delivering (transient error, token TTL bounds exposure)",
			"ti", id.TaskInstanceID, "run", id.RunID, "task", id.TaskID, "try", id.TryNumber, "kind", kind, "error", err)
		return nil
	}
	if live {
		return nil
	}
	// Positive not-live result: the TI is terminal, superseded, or reaped.
	if s.livenessMode == LivenessEnforce {
		slog.Warn("denying secrets: task instance is not live (secret_liveness_mode: enforce)",
			"ti", id.TaskInstanceID, "tenant", id.TenantID, "dag", id.DagID, "run", id.RunID,
			"task", id.TaskID, "try", id.TryNumber, "kind", kind)
		s.recordLiveness(ctx, id, kind, LivenessEnforce)
		return status.Error(codes.PermissionDenied, "task instance is no longer live")
	}
	// Observe mode: would-have-denied, but deliver.
	slog.Warn("would-have-denied secrets: task instance is not live (secret_liveness_mode: observe; delivering)",
		"ti", id.TaskInstanceID, "tenant", id.TenantID, "dag", id.DagID, "run", id.RunID,
		"task", id.TaskID, "try", id.TryNumber, "kind", kind)
	s.recordLiveness(ctx, id, kind, LivenessObserve)
	return nil
}

// recordLiveness writes the best-effort secret-path liveness audit event. A nil
// auditor or a write error only skips the row; it never changes the gate's
// decision.
func (s *Server) recordLiveness(ctx context.Context, id *auth.AgentIdentity, kind, mode string) {
	if s.livenessAudit == nil {
		return
	}
	if aerr := s.livenessAudit.RecordSecretLivenessDenial(ctx, id.TenantID, id.DagID, id.RunID, id.TaskID, id.TryNumber, kind, mode); aerr != nil {
		slog.Warn("recording secret-path liveness audit event",
			"ti", id.TaskInstanceID, "kind", kind, "mode", mode, "error", aerr)
	}
}

// GetVariables returns the calling task's tenant Variables for the agent to
// export as AIRFLOW_VAR_<KEY>.
func (s *Server) GetVariables(ctx context.Context, _ *agentv1.GetVariablesRequest) (*agentv1.GetVariablesResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	if aerr := s.requireAttemptToken(id); aerr != nil {
		return nil, aerr
	}
	if gerr := s.guardSecretChannel(ctx); gerr != nil {
		return nil, gerr
	}
	if lerr := s.checkLiveness(ctx, id, "variables"); lerr != nil {
		return nil, lerr
	}
	if s.scopingPolicy() == ScopingEnforce {
		declared, derr := s.declaredNames(ctx, id, "variables")
		if derr != nil {
			return nil, derr
		}
		vars, verr := s.secrets.SecretVariablesScoped(ctx, id.TenantID, declared)
		if verr != nil {
			return nil, internalStatus("fetching variables", verr, attemptAttrs(id)...)
		}
		return &agentv1.GetVariablesResponse{Variables: vars}, nil
	}
	vars, err := s.secrets.SecretVariables(ctx, id.TenantID)
	if err != nil {
		return nil, internalStatus("fetching variables", err, attemptAttrs(id)...)
	}
	// permissive warns on a narrow declaration; off disables the warn entirely.
	if s.scopingPolicy() == ScopingPermissive {
		s.warnIfNarrowerScope(ctx, id, "variables", vars)
	}
	return &agentv1.GetVariablesResponse{Variables: vars}, nil
}

// GetConnections returns the calling task's tenant Connections as Airflow URIs
// for the agent to export as AIRFLOW_CONN_<CONN_ID>.
func (s *Server) GetConnections(ctx context.Context, _ *agentv1.GetConnectionsRequest) (*agentv1.GetConnectionsResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	if aerr := s.requireAttemptToken(id); aerr != nil {
		return nil, aerr
	}
	if gerr := s.guardSecretChannel(ctx); gerr != nil {
		return nil, gerr
	}
	if lerr := s.checkLiveness(ctx, id, "connections"); lerr != nil {
		return nil, lerr
	}
	if s.scopingPolicy() == ScopingEnforce {
		declared, derr := s.declaredNames(ctx, id, "connections")
		if derr != nil {
			return nil, derr
		}
		uris, uerr := s.secrets.SecretConnectionURIsScoped(ctx, id.TenantID, declared)
		if uerr != nil {
			return nil, internalStatus("fetching connections", uerr, attemptAttrs(id)...)
		}
		return &agentv1.GetConnectionsResponse{ConnectionUris: uris}, nil
	}
	uris, err := s.secrets.SecretConnectionURIs(ctx, id.TenantID)
	if err != nil {
		return nil, internalStatus("fetching connections", err, attemptAttrs(id)...)
	}
	// permissive warns on a narrow declaration; off disables the warn entirely.
	if s.scopingPolicy() == ScopingPermissive {
		s.warnIfNarrowerScope(ctx, id, "connections", uris)
	}
	return &agentv1.GetConnectionsResponse{ConnectionUris: uris}, nil
}

// declaredNames resolves the caller's declared secret set for enforce mode from
// its task spec (E1a threaded it onto the agent-facing spec). kind is
// "variables" or "connections". A spec-load failure fails the RPC closed
// (Internal): under enforce we must never fall back to the whole vault when the
// declared set cannot be determined. The scoping is resolved server-side from
// the token identity, never from anything the agent claims.
func (s *Server) declaredNames(ctx context.Context, id *auth.AgentIdentity, kind string) ([]string, error) {
	spec, err := s.store.TaskSpec(ctx, *id)
	if err != nil {
		return nil, internalStatus("loading task spec for scope enforcement", err, attemptAttrs(id)...)
	}
	if kind == "connections" {
		return spec.DeclaredConnections, nil
	}
	return spec.DeclaredVariables, nil
}

// warnIfNarrowerScope emits a structured WARN log and a best-effort audit event
// when the calling task declared a non-empty secret set that is a strict subset
// of the full tenant set it is about to receive (ADR 0045 §Settled #3, ADR 0055).
// It never changes what is delivered — delivery stays fetch-all in the warn phase
// — and it records counts only, never secret names or values. kind is
// "variables" or "connections". A task that declared nothing is the permissive
// default and is not warned.
func (s *Server) warnIfNarrowerScope(ctx context.Context, id *auth.AgentIdentity, kind string, delivered map[string]string) {
	// The declared set lives on the task spec (ADR 0045). Loading it is only for
	// the warning; a load failure must never affect delivery, which already
	// succeeded, so log and return without warning.
	spec, err := s.store.TaskSpec(ctx, *id)
	if err != nil {
		slog.Warn("loading task spec for secret-scope warning; skipping warn",
			"ti", id.TaskInstanceID, "kind", kind, "error", err)
		return
	}
	declared := spec.DeclaredVariables
	if kind == "connections" {
		declared = spec.DeclaredConnections
	}
	declaredCount, deliveredCount, warn := scopeGap(declared, delivered)
	if !warn {
		return
	}
	slog.Warn("task received the full secret vault but declared a narrower set; under secret_scoping: enforce it would receive only its declared set",
		"ti", id.TaskInstanceID, "tenant", id.TenantID, "dag", id.DagID, "run", id.RunID,
		"task", id.TaskID, "kind", kind, "declared", declaredCount, "delivered", deliveredCount)
	if s.secretAudit == nil {
		return
	}
	if aerr := s.secretAudit.RecordSecretScopeWarning(ctx, id.TenantID, id.DagID, id.RunID, id.TaskID, kind, declaredCount, deliveredCount); aerr != nil {
		slog.Warn("recording secret-scope warning audit event",
			"ti", id.TaskInstanceID, "kind", kind, "error", aerr)
	}
}

// scopeGap compares a task's declared secret names against the full tenant set it
// is about to receive. It returns the count of declared names that are actually
// delivered, the delivered total, and whether that declaration is a strict subset
// (non-empty and covering fewer than all delivered secrets) — the condition the
// warn phase records. E1a rejects declaring a name that does not exist, so a
// declared name is expected to be delivered; a name that is not is simply not
// counted, keeping the comparison robust.
func scopeGap(declared []string, delivered map[string]string) (declaredCount, deliveredCount int, strictSubset bool) {
	deliveredCount = len(delivered)
	if len(declared) == 0 {
		return 0, deliveredCount, false
	}
	seen := make(map[string]struct{}, len(declared))
	for _, name := range declared {
		if _, ok := delivered[name]; ok {
			seen[name] = struct{}{}
		}
	}
	declaredCount = len(seen)
	strictSubset = declaredCount > 0 && declaredCount < deliveredCount
	return declaredCount, deliveredCount, strictSubset
}
