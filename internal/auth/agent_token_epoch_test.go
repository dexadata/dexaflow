package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
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

// resign replaces a token's payload with the given claims while keeping the
// original signature, which is what an agent that edits its own bearer can do.
func resign(t *testing.T, token string, claims map[string]any) string {
	t.Helper()
	parts := strings.Split(token, ".")
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + parts[2]
}

// TestAttemptEpochClaimCannotBeEditedByTheAgent: the agent holds its token, so
// it can rewrite the payload, but the HMAC no longer verifies. Neither the
// verifier nor renewal accepts a changed epoch or a stripped one (a downgrade
// to the legacy epoch-0 rule).
func TestAttemptEpochClaimCannotBeEditedByTheAgent(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	token, err := a.IssueAgentToken(epochIdentity(3), 10*time.Minute)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	bumped := rawClaims(t, token)
	bumped["attempt_epoch"] = 4
	stripped := rawClaims(t, token)
	delete(stripped, "attempt_epoch")
	for name, forged := range map[string]string{
		"changed":  resign(t, token, bumped),
		"stripped": resign(t, token, stripped),
	} {
		if _, err := a.AuthenticateAgent(forged); err == nil {
			t.Errorf("%s: AuthenticateAgent accepted an edited attempt_epoch", name)
		}
		if renewed, ok, err := a.RenewAgentToken(forged, 10*time.Minute, 0); err == nil || ok || renewed != "" {
			t.Errorf("%s: RenewAgentToken re-minted an edited token (ok=%v err=%v)", name, ok, err)
		}
	}
}

// legacyAgentClaims is the agent claim set of a binary that predates the
// attempt_epoch claim (v0.5.0), used to check both directions of a mixed
// version control plane.
type legacyAgentClaims struct {
	TenantID       string           `json:"tenant_id"`
	DagID          string           `json:"dag_id"`
	RunID          string           `json:"run_id"`
	TaskID         string           `json:"task_id"`
	TryNumber      int              `json:"try_number"`
	Scope          string           `json:"scope,omitempty"`
	DagVersionID   string           `json:"dag_version_id,omitempty"`
	OriginIssuedAt *jwt.NumericDate `json:"oiat,omitempty"`
	jwt.RegisteredClaims
}

// TestAttemptEpochMixedVersionVerifiers: an old verifier accepts a new token
// and reads the same task identity (the unknown claim is ignored), and a new
// verifier accepts a token an old minter signed and reads it as legacy.
func TestAttemptEpochMixedVersionVerifiers(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	want := epochIdentity(9)
	token, err := a.IssueAgentToken(want, 10*time.Minute)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	var old legacyAgentClaims
	if _, perr := jwt.ParseWithClaims(token, &old, func(*jwt.Token) (any, error) { return []byte("secret"), nil },
		jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceAgent), jwt.WithValidMethods([]string{"HS256"})); perr != nil {
		t.Fatalf("an old verifier rejected a token carrying attempt_epoch: %v", perr)
	}
	if old.Subject != want.TaskInstanceID || old.RunID != want.RunID || old.TaskID != want.TaskID || old.TryNumber != want.TryNumber {
		t.Errorf("an old verifier read %+v, want the identity %+v", old, want)
	}

	now := time.Now()
	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, legacyAgentClaims{
		TenantID: want.TenantID, DagID: want.DagID, RunID: want.RunID, TaskID: want.TaskID, TryNumber: want.TryNumber,
		OriginIssuedAt: jwt.NewNumericDate(now),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: want.TaskInstanceID, Issuer: tokenIssuer, Audience: jwt.ClaimStrings{audienceAgent},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
		},
	}).SignedString([]byte("secret"))
	if err != nil {
		t.Fatalf("signing a legacy token: %v", err)
	}
	got, err := a.AuthenticateAgent(legacy)
	if err != nil {
		t.Fatalf("a new verifier rejected a legacy token: %v", err)
	}
	wantLegacy := want
	wantLegacy.AttemptEpoch, wantLegacy.HasAttemptEpoch = 0, false
	if *got != wantLegacy {
		t.Errorf("legacy token identity = %+v, want %+v", *got, wantLegacy)
	}
	renewed, ok, err := a.RenewAgentToken(legacy, 10*time.Minute, 0)
	if err != nil || !ok {
		t.Fatalf("RenewAgentToken(legacy) ok=%v err=%v", ok, err)
	}
	if _, present := rawClaims(t, renewed)["attempt_epoch"]; present {
		t.Errorf("renewing a legacy token upgraded it to an epoch")
	}
}
