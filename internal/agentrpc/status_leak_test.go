package agentrpc_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/xcom"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// The task pod is a trust boundary: it runs the tenant's own image and
// entrypoint, so every gRPC status the agent RPCs return is text handed to
// authenticated, tenant-controlled code that can call TaskSpec / GetVariables /
// GetXCom in a loop. recovery.go already collapses a panic to a constant for
// exactly this audience; these tests extend the same rule to every storage,
// cache, object-store and signing failure behind the handlers (#1068, the
// agentrpc half of #961).
//
// The assertions are made on the status a REAL gRPC CLIENT receives, not on the
// value a handler returns, so they hold whichever way the text would have got
// out: status.Errorf with %v, a bare wrapped error rendered by grpc-go as
// Unknown, or a future third mechanism.

// pgFixture is a realistic driver error: a foreign-key violation as pgconn
// renders it, carrying the SQLSTATE, the constraint, the table and the column.
// Tests drive every seam with THIS value rather than a hand-made clean string,
// because a clean string cannot fail an assertion that a real one would.
func pgFixture() *pgconn.PgError {
	return &pgconn.PgError{
		Severity:       "ERROR",
		Code:           "23503",
		Message:        `insert or update on table "task_instances" violates foreign key constraint "task_instances_dag_run_id_fkey"`,
		Detail:         `Key (dag_run_id)=(0f8f1a1e-0000-0000-0000-000000000000) is not present in table "dag_runs".`,
		SchemaName:     "public",
		TableName:      "task_instances",
		ColumnName:     "dag_run_id",
		ConstraintName: "task_instances_dag_run_id_fkey",
		Routine:        "ri_ReportViolation",
		File:           "ri_triggers.c",
	}
}

// poison is the error the fakes return: the driver error wrapped exactly the
// way the storage layer wraps it, so the test exercises the real shape
// (fmt.Errorf("...: %w", pgErr)) and not a bare *pgconn.PgError.
func poison() error { return fmt.Errorf("loading run: %w", pgFixture()) }

// leakTokens are derived FROM the fixture rather than hand-listed, so widening
// the fixture automatically widens the scan instead of silently leaving the new
// field uncovered.
func leakTokens(pe *pgconn.PgError) []string {
	return []string{
		pe.Code, pe.Message, pe.Error(), pe.Severity + ":",
		pe.ConstraintName, pe.TableName, pe.ColumnName, pe.SchemaName,
		pe.Routine, pe.File, "SQLSTATE",
	}
}

// syncBuffer is an io.Writer safe to write from the server goroutine and read
// from the test goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ---------------------------------------------------------------------------
// Fakes: one per collaborator seam behind the agent RPCs. Each can be poisoned
// independently so a case names exactly the seam it drives.
// ---------------------------------------------------------------------------

type poisonStore struct {
	spec          agentrpc.TaskSpec
	specErr       error
	reportErr     error
	rescheduleErr error
	heartbeatErr  error
	bindErr       error
}

func (s *poisonStore) TaskSpec(context.Context, auth.AgentIdentity) (agentrpc.TaskSpec, error) {
	return s.spec, s.specErr
}

func (s *poisonStore) ReportState(context.Context, auth.AgentIdentity, domain.TaskState, int, string) error {
	return s.reportErr
}

func (s *poisonStore) Reschedule(context.Context, auth.AgentIdentity, time.Time) error {
	return s.rescheduleErr
}

func (s *poisonStore) RecordHeartbeat(context.Context, auth.AgentIdentity) error {
	return s.heartbeatErr
}

func (s *poisonStore) BindWarmAttempt(context.Context, string, string, int, string) error {
	return s.bindErr
}

type poisonXCom struct {
	pushErr  error
	fetchErr error
}

func (x *poisonXCom) Push(context.Context, xcom.Key, []byte, string, map[string]any) error {
	return x.pushErr
}

