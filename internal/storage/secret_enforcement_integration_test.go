//go:build integration

// Package storage_test — the end-to-end liveness + scoping round-trip.
//
// This wires the REAL agent gRPC server (agentrpc.Server) over a REAL gRPC
// channel, backed by the REAL ExecutionStore (task-spec + liveness predicate)
// and the REAL Repository (secret delivery + audit), against a REAL Postgres.
// It exercises the whole GetVariables/GetConnections path — identify →
// liveness gate → scope-by-policy → scoped/whole query — that no fake-store unit
// test can prove, because the scoping filter and the liveness predicate both
// live in SQL.
//
// Every case asserts the two-sided invariant: a LIVE attempt always resolves
// (the availability guard against a pipeline-breaking false-deny) and, where the
// policy denies, a not-live / undeclared request is refused (the security
// guard).
package storage_test

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dexadata/dexaflow/internal/agent"
	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/xcom"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// noXCom is a no-op XComService for the secret round-trip.
type noXCom struct{}

func (noXCom) Push(context.Context, xcom.Key, []byte, string, map[string]any) error { return nil }
func (noXCom) Fetch(context.Context, xcom.Key) (xcom.Entry, error) {
	return xcom.Entry{}, xcom.ErrNotFound
}

// dialSecretServer stands up the real agent gRPC server backed by exec + repo in
// the given scoping + liveness policy, dials it over a plaintext channel, and
// returns a client bound to the identity's agent token. The channel is insecure
// (allowInsecure=true) since this is a loopback test; the token authenticates.
func dialSecretServer(t *testing.T, exec *storage.ExecutionStore, repo *storage.Repository, id auth.AgentIdentity, scoping, livenessMode string) agentv1.AgentServiceClient {
	t.Helper()
	authn := auth.NewJWTAuthenticator(nil, "test-secret", time.Hour)
	srv := agentrpc.NewServer(authn, exec, noXCom{})
	srv.SetSecrets(repo, true) // allow the insecure loopback channel
	srv.SetSecretScoping(scoping)
	srv.SetLivenessGate(exec, livenessMode)
	srv.SetSecretScopeAuditor(repo)
	srv.SetSecretLivenessAuditor(repo)

	gs := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(gs, srv)
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	token, err := authn.IssueAgentToken(id, time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	client, conn, _, err := agent.Dial(lis.Addr().String(), token, true, "")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return client
}

// seedDeclaringRun seeds a tenant with two variables (V_DECLARED, V_OTHER) and
// two connections (c_declared, c_other), then registers a DAG whose "declares"
// task declares only the "_declared" secrets and whose "bare" task declares
// nothing, and brings both task instances to running. It returns the tenant UUID
// and the run UUID. The variables/connections are seeded BEFORE registration
// because E1a rejects a DAG that declares an unknown name.
func seedDeclaringRun(t *testing.T, repo *storage.Repository, sched *storage.SchedulerStore, ctx context.Context, dagID string) (tenantUUID, runUUID string) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i) + 7
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	for _, v := range []domain.Variable{{Key: "V_DECLARED", Value: "d"}, {Key: "V_OTHER", Value: "o"}} {
		if err := repo.SetVariable(ctx, "default", v); err != nil {
			t.Fatalf("SetVariable %s: %v", v.Key, err)
		}
	}
	for _, c := range []domain.Connection{
		{ConnID: "c_declared", ConnType: "http", Host: "declared.example"},
		{ConnID: "c_other", ConnType: "http", Host: "other.example"},
	} {
		if err := repo.SetConnection(ctx, "default", c); err != nil {
			t.Fatalf("SetConnection %s: %v", c.ConnID, err)
		}
	}

	tasks := []domain.TaskSpec{
		{TaskID: "declares", Type: domain.TaskTypePython, Variables: []string{"V_DECLARED"}, Connections: []string{"c_declared"}},
		{TaskID: "bare", Type: domain.TaskTypePython},
	}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID = resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	for _, taskID := range []string{"declares", "bare"} {
		for _, st := range []domain.TaskState{domain.TaskStateQueued, domain.TaskStateRunning} {
			if err := sched.ApplyTransition(ctx, runUUID, taskID, st); err != nil {
				t.Fatalf("ApplyTransition %s->%s: %v", taskID, st, err)
			}
		}
	}
	tenantUUID, err = repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	return tenantUUID, runUUID
}

func identityFor(tenantUUID, dagID, runUUID, taskID string) auth.AgentIdentity {
	return auth.AgentIdentity{
		TaskInstanceID: taskID + "-ti", TenantID: tenantUUID, DagID: dagID,
		RunID: runUUID, TaskID: taskID, TryNumber: 1,
	}
}

// TestSecretRoundTripPermissiveNoDeclarationWholeVault: the SAFE default — a task
// that declares nothing receives the WHOLE tenant vault, exactly as before this
// shipment. This is the unchanged-behaviour guarantee.
func TestSecretRoundTripPermissiveNoDeclarationWholeVault(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("secjrt_perm_bare_%d", time.Now().UnixNano())
	tenantUUID, runUUID := seedDeclaringRun(t, repo, sched, ctx, dagID)

	client := dialSecretServer(t, exec, repo, identityFor(tenantUUID, dagID, runUUID, "bare"), "permissive", "observe")
	vresp, err := client.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	// The shared 'default' tenant may carry variables seeded by sibling tests, so
	// assert the UNDECLARED seeded var is delivered (proving permissive does not
	// subset) rather than an exact vault size.
	if vars := vresp.GetVariables(); vars["V_DECLARED"] != "d" || vars["V_OTHER"] != "o" {
		t.Errorf("permissive + no declaration must deliver both V_DECLARED and V_OTHER (not subset): got %v", vars)
	}
	cresp, err := client.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
	if err != nil {
		t.Fatalf("GetConnections: %v", err)
	}
	if conns := cresp.GetConnectionUris(); conns["c_declared"] == "" || conns["c_other"] == "" {
		t.Errorf("permissive + no declaration must deliver both connections (not subset): got %v", conns)
	}
}

