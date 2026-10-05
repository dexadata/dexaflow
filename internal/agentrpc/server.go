// Package agentrpc implements the control-plane side of the agent gRPC protocol:
// it authenticates each in-pod agent by its per-task-instance token, serves the
// task specification, and records the state transitions the agent reports.
package agentrpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/xcom"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TaskSpec is the execution specification the agent needs to run a task.
type TaskSpec struct {
	Operator         string
	Entrypoint       string
	DagVersion       string
	Environment      map[string]string
	XComInputMapping map[string][]string
	XComSchema       map[string]any
	TimeoutSeconds   int
	// CallArgsJSON carries TaskFlow literal call args captured by the parser
	// (#115). The agent injects this verbatim as LEOFLOW_CALL_ARGS_JSON; the
	// runtime decodes it. Empty when the task has no literals. The name
	// keeps Airflow's DAG-run `params` term free for a future feature (#148).
	CallArgsJSON string
	// OperatorClass is the dotted Airflow operator/sensor class for an
	// airflow_operator task (ADR 0040); empty for native operators. The agent
	// passes it to BuildCommand to dispatch the runtime's --operator mode.
	OperatorClass string
	// OperatorArgsJSON carries the operator's constructor kwargs (JSON). The agent
	// injects it as LEOFLOW_OPERATOR_ARGS; the runtime decodes it. Empty when the
	// operator takes no args.
	OperatorArgsJSON string
	// LogicalDate is the DagRun's logical date in RFC3339; the agent derives the
	// runtime's LEOFLOW_TS/LEOFLOW_DS from it (ADR 0040). Empty leaves them unset.
	LogicalDate string
	// DependsOn lists the task's upstream task_ids. The agent fetches each one's
	// return_value so a captured operator's ti.xcom_pull(<id>) resolves it (ADR 0040).
	DependsOn []string
	// DataIntervalStart/End are the DagRun's data interval in RFC3339; the agent
	// stamps the runtime's data_interval_start/end context from them (ADR 0040).
	DataIntervalStart string
	DataIntervalEnd   string
	// ParamsJSON is the DagRun's params/conf, JSON-encoded (#148); the agent stamps
	// LEOFLOW_PARAMS so the runtime exposes context['params'] / {{ params.X }}.
	ParamsJSON string
	// FirstRescheduleAt is when a reschedule-mode sensor first entered reschedule,
	// RFC3339 (#380). The agent stamps LEOFLOW_FIRST_RESCHEDULE_AT so the sensor's
	// get_first_reschedule_date returns it and cumulative timeout works. Empty on
	// the first attempt (not yet rescheduled).
	FirstRescheduleAt string
	// MaxTries is the task's total attempt budget (retries + 1). The agent stamps
	// LEOFLOW_MAX_TRIES so the runtime fires on_failure_callback only on the
	// terminal attempt (#424). Zero is treated as 1 (no retries).
	MaxTries int
	// OnFailureCallback marks that the task declares an Airflow on_failure_callback
	// (#424). The agent stamps LEOFLOW_ON_FAILURE_CALLBACK=1 so the runtime runs it
	// in-process on the task's final failure.
	OnFailureCallback bool
	// DeclaredVariables and DeclaredConnections are the secret names this task
	// declared (ADR 0045, ADR 0055): the task's own set when it narrows, otherwise
	// the DAG's. They are carried on the resolved spec so a later increment can
	// scope secret delivery server-side to the declared set. Today this is data
	// only: the secret RPCs still return the whole tenant vault, so a declaration
	// changes nothing about what the agent receives.
	DeclaredVariables   []string
	DeclaredConnections []string
}

// Authenticator verifies an agent bearer token into a task instance identity.
type Authenticator interface {
	AuthenticateAgent(token string) (*auth.AgentIdentity, error)
}

// AgentTokenRenewer re-mints a live attempt's agent token with a fresh short
// TTL, preserving the identity and the attempt's first-dispatch origin. It is
// consulted only on a liveness-proven heartbeat (ADR 0055 Fix #4). ok is false
// when the attempt has outlived its max-lifetime ceiling — the signal to let the
// credential lapse rather than refresh it. Implemented by *auth.JWTAuthenticator.
type AgentTokenRenewer interface {
	RenewAgentToken(token string, ttl, maxLifetime time.Duration) (string, bool, error)
}