func (x *poisonXCom) Fetch(context.Context, xcom.Key) (xcom.Entry, error) {
	if x.fetchErr != nil {
		return xcom.Entry{}, x.fetchErr
	}
	return xcom.Entry{Value: []byte(`"v"`), ContentType: "application/json"}, nil
}

type poisonSecrets struct{ err error }

func (s *poisonSecrets) SecretVariables(context.Context, string) (map[string]string, error) {
	return nil, s.err
}

func (s *poisonSecrets) SecretConnectionURIs(context.Context, string) (map[string]string, error) {
	return nil, s.err
}

func (s *poisonSecrets) SecretVariablesScoped(context.Context, string, []string) (map[string]string, error) {
	return nil, s.err
}

func (s *poisonSecrets) SecretConnectionURIsScoped(context.Context, string, []string) (map[string]string, error) {
	return nil, s.err
}

type poisonSink struct {
	openErr  error
	writeErr error
	closeErr error
}

func (s *poisonSink) Open(logs.Ref) (logs.LogWriter, error) {
	if s.openErr != nil {
		return nil, s.openErr
	}
	return &poisonWriter{writeErr: s.writeErr, closeErr: s.closeErr}, nil
}

type poisonWriter struct {
	writeErr error
	closeErr error
}

func (w *poisonWriter) WriteEvent(logs.Event) error { return w.writeErr }
func (w *poisonWriter) Close() error                { return w.closeErr }

type stubReviewer struct{}

func (stubReviewer) ReviewProjectedToken(context.Context, string) (agentrpc.ReviewedPod, error) {
	return agentrpc.ReviewedPod{Namespace: "tasks", PodName: "etl-extract-1", PodUID: "uid-1"}, nil
}

type poisonResolver struct{ err error }

func (r *poisonResolver) ResolveTaskInstance(context.Context, agentrpc.ReviewedPod) (auth.AgentIdentity, error) {
	return auth.AgentIdentity{}, r.err
}

func (r *poisonResolver) ResolveAgent(context.Context, agentrpc.ReviewedPod) (auth.AgentIdentity, error) {
	if r.err != nil {
		return auth.AgentIdentity{}, r.err
	}
	return leakIdentity(), nil
}

type poisonMinter struct{ err error }

func (m *poisonMinter) IssueAgentToken(auth.AgentIdentity, time.Duration) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return "minted", nil
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

func leakIdentity() auth.AgentIdentity {
	return auth.AgentIdentity{
		TaskInstanceID: "ti-1", TenantID: "acme", DagID: "etl",
		RunID: "run-1", TaskID: "extract", TryNumber: 1,
	}
}

// leakFakes holds the poisonable collaborators so a case can reach into the one
// seam it drives.
type leakFakes struct {
	store    *poisonStore
	xcom     *poisonXCom
	secrets  *poisonSecrets
	sink     *poisonSink
	resolver *poisonResolver
	minter   *poisonMinter
	scoping  string
}

func newLeakFakes() *leakFakes {
	return &leakFakes{
		store: &poisonStore{spec: agentrpc.TaskSpec{
			Operator: "python", Entrypoint: "dag:main", DagVersion: "v1",
			XComInputMapping:  map[string][]string{"v": {"upstream"}},
			DeclaredVariables: []string{"region"}, DeclaredConnections: []string{"warehouse"},
		}},
		xcom:     &poisonXCom{},
		secrets:  &poisonSecrets{},
		sink:     &poisonSink{},
		resolver: &poisonResolver{},
		minter:   &poisonMinter{},
		scoping:  agentrpc.ScopingPermissive,
	}
}

