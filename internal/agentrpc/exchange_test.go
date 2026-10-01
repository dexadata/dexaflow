package agentrpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// fakeReviewer is a mockable stand-in for the Kubernetes TokenReview client, so
// the exchange handler is unit-tested without a real apiserver.
type fakeReviewer struct {
	pod   ReviewedPod
	err   error
	calls int
	seen  string // the token last presented for review
}

func (r *fakeReviewer) ReviewProjectedToken(_ context.Context, token string) (ReviewedPod, error) {
	r.calls++
	r.seen = token
	if r.err != nil {
		return ReviewedPod{}, r.err
	}
	return r.pod, nil
}

// fakeResolver maps a reviewed pod to an agent identity. warmID (when the pod
// carries the warm-worker label) lets a test drive the warm branch of ResolveAgent
// distinctly from the task branch (id).
type fakeResolver struct {
	id     auth.AgentIdentity
	warmID auth.AgentIdentity
	err    error
	seen   ReviewedPod
}

func (r *fakeResolver) ResolveTaskInstance(_ context.Context, pod ReviewedPod) (auth.AgentIdentity, error) {
	r.seen = pod
	if r.err != nil {
		return auth.AgentIdentity{}, r.err
	}
	return r.id, nil
}

// ResolveAgent mirrors the concrete resolver's DP1 branch: a pod flagged warm (its
// ServiceAccount carries the "warm" marker in this fake) resolves to the
// warm-worker identity; anything else resolves to the task identity.
func (r *fakeResolver) ResolveAgent(_ context.Context, pod ReviewedPod) (auth.AgentIdentity, error) {
	r.seen = pod
	if r.err != nil {
		return auth.AgentIdentity{}, r.err
	}
	if pod.ServiceAccount == "warm" {
		return r.warmID, nil
	}
	return r.id, nil
}

// ctxWithBootstrap carries an opaque projected SA token as the bearer (it is NOT
// a leoflow JWT — the exchange validates it via the reviewer, not the signer).
func ctxWithBootstrap(token string) context.Context {
	md := metadata.Pairs("authorization", "Bearer "+token)
	return metadata.NewIncomingContext(context.Background(), md)
}

func reviewedPod() ReviewedPod {
	return ReviewedPod{Namespace: "leoflow", PodName: "leoflow-etl-extract-1-abcd", PodUID: "uid-1", ServiceAccount: "system:serviceaccount:leoflow:task"}
}

// TestExchangeTokenMintsScopedJWT: a valid projected token is reviewed, resolved
// to its task instance, and exchanged for a task-scoped agent JWT that
// AuthenticateAgent verifies back to the SAME identity.
func TestExchangeTokenMintsScopedJWT(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	rev := &fakeReviewer{pod: reviewedPod()}
	res := &fakeResolver{id: testIdentity()}
	srv.SetTokenExchange(rev, res, a, time.Hour, true)

	resp, err := srv.ExchangeToken(ctxWithBootstrap("projected-sa-token"), &agentv1.ExchangeTokenRequest{})
	if err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	if resp.GetAgentToken() == "" {
		t.Fatal("ExchangeToken returned an empty agent token")
	}
	if rev.seen != "projected-sa-token" {
		t.Errorf("reviewer saw token %q, want the presented projected token", rev.seen)
	}
	if res.seen != rev.pod {
		t.Errorf("resolver saw pod %+v, want the reviewed pod %+v", res.seen, rev.pod)
	}
	// The minted JWT must verify back to the resolved task instance.
	id, verr := a.AuthenticateAgent(resp.GetAgentToken())
	if verr != nil {
		t.Fatalf("minted token does not verify: %v", verr)
	}
	if *id != testIdentity() {
		t.Errorf("minted identity = %+v, want %+v", *id, testIdentity())
	}
}

// TestExchangeTokenRejectsInvalidProjectedToken: the reviewer rejecting the token
// (bad signature, expired, or WRONG AUDIENCE — all surfaced by the concrete
// TokenReview client) fails the exchange Unauthenticated. No JWT is minted.
func TestExchangeTokenRejectsInvalidProjectedToken(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	rev := &fakeReviewer{err: errors.New("token review: not authenticated (wrong audience or expired)")}
	res := &fakeResolver{id: testIdentity()}
	srv.SetTokenExchange(rev, res, a, time.Hour, true)

	if _, err := srv.ExchangeToken(ctxWithBootstrap("bad-token"), &agentv1.ExchangeTokenRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("invalid projected token: code = %v, want Unauthenticated", status.Code(err))
	}
	if res.seen != (ReviewedPod{}) {
		t.Error("resolver must not be consulted when review fails")
	}
}

// TestExchangeTokenResolverFailureFailsClosed: a pod that reviews but cannot be
// resolved to a task instance fails the exchange (Internal) — it must never mint
// an unscoped or misattributed token.
func TestExchangeTokenResolverFailureFailsClosed(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	rev := &fakeReviewer{pod: reviewedPod()}
	res := &fakeResolver{err: errors.New("no task instance for pod")}
	srv.SetTokenExchange(rev, res, a, time.Hour, true)

	if _, err := srv.ExchangeToken(ctxWithBootstrap("projected-sa-token"), &agentv1.ExchangeTokenRequest{}); status.Code(err) != codes.Internal {
		t.Errorf("resolver failure: code = %v, want Internal", status.Code(err))
	}
}