// ErrStaleReport is returned by Store.ReportState when the report did not apply
// because the task instance had already moved on — a reaper settled it, or a
// retry advanced past the attempt the reporting agent was dispatched for. It is
// not a failure of the RPC: the agent did nothing wrong and must not retry, so
// the handler acknowledges and logs. Declared here rather than in the storage
// package because storage implements this interface, not the reverse.
var ErrStaleReport = errors.New("task state report did not apply: the task instance already moved on")

// Store is the server's view of persistent task state.
type Store interface {
	// TaskSpec returns the execution spec for the identified task instance.
	TaskSpec(ctx context.Context, id auth.AgentIdentity) (TaskSpec, error)
	// ReportState records a state transition reported by the agent.
	ReportState(ctx context.Context, id auth.AgentIdentity, state domain.TaskState, exitCode int, errMsg string) error
	// Reschedule parks an active TI in up_for_reschedule with its next-poke time so
	// the scheduler re-dispatches it later without consuming retry budget (#380).
	Reschedule(ctx context.Context, id auth.AgentIdentity, at time.Time) error
	// RecordHeartbeat stamps last_heartbeat_at on the identified TI so the
	// scheduler's heartbeat reaper (#128) can tell live tasks from agent-lost
	// ones. The state guard inside the SQL skips already-terminal rows.
	RecordHeartbeat(ctx context.Context, id auth.AgentIdentity) error
	// BindWarmAttempt records the durable warm-attempt binding (ADR 0058 N1d-a1):
	// the warm worker pod (workerPod) that acked this attempt (runID, taskID,
	// tryNumber) as started. The write is guarded on active state, so a settled
	// attempt is never bound (a benign no-op, not an error). Called only on a warm
	// ack — with warm pools off no assignment is ever acked, so it is never called
	// and warm_worker_id stays NULL.
	BindWarmAttempt(ctx context.Context, runID, taskID string, tryNumber, attemptEpoch int, workerPod string) error
}

// XComService stores and retrieves XCom values for the agent.
type XComService interface {
	Push(ctx context.Context, key xcom.Key, value []byte, contentType string, schema map[string]any) error
	Fetch(ctx context.Context, key xcom.Key) (xcom.Entry, error)
}

// LogSink opens a writer for a task attempt's streamed logs.
type LogSink interface {
	Open(ref logs.Ref) (logs.LogWriter, error)
}

// LogPublisher fans a log line out for live tailing (optional).
type LogPublisher interface {
	Publish(ctx context.Context, ref logs.Ref, line string) error
}

// Server implements agentv1.AgentServiceServer over a Store and Authenticator.
type Server struct {
	agentv1.UnimplementedAgentServiceServer
	auth                 Authenticator
	store                Store
	xcom                 XComService
	logs                 LogSink
	tail                 LogPublisher
	tailOnDemand         bool
	secrets              SecretsStore
	secretAudit          SecretScopeAuditor
	liveness             TaskLivenessChecker
	livenessMode         string
	livenessAudit        SecretLivenessAuditor
	scoping              string
	allowInsecureSecrets bool
	renewer              AgentTokenRenewer
	renewalTTL           time.Duration
	maxAttemptLifetime   time.Duration
	// Projected-SA-token exchange (ADR 0055 Fix #3). All nil/zero by default, so a
	// deployment that does not opt into the exchange transport is byte-identical to
	// today (env-var token); ExchangeToken then reports Unimplemented.
	reviewer              TokenReviewer
	podResolver           PodTaskResolver
	tokenMinter           AgentTokenMinter
	exchangeTTL           time.Duration
	allowInsecureExchange bool
	now                   func() time.Time
	// Warm worker assignment transport (ADR 0058 N1b). nil by default, so
	// AwaitAssignment is inert (returns FailedPrecondition) unless the operator
	// enabled warm pools and the server wired a registry via SetWarmPools.
	warmPools *WorkerRegistry
	// leaderCheck gates AwaitAssignment to the scheduler LEADER (warm-pool Hole B).
	// The warm-pool placer + reconciler run leader-only, but startAgentGRPC (and its
	// in-memory WorkerRegistry) runs on every scheduler replica; behind one Service,
	// a warm worker's stream can load-balance onto a FOLLOWER whose registry the
	// leader's placer never consults — orphaning that worker. A follower therefore
	// refuses the stream so the worker reconnects toward the leader. nil means
	// "unchecked" (single-node / tests serve as before); production wires it to the
	// scheduler's own leadership so exactly one replica — the leader — serves.
	leaderCheck func() bool
	// shutdown, when non-nil, is closed as the control plane begins shutting down.
	// StreamLogs is a bidi stream held open for the whole task; without this signal
	// its receive loop blocked on Recv until the agent finished, so a graceful stop
	// waited on every running task and the pod was SIGKILLed with its log writers
	// never Closed. AwaitAssignment is the same bug class from the other side: an
	// idle warm worker holds one open with nothing arriving on it at all, so with
	// warm pools on the graceful stop exhausted its bound on EVERY shutdown and the
	// forced fallback became the normal path. Every handler that can block
	// indefinitely on a peer must select on this. nil (the default) keeps streams
	// open until the peer ends them.
	shutdown <-chan struct{}
	// legacyTokens meters task tokens without an attempt_epoch claim (unset or
	// nil recorder: off). Atomic because it is attached after Serve starts.
	legacyTokens atomic.Pointer[legacyRecorderBox]
	// attemptSpecs caches each live attempt's XCom spec fields (attemptSpec) so
	// PushXCom and FetchXCom do not reload the full task spec on every call.
	attemptSpecs *attemptSpecCache
}

