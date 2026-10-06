package agent

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"github.com/golang-jwt/jwt/v5"
	"google.golang.org/grpc"
)

// stubStore is a minimal agentrpc.Store: heartbeats are always live (nil), the
// rest are unused by this heartbeat-only round-trip.
type stubStore struct{}

func (stubStore) TaskSpec(context.Context, auth.AgentIdentity) (agentrpc.TaskSpec, error) {
	return agentrpc.TaskSpec{}, nil
}
func (stubStore) ReportState(context.Context, auth.AgentIdentity, domain.TaskState, int, string) error {
	return nil
}
func (stubStore) Reschedule(context.Context, auth.AgentIdentity, time.Time) error { return nil }
func (stubStore) RecordHeartbeat(context.Context, auth.AgentIdentity) error       { return nil }
func (stubStore) BindWarmAttempt(context.Context, string, string, int, int, string) error {
	return nil
}

// recordingAuth wraps a real authenticator and records every raw bearer the
// server was asked to verify, so a test can prove which token the agent sent on
// each call.
type recordingAuth struct {
	inner *auth.JWTAuthenticator
	mu    sync.Mutex
	seen  []string
}

func (r *recordingAuth) AuthenticateAgent(token string) (*auth.AgentIdentity, error) {
	r.mu.Lock()
	r.seen = append(r.seen, token)
	r.mu.Unlock()
	return r.inner.AuthenticateAgent(token)
}

func (r *recordingAuth) tokens() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func ttlOf(t *testing.T, token string) time.Duration {
	t.Helper()
	var c jwt.MapClaims
	if _, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) {
		return []byte("secret"), nil
	}, jwt.WithValidMethods([]string{"HS256"})); err != nil {
		t.Fatalf("parsing token: %v", err)
	}
	iat, _ := c.GetIssuedAt()
	exp, _ := c.GetExpirationTime()
	if iat == nil || exp == nil {
		t.Fatal("token missing iat/exp")
	}
	return exp.Sub(iat.Time)
}

// TestHeartbeatRenewalRoundTrip drives real gRPC across two heartbeats: the
// control plane renews the bearer on each live beat, the agent swaps it into its
// TokenSource, and the interceptor sends the renewed token on the next call. The
// dispatch token uses a different TTL from the renewal so the token the server
// receives on beat #2 is provably the renewed one, not the original.
func TestHeartbeatRenewalRoundTrip(t *testing.T) {
	authn := auth.NewJWTAuthenticator(nil, "secret", time.Hour)
	rec := &recordingAuth{inner: authn}
	srv := agentrpc.NewServer(rec, stubStore{}, nil)
	const (
		dispatchTTL = 5 * time.Minute
		renewalTTL  = 10 * time.Minute
	)
	srv.SetTokenRenewal(authn, renewalTTL, 24*time.Hour)

	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gsrv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(gsrv, srv)
	go func() { _ = gsrv.Serve(lis) }()
	defer gsrv.Stop()

	id := auth.AgentIdentity{TaskInstanceID: "ti-1", TenantID: "acme", DagID: "etl", RunID: "run-1", TaskID: "extract", TryNumber: 1}
	dispatched, err := authn.IssueAgentToken(id, dispatchTTL)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	client, conn, tokens, err := Dial(lis.Addr().String(), dispatched, true, "")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	r := &Runner{Token: tokens}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Beat 1: carries the dispatch bearer; the reply renews it.
	resp1, err := client.Heartbeat(ctx, &agentv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("Heartbeat #1: %v", err)
	}
	if resp1.GetRenewedToken() == "" {
		t.Fatal("beat #1 must return a renewed token")
	}
	r.applyHeartbeatResponse(resp1)

	// Beat 2: must now carry the renewed bearer.
	resp2, err := client.Heartbeat(ctx, &agentv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("Heartbeat #2: %v", err)
	}
	if resp2.GetRenewedToken() == "" {
		t.Fatal("beat #2 must return a renewed token")
	}
	r.applyHeartbeatResponse(resp2)

	seen := rec.tokens()
	if len(seen) < 2 {
		t.Fatalf("server saw %d tokens, want >= 2", len(seen))
	}
	// Beat #1 carried the dispatch token (5m TTL); beat #2 carried the renewed one
	// (10m TTL) — proof the agent swapped to the new bearer.
	if got := ttlOf(t, seen[0]); got != dispatchTTL {
		t.Errorf("beat #1 bearer TTL = %v, want the dispatch TTL %v", got, dispatchTTL)
	}
	if got := ttlOf(t, seen[1]); got != renewalTTL {
		t.Errorf("beat #2 bearer TTL = %v, want the renewal TTL %v (agent did not swap)", got, renewalTTL)
	}
	// Every token the server accepted still resolves the same identity.
	for i, tok := range seen {
		gotID, aerr := authn.AuthenticateAgent(tok)
		if aerr != nil {
			t.Fatalf("token %d must authenticate: %v", i, aerr)
		}
		if gotID.TaskInstanceID != "ti-1" || gotID.TryNumber != 1 {
			t.Errorf("token %d identity = %+v, want ti-1/try-1", i, *gotID)
		}
	}
}
