package agentrpc

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/auth"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// warmServerWithSecrets builds a server with secrets wired over the (dev) insecure
// channel AND a task spec, so a TASK token would succeed on every secret/task RPC.
// That isolates the scope gate: when a warm token is denied, it is denied by the
// scope check, not by the secret-channel or spec-load guards.
func warmServerWithSecrets(t *testing.T) (srv *Server, store *fakeStore, sec *fakeSecrets, warmCtx, taskCtx context.Context) {
	t.Helper()
	store = &fakeStore{spec: TaskSpec{
		Operator: "python", Entrypoint: "dag:x",
		XComInputMapping: map[string][]string{"in": {"up"}},
	}}
	sec = &fakeSecrets{vars: map[string]string{"FOO": "bar"}, conns: map[string]string{"pg": "postgres://u:p@h/db"}}
	var a *auth.JWTAuthenticator
	srv, a = newServerX(store, &fakeXCom{})
	srv.SetSecrets(sec, true) // dev: allow over the insecure test channel
	warmCtx = ctxWithWarmToken(t, a)
	taskCtx = ctxWithToken(t, a)
	return srv, store, sec, warmCtx, taskCtx
}

// TestWarmTokenDeniedOnEverySecretAndTaskRPC is the DP2 security invariant: a
// warm-worker-scoped token — a leaked or forged one included — is PermissionDenied
// on every RPC that reads secrets, the task spec, or XCom, or that reports/
// heartbeats a task. It must never reach the vault. Each RPC is asserted
// individually so a newly-added-but-ungated handler is caught.
func TestWarmTokenDeniedOnEverySecretAndTaskRPC(t *testing.T) {
	srv, _, sec, warmCtx, _ := warmServerWithSecrets(t)

	cases := []struct {
		name string
		call func(context.Context) error
	}{
		{"GetVariables", func(ctx context.Context) error {
			_, err := srv.GetVariables(ctx, &agentv1.GetVariablesRequest{})
			return err
		}},
		{"GetConnections", func(ctx context.Context) error {
			_, err := srv.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
			return err
		}},
		{"GetTaskSpec", func(ctx context.Context) error {
			_, err := srv.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{})
			return err
		}},
		{"ReportState", func(ctx context.Context) error {
			_, err := srv.ReportState(ctx, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_SUCCESS})
			return err
		}},
		{"Heartbeat", func(ctx context.Context) error {
			_, err := srv.Heartbeat(ctx, &agentv1.HeartbeatRequest{})
			return err
		}},
		{"PushXCom", func(ctx context.Context) error {
			_, err := srv.PushXCom(ctx, &agentv1.PushXComRequest{Value: []byte("v")})
			return err
		}},
		{"FetchXCom", func(ctx context.Context) error {
			_, err := srv.FetchXCom(ctx, &agentv1.FetchXComRequest{UpstreamTaskId: "up"})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(warmCtx); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("%s with a warm-worker token = %v, want PermissionDenied", tc.name, err)
			}
		})
	}
	// The vault store must never have been consulted for the warm caller.
	if sec.gotVarTn != "" {
		t.Errorf("secrets store was queried for a warm caller (tenant %q); the scope gate must reject before any vault read", sec.gotVarTn)
	}
}

// TestWarmTokenDeniedOnStreamLogs: StreamLogs writes to a task-instance-keyed log
// ref, so it is a task RPC too — a warm-worker token is denied before the sink opens.
func TestWarmTokenDeniedOnStreamLogs(t *testing.T) {
	srv, _, _, warmCtx, _ := warmServerWithSecrets(t)
	stream := &fakeStreamLogsServer{ctx: warmCtx}
	if err := srv.StreamLogs(stream); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("StreamLogs with a warm-worker token = %v, want PermissionDenied", err)
	}
}

// TestTaskTokenStillWorksOnSecretAndTaskRPCs: the task ("attempt") token — the
// default scope — is UNCHANGED: it still resolves secrets, the task spec, and
// reports state. The scope gate does not touch the attempt path.
func TestTaskTokenStillWorksOnSecretAndTaskRPCs(t *testing.T) {
	srv, _, _, _, taskCtx := warmServerWithSecrets(t)

	if _, err := srv.GetVariables(taskCtx, &agentv1.GetVariablesRequest{}); err != nil {
		t.Errorf("GetVariables with a task token: %v", err)
	}
	if _, err := srv.GetConnections(taskCtx, &agentv1.GetConnectionsRequest{}); err != nil {
		t.Errorf("GetConnections with a task token: %v", err)
	}
	if _, err := srv.GetTaskSpec(taskCtx, &agentv1.GetTaskSpecRequest{}); err != nil {
		t.Errorf("GetTaskSpec with a task token: %v", err)
	}
	if _, err := srv.Heartbeat(taskCtx, &agentv1.HeartbeatRequest{}); err != nil {
		t.Errorf("Heartbeat with a task token: %v", err)
	}
	if _, err := srv.ReportState(taskCtx, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_RUNNING}); err != nil {
		t.Errorf("ReportState with a task token: %v", err)
	}
}

// TestRegisterAcceptsBothScopes: Register is the one control RPC both a warm worker
// and a task pod call, so it accepts either scope.
func TestRegisterAcceptsBothScopes(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	if _, err := srv.Register(ctxWithToken(t, a), &agentv1.RegisterRequest{}); err != nil {
		t.Errorf("Register with a task token: %v", err)
	}
	if _, err := srv.Register(ctxWithWarmToken(t, a), &agentv1.RegisterRequest{}); err != nil {
		t.Errorf("Register with a warm-worker token: %v", err)
	}
}

// TestAwaitAssignmentRejectsTaskToken: the mirror gate — a task ("attempt") token
// may NOT open the warm-worker assignment stream. It is denied before registration.
func TestAwaitAssignmentRejectsTaskToken(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	stream := newFakeAwaitStream(ctxWithToken(t, a)) // a TASK token, wrong scope
	if err := srv.AwaitAssignment(stream); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("AwaitAssignment with a task token = %v, want PermissionDenied", err)
	}
	if reg.registered("ti-1") {
		t.Error("a task token must not land in the warm registry")
	}
}