// NewServer builds an AgentService server backed by the given authenticator,
// store, and XCom service.
func NewServer(authn Authenticator, store Store, xcomSvc XComService) *Server {
	s := &Server{auth: authn, store: store, xcom: xcomSvc, now: time.Now}
	s.attemptSpecs = newAttemptSpecCache(attemptSpecCacheSize, attemptSpecCacheTTL, func() time.Time { return s.now() })
	return s
}

// SetTokenRenewal wires per-attempt token renewal (ADR 0055 Fix #4): on a
// liveness-proven heartbeat the server re-mints the caller's bearer with a fresh
// renewalTTL and returns it on HeartbeatResponse.renewed_token, so a long task
// keeps a working credential while the short TTL bounds a stolen/finished one.
// maxAttemptLifetime is the hard ceiling on an attempt's total credential age
// since dispatch (0 disables it). A nil renewer or non-positive renewalTTL
// leaves renewal off — the heartbeat returns no token (unchanged behavior).
func (s *Server) SetTokenRenewal(renewer AgentTokenRenewer, renewalTTL, maxAttemptLifetime time.Duration) {
	s.renewer, s.renewalTTL, s.maxAttemptLifetime = renewer, renewalTTL, maxAttemptLifetime
}

// SetShutdown wires the control plane's shutdown context: once ctx ends, every
// long-lived stream returns Unavailable, so the gRPC graceful stop that follows
// completes instead of waiting for the tasks themselves to finish. An open
// StreamLogs first closes (flushes) its log writer; the agent treats the closed
// stream as best-effort log delivery and keeps running its task. A StreamLogs
// that arrives AFTER the signal is refused before its writer is opened — the
// listener keeps accepting for the rest of the shutdown, and an opened-then-
// abandoned writer Puts an empty object over an attempt that is logging
// elsewhere. An open
// AwaitAssignment ends too — an idle warm worker holds one open indefinitely, so
// leaving it out would make the forced stop the normal shutdown path.
func (s *Server) SetShutdown(ctx context.Context) { s.shutdown = ctx.Done() }

// SetLogSink attaches the log sink that StreamLogs writes to. Without it,
// StreamLogs reports Unimplemented.
func (s *Server) SetLogSink(sink LogSink) { s.logs = sink }

// SetLogPublisher attaches the live-tail publisher (optional). When set,
// StreamLogs publishes each line for the UI's live tail.
func (s *Server) SetLogPublisher(p LogPublisher) { s.tail = p }

// SetTailPublishOnDemand selects logs.tail.publish: false (the default,
// "always") publishes every line as it arrives; true ("on_demand") publishes
// only while a probe finds a follower, replaying the lines held since the last
// probe that found none (see tailGate).
func (s *Server) SetTailPublishOnDemand(on bool) { s.tailOnDemand = on }

// tailGateFor builds the live-tail gate for one attempt's log stream.
func (s *Server) tailGateFor(ref logs.Ref) *tailGate {
	return newTailGate(s.tail, ref, time.Now, s.tailOnDemand)
}

