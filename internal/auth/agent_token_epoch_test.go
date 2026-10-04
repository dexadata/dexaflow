package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// epochIdentity is a task credential for one attempt carrying the given epoch.
func epochIdentity(epoch int) AgentIdentity {
	id := agentIdentity()
	id.AttemptEpoch = epoch
	id.HasAttemptEpoch = true
	return id
}

// rawClaims decodes a signed token's payload without verifying it, so a test
// can assert what is actually on the wire.
func rawClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", token)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return m
}

// TestAgentTokenAttemptEpochRoundTrip: the epoch a dispatch mints is the epoch
// the verifier reads back, with the presence bit set, for 0 as well as N.
func TestAgentTokenAttemptEpochRoundTrip(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	for _, epoch := range []int{0, 1, 7} {
		want := epochIdentity(epoch)
		token, err := a.IssueAgentToken(want, 10*time.Minute)
		if err != nil {
			t.Fatalf("IssueAgentToken: %v", err)
		}
		if got, ok := rawClaims(t, token)["attempt_epoch"]; !ok || got != float64(epoch) {
			t.Errorf("epoch %d: the attempt_epoch claim on the wire = %v (present=%v)", epoch, got, ok)
		}
		got, err := a.AuthenticateAgent(token)
		if err != nil {
			t.Fatalf("AuthenticateAgent: %v", err)
		}
		if *got != want {
			t.Errorf("epoch %d: identity = %+v, want %+v", epoch, *got, want)
		}
	}
}

// TestLegacyAgentTokenHasNoAttemptEpoch: a token minted without the claim (by
// a binary that predates it) decodes with the presence bit unset, so the fence
// can tell "absent" from epoch 0 and apply the legacy rule.
func TestLegacyAgentTokenHasNoAttemptEpoch(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	token, err := a.IssueAgentToken(agentIdentity(), 10*time.Minute)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	if _, ok := rawClaims(t, token)["attempt_epoch"]; ok {
		t.Errorf("a token minted without an epoch must not carry the attempt_epoch claim")
	}
	got, err := a.AuthenticateAgent(token)
	if err != nil {
		t.Fatalf("AuthenticateAgent: %v", err)
	}
	if got.HasAttemptEpoch || got.AttemptEpoch != 0 {
		t.Errorf("a legacy token must decode with no epoch, got has=%v epoch=%d", got.HasAttemptEpoch, got.AttemptEpoch)
	}
}

// TestRenewAgentTokenPreservesAttemptEpoch: renewal re-mints the epoch
// verbatim, including its absence. Renewal must never upgrade a legacy token to
// some epoch: that would hand a superseded attempt its replacement's identity.
func TestRenewAgentTokenPreservesAttemptEpoch(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	cases := map[string]AgentIdentity{
		"absent": agentIdentity(),
		"zero":   epochIdentity(0),
		"n":      epochIdentity(5),
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			token, err := a.IssueAgentToken(want, 10*time.Minute)
			if err != nil {
				t.Fatalf("IssueAgentToken: %v", err)
			}
			renewed, ok, err := a.RenewAgentToken(token, 10*time.Minute, 0)
			if err != nil || !ok {
				t.Fatalf("RenewAgentToken ok=%v err=%v", ok, err)
			}
			_, present := rawClaims(t, renewed)["attempt_epoch"]
			if present != want.HasAttemptEpoch {
				t.Errorf("renewed claim present=%v, want %v", present, want.HasAttemptEpoch)
			}
			got, err := a.AuthenticateAgent(renewed)
			if err != nil {
				t.Fatalf("AuthenticateAgent(renewed): %v", err)
			}
			if *got != want {
				t.Errorf("renewed identity = %+v, want %+v", *got, want)
			}
		})
	}
}

// TestWarmWorkerCredentialCarriesNoAttemptEpoch: a warm worker's bootstrap
// credential names no attempt, so it never carries an epoch.
func TestWarmWorkerCredentialCarriesNoAttemptEpoch(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	token, err := a.IssueAgentToken(AgentIdentity{
		Scope: ScopeWarmWorker, WorkerID: "warm-1", DagVersionID: "v1", TenantID: "t1",
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	if _, ok := rawClaims(t, token)["attempt_epoch"]; ok {
		t.Errorf("a warm-worker credential must not carry attempt_epoch")
	}
}
