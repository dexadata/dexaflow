package auth

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeTokenStore serves one stored API token, keyed by its hash, and records
// the last-use writes.
type fakeTokenStore struct {
	hash    string
	token   *APIToken
	findErr error
	touched []time.Time
}

func (f *fakeTokenStore) FindAPITokenByHash(_ context.Context, hash string) (*APIToken, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	if hash != f.hash || f.token == nil {
		return nil, ErrAPITokenNotFound
	}
	copied := *f.token
	return &copied, nil
}

func (f *fakeTokenStore) TouchAPIToken(_ context.Context, _ string, at time.Time) (bool, error) {
	f.touched = append(f.touched, at)
	return true, nil
}

// nextAuth records whether the API token authenticator delegated to it.
type nextAuth struct{ called bool }

func (n *nextAuth) Authenticate(context.Context, string) (*User, error) {
	n.called = true
	return &User{ID: "jwt-user", TenantID: "default"}, nil
}

func (n *nextAuth) IssueToken(context.Context, Credentials) (string, error) { return "jwt", nil }

var tokenNow = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// newTokenFixture builds an authenticator holding one live deploy token owned
// by an active deployer in tenant "acme", and returns the plaintext.
func newTokenFixture(t *testing.T) (*APITokenAuthenticator, *fakeTokenStore, *fakeStore, *nextAuth, string) {
	t.Helper()
	plain, hash, err := NewAPIToken()
	if err != nil {
		t.Fatalf("NewAPIToken: %v", err)
	}
	tokens := &fakeTokenStore{hash: hash, token: &APIToken{
		ID: "tok-1", TenantID: "acme", UserID: "u-ci", Name: "ci-x",
		Scopes:    []string{ScopeRead, ScopeDeploy},
		CreatedAt: tokenNow.Add(-time.Hour), ExpiresAt: tokenNow.Add(90 * 24 * time.Hour),
	}}
	users := &fakeStore{byIDUser: &User{ID: "u-ci", TenantID: "acme", Email: "ci@acme.com", Roles: []string{"deployer"},
		Permissions: []Permission{{Action: "read", Resource: "dag"}, {Action: "deploy", Resource: "dag"}}}}
	next := &nextAuth{}
	a := NewAPITokenAuthenticator(next, tokens, users)
	a.now = func() time.Time { return tokenNow }
	return a, tokens, users, next, plain
}

func TestNewAPITokenIsPrefixedRandomAndHashedApart(t *testing.T) {
	a, ha, err := NewAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := NewAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, APITokenPrefix) || !IsAPIToken(a) {
		t.Errorf("token %q lacks the %s prefix", a, APITokenPrefix)
	}
	if a == b {
		t.Error("two tokens are equal")
	}
	// 32 random bytes in unpadded base64url are 43 characters.
	if got := len(strings.TrimPrefix(a, APITokenPrefix)); got != 43 {
		t.Errorf("secret length = %d, want 43", got)
	}
	if ha != HashAPIToken(a) || strings.Contains(ha, strings.TrimPrefix(a, APITokenPrefix)) {
		t.Errorf("hash %q is not the digest of the token", ha)
	}
	if len(ha) != 64 {
		t.Errorf("hash length = %d, want 64 hex characters", len(ha))
	}
	if IsAPIToken("eyJhbGciOiJIUzI1NiJ9.x.y") {
		t.Error("a JWT is not an API token")
	}
}

func TestAPITokenAuthenticatesAsScopedOwner(t *testing.T) {
	a, tokens, _, next, plain := newTokenFixture(t)

	user, err := a.Authenticate(context.Background(), plain)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if next.called {
		t.Error("an API token was handed to the JWT authenticator")
	}
	if user.ID != "u-ci" || user.TenantID != "acme" {
		t.Errorf("principal = %+v, want the token owner in acme", user)
	}
	if !user.Scoped || !slices.Equal(user.Scopes, []string{ScopeRead, ScopeDeploy}) {
		t.Errorf("scoped = %v scopes = %v, want the token's scopes", user.Scoped, user.Scopes)
	}
	if !user.HasPermission("deploy", "dag") {
		t.Error("the owner's permissions were not reloaded")
	}
	if len(tokens.touched) != 1 || !tokens.touched[0].Equal(tokenNow) {
		t.Errorf("last use writes = %v, want one at now", tokens.touched)
	}
}