// Register acknowledges an agent's startup and returns the server clock.
func (s *Server) Register(ctx context.Context, _ *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	return &agentv1.RegisterResponse{
		SessionId:  id.TaskInstanceID,
		ServerTime: timestamppb.New(s.now()),
	}, nil
}

// GetTaskSpec returns the execution spec for the calling task instance.
func (s *Server) GetTaskSpec(ctx context.Context, _ *agentv1.GetTaskSpecRequest) (*agentv1.TaskSpec, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	if aerr := s.requireAttemptToken(id); aerr != nil {
		return nil, aerr
	}
	spec, err := s.store.TaskSpec(ctx, *id)
	if err != nil {
		return nil, internalStatus("loading task spec", err, attemptAttrs(id)...)
	}
	s.attemptSpecs.put(*id, attemptSpecOf(spec))
	return &agentv1.TaskSpec{
		TenantId:                id.TenantID,
		DagId:                   id.DagID,
		DagVersion:              spec.DagVersion,
		RunId:                   id.RunID,
		TaskId:                  id.TaskID,
		TryNumber:               clampInt32(id.TryNumber),
		AttemptEpoch:            int64(id.AttemptEpoch),
		Operator:                spec.Operator,
		Entrypoint:              spec.Entrypoint,
		Environment:             spec.Environment,
		XcomInputMapping:        toXComUpstreamsMap(spec.XComInputMapping),
		ExecutionTimeoutSeconds: clampInt32(spec.TimeoutSeconds),
		CallArgsJson:            spec.CallArgsJSON,
		OperatorClass:           spec.OperatorClass,
		OperatorArgsJson:        spec.OperatorArgsJSON,
		LogicalDate:             spec.LogicalDate,
		DependsOn:               spec.DependsOn,
		DataIntervalStart:       spec.DataIntervalStart,
		DataIntervalEnd:         spec.DataIntervalEnd,
		ParamsJson:              spec.ParamsJSON,
		FirstRescheduleAt:       spec.FirstRescheduleAt,
		MaxTries:                clampInt32(spec.MaxTries),
		OnFailureCallback:       spec.OnFailureCallback,
		DeclaredVariables:       spec.DeclaredVariables,
		DeclaredConnections:     spec.DeclaredConnections,
	}, nil
}

// ReportState records a state transition the agent observed for its task.
func (s *Server) ReportState(ctx context.Context, req *agentv1.ReportStateRequest) (*agentv1.ReportStateResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	if aerr := s.requireAttemptToken(id); aerr != nil {
		return nil, aerr
	}
	// Any report but running ends this attempt, so its cached XCom spec goes. A
	// later RPC of the same attempt (a retried report) just reloads it.
	if req.GetState() != agentv1.TaskState_TASK_STATE_RUNNING {
		s.attemptSpecs.drop(*id)
	}
	// A reschedule-mode sensor reports up_for_reschedule + its next-poke time; route
	// it to the dedicated store path that persists reschedule_at, instead of the
	// generic state write (#380).
	if req.GetState() == agentv1.TaskState_TASK_STATE_UP_FOR_RESCHEDULE {
		if rerr := s.store.Reschedule(ctx, *id, req.GetRescheduleAt().AsTime()); rerr != nil {
			// The poke came from a superseded attempt (or onto a settled row): the
			// same "moved on" answer a stale state report gets (ADR 0051 amendment).
			if errors.Is(rerr, ErrStaleReport) {
				slog.Warn("ignoring stale reschedule report; signaling terminate", attemptAttrs(id)...)
				return &agentv1.ReportStateResponse{Acknowledged: true, ShouldTerminate: true}, nil
			}
			return nil, internalStatus("recording reschedule", rerr, attemptAttrs(id)...)
		}
		return &agentv1.ReportStateResponse{Acknowledged: true}, nil
	}
	state, err := mapState(req.GetState())
	if err != nil {
		return nil, err
	}
	if rerr := s.store.ReportState(ctx, *id, state, int(req.GetExitCode()), req.GetErrorMessage()); rerr != nil {
		// A stale report is the guard working, not a fault: the row was already
		// settled or already on a later attempt. Acknowledge so the agent stops
		// (retrying would never apply either), but log it — a late report is
		// evidence of a partition or a slow pod, and dropping it silently is the
		// failure mode the guard exists to end.
		if errors.Is(rerr, ErrStaleReport) {
			slog.Warn("ignoring stale task state report; signaling terminate",
				"ti", id.TaskInstanceID, "run", id.RunID, "task", id.TaskID,
				"try", id.TryNumber, "reported_state", state)
			// The row already moved on (a reaper settled it, or a later attempt
			// bumped try_number). Acknowledge so the agent stops retrying, and set
			// should_terminate so a reaped-but-still-running pod cancels its work —
			// the at-most-once kill switch (#474). Safe by construction: the report
			// applies (rows>0, not stale) for the live, matching attempt, so a live
			// execution is never told to terminate.
			return &agentv1.ReportStateResponse{Acknowledged: true, ShouldTerminate: true}, nil
		}
		return nil, internalStatus("recording state", rerr, attemptAttrs(id)...)
	}
	return &agentv1.ReportStateResponse{Acknowledged: true}, nil
}

