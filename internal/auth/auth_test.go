package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	user *User
	hash string
	err  error

	// byIDUser, when set, is what FindUserByID returns; otherwise it falls back
	// to user so login-only tests need not set it. inactive makes the reload
	// report the account as deactivated; byIDErr forces a store error (the
	// fail-closed path) and is errors.Is-matchable so a test can pass
	// ErrUserNotFound to exercise the trusted-token claims fallback.
	byIDUser *User
	inactive bool
	byIDErr  error
}

func (f *fakeStore) FindUserByLogin(_ context.Context, _, _ string) (*User, string, error) {
	return f.user, f.hash, f.err
}

func (f *fakeStore) FindUserByID(_ context.Context, _ string) (*User, bool, error) {
	if f.byIDErr != nil {
		return nil, false, f.byIDErr
	}
	u := f.byIDUser
	if u == nil {
		u = f.user
	}
	return u, !f.inactive, nil
}

func must(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}

func TestHasPermission(t *testing.T) {
	if !(&User{Roles: []string{"admin"}}).HasPermission("write", "dag") {
		t.Error("admin role should grant everything")
	}
	reader := &User{Permissions: []Permission{{Action: "read", Resource: "dag"}}}
	if !reader.HasPermission("read", "dag") {
		t.Error("reader should have read:dag")
	}
	if reader.HasPermission("write", "dag") {
		t.Error("reader should not have write:dag")
	}
	if !(&User{Permissions: []Permission{{Action: "admin", Resource: "*"}}}).HasPermission("execute", "dag") {
		t.Error("admin:* should grant everything")
	}
}

func TestPasswordHashVerify(t *testing.T) {
	h, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(h, "s3cret") {
		t.Error("correct password should verify")
	}
	if VerifyPassword(h, "wrong") {
		t.Error("wrong password should not verify")
	}
}

func TestJWTIssueAndAuthenticate(t *testing.T) {
	store := &fakeStore{user: &User{ID: "u1", TenantID: "default", Email: "a@b.c", Roles: []string{"admin"}}, hash: must(HashPassword("pw"))}
	a := NewJWTAuthenticator(store, "secret", time.Hour)
	tok, err := a.IssueToken(context.Background(), Credentials{Tenant: "default", Username: "a@b.c", Password: "pw"})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	u, err := a.Authenticate(context.Background(), tok)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if u.ID != "u1" || u.TenantID != "default" || len(u.Roles) != 1 || u.Roles[0] != "admin" {
		t.Errorf("unexpected user: %+v", u)
	}
	// Email must survive the round-trip, or /ui/auth/me shows a blank username.
	if u.Email != "a@b.c" {
		t.Errorf("email not preserved through token: %q", u.Email)
	}
}

func TestJWTRejectsBadPassword(t *testing.T) {
	store := &fakeStore{user: &User{ID: "u1"}, hash: must(HashPassword("pw"))}
	a := NewJWTAuthenticator(store, "secret", time.Hour)
	if _, err := a.IssueToken(context.Background(), Credentials{Password: "nope"}); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("want ErrInvalidCredentials, got %v", err)
	}
}

func TestJWTRejectsWrongSecretAndTampered(t *testing.T) {
	store := &fakeStore{user: &User{ID: "u1", Roles: []string{"admin"}}, hash: must(HashPassword("pw"))}
	a := NewJWTAuthenticator(store, "secret", time.Hour)
	tok, _ := a.IssueToken(context.Background(), Credentials{Password: "pw"})

	other := NewJWTAuthenticator(store, "other-secret", time.Hour)
	if _, err := other.Authenticate(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("wrong secret should yield ErrInvalidToken, got %v", err)
	}
	if _, err := a.Authenticate(context.Background(), tok+"tampered"); err == nil {
		t.Error("tampered token should fail")
	}
}