// TestAPITokenEveryUseIsOfferedToTheStore: the authenticator offers every
// use to the store, which decides in SQL whether to write (the throttle is
// pinned by TestAPITokenLastUseIsThrottledInSQLIntegration).
func TestAPITokenEveryUseIsOfferedToTheStore(t *testing.T) {
	a, tokens, _, _, plain := newTokenFixture(t)
	recent := tokenNow.Add(-time.Minute)
	tokens.token.LastUsedAt = &recent
	if _, err := a.Authenticate(context.Background(), plain); err != nil {
		t.Fatal(err)
	}
	if len(tokens.touched) != 1 || !tokens.touched[0].Equal(tokenNow) {
		t.Errorf("uses offered = %v, want one at now", tokens.touched)
	}
}

func TestAPITokenRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeTokenStore, *fakeStore) string
	}{
		{"unknown token", func(*fakeTokenStore, *fakeStore) string { return APITokenPrefix + "nope" }},
		{"revoked", func(ts *fakeTokenStore, _ *fakeStore) string {
			at := tokenNow.Add(-time.Second)
			ts.token.RevokedAt = &at
			return ""
		}},
		{"expired", func(ts *fakeTokenStore, _ *fakeStore) string {
			ts.token.ExpiresAt = tokenNow
			return ""
		}},
		{"owner deactivated", func(_ *fakeTokenStore, us *fakeStore) string {
			us.inactive = true
			return ""
		}},
		{"owner deleted", func(_ *fakeTokenStore, us *fakeStore) string {
			us.byIDErr = ErrUserNotFound
			return ""
		}},
		{"owner in another tenant", func(_ *fakeTokenStore, us *fakeStore) string {
			us.byIDUser.TenantID = "other"
			return ""
		}},
		{"tokenless tenant", func(ts *fakeTokenStore, us *fakeStore) string {
			ts.token.TenantID = ""
			us.byIDUser.TenantID = ""
			return ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, tokens, users, next, plain := newTokenFixture(t)
			if override := tc.setup(tokens, users); override != "" {
				plain = override
			}
			user, err := a.Authenticate(context.Background(), plain)
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("Authenticate = %+v, %v; want ErrInvalidToken", user, err)
			}
			if next.called {
				t.Error("a refused API token fell through to the JWT authenticator")
			}
			if len(tokens.touched) != 0 {
				t.Error("a refused token recorded a use")
			}
		})
	}
}

// TestAPITokenStoreFailureIsNotInvalid: a database that cannot be read did not
// say the token is invalid, so the error propagates (503), as Authenticate does.
func TestAPITokenStoreFailureIsNotInvalid(t *testing.T) {
	a, tokens, users, _, plain := newTokenFixture(t)
	down := errors.New("connection refused")
	tokens.findErr = down
	if _, err := a.Authenticate(context.Background(), plain); !errors.Is(err, down) || errors.Is(err, ErrInvalidToken) {
		t.Errorf("token lookup failure = %v, want the store error, not invalid", err)
	}
	tokens.findErr = nil
	users.byIDErr = down
	if _, err := a.Authenticate(context.Background(), plain); !errors.Is(err, down) || errors.Is(err, ErrInvalidToken) {
		t.Errorf("owner reload failure = %v, want the store error, not invalid", err)
	}
}

func TestNonAPITokenIsDelegated(t *testing.T) {
	a, _, _, next, _ := newTokenFixture(t)
	user, err := a.Authenticate(context.Background(), "eyJhbGciOiJIUzI1NiJ9.x.y")
	if err != nil || !next.called || user.ID != "jwt-user" {
		t.Errorf("JWT = %+v, %v (delegated %v), want the next authenticator's answer", user, err, next.called)
	}
	if tok, err := a.IssueToken(context.Background(), Credentials{}); err != nil || tok != "jwt" {
		t.Errorf("IssueToken = %q, %v, want the next authenticator's", tok, err)
	}
}

func TestAPITokenScopes(t *testing.T) {
	// A deploy token does not carry dexaflow:read: the reads a deploy needs
	// accept dexaflow:deploy, and every other read stays closed (ADR 0074).
	if got, ok := APITokenScopes("deploy"); !ok || !slices.Equal(got, []string{ScopeDeploy}) {
		t.Errorf("deploy = %v, %v", got, ok)
	}
	if got, ok := APITokenScopes("read"); !ok || !slices.Equal(got, []string{ScopeRead}) {
		t.Errorf("read = %v, %v", got, ok)
	}
	for _, bad := range []string{"", "run", "admin", "DEPLOY", "deploy "} {
		if _, ok := APITokenScopes(bad); ok {
			t.Errorf("scope %q accepted", bad)
		}
	}
}