// Heartbeat stamps the per-TI liveness signal (#128) and returns the server
// clock so the agent can detect skew. A storage error stamping the heartbeat
// is logged but does not fail the RPC — failing the call would risk the
// agent terminating itself unnecessarily on a transient DB blip. The
// scheduler reaper would, in the worst case, fail the TI as agent_lost on
// the next tick; correct under "do no harm" (ADR 0031).
func (s *Server) Heartbeat(ctx context.Context, _ *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	if aerr := s.requireAttemptToken(id); aerr != nil {
		return nil, aerr
	}
	if hbErr := s.store.RecordHeartbeat(ctx, *id); hbErr != nil {
		// A stale heartbeat is the guard working, not a fault: the row moved on —
		// a reaper settled it, or a later attempt bumped try_number past this one
		// (the same "moved on" predicate the state report is guarded by, #467).
		// Signal terminate so a reaped-but-alive pod stops itself (#474). The live,
		// matching attempt stamps a row (no error), so it never gets the signal.
		// Deliberately NO renewed token here: a superseded attempt's credential must
		// lapse, never be refreshed — renewal happens ONLY on the liveness-proven
		// (nil) branch below.
		if errors.Is(hbErr, ErrStaleReport) {
			slog.Warn("stale heartbeat from a superseded attempt; signaling terminate",
				"ti", id.TaskInstanceID, "run", id.RunID, "task", id.TaskID, "try", id.TryNumber)
			return &agentv1.HeartbeatResponse{ServerTime: timestamppb.New(s.now()), ShouldTerminate: true}, nil
		}
		// A genuine (non-stale) store error is logged but does not fail the RPC or
		// signal terminate — failing would risk the agent killing a live task on a
		// transient DB blip. The scheduler's heartbeat reaper is the backstop. It
		// also does NOT renew: liveness is not proven, so the credential is not
		// refreshed on a blip.
		slog.Warn("recording heartbeat",
			"ti", id.TaskInstanceID, "run", id.RunID, "task", id.TaskID, "error", hbErr)
		return &agentv1.HeartbeatResponse{ServerTime: timestamppb.New(s.now())}, nil
	}
	// Liveness proven (RecordHeartbeat applied for this exact attempt): refresh the
	// bearer so a long-running task keeps a working credential while the short
	// per-attempt TTL bounds a stolen or finished one (ADR 0055 Fix #4).
	resp := &agentv1.HeartbeatResponse{ServerTime: timestamppb.New(s.now())}
	s.renewToken(ctx, id, resp)
	return resp, nil
}

// renewToken re-mints the caller's agent token onto resp.RenewedToken when a
// renewer is configured and the attempt has not outlived its lifetime ceiling.
// It runs ONLY on the liveness-proven heartbeat branch. Best-effort: a renewer
// error or a past-ceiling result leaves the response without a renewed token
// (the agent keeps its current bearer) and never fails the heartbeat. The token
// is never logged.
func (s *Server) renewToken(ctx context.Context, id *auth.AgentIdentity, resp *agentv1.HeartbeatResponse) {
	if s.renewer == nil || s.renewalTTL <= 0 {
		return
	}
	token, ok := bearerFromContext(ctx)
	if !ok || token == "" {
		return
	}
	renewed, ok, err := s.renewer.RenewAgentToken(token, s.renewalTTL, s.maxAttemptLifetime)
	if err != nil {
		slog.Warn("renewing agent token on heartbeat; agent keeps its current bearer",
			"ti", id.TaskInstanceID, "run", id.RunID, "task", id.TaskID, "try", id.TryNumber, "error", err)
		return
	}
	if !ok {
		// Past max_attempt_credential_lifetime: let the credential lapse rather than
		// keep a runaway attempt alive.
		slog.Warn("not renewing agent token: attempt past max_attempt_credential_lifetime",
			"ti", id.TaskInstanceID, "run", id.RunID, "task", id.TaskID, "try", id.TryNumber)
		return
	}
	resp.RenewedToken = renewed
}