func TestJWTRejectsExpired(t *testing.T) {
	store := &fakeStore{user: &User{ID: "u1"}, hash: must(HashPassword("pw"))}
	a := NewJWTAuthenticator(store, "secret", -time.Hour)
	tok, _ := a.IssueToken(context.Background(), Credentials{Password: "pw"})
	if _, err := a.Authenticate(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expired token should yield ErrInvalidToken, got %v", err)
	}
}

func TestAuthenticateReloadsAuthzFromStore(t *testing.T) {
	// The token carries roles only (sign() never puts permissions in the JWT).
	// Permissions must be reloaded from the store on every request, or a
	// non-admin role can never gate anything. Mint a viewer token with an empty
	// permission set and prove the store's current grant shows up on the result.
	const secret = "reload-secret"
	tok, err := MintUserToken(secret, time.Hour, User{ID: "u-viewer", TenantID: "default", Email: "v@x.y", Roles: []string{"viewer"}})
	if err != nil {
		t.Fatalf("MintUserToken: %v", err)
	}
	store := &fakeStore{byIDUser: &User{
		ID: "u-viewer", TenantID: "default", Email: "v@x.y", Roles: []string{"viewer"},
		Permissions: []Permission{{Action: "read", Resource: "dag"}},
	}}
	a := NewJWTAuthenticator(store, secret, time.Hour)
	u, err := a.Authenticate(context.Background(), tok)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if !u.HasPermission("read", "dag") {
		t.Errorf("permissions must come from the store reload, got %+v", u.Permissions)
	}
}

func TestAuthenticateRejectsInactiveUser(t *testing.T) {
	// Deactivating a user must revoke their live tokens within the token TTL.
	const secret = "inactive-secret"
	tok, _ := MintUserToken(secret, time.Hour, User{ID: "u1", TenantID: "default", Roles: []string{"admin"}})
	store := &fakeStore{user: &User{ID: "u1", Roles: []string{"admin"}}, inactive: true}
	a := NewJWTAuthenticator(store, secret, time.Hour)
	if _, err := a.Authenticate(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("deactivated user must be rejected, got %v", err)
	}
}

func TestAuthenticateFailsClosedOnStoreError(t *testing.T) {
	// A DB failure during the reload must reject the request, never fall through
	// to the token claims (which would let a failing store silently disable
	// revocation) and never panic.
	//
	// The assertion used to be `errors.Is(err, ErrInvalidToken)`, which encoded
	// the CLASSIFICATION rather than the security property and made a database
	// outage indistinguishable from a forged token (#1087). Fail-closed does not
	// depend on the error's identity: both callers of Authenticate allow on
	// `err == nil` and deny on anything else, and nothing outside this package
	// references ErrInvalidToken. So the property is "an error, and no user" —
	// which is what this now asserts, plus the new requirement that a backend
	// failure is not labeled invalid.
	const secret = "dberr-secret"
	tok, _ := MintUserToken(secret, time.Hour, User{ID: "u1", TenantID: "default", Roles: []string{"admin"}})
	dbErr := errors.New("connection refused")
	store := &fakeStore{byIDErr: dbErr}
	a := NewJWTAuthenticator(store, secret, time.Hour)
	user, err := a.Authenticate(context.Background(), tok)
	if err == nil {
		t.Fatal("a store error must reject the request; got err = nil")
	}
	if user != nil {
		t.Errorf("a store error must not return a user (that would disable revocation); got %+v", user)
	}
	if !errors.Is(err, dbErr) {
		t.Errorf("the cause must survive for the log; got %v", err)
	}
	if errors.Is(err, ErrInvalidToken) {
		t.Error("a backend failure must not be labeled an invalid token — the caller maps that to 401 and tells the client to re-authenticate against the database that is down")
	}
}