// startLeakServer wires a real *agentrpc.Server with the given fakes behind a
// real gRPC server on a bufconn, exactly as main.go wires it (recovery
// outermost), and returns a real client plus the captured control-plane log.
func startLeakServer(t *testing.T, f *leakFakes) (agentv1.AgentServiceClient, context.Context, *syncBuffer) {
	t.Helper()
	captured := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(captured, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	authn := auth.NewJWTAuthenticator(nil, "leak-test-secret", time.Hour)
	srv := agentrpc.NewServer(authn, f.store, f.xcom)
	srv.SetSecrets(f.secrets, true)
	srv.SetSecretScoping(f.scoping)
	srv.SetLogSink(f.sink)
	srv.SetTokenExchange(stubReviewer{}, f.resolver, f.minter, time.Hour, true)

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(agentrpc.RecoveryUnaryInterceptor(slog.Default())),
		grpc.ChainStreamInterceptor(agentrpc.RecoveryStreamInterceptor(slog.Default())),
	)
	agentv1.RegisterAgentServiceServer(grpcSrv, srv)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	token, err := authn.IssueAgentToken(leakIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	return agentv1.NewAgentServiceClient(conn), ctx, captured
}

// streamLogsErr drives StreamLogs to completion and returns the terminating
// status the agent sees. It sends one line, then half-closes, so both the
// per-line write path and the end-of-stream flush path are reachable.
func streamLogsErr(ctx context.Context, c agentv1.AgentServiceClient) error {
	stream, err := c.StreamLogs(ctx)
	if err != nil {
		return err
	}
	if serr := stream.Send(&agentv1.LogLine{Message: "hello", Stream: "stdout"}); serr != nil {
		// A Send race with a handler that already returned surfaces as EOF; the
		// real status comes from Recv below.
		if !errors.Is(serr, io.EOF) {
			return serr
		}
	}
	if serr := stream.CloseSend(); serr != nil {
		return serr
	}
	for {
		if _, rerr := stream.Recv(); rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return rerr
		}
	}
}

// ---------------------------------------------------------------------------
// The sweep
// ---------------------------------------------------------------------------