// PushXCom stores a value the task produced, keyed by the caller's identity.
// Size/schema violations are returned as a rejection, not a transport error, so
// the agent can fail the task with a clear reason.
func (s *Server) PushXCom(ctx context.Context, req *agentv1.PushXComRequest) (*agentv1.PushXComResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	if aerr := s.requireAttemptToken(id); aerr != nil {
		return nil, aerr
	}
	spec, err := s.xcomSpec(ctx, id)
	if err != nil {
		return nil, internalStatus("loading task spec", err, attemptAttrs(id)...)
	}
	key := xcomKey(*id, id.TaskID, req.GetKey())
	perr := s.xcom.Push(ctx, key, req.GetValue(), req.GetContentType(), spec.XComSchema)
	switch {
	case errors.Is(perr, xcom.ErrTooLarge):
		return &agentv1.PushXComResponse{Accepted: false, RejectionReason: "payload_too_large"}, nil
	case errors.Is(perr, xcom.ErrSchemaMismatch):
		return &agentv1.PushXComResponse{Accepted: false, RejectionReason: "schema_mismatch"}, nil
	case perr != nil:
		return nil, internalStatus("storing xcom", perr, attemptAttrs(id)...)
	}
	return &agentv1.PushXComResponse{Accepted: true}, nil
}

// FetchXCom returns an upstream task's value, but only from a task the caller
// declared as an XCom input within the same run (and, by construction, the same
// tenant), enforcing cross-tenant and cross-run isolation.
func (s *Server) FetchXCom(ctx context.Context, req *agentv1.FetchXComRequest) (*agentv1.FetchXComResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	if aerr := s.requireAttemptToken(id); aerr != nil {
		return nil, aerr
	}
	spec, err := s.xcomSpec(ctx, id)
	if err != nil {
		return nil, internalStatus("loading task spec", err, attemptAttrs(id)...)
	}
	// A task may fetch XCom from an upstream it declared as an xcom input OR from any
	// of its direct dependencies (depends_on) — the latter powers a captured
	// operator's ti.xcom_pull(<upstream>) chaining (ADR 0040), like Airflow. Anything
	// else is denied to keep tasks from reading unrelated tasks' XCom.
	if !declaresUpstream(spec.XComInputMapping, req.GetUpstreamTaskId()) &&
		!slices.Contains(spec.DependsOn, req.GetUpstreamTaskId()) {
		return nil, status.Errorf(codes.PermissionDenied, "task %q may not read xcom from %q (not a declared input or dependency)", id.TaskID, req.GetUpstreamTaskId())
	}
	entry, err := s.xcom.Fetch(ctx, xcomKey(*id, req.GetUpstreamTaskId(), req.GetKey()))
	if errors.Is(err, xcom.ErrNotFound) {
		return nil, status.Errorf(codes.NotFound, "no xcom for task %q", req.GetUpstreamTaskId())
	}
	if err != nil {
		return nil, internalStatus("reading xcom", err, attemptAttrs(id)...)
	}
	return &agentv1.FetchXComResponse{
		Value:       entry.Value,
		ContentType: entry.ContentType,
		SizeBytes:   clampInt32(entry.SizeBytes),
		CreatedAt:   timestamppb.New(entry.CreatedAt),
	}, nil
}