func TestAuthenticateFallsBackToClaimsForMintedToken(t *testing.T) {
	// A directly-minted token (dexaflow lite / in-process trusted caller) has no
	// backing user row. Its signed claims stay the source of truth so the dev
	// inner loop keeps working even when the control plane enforces real auth.
	const secret = "minted-secret"
	tok, _ := MintUserToken(secret, time.Hour, User{ID: "leoflow-dev", TenantID: "default", Email: "dev@leoflow.local", Roles: []string{"admin"}})
	store := &fakeStore{byIDErr: ErrUserNotFound}
	a := NewJWTAuthenticator(store, secret, time.Hour)
	u, err := a.Authenticate(context.Background(), tok)
	if err != nil {
		t.Fatalf("minted token must authenticate via claims fallback: %v", err)
	}
	if u.ID != "leoflow-dev" || len(u.Roles) != 1 || u.Roles[0] != "admin" {
		t.Errorf("claims not preserved on fallback: %+v", u)
	}
}

// TestAuthenticateRejectsMissingNonDevSubject pins the hardening: the claims
// fallback is ONLY for the dev-token subject. Any other token whose subject has
// no user row (e.g. a hard-deleted user) fails closed instead of keeping the
// roles baked into its signed claims until the token expires.
func TestAuthenticateRejectsMissingNonDevSubject(t *testing.T) {
	const secret = "minted-secret"
	tok, _ := MintUserToken(secret, time.Hour, User{ID: "deleted-user-id", TenantID: "default", Email: "gone@leoflow.local", Roles: []string{"admin"}})
	store := &fakeStore{byIDErr: ErrUserNotFound}
	a := NewJWTAuthenticator(store, secret, time.Hour)
	if _, err := a.Authenticate(context.Background(), tok); err == nil {
		t.Fatal("a non-dev subject with no user row must be rejected, not trusted from claims")
	}
}

func TestRateLimiter(t *testing.T) {
	now := time.Now()
	r := NewRateLimiter(3, time.Minute)
	r.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !r.Allow("ip") {
			t.Errorf("event %d should be allowed", i)
		}
	}
	if r.Allow("ip") {
		t.Error("4th event in window should be denied")
	}
	if !r.Allow("other-ip") {
		t.Error("a different key should be allowed")
	}
	now = now.Add(time.Minute + time.Second)
	if !r.Allow("ip") {
		t.Error("after the window resets, should be allowed again")
	}
}

func TestRateLimiterBlockedIsAPeek(t *testing.T) {
	now := time.Now()
	r := NewRateLimiter(2, time.Minute)
	r.now = func() time.Time { return now }
	// Blocked must NOT consume budget: peeking any number of times keeps the key
	// usable. This lets the handler reject over-limit callers up front while only
	// counting actual failures.
	for i := 0; i < 10; i++ {
		if r.Blocked("ip") {
			t.Fatal("peek must not count toward the limit")
		}
	}
	r.Allow("ip")
	if r.Blocked("ip") {
		t.Error("one recorded event must not block (limit 2)")
	}
	r.Allow("ip")
	if !r.Blocked("ip") {
		t.Error("reaching the limit must block")
	}
	// The window resets the block.
	now = now.Add(time.Minute + time.Second)
	if r.Blocked("ip") {
		t.Error("a new window must not be blocked")
	}
}

func TestMintUserTokenRoundTrips(t *testing.T) {
	const secret = "dev-insecure-jwt-secret-change-me"
	token, err := MintUserToken(secret, time.Hour, User{
		ID: "dev", TenantID: "default", Email: "admin@leoflow.local", Roles: []string{"admin"},
	})
	if err != nil {
		t.Fatalf("MintUserToken: %v", err)
	}
	a := NewJWTAuthenticator(nil, secret, time.Hour)
	u, aerr := a.Authenticate(context.Background(), token)
	if aerr != nil {
		t.Fatalf("Authenticate(minted) = %v", aerr)
	}
	if u.TenantID != "default" || u.Email != "admin@leoflow.local" || len(u.Roles) != 1 || u.Roles[0] != "admin" {
		t.Errorf("round-trip user = %+v, want admin@default", u)
	}
	// A token signed with a different secret must be rejected.
	if _, e := a.Authenticate(context.Background(), func() string { s, _ := MintUserToken("other", time.Hour, User{ID: "x"}); return s }()); e == nil {
		t.Error("token signed with a different secret must not authenticate")
	}
}
