package agentrpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/xcom"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// fakeStore records ReportState calls and serves a fixed task spec.
type fakeStore struct {
	spec          TaskSpec
	specErr       error
	reported      []reportedState
	reportErr     error
	rescheduled   []rescheduleCall
	rescheduleErr error
	heartbeats    []auth.AgentIdentity
	heartbeatErr  error
	// warm bindings are recorded from the AwaitAssignment pump goroutine while the
	// test asserts from its own goroutine, so this slice needs its own lock.
	warmMu       sync.Mutex
	warmBindings []warmBindingCall
	bindWarmErr  error
}

type warmBindingCall struct {
	runID     string
	taskID    string
	tryNumber int
	workerPod string
}

type reportedState struct {
	id       auth.AgentIdentity
	state    domain.TaskState
	exitCode int
	errMsg   string
}

type rescheduleCall struct {
	id auth.AgentIdentity
	at time.Time
}

func (s *fakeStore) TaskSpec(context.Context, auth.AgentIdentity) (TaskSpec, error) {
	return s.spec, s.specErr
}

func (s *fakeStore) ReportState(_ context.Context, id auth.AgentIdentity, st domain.TaskState, exit int, msg string) error {
	s.reported = append(s.reported, reportedState{id, st, exit, msg})
	return s.reportErr
}

func (s *fakeStore) Reschedule(_ context.Context, id auth.AgentIdentity, at time.Time) error {
	s.rescheduled = append(s.rescheduled, rescheduleCall{id, at})
	return s.rescheduleErr
}

func (s *fakeStore) RecordHeartbeat(_ context.Context, id auth.AgentIdentity) error {
	s.heartbeats = append(s.heartbeats, id)
	return s.heartbeatErr
}

func (s *fakeStore) BindWarmAttempt(_ context.Context, runID, taskID string, tryNumber int, workerPod string) error {
	s.warmMu.Lock()
	s.warmBindings = append(s.warmBindings, warmBindingCall{runID, taskID, tryNumber, workerPod})
	s.warmMu.Unlock()
	return s.bindWarmErr
}

// warmBindingCalls returns a snapshot of the recorded BindWarmAttempt calls,
// taken under the lock so a polling assertion never races the pump goroutine.
func (s *fakeStore) warmBindingCalls() []warmBindingCall {
	s.warmMu.Lock()
	defer s.warmMu.Unlock()
	return append([]warmBindingCall(nil), s.warmBindings...)
}

func testIdentity() auth.AgentIdentity {
	return auth.AgentIdentity{
		TaskInstanceID: "ti-1", TenantID: "acme", DagID: "etl",
		RunID: "run-1", TaskID: "extract", TryNumber: 1,
	}
}