// StreamLogs receives the task's log lines and writes them through the sink,
// flushing on stream end so the logs survive the pod. The stream also ends when
// the control plane shuts down (SetShutdown), so the flush runs before exit —
// and a stream that ARRIVES after that signal is refused before any writer is
// opened, so it leaves no empty object behind.
func (s *Server) StreamLogs(stream agentv1.AgentService_StreamLogsServer) (err error) {
	id, ierr := s.identify(stream.Context())
	if ierr != nil {
		return ierr
	}
	if aerr := s.requireAttemptToken(id); aerr != nil {
		return aerr
	}
	if s.logs == nil {
		return status.Error(codes.Unimplemented, "log shipping is not configured")
	}
	// Refuse BEFORE a writer exists. The gRPC listener is the last thing the
	// process stops, so it keeps ACCEPTING new log streams for the whole of the
	// HTTP shutdown plus the dispatch drain after SIGTERM. Opening a writer for
	// one of those and only then noticing the shutdown left a deferred Close that
	// Put an EMPTY object — and an empty object means "the attempt ran and was
	// silent" to every reader of this sink, which is a lie about an attempt whose
	// lines are going to a live replica instead. A nil channel never fires, so an
	// unwired server (tests, embedders) is unaffected (#918).
	select {
	case <-s.shutdown:
		return status.Error(codes.Unavailable, "control plane shutting down; log stream not accepted")
	default:
	}
	w, oerr := s.logs.Open(logs.Ref{
		TenantID: id.TenantID, DagID: id.DagID, RunID: id.RunID, TaskID: id.TaskID, TryNumber: id.TryNumber,
	})
	if oerr != nil {
		// The agent is told WHICH step failed — without that it sees only a bare
		// stream EOF, with no server-side explanation (#36) — but not the sink's
		// own text, which carries host paths, bucket names and credential-adjacent
		// detail into the tenant's pod. internalStatus records the cause on the
		// control-plane log, keyed by the attempt (#1068).
		return internalStatus("opening log sink for task; logs will not be shipped", oerr, attemptAttrs(id)...)
	}
	defer func() {
		cerr := w.Close()
		if cerr == nil {
			return
		}
		if err == nil {
			err = internalStatus("flushing logs", cerr, attemptAttrs(id)...)
			return
		}
		// The stream already ended with its own error (the shutdown path returns
		// Unavailable), so the failed final flush cannot ride on the return
		// value; it is the signal that this attempt's tail did not reach the
		// store, and it must not be silent.
		slog.Warn("flushing logs on stream end failed; the attempt's tail may not have reached the store",
			"dag", id.DagID, "run", id.RunID, "task", id.TaskID, "try", id.TryNumber, "error", cerr)
	}()

	ref := logs.Ref{TenantID: id.TenantID, DagID: id.DagID, RunID: id.RunID, TaskID: id.TaskID, TryNumber: id.TryNumber}
	publish := func(string) {}
	if s.tail != nil {
		gate := s.tailGateFor(ref)
		publish = func(line string) { gate.publish(stream.Context(), line) }
	}
	return writeLines(s.shutdown, w, stream.Recv, publish, attemptAttrs(id))
}

// logLevelString maps the protobuf log level onto the lowercase level name the
// UI's log viewer colors by. Unspecified defaults to info.
func logLevelString(level agentv1.LogLevel) string {
	switch level {
	case agentv1.LogLevel_LOG_LEVEL_DEBUG:
		return "debug"
	case agentv1.LogLevel_LOG_LEVEL_WARN:
		return "warning"
	case agentv1.LogLevel_LOG_LEVEL_ERROR:
		return "error"
	default:
		return "info"
	}
}

// receivedLine is one Recv result handed from the receiver goroutine to the
// writer loop.
type receivedLine struct {
	line *agentv1.LogLine
	err  error
}

