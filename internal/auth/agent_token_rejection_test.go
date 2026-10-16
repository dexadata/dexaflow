package auth

import (
	"errors"
	"testing"
	"time"
)

// rejectionOf authenticates token and returns the AgentTokenRejection the
// error carries, failing the test when the token is accepted or the error
// carries none.
func rejectionOf(t *testing.T, a *JWTAuthenticator, token string) *AgentTokenRejection {
	t.Helper()
	_, err := a.AuthenticateAgent(token)
	if err == nil {
		t.Fatal("AuthenticateAgent accepted a token it must reject")
	}
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("error %v must still match ErrInvalidToken", err)
	}
	var rej *AgentTokenRejection
	if !errors.As(err, &rej) {
		t.Fatalf("error %v carries no AgentTokenRejection", err)
	}
	return rej
}

// An expired agent token is classified as expired, and because its signature
// and audience verified, the rejection names the attempt it belonged to and
// when it lapsed: that is what lets an operator tell a lapsed credential from a
// network fault (#1125).
func TestAgentTokenRejectionExpiredNamesTheAttempt(t *testing.T) {
	mint := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	a.now = func() time.Time { return mint }
	id := agentIdentity()
	id.AttemptEpoch, id.HasAttemptEpoch = 3, true
	token, err := a.IssueAgentToken(id, 10*time.Minute)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	a.now = func() time.Time { return mint.Add(15 * time.Minute) }

	rej := rejectionOf(t, a, token)
	if rej.Reason != AgentTokenExpired {
		t.Errorf("reason = %q, want %q", rej.Reason, AgentTokenExpired)
	}
	if rej.Identity == nil || *rej.Identity != id {
		t.Errorf("identity = %+v, want %+v", rej.Identity, id)
	}
	if want := mint.Add(10 * time.Minute); !rej.ExpiredAt.Equal(want) {
		t.Errorf("expired at = %v, want %v", rej.ExpiredAt, want)
	}
}

// A user token presented to the agent service verifies its signature but names
// the user audience: a wiring bug, classified apart from expiry, and its claims
// are not read as an attempt identity.
func TestAgentTokenRejectionWrongAudience(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	userToken, err := a.sign(&User{ID: "u1", TenantID: "acme", Roles: []string{"admin"}})
	if err != nil {
		t.Fatalf("sign user: %v", err)
	}
	rej := rejectionOf(t, a, userToken)
	if rej.Reason != AgentTokenWrongAudience {
		t.Errorf("reason = %q, want %q", rej.Reason, AgentTokenWrongAudience)
	}
	if rej.Identity != nil {
		t.Errorf("identity = %+v, want none for a token of another audience", rej.Identity)
	}
}

// A token signed with another key (key rotation, another install) is a bad
// signature, and its claims are unauthenticated, so they are never surfaced.
func TestAgentTokenRejectionBadSignature(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	other := NewJWTAuthenticator(nil, "different-secret", time.Hour)
	token, err := other.IssueAgentToken(agentIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	rej := rejectionOf(t, a, token)
	if rej.Reason != AgentTokenBadSignature {
		t.Errorf("reason = %q, want %q", rej.Reason, AgentTokenBadSignature)
	}
	if rej.Identity != nil {
		t.Errorf("identity = %+v, want none for an unverified token", rej.Identity)
	}
}

// A bearer that is not a JWT at all is malformed.
func TestAgentTokenRejectionMalformed(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	rej := rejectionOf(t, a, "not-a-jwt")
	if rej.Reason != AgentTokenMalformed {
		t.Errorf("reason = %q, want %q", rej.Reason, AgentTokenMalformed)
	}
}

// The rejection's text names the reason only: it is logged, so it must never
// carry the token or any part of it.
func TestAgentTokenRejectionErrorOmitsToken(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	token, err := a.IssueAgentToken(agentIdentity(), -time.Minute)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	rej := rejectionOf(t, a, token)
	if got, want := rej.Error(), "agent token rejected: expired"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