// TestExchangeTokenUnconfiguredIsUnimplemented: the default (env-var) transport
// never wires the exchanger, so a stray ExchangeToken call is Unimplemented — no
// panic, no TokenReview.
func TestExchangeTokenUnconfiguredIsUnimplemented(t *testing.T) {
	srv, _ := newServer(&fakeStore{})
	if _, err := srv.ExchangeToken(ctxWithBootstrap("whatever"), &agentv1.ExchangeTokenRequest{}); status.Code(err) != codes.Unimplemented {
		t.Errorf("unconfigured exchange: code = %v, want Unimplemented", status.Code(err))
	}
}

// TestExchangeTokenRejectsMissingBearer: no authorization metadata → Unauthenticated.
func TestExchangeTokenRejectsMissingBearer(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	srv.SetTokenExchange(&fakeReviewer{pod: reviewedPod()}, &fakeResolver{id: testIdentity()}, a, time.Hour, true)
	if _, err := srv.ExchangeToken(context.Background(), &agentv1.ExchangeTokenRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("missing bearer: code = %v, want Unauthenticated", status.Code(err))
	}
}

// TestNormalRPCsNeverInvokeTokenReview: even with the exchanger configured, the
// steady-state path (identify() on a real agent JWT) authenticates by signature
// and NEVER calls the apiserver TokenReview — the exchange is a one-time
// bootstrap, not a per-RPC capability (ADR 0055: the secret hot path is
// apiserver-free).
func TestNormalRPCsNeverInvokeTokenReview(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{Operator: "python", Entrypoint: "dag:x"}}
	srv, a := newServer(store)
	rev := &fakeReviewer{pod: reviewedPod()}
	srv.SetTokenExchange(rev, &fakeResolver{id: testIdentity()}, a, time.Hour, true)

	if _, err := srv.GetTaskSpec(ctxWithToken(t, a), &agentv1.GetTaskSpecRequest{}); err != nil {
		t.Fatalf("GetTaskSpec: %v", err)
	}
	if rev.calls != 0 {
		t.Errorf("TokenReview was called %d times on the steady-state path, want 0", rev.calls)
	}
}

// warmReviewedPod is a reviewed pod the fake resolver treats as a warm worker
// (ServiceAccount == "warm"), so ExchangeToken takes the DP1 warm branch.
func warmReviewedPod() ReviewedPod {
	return ReviewedPod{Namespace: "leoflow", PodName: "leoflow-warm-dagver-9-abcd", PodUID: "uid-w", ServiceAccount: "warm"}
}

func warmWorkerID() auth.AgentIdentity {
	return auth.AgentIdentity{Scope: auth.ScopeWarmWorker, DagVersionID: "dagver-9", TenantID: "acme", WorkerID: "leoflow-warm-dagver-9-abcd"}
}

// TestExchangeTokenMintsWorkerScopedJWT: a reviewed pod WITH the warm-worker label
// is exchanged for a WORKER-scoped agent JWT (scope=warm-worker, dag_version set,
// NO task claims), verifiable back to the same worker identity.
func TestExchangeTokenMintsWorkerScopedJWT(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	rev := &fakeReviewer{pod: warmReviewedPod()}
	res := &fakeResolver{id: testIdentity(), warmID: warmWorkerID()}
	srv.SetTokenExchange(rev, res, a, time.Hour, true)

	resp, err := srv.ExchangeToken(ctxWithBootstrap("projected-sa-token"), &agentv1.ExchangeTokenRequest{})
	if err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	id, verr := a.AuthenticateAgent(resp.GetAgentToken())
	if verr != nil {
		t.Fatalf("minted worker token does not verify: %v", verr)
	}
	if id.Scope != auth.ScopeWarmWorker {
		t.Errorf("scope = %q, want %q", id.Scope, auth.ScopeWarmWorker)
	}
	if id.DagVersionID != "dagver-9" {
		t.Errorf("dag_version_id = %q, want dagver-9", id.DagVersionID)
	}
	if id.TaskInstanceID != "" || id.RunID != "" || id.TaskID != "" || id.TryNumber != 0 {
		t.Errorf("worker token must carry no task claims, got %+v", *id)
	}
}

// TestExchangeTokenWithoutWarmLabelMintsTaskToken: a pod WITHOUT the warm-worker
// label is exchanged for the task-scoped token exactly as today (Scope empty).
func TestExchangeTokenWithoutWarmLabelMintsTaskToken(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	rev := &fakeReviewer{pod: reviewedPod()} // ServiceAccount != "warm"
	res := &fakeResolver{id: testIdentity(), warmID: warmWorkerID()}
	srv.SetTokenExchange(rev, res, a, time.Hour, true)

	resp, err := srv.ExchangeToken(ctxWithBootstrap("projected-sa-token"), &agentv1.ExchangeTokenRequest{})
	if err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	id, verr := a.AuthenticateAgent(resp.GetAgentToken())
	if verr != nil {
		t.Fatalf("minted token does not verify: %v", verr)
	}
	if id.Scope != "" {
		t.Errorf("task path scope = %q, want empty", id.Scope)
	}
	if *id != testIdentity() {
		t.Errorf("minted identity = %+v, want %+v", *id, testIdentity())
	}
}