// writeLines drains log lines from recv into the writer until the stream ends
// or shutdown fires, also publishing each line for live tailing. recv blocks for
// as long as the agent keeps the stream open (the whole task), so it runs on its
// own goroutine and the loop selects between the next line and the shutdown
// signal; on shutdown it returns Unavailable so the caller's deferred writer
// Close (the final flush) runs while the process is still alive. A nil shutdown
// channel never fires. The receiver goroutine exits once recv returns, which
// gRPC guarantees after the handler returns and the stream context is canceled.
// attrs carries the attempt identity onto the cause log line of a redacted
// failure (#1068); nil is fine for callers that have none.
func writeLines(shutdown <-chan struct{}, w logs.LogWriter, recv func() (*agentv1.LogLine, error), publish func(string), attrs []any) error {
	lines := make(chan receivedLine)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			line, err := recv()
			select {
			case lines <- receivedLine{line: line, err: err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	for {
		var rl receivedLine
		select {
		case rl = <-lines:
		case <-shutdown:
			return status.Error(codes.Unavailable, "control plane shutting down; log stream closed")
		}
		if errors.Is(rl.err, io.EOF) {
			return nil
		}
		if rl.err != nil {
			return peerStatus("receiving log line", rl.err, attrs...)
		}
		if werr := writeLine(w, rl.line, publish, attrs); werr != nil {
			return werr
		}
	}
}

// writeLine stores one received line and publishes it for live tailing. attrs
// carries the attempt identity onto the cause log line of a redacted failure.
func writeLine(w logs.LogWriter, line *agentv1.LogLine, publish func(string), attrs []any) error {
	msg := line.GetMessage()
	// The agent derives the wire level from the source stream (stdout=info,
	// stderr=error), which mis-colors an error printed to stdout or an info
	// line written to stderr (e.g. Python logging). Refine it from the line's
	// own text when that carries a clear level token; otherwise the
	// stream-derived level stands. The Event shape and stream are unchanged.
	ev := logs.Event{
		Time:    line.GetTime().AsTime(),
		Level:   logs.RefineLevel(msg, logLevelString(line.GetLevel())),
		Stream:  line.GetStream(),
		Message: msg,
	}
	// Encode once: the same JSON line is stored and published. The full event
	// (level/stream/ts), not just the text, so a live NDJSON follower can color
	// lines exactly like the stored drill-down.
	encoded := logs.EncodeLine(ev)
	var werr error
	if lw, ok := w.(logs.LineWriter); ok {
		werr = lw.WriteLine(encoded)
	} else {
		werr = w.WriteEvent(ev)
	}
	if werr != nil {
		return internalStatus("writing log line", werr, attrs...)
	}
	publish(encoded)
	return nil
}

// xcomKey builds the XCom key for a task within the caller's tenant/dag/run.
func xcomKey(id auth.AgentIdentity, taskID, name string) xcom.Key {
	if name == "" {
		name = "return_value"
	}
	return xcom.Key{TenantID: id.TenantID, DagID: id.DagID, RunID: id.RunID, TaskID: taskID, Name: name}
}

// declaresUpstream reports whether the task declared upstreamTaskID as an input
// to any parameter. A fan-in parameter (`combine([a(), b(), c()])`) declares
// each upstream in the list, so a FetchXCom from any of them is authorized.
func declaresUpstream(mapping map[string][]string, upstreamTaskID string) bool {
	for _, upstreams := range mapping {
		for _, declared := range upstreams {
			if declared == upstreamTaskID {
				return true
			}
		}
	}
	return false
}

// toXComUpstreamsMap converts the domain map[string][]string into the proto
// shape map[string]*XComUpstreams the agent receives over the wire.
func toXComUpstreamsMap(in map[string][]string) map[string]*agentv1.XComUpstreams {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]*agentv1.XComUpstreams, len(in))
	for param, upstreams := range in {
		out[param] = &agentv1.XComUpstreams{TaskIds: append([]string(nil), upstreams...)}
	}
	return out
}

// identify extracts and verifies the agent token from the request metadata.
func (s *Server) identify(ctx context.Context) (*auth.AgentIdentity, error) {
	token, ok := bearerFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing authorization token")
	}
	id, err := s.auth.AuthenticateAgent(token)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid agent token")
	}
	return id, nil
}

// bearerFromContext pulls the raw bearer token (Bearer prefix stripped) from the
// incoming gRPC metadata. ok is false when no metadata or no authorization
// header is present. It is the shared extraction used by both identify (to
// authenticate) and renewToken (to re-mint the same token).
func bearerFromContext(ctx context.Context) (string, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", false
	}
	return strings.TrimPrefix(values[0], "Bearer "), true
}

// mapState translates a protobuf task state into the domain vocabulary.
func mapState(state agentv1.TaskState) (domain.TaskState, error) {
	switch state {
	case agentv1.TaskState_TASK_STATE_RUNNING:
		return domain.TaskStateRunning, nil
	case agentv1.TaskState_TASK_STATE_SUCCESS:
		return domain.TaskStateSuccess, nil
	case agentv1.TaskState_TASK_STATE_FAILED:
		return domain.TaskStateFailed, nil
	case agentv1.TaskState_TASK_STATE_SKIPPED:
		return domain.TaskStateSkipped, nil
	case agentv1.TaskState_TASK_STATE_UP_FOR_RESCHEDULE:
		return domain.TaskStateUpForReschedule, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "unsupported task state %v", state)
	}
}

// clampInt32 narrows a non-negative count to int32 for the wire protocol.
func clampInt32(n int) int32 {
	if n < 0 {
		return 0
	}
	if n > 1<<31-1 {
		return 1<<31 - 1
	}
	return int32(n)
}