// ctxWithToken builds an incoming context carrying a freshly minted (task-scoped)
// agent token — the default per-attempt credential.
func ctxWithToken(t *testing.T, a *auth.JWTAuthenticator) context.Context {
	t.Helper()
	token, err := a.IssueAgentToken(testIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	md := metadata.Pairs("authorization", "Bearer "+token)
	return metadata.NewIncomingContext(context.Background(), md)
}

// warmTokenIdentity is the warm-worker credential the assignment stream authorizes
// (ADR 0058 D2): a control-channel-only token whose WorkerID is the registry key.
// It reuses "ti-1" as the worker id so the warm-pool tests' registry assertions
// (reg.registered("ti-1")) read the authenticated identity.
func warmTokenIdentity() auth.AgentIdentity {
	return auth.AgentIdentity{Scope: auth.ScopeWarmWorker, WorkerID: "ti-1", DagVersionID: "dagver-1", TenantID: "acme"}
}

// ctxWithWarmToken builds an incoming context carrying a freshly minted
// WARM-WORKER-scoped token — the bootstrap credential that authorizes only
// Register + AwaitAssignment.
func ctxWithWarmToken(t *testing.T, a *auth.JWTAuthenticator) context.Context {
	t.Helper()
	token, err := a.IssueAgentToken(warmTokenIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken (warm): %v", err)
	}
	md := metadata.Pairs("authorization", "Bearer "+token)
	return metadata.NewIncomingContext(context.Background(), md)
}

type fakeXCom struct {
	entries map[string]xcom.Entry
	pushErr error
}

func (x *fakeXCom) Push(_ context.Context, key xcom.Key, value []byte, ct string, _ map[string]any) error {
	if x.pushErr != nil {
		return x.pushErr
	}
	if x.entries == nil {
		x.entries = map[string]xcom.Entry{}
	}
	x.entries[key.String()] = xcom.Entry{Value: value, ContentType: ct}
	return nil
}

func (x *fakeXCom) Fetch(_ context.Context, key xcom.Key) (xcom.Entry, error) {
	if e, ok := x.entries[key.String()]; ok {
		return e, nil
	}
	return xcom.Entry{}, xcom.ErrNotFound
}

func newServer(store Store) (*Server, *auth.JWTAuthenticator) {
	return newServerX(store, &fakeXCom{})
}

func newServerX(store Store, x XComService) (*Server, *auth.JWTAuthenticator) {
	a := auth.NewJWTAuthenticator(nil, "secret", time.Hour)
	return NewServer(a, store, x), a
}

func TestGetTaskSpecMapsStoreData(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{
		Operator: "python", Entrypoint: "dag:hello", DagVersion: "v1",
		Environment:         map[string]string{"FOO": "bar"},
		XComInputMapping:    map[string][]string{"val": {"upstream"}},
		TimeoutSeconds:      300,
		DeclaredVariables:   []string{"region"},
		DeclaredConnections: []string{"warehouse"},
	}}
	srv, a := newServer(store)

	spec, err := srv.GetTaskSpec(ctxWithToken(t, a), &agentv1.GetTaskSpecRequest{})
	if err != nil {
		t.Fatalf("GetTaskSpec: %v", err)
	}
	if spec.GetOperator() != "python" || spec.GetEntrypoint() != "dag:hello" {
		t.Errorf("operator/entrypoint = %q/%q", spec.GetOperator(), spec.GetEntrypoint())
	}
	if spec.GetTaskId() != "extract" || spec.GetRunId() != "run-1" || spec.GetTryNumber() != 1 {
		t.Errorf("identity not propagated: %+v", spec)
	}
	if spec.GetEnvironment()["FOO"] != "bar" {
		t.Errorf("environment not mapped: %v", spec.GetEnvironment())
	}
	if upstreams := spec.GetXcomInputMapping()["val"]; upstreams == nil || len(upstreams.GetTaskIds()) != 1 || upstreams.GetTaskIds()[0] != "upstream" {
		t.Errorf("xcom mapping not mapped: %v", spec.GetXcomInputMapping())
	}
	if spec.GetExecutionTimeoutSeconds() != 300 {
		t.Errorf("timeout = %d", spec.GetExecutionTimeoutSeconds())
	}
	// The declared secret names must reach the agent so a pod-side resolver asks
	// the external backend only for what the DAG declared (ADR 0060 B2). They live
	// server-side today; GetTaskSpec must thread them onto the agent proto.
	if got := spec.GetDeclaredVariables(); len(got) != 1 || got[0] != "region" {
		t.Errorf("declared variables = %v, want [region]", got)
	}
	if got := spec.GetDeclaredConnections(); len(got) != 1 || got[0] != "warehouse" {
		t.Errorf("declared connections = %v, want [warehouse]", got)
	}
}