// TestAgentRPCStatusesCarryNoStorageErrorText drives every agent RPC with a
// realistic driver error on every collaborator seam behind it and asserts the
// status a real client receives carries none of that error's text — while the
// control-plane log still carries all of it under `cause`, and the message
// still names the operation that failed so the task's own log stays usable.
func TestAgentRPCStatusesCarryNoStorageErrorText(t *testing.T) {
	cases := []struct {
		name     string
		poison   func(*leakFakes)
		call     func(context.Context, agentv1.AgentServiceClient) error
		wantCode codes.Code
		wantOp   string
	}{
		{
			name:   "GetTaskSpec: the task spec cannot be loaded",
			poison: func(f *leakFakes) { f.store.specErr = poison() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{})
				return err
			},
			wantCode: codes.Internal, wantOp: "loading task spec",
		},
		{
			name:   "ReportState: the state write fails",
			poison: func(f *leakFakes) { f.store.reportErr = poison() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.ReportState(ctx, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_SUCCESS})
				return err
			},
			wantCode: codes.Internal, wantOp: "recording state",
		},
		{
			name:   "ReportState: the reschedule write fails",
			poison: func(f *leakFakes) { f.store.rescheduleErr = poison() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.ReportState(ctx, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_UP_FOR_RESCHEDULE})
				return err
			},
			wantCode: codes.Internal, wantOp: "recording reschedule",
		},
		{
			name:   "PushXCom: the task spec cannot be loaded",
			poison: func(f *leakFakes) { f.store.specErr = poison() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.PushXCom(ctx, &agentv1.PushXComRequest{Key: "return_value", Value: []byte(`"v"`)})
				return err
			},
			wantCode: codes.Internal, wantOp: "loading task spec",
		},
		{
			name:   "PushXCom: the xcom backend rejects the write",
			poison: func(f *leakFakes) { f.xcom.pushErr = poison() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.PushXCom(ctx, &agentv1.PushXComRequest{Key: "return_value", Value: []byte(`"v"`)})
				return err
			},
			wantCode: codes.Internal, wantOp: "storing xcom",
		},
		{
			name:   "FetchXCom: the task spec cannot be loaded",
			poison: func(f *leakFakes) { f.store.specErr = poison() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.FetchXCom(ctx, &agentv1.FetchXComRequest{UpstreamTaskId: "upstream", Key: "return_value"})
				return err
			},
			wantCode: codes.Internal, wantOp: "loading task spec",
		},
		{
			name:   "FetchXCom: the xcom backend fails the read",
			poison: func(f *leakFakes) { f.xcom.fetchErr = poison() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.FetchXCom(ctx, &agentv1.FetchXComRequest{UpstreamTaskId: "upstream", Key: "return_value"})
				return err
			},
			wantCode: codes.Internal, wantOp: "reading xcom",
		},
		{
			name:     "StreamLogs: the sink cannot be opened",
			poison:   func(f *leakFakes) { f.sink.openErr = poison() },
			call:     streamLogsErr,
			wantCode: codes.Internal, wantOp: "opening log sink",
		},
		{
			name:     "StreamLogs: a line cannot be written",
			poison:   func(f *leakFakes) { f.sink.writeErr = poison() },
			call:     streamLogsErr,
			wantCode: codes.Internal, wantOp: "writing log line",
		},
		{
			name:     "StreamLogs: the final flush fails",
			poison:   func(f *leakFakes) { f.sink.closeErr = poison() },
			call:     streamLogsErr,
			wantCode: codes.Internal, wantOp: "flushing logs",
		},
		{
			name:   "GetVariables: the vault read fails (permissive)",
			poison: func(f *leakFakes) { f.secrets.err = poison() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.GetVariables(ctx, &agentv1.GetVariablesRequest{})
				return err
			},
			wantCode: codes.Internal, wantOp: "fetching variables",
		},
		{
			name: "GetVariables: the scoped vault read fails (enforce)",
			poison: func(f *leakFakes) {
				f.scoping = agentrpc.ScopingEnforce
				f.secrets.err = poison()
			},
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.GetVariables(ctx, &agentv1.GetVariablesRequest{})
				return err
			},
			wantCode: codes.Internal, wantOp: "fetching variables",
		},
		{
			name: "GetVariables: the declared-set lookup fails (enforce)",
			poison: func(f *leakFakes) {
				f.scoping = agentrpc.ScopingEnforce
				f.store.specErr = poison()
			},
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.GetVariables(ctx, &agentv1.GetVariablesRequest{})
				return err
			},
			wantCode: codes.Internal, wantOp: "loading task spec for scope enforcement",
		},
		{
			name:   "GetConnections: the vault read fails (permissive)",
			poison: func(f *leakFakes) { f.secrets.err = poison() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
				return err
			},
			wantCode: codes.Internal, wantOp: "fetching connections",
		},
		{
			name: "GetConnections: the scoped vault read fails (enforce)",
			poison: func(f *leakFakes) {
				f.scoping = agentrpc.ScopingEnforce
				f.secrets.err = poison()
			},
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
				return err
			},
			wantCode: codes.Internal, wantOp: "fetching connections",
		},
		{
			name: "GetConnections: the declared-set lookup fails (enforce)",
			poison: func(f *leakFakes) {
				f.scoping = agentrpc.ScopingEnforce
				f.store.specErr = poison()
			},
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
				return err
			},
			wantCode: codes.Internal, wantOp: "loading task spec for scope enforcement",
		},
		{
			name:   "ExchangeToken: the reviewed pod cannot be resolved",
			poison: func(f *leakFakes) { f.resolver.err = poison() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.ExchangeToken(ctx, &agentv1.ExchangeTokenRequest{})
				return err
			},
			wantCode: codes.Internal, wantOp: "resolving pod to agent identity",
		},
		{
			name:   "ExchangeToken: the token cannot be minted",
			poison: func(f *leakFakes) { f.minter.err = poison() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.ExchangeToken(ctx, &agentv1.ExchangeTokenRequest{})
				return err
			},
			wantCode: codes.Internal, wantOp: "minting agent token",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLeakFakes()
			tc.poison(f)
			client, ctx, captured := startLeakServer(t, f)

			err := tc.call(ctx, client)
			if err == nil {
				t.Fatal("the poisoned seam produced no error; this case no longer drives the path it names")
			}
			if got := status.Code(err); got != tc.wantCode {
				t.Errorf("status code = %v, want %v (%v)", got, tc.wantCode, err)
			}
			msg := status.Convert(err).Message()
			for _, leak := range leakTokens(pgFixture()) {
				if leak == "" {
					continue
				}
				if strings.Contains(msg, leak) {
					t.Errorf("the status the task pod receives leaks %q:\n  %s", leak, msg)
				}
			}
			// Over-redaction is a real regression too: a task that fails with no
			// usable reason in its own log costs the person debugging the DAG.
			if !strings.Contains(msg, tc.wantOp) {
				t.Errorf("the status no longer names the operation %q, so the task log says nothing usable:\n  %s", tc.wantOp, msg)
			}
			// The operator must lose nothing: the real cause belongs on the
			// control-plane log line, correlatable with what the caller got.
			logged := captured.String()
			if !strings.Contains(logged, `"cause"`) {
				t.Errorf("the control-plane log has no cause field; the operator lost the diagnosis:\n%s", logged)
			}
			for _, want := range []string{pgFixture().Code, pgFixture().ConstraintName} {
				if !strings.Contains(logged, want) {
					t.Errorf("the control-plane log lost %q; the operator lost the diagnosis:\n%s", want, logged)
				}
			}
			if !strings.Contains(logged, tc.wantOp) {
				t.Errorf("the control-plane log does not name %q, so it cannot be correlated with what the agent saw:\n%s", tc.wantOp, logged)
			}
		})
	}
}