// TestSecretRoundTripEnforceReturnsOnlyDeclared: under enforce the declaring task
// receives ONLY its declared subset, filtered in the query — and the bare task
// receives NOTHING (the load-bearing [] case).
func TestSecretRoundTripEnforceReturnsOnlyDeclared(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("secjrt_enforce_%d", time.Now().UnixNano())
	tenantUUID, runUUID := seedDeclaringRun(t, repo, sched, ctx, dagID)

	// Declaring task: only V_DECLARED / c_declared.
	dc := dialSecretServer(t, exec, repo, identityFor(tenantUUID, dagID, runUUID, "declares"), "enforce", "observe")
	vresp, err := dc.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if len(vresp.GetVariables()) != 1 || vresp.GetVariables()["V_DECLARED"] != "d" {
		t.Errorf("enforce must deliver only {V_DECLARED}: got %v", vresp.GetVariables())
	}
	cresp, err := dc.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
	if err != nil {
		t.Fatalf("GetConnections: %v", err)
	}
	if len(cresp.GetConnectionUris()) != 1 || cresp.GetConnectionUris()["c_declared"] == "" {
		t.Errorf("enforce must deliver only {c_declared}: got %v", cresp.GetConnectionUris())
	}

	// Bare task: nothing.
	bc := dialSecretServer(t, exec, repo, identityFor(tenantUUID, dagID, runUUID, "bare"), "enforce", "observe")
	bvresp, err := bc.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables (bare): %v", err)
	}
	if len(bvresp.GetVariables()) != 0 {
		t.Errorf("enforce + empty declaration must deliver NO variables: got %v", bvresp.GetVariables())
	}
}

// TestSecretRoundTripObserveDoesNotDenyNotLive: the observe-mode rollout proof —
// a NOT-live TI (settled terminal) still receives its secrets under observe. A
// bug that denied here would break every legitimate pipeline, so this is the
// load-bearing availability assertion.
func TestSecretRoundTripObserveDoesNotDenyNotLive(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("secjrt_observe_%d", time.Now().UnixNano())
	tenantUUID, runUUID := seedDeclaringRun(t, repo, sched, ctx, dagID)

	// Settle the declaring task terminal — its token is now NOT live.
	if err := sched.ApplyTransition(ctx, runUUID, "declares", domain.TaskStateSuccess); err != nil {
		t.Fatalf("ApplyTransition to success: %v", err)
	}
	if live, err := exec.IsTaskInstanceLive(ctx, auth.AgentIdentity{RunID: runUUID, TaskID: "declares", TryNumber: 1}); err != nil || live {
		t.Fatalf("precondition: declares must be not-live, got live=%v err=%v", live, err)
	}

	client := dialSecretServer(t, exec, repo, identityFor(tenantUUID, dagID, runUUID, "declares"), "permissive", "observe")
	vresp, err := client.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("observe mode must NOT deny a not-live TI: %v", err)
	}
	if len(vresp.GetVariables()) == 0 {
		t.Errorf("observe mode must still deliver to a not-live TI: got %v", vresp.GetVariables())
	}
}

// TestSecretRoundTripEnforceDeniesNotLive: with the liveness gate in enforce
// mode, a not-live TI is denied with PermissionDenied — the security side.
func TestSecretRoundTripEnforceDeniesNotLive(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("secjrt_liveenf_%d", time.Now().UnixNano())
	tenantUUID, runUUID := seedDeclaringRun(t, repo, sched, ctx, dagID)

	if err := sched.ApplyTransition(ctx, runUUID, "bare", domain.TaskStateFailed); err != nil {
		t.Fatalf("ApplyTransition to failed: %v", err)
	}

	client := dialSecretServer(t, exec, repo, identityFor(tenantUUID, dagID, runUUID, "bare"), "permissive", "enforce")
	if _, err := client.GetVariables(ctx, &agentv1.GetVariablesRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("enforce liveness + not-live TI = %v, want PermissionDenied", err)
	}

	// The still-live "declares" task in the same run is never denied — the
	// availability side, proving enforce does not over-deny a live sibling.
	live := dialSecretServer(t, exec, repo, identityFor(tenantUUID, dagID, runUUID, "declares"), "permissive", "enforce")
	if _, err := live.GetVariables(ctx, &agentv1.GetVariablesRequest{}); err != nil {
		t.Fatalf("enforce liveness must never deny a LIVE TI: %v", err)
	}
}

// TestSecretRoundTripTransientLivenessErrorDoesNotDeny: an inconclusive liveness
// read must not deny, even in enforce mode. A malformed run id makes the store's
// liveness read error (it cannot conclude not-live); the gate must treat that as
// inconclusive and deliver, never break a pipeline on a transient read.
func TestSecretRoundTripTransientLivenessErrorDoesNotDeny(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("secjrt_transient_%d", time.Now().UnixNano())
	tenantUUID, _ := seedDeclaringRun(t, repo, sched, ctx, dagID)

	// A run id that is not a UUID makes IsTaskInstanceLive error rather than
	// return a definitive not-live — the "DB blip" shape.
	id := identityFor(tenantUUID, dagID, "not-a-uuid", "bare")
	client := dialSecretServer(t, exec, repo, id, "off", "enforce")
	vresp, err := client.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("an inconclusive liveness read must NOT deny (enforce): %v", err)
	}
	if len(vresp.GetVariables()) == 0 {
		t.Errorf("an inconclusive liveness read must still deliver: got %v", vresp.GetVariables())
	}
}