func TestRPCsRejectMissingToken(t *testing.T) {
	srv, _ := newServer(&fakeStore{})
	if _, err := srv.GetTaskSpec(context.Background(), &agentv1.GetTaskSpecRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("missing token: code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestRPCsRejectInvalidToken(t *testing.T) {
	srv, _ := newServer(&fakeStore{})
	md := metadata.Pairs("authorization", "Bearer not-a-token")
	ctx := metadata.NewIncomingContext(context.Background(), md)
	if _, err := srv.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("invalid token: code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestReportStateAppliesTransition(t *testing.T) {
	store := &fakeStore{}
	srv, a := newServer(store)

	_, err := srv.ReportState(ctxWithToken(t, a), &agentv1.ReportStateRequest{
		State:    agentv1.TaskState_TASK_STATE_SUCCESS,
		ExitCode: 0,
	})
	if err != nil {
		t.Fatalf("ReportState: %v", err)
	}
	if len(store.reported) != 1 {
		t.Fatalf("expected one reported state, got %d", len(store.reported))
	}
	got := store.reported[0]
	if got.state != domain.TaskStateSuccess {
		t.Errorf("state = %q, want success", got.state)
	}
	if got.id.TaskInstanceID != "ti-1" {
		t.Errorf("identity = %+v", got.id)
	}
}

func TestReportStateReschedule(t *testing.T) {
	store := &fakeStore{}
	srv, a := newServer(store)
	at := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)

	_, err := srv.ReportState(ctxWithToken(t, a), &agentv1.ReportStateRequest{
		State:        agentv1.TaskState_TASK_STATE_UP_FOR_RESCHEDULE,
		RescheduleAt: timestamppb.New(at),
	})
	if err != nil {
		t.Fatalf("ReportState(up_for_reschedule): %v", err)
	}
	// Routed to the dedicated reschedule path with the next-poke time, NOT the
	// generic state write (#380).
	if len(store.reported) != 0 {
		t.Errorf("up_for_reschedule must not go through ReportState, got %d", len(store.reported))
	}
	if len(store.rescheduled) != 1 {
		t.Fatalf("expected one Reschedule call, got %d", len(store.rescheduled))
	}
	if got := store.rescheduled[0]; !got.at.Equal(at) || got.id.TaskInstanceID != "ti-1" {
		t.Errorf("reschedule call = %+v, want at=%s ti=ti-1", got, at)
	}
}

func TestReportStateRejectsUnknownState(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	if _, err := srv.ReportState(ctxWithToken(t, a), &agentv1.ReportStateRequest{
		State: agentv1.TaskState_TASK_STATE_UNSPECIFIED,
	}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("unspecified state: code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestPushXComStoresUnderIdentityKey(t *testing.T) {
	x := &fakeXCom{}
	srv, a := newServerX(&fakeStore{spec: TaskSpec{Operator: "python"}}, x)

	resp, err := srv.PushXCom(ctxWithToken(t, a), &agentv1.PushXComRequest{Value: []byte(`{"rows":1}`)})
	if err != nil {
		t.Fatalf("PushXCom: %v", err)
	}
	if !resp.GetAccepted() {
		t.Fatalf("push not accepted: %s", resp.GetRejectionReason())
	}
	// identity = ti-1 / acme / etl / run-1 / extract; default key return_value.
	if _, ok := x.entries["xcom:acme:etl:run-1:extract:return_value"]; !ok {
		t.Errorf("stored keys = %v, want the identity-derived key", x.entries)
	}
}

func TestPushXComRejectsOversizeAndSchema(t *testing.T) {
	cases := map[string]struct {
		err    error
		reason string
	}{
		"too large":       {xcom.ErrTooLarge, "payload_too_large"},
		"schema mismatch": {xcom.ErrSchemaMismatch, "schema_mismatch"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv, a := newServerX(&fakeStore{spec: TaskSpec{Operator: "python"}}, &fakeXCom{pushErr: c.err})
			resp, err := srv.PushXCom(ctxWithToken(t, a), &agentv1.PushXComRequest{Value: []byte(`x`)})
			if err != nil {
				t.Fatalf("PushXCom returned a transport error: %v", err)
			}
			if resp.GetAccepted() || resp.GetRejectionReason() != c.reason {
				t.Errorf("rejection = (%v, %q), want (false, %q)", resp.GetAccepted(), resp.GetRejectionReason(), c.reason)
			}
		})
	}
}

func TestFetchXComReturnsDeclaredUpstream(t *testing.T) {
	x := &fakeXCom{entries: map[string]xcom.Entry{
		"xcom:acme:etl:run-1:upstream:return_value": {Value: []byte(`{"n":9}`), ContentType: "application/json"},
	}}
	store := &fakeStore{spec: TaskSpec{XComInputMapping: map[string][]string{"val": {"upstream"}}}}
	srv, a := newServerX(store, x)

	resp, err := srv.FetchXCom(ctxWithToken(t, a), &agentv1.FetchXComRequest{UpstreamTaskId: "upstream"})
	if err != nil {
		t.Fatalf("FetchXCom: %v", err)
	}
	if string(resp.GetValue()) != `{"n":9}` {
		t.Errorf("value = %s", resp.GetValue())
	}
}

func TestFetchXComAllowsDependsOnUpstream(t *testing.T) {
	// A captured operator chains via ti.xcom_pull(<upstream>) (ADR 0040): the
	// upstream is a declared dependency (depends_on) but not an xcom_input binding.
	// FetchXCom must authorize it (Airflow lets a task pull an upstream), else the
	// agent's xcom-by-task delivery fails the task — the k3d operator-e2e regression.
	x := &fakeXCom{entries: map[string]xcom.Entry{
		"xcom:acme:etl:run-1:compile:return_value": {Value: []byte(`{"name":"abc"}`), ContentType: "application/json"},
	}}
	store := &fakeStore{spec: TaskSpec{DependsOn: []string{"compile"}}}
	srv, a := newServerX(store, x)

	resp, err := srv.FetchXCom(ctxWithToken(t, a), &agentv1.FetchXComRequest{UpstreamTaskId: "compile"})
	if err != nil {
		t.Fatalf("FetchXCom for a depends_on upstream: %v", err)
	}
	if string(resp.GetValue()) != `{"name":"abc"}` {
		t.Errorf("value = %s", resp.GetValue())
	}
}

func TestFetchXComDeniesUndeclaredUpstream(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{XComInputMapping: map[string][]string{"val": {"other"}}}}
	srv, a := newServerX(store, &fakeXCom{})
	_, err := srv.FetchXCom(ctxWithToken(t, a), &agentv1.FetchXComRequest{UpstreamTaskId: "secret"})
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("fetching an undeclared upstream: code = %v, want PermissionDenied", status.Code(err))
	}
}

func TestFetchXComNotFound(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{XComInputMapping: map[string][]string{"val": {"upstream"}}}}
	srv, a := newServerX(store, &fakeXCom{})
	_, err := srv.FetchXCom(ctxWithToken(t, a), &agentv1.FetchXComRequest{UpstreamTaskId: "upstream"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("missing xcom: code = %v, want NotFound", status.Code(err))
	}
}

type fakeLogWriter struct {
	lines  []string
	closed bool
}

func (w *fakeLogWriter) WriteEvent(ev logs.Event) error {
	w.lines = append(w.lines, ev.Message)
	return nil
}
func (w *fakeLogWriter) Close() error { w.closed = true; return nil }

func TestWriteLinesDrainsStreamToSink(t *testing.T) {
	msgs := []string{"line one", "line two", "line three"}
	i := 0
	recv := func() (*agentv1.LogLine, error) {
		if i >= len(msgs) {
			return nil, io.EOF
		}
		m := msgs[i]
		i++
		return &agentv1.LogLine{Message: m}, nil
	}
	w := &fakeLogWriter{}
	var published []string
	if err := writeLines(nil, w, recv, func(l string) { published = append(published, l) }, nil); err != nil {
		t.Fatalf("writeLines: %v", err)
	}
	if len(w.lines) != 3 || w.lines[2] != "line three" {
		t.Errorf("written lines = %v, want the three messages", w.lines)
	}
	// The tail channel now carries the full event JSON (level/stream/ts), so a
	// live NDJSON follower can color lines; decoding it yields the message.
	if len(published) != 3 || logs.DecodeLine(published[0]).Message != "line one" {
		t.Errorf("published lines = %v, want the three events tailed", published)
	}
}

func TestWriteLinesPropagatesRecvError(t *testing.T) {
	recv := func() (*agentv1.LogLine, error) { return nil, errors.New("stream broke") }
	if err := writeLines(nil, &fakeLogWriter{}, recv, func(string) {}, nil); err == nil {
		t.Error("a non-EOF receive error should propagate")
	}
}

// fakeStreamLogsServer is a minimal bidi server stream for StreamLogs tests.
type fakeStreamLogsServer struct {
	grpc.ServerStream
	ctx  context.Context
	msgs []*agentv1.LogLine
	i    int
}

func (s *fakeStreamLogsServer) Context() context.Context   { return s.ctx }
func (s *fakeStreamLogsServer) Send(*agentv1.LogAck) error { return nil }
func (s *fakeStreamLogsServer) Recv() (*agentv1.LogLine, error) {
	if s.i >= len(s.msgs) {
		return nil, io.EOF
	}
	m := s.msgs[s.i]
	s.i++
	return m, nil
}

func TestStreamLogsWritesToSink(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	sink := &fakeLogSink{}
	srv.SetLogSink(sink)

	stream := &fakeStreamLogsServer{
		ctx:  ctxWithToken(t, a),
		msgs: []*agentv1.LogLine{{Message: "hello"}, {Message: "world"}},
	}
	if err := srv.StreamLogs(stream); err != nil {
		t.Fatalf("StreamLogs: %v", err)
	}
	if len(sink.lines) != 2 || sink.lines[0] != "hello" {
		t.Errorf("sink lines = %v, want the two messages", sink.lines)
	}
}

func TestStreamLogsUnimplementedWithoutSink(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	stream := &fakeStreamLogsServer{ctx: ctxWithToken(t, a)}
	if err := srv.StreamLogs(stream); status.Code(err) != codes.Unimplemented {
		t.Errorf("StreamLogs without a sink: code = %v, want Unimplemented", status.Code(err))
	}
}

func TestRPCsReportStoreErrors(t *testing.T) {
	errStore := &fakeStore{specErr: errors.New("db down")}
	srv, a := newServer(errStore)
	ctx := ctxWithToken(t, a)
	if _, err := srv.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{}); status.Code(err) != codes.Internal {
		t.Errorf("GetTaskSpec store error: code = %v, want Internal", status.Code(err))
	}
	if _, err := srv.FetchXCom(ctx, &agentv1.FetchXComRequest{UpstreamTaskId: "x"}); status.Code(err) != codes.Internal {
		t.Errorf("FetchXCom store error: code = %v, want Internal", status.Code(err))
	}

	repErr := &fakeStore{reportErr: errors.New("write failed")}
	srv2, a2 := newServer(repErr)
	if _, err := srv2.ReportState(ctxWithToken(t, a2), &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_SUCCESS}); status.Code(err) != codes.Internal {
		t.Errorf("ReportState store error: code = %v, want Internal", status.Code(err))
	}
}