// TestAgentRPCKeepsTheAttemptIdentityOnTheCauseLine pins the correlation handle.
// The agent has no request id to echo, so the attempt identity IS the join key
// between the phrase the task log shows and the cause on the control-plane log.
func TestAgentRPCKeepsTheAttemptIdentityOnTheCauseLine(t *testing.T) {
	f := newLeakFakes()
	f.store.specErr = poison()
	client, ctx, captured := startLeakServer(t, f)

	if _, err := client.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{}); err == nil {
		t.Fatal("GetTaskSpec should have failed")
	}
	logged := captured.String()
	id := leakIdentity()
	for field, want := range map[string]string{
		"ti": id.TaskInstanceID, "run": id.RunID, "task": id.TaskID,
	} {
		if !strings.Contains(logged, `"`+field+`":"`+want+`"`) {
			t.Errorf("the cause line has no %s=%s, so it cannot be correlated with the failing attempt:\n%s", field, want, logged)
		}
	}
}

// TestAgentRPCPassesThroughDeliberatelyClientFacingMessages is the other side of
// the guard. Blanket redaction would take away the one storage message the agent
// genuinely needs: "task X not found in run Y" tells the operator the pod is
// running a task its dag_version does not declare (a stale image, a mismatched
// version) rather than that the control plane is broken. domain.SafeError is the
// opt-in that lets exactly those phrases through.
//
// This asserts the FORMATTING layer honors the opt-in. That storage actually
// PRODUCES one is pinned separately, at the layer that produces it — see
// internal/storage/agent_safe_error_integration_test.go — because this test is
// satisfied by a fake and would stay green with every Safef reverted.
func TestAgentRPCPassesThroughDeliberatelyClientFacingMessages(t *testing.T) {
	f := newLeakFakes()
	f.store.specErr = domain.Safef(domain.ErrNotFound, "task %q not found in run %q", "extract", "run-1")
	client, ctx, _ := startLeakServer(t, f)

	_, err := client.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{})
	if err == nil {
		t.Fatal("GetTaskSpec should have failed")
	}
	msg := status.Convert(err).Message()
	if !strings.Contains(msg, `task "extract" not found in run "run-1"`) {
		t.Errorf("a deliberately client-facing message was redacted; the task fails with no usable reason:\n  %s", msg)
	}
}
