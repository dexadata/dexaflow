package agentrpc

import (
	"errors"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"github.com/golang-jwt/jwt/v5"
)

// errTransient is a non-stale store error: the "DB blip" case that must neither
// terminate nor renew.
var errTransient = errors.New("db transient blip")

// ttlOfToken decodes the short-TTL window (exp - iat) of a signed agent token,
// verifying it against the test authenticator's secret. The agentrpc test lives
// outside the auth package, so it reads the registered claims via MapClaims
// rather than the (unexported) agentClaims type.
func ttlOfToken(t *testing.T, token string) time.Duration {
	t.Helper()
	var c jwt.MapClaims
	if _, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) {
		return []byte("secret"), nil
	}, jwt.WithValidMethods([]string{"HS256"})); err != nil {
		t.Fatalf("parsing renewed token: %v", err)
	}
	iat, err := c.GetIssuedAt()
	if err != nil || iat == nil {
		t.Fatalf("renewed token has no iat: %v", err)
	}
	exp, err := c.GetExpirationTime()
	if err != nil || exp == nil {
		t.Fatalf("renewed token has no exp: %v", err)
	}
	return exp.Sub(iat.Time)
}

// TestHeartbeatRenewsTokenOnLiveAttempt is the live side of the two-sided
// renewal contract: when RecordHeartbeat applies (the attempt is live), the
// response carries a freshly minted task-scoped token with exp = now+shortTTL,
// it authenticates to the SAME identity, and it never signals terminate.
func TestHeartbeatRenewsTokenOnLiveAttempt(t *testing.T) {
	store := &fakeStore{}
	srv, authn := newServer(store)
	const shortTTL = 10 * time.Minute
	srv.SetTokenRenewal(authn, shortTTL, 24*time.Hour)

	resp, err := srv.Heartbeat(ctxWithToken(t, authn), &agentv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if resp.GetShouldTerminate() {
		t.Fatal("live heartbeat must not signal terminate")
	}
	renewed := resp.GetRenewedToken()
	if renewed == "" {
		t.Fatal("live heartbeat must carry a renewed token")
	}
	// The renewed bearer verifies unchanged and still identifies the same TI.
	got, err := authn.AuthenticateAgent(renewed)
	if err != nil {
		t.Fatalf("renewed token must authenticate: %v", err)
	}
	if got.TaskInstanceID != "ti-1" || got.RunID != "run-1" || got.TaskID != "extract" || got.TryNumber != 1 {
		t.Errorf("renewed identity = %+v, want the testIdentity", *got)
	}
	// exp = now + shortTTL exactly (never accumulated onto the old exp).
	if ttl := ttlOfToken(t, renewed); ttl != shortTTL {
		t.Errorf("renewed exp-iat = %v, want %v", ttl, shortTTL)
	}
}

// TestHeartbeatSupersededReturnsTerminateNoToken is the dead side: a superseded
// attempt (ErrStaleReport) is told to terminate and is NEVER handed a renewed
// token — the two outcomes are mutually exclusive, so a finished/superseded
// attempt's credential lapses rather than being refreshed.
func TestHeartbeatSupersededReturnsTerminateNoToken(t *testing.T) {
	store := &fakeStore{heartbeatErr: ErrStaleReport}
	srv, authn := newServer(store)
	srv.SetTokenRenewal(authn, 10*time.Minute, 24*time.Hour)

	resp, err := srv.Heartbeat(ctxWithToken(t, authn), &agentv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("stale heartbeat must not be an RPC error: %v", err)
	}
	if !resp.GetShouldTerminate() {
		t.Error("superseded heartbeat: should_terminate = false, want true")
	}
	if resp.GetRenewedToken() != "" {
		t.Error("superseded heartbeat must NOT carry a renewed token (never both)")
	}
}

// TestHeartbeatTransientErrorDoesNotRenew: a genuine (non-stale) store error
// leaves liveness unproven, so the heartbeat neither terminates nor renews — a
// DB blip must not re-credential (nor kill) a task.
func TestHeartbeatTransientErrorDoesNotRenew(t *testing.T) {
	store := &fakeStore{heartbeatErr: errTransient}
	srv, authn := newServer(store)
	srv.SetTokenRenewal(authn, 10*time.Minute, 24*time.Hour)

	resp, err := srv.Heartbeat(ctxWithToken(t, authn), &agentv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("transient heartbeat error must not surface as RPC error: %v", err)
	}
	if resp.GetShouldTerminate() {
		t.Error("transient DB error must not signal terminate")
	}
	if resp.GetRenewedToken() != "" {
		t.Error("transient DB error must not renew (liveness unproven)")
	}
}

// TestHeartbeatCeilingStopsRenewal: once an attempt has been alive past
// max_attempt_credential_lifetime since first dispatch, a live heartbeat still
// succeeds but stops handing out renewed tokens, so a runaway attempt's
// credential lapses. Modeled here with a sub-tick ceiling that the dispatch
// origin has already exceeded.
func TestHeartbeatCeilingStopsRenewal(t *testing.T) {
	store := &fakeStore{}
	srv, authn := newServer(store)
	srv.SetTokenRenewal(authn, 10*time.Minute, time.Nanosecond)

	resp, err := srv.Heartbeat(ctxWithToken(t, authn), &agentv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if resp.GetShouldTerminate() {
		t.Error("a live attempt past the ceiling must not be told to terminate; its token simply lapses")
	}
	if resp.GetRenewedToken() != "" {
		t.Error("past the max-attempt-lifetime ceiling the heartbeat must not renew")
	}
}

// TestHeartbeatNoRenewerLeavesTokenEmpty: with renewal unconfigured the handler
// behaves exactly as before — a live heartbeat carries no renewed token.
func TestHeartbeatNoRenewerLeavesTokenEmpty(t *testing.T) {
	srv, authn := newServer(&fakeStore{})
	resp, err := srv.Heartbeat(ctxWithToken(t, authn), &agentv1.HeartbeatRequest{})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if resp.GetRenewedToken() != "" {
		t.Error("no renewer configured: renewed token must be empty")
	}
}