type fakeLogSink struct{ lines []string }

func (s *fakeLogSink) Open(logs.Ref) (logs.LogWriter, error) { return &fakeSinkWriter{s: s}, nil }

type fakeSinkWriter struct{ s *fakeLogSink }

func (w *fakeSinkWriter) WriteEvent(ev logs.Event) error {
	w.s.lines = append(w.s.lines, ev.Message)
	return nil
}
func (w *fakeSinkWriter) Close() error { return nil }

// TestHeartbeatRecordsLivenessOnStore: the Heartbeat RPC stamps
// last_heartbeat_at via store.RecordHeartbeat so the scheduler's heartbeat
// reaper (#128) can distinguish live tasks from agent-lost ones. The RPC
// must still succeed when the store stamp fails (transient DB blip) — see
// the comment on Server.Heartbeat: failing the call would risk the agent
// terminating itself unnecessarily.
func TestHeartbeatRecordsLivenessOnStore(t *testing.T) {
	store := &fakeStore{}
	srv, authn := newServer(store)
	if _, err := srv.Heartbeat(ctxWithToken(t, authn), &agentv1.HeartbeatRequest{}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if len(store.heartbeats) != 1 {
		t.Fatalf("heartbeats recorded = %d, want 1", len(store.heartbeats))
	}
	got := store.heartbeats[0]
	if got.TaskInstanceID != "ti-1" || got.RunID != "run-1" || got.TaskID != "extract" {
		t.Errorf("heartbeat identity = %+v, want the testIdentity", got)
	}
}

// TestHeartbeatSucceedsWhenStoreFails pins the resilience contract: a
// transient DB error inside RecordHeartbeat must not surface as an RPC error
// to the agent. Otherwise the agent could terminate itself on a DB blip.
// The scheduler reaper would in the worst case fail the TI as agent_lost on
// the next tick — correct under "do no harm" (ADR 0031).
func TestHeartbeatSucceedsWhenStoreFails(t *testing.T) {
	store := &fakeStore{heartbeatErr: errors.New("db transient blip")}
	srv, authn := newServer(store)
	if _, err := srv.Heartbeat(ctxWithToken(t, authn), &agentv1.HeartbeatRequest{}); err != nil {
		t.Errorf("Heartbeat must not surface a transient store error to the agent; got %v", err)
	}
}

// TestHeartbeatSignalsTerminateOnStaleAttempt is the #474 kill switch: when the
// heartbeating agent's attempt no longer matches the live row (a reaper settled
// it, or try_number moved on), RecordHeartbeat reports ErrStaleReport and the
// handler answers should_terminate=true so the reaped-but-alive pod stops
// itself. The predicate is exactly #467's "moved on" guard — no second notion
// of stale. A stale heartbeat must NEVER be a hard RPC error (the agent would
// then keep running), only a terminate signal.
func TestHeartbeatSignalsTerminateOnStaleAttempt(t *testing.T) {
	store := &fakeStore{heartbeatErr: ErrStaleReport}
	srv, authn := newServer(store)
	resp, err := srv.Heartbeat(ctxWithToken(t, authn), &agentv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("stale heartbeat must not be an RPC error: %v", err)
	}
	if !resp.GetShouldTerminate() {
		t.Errorf("stale heartbeat: should_terminate = false, want true")
	}
}

// TestHeartbeatDoesNotTerminateLiveAttempt: the live, matching attempt (the
// happy path, nil store error) must get should_terminate=false — the invariant
// that a live execution is never told to stop.
func TestHeartbeatDoesNotTerminateLiveAttempt(t *testing.T) {
	srv, authn := newServer(&fakeStore{})
	resp, err := srv.Heartbeat(ctxWithToken(t, authn), &agentv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if resp.GetShouldTerminate() {
		t.Errorf("live heartbeat: should_terminate = true, want false")
	}
}

// TestHeartbeatTransientErrorDoesNotTerminate: a genuine (non-stale) DB error is
// logged and swallowed, and must NOT signal terminate — otherwise a DB blip
// would kill a live task. Distinct from the stale case above.
func TestHeartbeatTransientErrorDoesNotTerminate(t *testing.T) {
	srv, authn := newServer(&fakeStore{heartbeatErr: errors.New("db transient blip")})
	resp, err := srv.Heartbeat(ctxWithToken(t, authn), &agentv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("transient heartbeat error must not surface as RPC error: %v", err)
	}
	if resp.GetShouldTerminate() {
		t.Errorf("transient DB error: should_terminate = true, want false")
	}
}

// TestReportStateSignalsTerminateOnStaleReport: a state report from an attempt
// that has moved on (ErrStaleReport from the #467 guard) is acknowledged AND
// carries should_terminate=true, so a reaped pod stops (#474).
func TestReportStateSignalsTerminateOnStaleReport(t *testing.T) {
	srv, authn := newServer(&fakeStore{reportErr: ErrStaleReport})
	resp, err := srv.ReportState(ctxWithToken(t, authn), &agentv1.ReportStateRequest{
		State: agentv1.TaskState_TASK_STATE_RUNNING,
	})
	if err != nil {
		t.Fatalf("stale report must be acknowledged, not errored: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Errorf("stale report: acknowledged = false, want true")
	}
	if !resp.GetShouldTerminate() {
		t.Errorf("stale report: should_terminate = false, want true")
	}
}

// TestReportStateDoesNotTerminateLiveAttempt: a report that applies (live
// attempt) must not carry should_terminate — the live-execution invariant.
func TestReportStateDoesNotTerminateLiveAttempt(t *testing.T) {
	srv, authn := newServer(&fakeStore{})
	resp, err := srv.ReportState(ctxWithToken(t, authn), &agentv1.ReportStateRequest{
		State: agentv1.TaskState_TASK_STATE_RUNNING,
	})
	if err != nil {
		t.Fatalf("ReportState: %v", err)
	}
	if resp.GetShouldTerminate() {
		t.Errorf("live report: should_terminate = true, want false")
	}
}

func TestRegisterAndHeartbeatReturnServerTime(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	ctx := ctxWithToken(t, a)

	reg, err := srv.Register(ctx, &agentv1.RegisterRequest{AgentVersion: "test"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if reg.GetSessionId() == "" || reg.GetServerTime() == nil {
		t.Errorf("register response incomplete: %+v", reg)
	}
	hb, err := srv.Heartbeat(ctx, &agentv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if hb.GetServerTime() == nil {
		t.Error("heartbeat should return server time")
	}
}

// TestGetTaskSpecMapsOperatorFields covers the airflow_operator wiring (ADR 0040
// A4): the store's OperatorClass + OperatorArgsJSON must reach the proto so the
// agent can dispatch the runtime's --operator mode (review polish #6).
func TestGetTaskSpecMapsOperatorFields(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{
		Operator:         "airflow_operator",
		OperatorClass:    "airflow.providers.snowflake.operators.snowflake.SQLExecuteQueryOperator",
		OperatorArgsJSON: `{"sql":"SELECT 1","conn_id":"sf"}`,
	}}
	srv, a := newServer(store)
	spec, err := srv.GetTaskSpec(ctxWithToken(t, a), &agentv1.GetTaskSpecRequest{})
	if err != nil {
		t.Fatalf("GetTaskSpec: %v", err)
	}
	if spec.GetOperatorClass() != store.spec.OperatorClass {
		t.Errorf("operator_class = %q, want %q", spec.GetOperatorClass(), store.spec.OperatorClass)
	}
	if spec.GetOperatorArgsJson() != store.spec.OperatorArgsJSON {
		t.Errorf("operator_args_json = %q, want %q", spec.GetOperatorArgsJson(), store.spec.OperatorArgsJSON)
	}
}
