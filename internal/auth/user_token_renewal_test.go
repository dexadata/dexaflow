package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// userClaimsOf decodes a signed user token's registered + custom claims so a test
// can assert renewal preserves identity and stamps a fresh, non-accumulating exp.
func userClaimsOf(t *testing.T, a *JWTAuthenticator, token string) jwtClaims {
	t.Helper()
	var c jwtClaims
	if _, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceUser), jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(a.clock)); err != nil {
		t.Fatalf("parsing user token: %v", err)
	}
	return c
}

// TestRenewUserTokenShortTTLAndPreservedOrigin: a renewal for a live session
// re-mints the SAME identity/claims with a fresh short TTL (exp = now + ttl, never
// accumulating) while preserving the original login time (oiat) so the session's
// total lifetime can be bounded across renewals. This is the transparent CLI
// renewal that removes the hourly re-login (EKS validation aresta #5), modeled on
// RenewAgentToken.
func TestRenewUserTokenShortTTLAndPreservedOrigin(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0 }

	// Login mints the initial access token; its origin (oiat) is t0.
	user := User{ID: "u1", TenantID: "default", Email: "a@b.c", Roles: []string{"admin"}}
	issued, err := a.mintUserToken(&user, time.Hour, t0)
	if err != nil {
		t.Fatalf("mintUserToken: %v", err)
	}
	if o := userClaimsOf(t, a, issued).OriginIssuedAt; o == nil || !o.Equal(t0) {
		t.Fatalf("issued origin = %v, want %v", o, t0)
	}

	// 55 minutes later, near expiry, the CLI silently renews.
	t1 := t0.Add(55 * time.Minute)
	a.now = func() time.Time { return t1 }
	renewed, ok, err := a.RenewUserToken(context.Background(), issued, time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("RenewUserToken: %v", err)
	}
	if !ok {
		t.Fatal("renewal within the ceiling must succeed (ok=false)")
	}

	// The renewed token verifies unchanged (signature/issuer/leoflow-user
	// audience/HS256, under the mock clock) and carries the same identity/claims.
	got := userClaimsOf(t, a, renewed)
	if got.Subject != "u1" || got.TenantID != "default" || got.Email != "a@b.c" ||
		len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Errorf("renewed claims = %+v, want same identity as issued", got)
	}

	// exp = now + ttl exactly (fresh short TTL, never accumulated onto old exp),
	// and strictly later than the original exp so the session keeps working.
	if d := got.ExpiresAt.Sub(got.IssuedAt.Time); d != time.Hour {
		t.Errorf("renewed exp-iat = %v, want 1h (no accumulation)", d)
	}
	if !got.IssuedAt.Equal(t1) {
		t.Errorf("renewed iat = %v, want the renewal instant %v", got.IssuedAt.Time, t1)
	}
	origIssued := userClaimsOf(t, a, issued)
	if !got.ExpiresAt.After(origIssued.ExpiresAt.Time) {
		t.Errorf("renewed exp %v must be later than original exp %v", got.ExpiresAt.Time, origIssued.ExpiresAt.Time)
	}

	// Origin is preserved across the renewal — it still points at login.
	if o := got.OriginIssuedAt; o == nil || !o.Equal(t0) {
		t.Errorf("renewed origin = %v, want the login time %v (must not advance)", o, t0)
	}
}

// TestRenewUserTokenRejectsExpired: an expired token is refused with an error
// (never silently re-minted). The handler maps this to 401 so the user re-logs in.
func TestRenewUserTokenRejectsExpired(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0 }
	user := User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	// A short-lived token that is already expired by the time renewal is attempted.
	expired, err := a.mintUserToken(&user, time.Minute, t0)
	if err != nil {
		t.Fatalf("mintUserToken: %v", err)
	}
	a.now = func() time.Time { return t0.Add(2 * time.Minute) }
	renewed, ok, err := a.RenewUserToken(context.Background(), expired, time.Hour, 24*time.Hour)
	if err == nil || ok || renewed != "" {
		t.Errorf("renewing an expired token must error and mint nothing; got ok=%v renewed=%q err=%v", ok, renewed, err)
	}
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("expired renewal error = %v, want ErrInvalidToken", err)
	}
}

// TestRenewUserTokenCeilingStopsRenewal: once a session has been alive past
// max_lifetime since first login, renewal is refused (ok=false, no token, no
// error) so the user must re-authenticate — the real security bound on a
// transparently-renewed credential.
func TestRenewUserTokenCeilingStopsRenewal(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	// A token freshly minted (still valid) but whose preserved origin is 25h old.
	a.now = func() time.Time { return t0.Add(25 * time.Hour) }
	aged, err := a.mintUserToken(&User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, time.Hour, t0)
	if err != nil {
		t.Fatalf("mintUserToken: %v", err)
	}
	renewed, ok, err := a.RenewUserToken(context.Background(), aged, time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("RenewUserToken must not error past the ceiling: %v", err)
	}
	if ok || renewed != "" {
		t.Errorf("renewal past max_lifetime must be refused with no token; got ok=%v renewed=%q", ok, renewed)
	}
}

// TestRenewUserTokenNoCeiling: a non-positive max_lifetime disables the bound —
// renewal always succeeds regardless of age.
func TestRenewUserTokenNoCeiling(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0.Add(1000 * time.Hour) }
	aged, err := a.mintUserToken(&User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, time.Hour, t0)
	if err != nil {
		t.Fatalf("mintUserToken: %v", err)
	}
	if _, ok, err := a.RenewUserToken(context.Background(), aged, time.Hour, 0); err != nil || !ok {
		t.Errorf("a non-positive ceiling must never refuse renewal; got ok=%v err=%v", ok, err)
	}
}

// TestRenewUserTokenRejectsForeignAndAgentTokens: renewal validates the incoming
// token exactly as Authenticate does — a foreign-signed token is refused, and an
// agent-audience token can never be renewed on the user path (audience isolation).
func TestRenewUserTokenRejectsForeignAndAgentTokens(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)

	other := NewJWTAuthenticator(nil, "different-secret", time.Hour)
	foreign, err := other.mintUserToken(&User{ID: "u1"}, time.Hour, other.clock())
	if err != nil {
		t.Fatalf("mintUserToken: %v", err)
	}
	if _, ok, ferr := a.RenewUserToken(context.Background(), foreign, time.Hour, 24*time.Hour); ferr == nil || ok {
		t.Errorf("renewing a foreign-signed token must fail; got ok=%v err=%v", ok, ferr)
	}

	agentTok, err := a.IssueAgentToken(agentIdentity(), 10*time.Minute)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	if _, ok, aerr := a.RenewUserToken(context.Background(), agentTok, time.Hour, 24*time.Hour); aerr == nil || ok {
		t.Errorf("an agent-audience token must not be renewable on the user path; got ok=%v err=%v", ok, aerr)
	}
}

// TestIssueTokenStampsOrigin: a freshly issued user token carries an origin (oiat)
// claim anchored at the issue instant, so the max_lifetime ceiling is measured
// from first login even across later renewals.
func TestIssueTokenStampsOrigin(t *testing.T) {
	store := &fakeStore{user: &User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, hash: must(HashPassword("pw"))}
	a := NewJWTAuthenticator(store, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0 }
	tok, err := a.IssueToken(context.Background(), Credentials{Tenant: "default", Username: "u1", Password: "pw"})
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	c := userClaimsOf(t, a, tok)
	if c.OriginIssuedAt == nil || !c.OriginIssuedAt.Equal(t0) {
		t.Errorf("issued origin = %v, want issue instant %v", c.OriginIssuedAt, t0)
	}
}

// TestRenewUserTokenRefusesDeactivatedUser pins the invariant this whole file
// had missed: renewal must RE-PROVE the principal against the store, exactly as
// Authenticate does, instead of rebuilding it from the incoming token's claims.
// Every other renewal test above builds the authenticator with a nil store, so
// nothing here exercised the reload at all — a deactivated user kept collecting
// fresh tokens from /auth/token/renew until the session ceiling elapsed (#801).
// TestRenewUserTokenTakesRolesFromStore is the success-path lock the refusal
// tests do not provide. Every other new case here pins a branch that REFUSES,
// so all of them keep passing if the reload's result is discarded and the
// claimed principal is re-minted instead — which is the one behavior change a
// user can observe on the happy path, and the whole point of reloading. Mint a
// token claiming a role the store no longer grants, renew, and read the roles
// back off the re-minted token: they must be the store's.
func TestRenewUserTokenTakesRolesFromStore(t *testing.T) {
	const secret = "renew-roles-secret"
	issued, err := MintUserToken(secret, time.Hour, User{
		ID: "u-stale", TenantID: "default", Email: "s@x.y", Roles: []string{"admin"},
	})
	if err != nil {
		t.Fatalf("MintUserToken: %v", err)
	}
	store := &fakeStore{byIDUser: &User{
		ID: "u-stale", TenantID: "default", Email: "s@x.y", Roles: []string{"viewer"},
	}}
	a := NewJWTAuthenticator(store, secret, time.Hour)

	renewed, ok, err := a.RenewUserToken(context.Background(), issued, time.Hour, 24*time.Hour)
	if err != nil || !ok {
		t.Fatalf("RenewUserToken: ok=%v err=%v", ok, err)
	}
	got := userClaimsOf(t, a, renewed).Roles
	if len(got) != 1 || got[0] != "viewer" {
		t.Errorf("re-minted roles = %v, want the store's [viewer] — a renewal that re-mints the "+
			"claimed roles carries a grant the store has already withdrawn", got)
	}
}

func TestRenewUserTokenRefusesDeactivatedUser(t *testing.T) {
	store := &fakeStore{user: &User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, inactive: true}
	a := NewJWTAuthenticator(store, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0 }
	issued, err := a.mintUserToken(&User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, time.Hour, t0)
	if err != nil {
		t.Fatalf("mintUserToken: %v", err)
	}
	renewed, ok, err := a.RenewUserToken(context.Background(), issued, time.Hour, 24*time.Hour)
	if err == nil || ok || renewed != "" {
		t.Fatalf("a deactivated user must be refused and minted nothing; got ok=%v renewed=%q err=%v", ok, renewed, err)
	}
	if !errors.Is(err, ErrInvalidToken) {
		t.Errorf("deactivated renewal error = %v, want ErrInvalidToken", err)
	}
}

// TestRenewUserTokenRefusesMissingNonDevSubject mirrors
// TestAuthenticateRejectsMissingNonDevSubject onto the renewal path: a subject
// with no user row (a hard-deleted user) is refused rather than trusted from the
// roles baked into its signed claims.
func TestRenewUserTokenRefusesMissingNonDevSubject(t *testing.T) {
	a := NewJWTAuthenticator(&fakeStore{byIDErr: ErrUserNotFound}, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0 }
	issued, err := a.mintUserToken(&User{ID: "deleted-user-id", TenantID: "default", Roles: []string{"admin"}}, time.Hour, t0)
	if err != nil {
		t.Fatalf("mintUserToken: %v", err)
	}
	renewed, ok, err := a.RenewUserToken(context.Background(), issued, time.Hour, 24*time.Hour)
	if err == nil || ok || renewed != "" {
		t.Fatalf("a non-dev subject with no user row must be refused; got ok=%v renewed=%q err=%v", ok, renewed, err)
	}
}

// TestRenewUserTokenFailsClosedOnStoreError: a store failure during the reload
// refuses the renewal instead of falling through to the token's claims, which
// would let a flaky database silently restore the old claims-only behavior. It
// adds no new failure mode — a blip that refuses the renewal would refuse the
// following request too, and the client's refresh is best-effort by construction.
func TestRenewUserTokenFailsClosedOnStoreError(t *testing.T) {
	a := NewJWTAuthenticator(&fakeStore{byIDErr: errors.New("connection refused")}, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0 }
	issued, err := a.mintUserToken(&User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, time.Hour, t0)
	if err != nil {
		t.Fatalf("mintUserToken: %v", err)
	}
	if renewed, ok, rerr := a.RenewUserToken(context.Background(), issued, time.Hour, 24*time.Hour); rerr == nil || ok || renewed != "" {
		t.Fatalf("a store error must fail closed; got ok=%v renewed=%q err=%v", ok, renewed, rerr)
	}
}

// TestRenewUserTokenAllowsDevSubjectWithNoUserRow preserves the first of
// Authenticate's two carve-outs on the renewal path: the in-process `dexaflow lite`
// token intentionally has no user row, so its signed claims stay the source of
// truth and its session keeps renewing.
func TestRenewUserTokenAllowsDevSubjectWithNoUserRow(t *testing.T) {
	a := NewJWTAuthenticator(&fakeStore{byIDErr: ErrUserNotFound}, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0 }
	issued, err := a.mintUserToken(
		&User{ID: DevTokenSubject, TenantID: "default", Email: "dev@leoflow.local", Roles: []string{"admin"}},
		time.Hour, t0)
	if err != nil {
		t.Fatalf("mintUserToken: %v", err)
	}
	renewed, ok, err := a.RenewUserToken(context.Background(), issued, time.Hour, 24*time.Hour)
	if err != nil || !ok {
		t.Fatalf("the dev subject must keep renewing without a user row; got ok=%v err=%v", ok, err)
	}
	got := userClaimsOf(t, a, renewed)
	if got.Subject != DevTokenSubject || len(got.Roles) != 1 || got.Roles[0] != "admin" {
		t.Errorf("renewed dev claims = %+v, want the signed claims preserved", got)
	}
}

// TestRenewUserTokenAllowsNilStore preserves Authenticate's other carve-out: with
// no data plane bound (the trusted in-process minting context — `dexaflow lite`,
// tests) there is nothing to reload, so renewal proceeds on the signed claims.
func TestRenewUserTokenAllowsNilStore(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0 }
	issued, err := a.mintUserToken(&User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, time.Hour, t0)
	if err != nil {
		t.Fatalf("mintUserToken: %v", err)
	}
	if _, ok, rerr := a.RenewUserToken(context.Background(), issued, time.Hour, 24*time.Hour); rerr != nil || !ok {
		t.Fatalf("a nil store must still renew; got ok=%v err=%v", ok, rerr)
	}
}

// TestRenewUserTokenPropagatesBackendError is the renewal half of #1087.
//
// Authenticate and reloadForRenewal read the SAME store for the SAME reason, so
// they must classify its failures the same way. reloadForRenewal kept joining
// ErrInvalidToken onto every store error, and renewTokenHandler maps any error
// to 401 "log in again" — so during a database outage the renew route still
// answered with a statement about the caller's credential, and the cause never
// reached the log at all (AbortProblem carries no cause). Fail-closed is
// unchanged: the renewal is still refused, it is just no longer mislabeled.
func TestRenewUserTokenPropagatesBackendError(t *testing.T) {
	dbErr := errors.New("dial tcp 10.0.0.1:5432: i/o timeout")
	a := NewJWTAuthenticator(&fakeStore{byIDErr: dbErr}, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0 }
	issued, err := a.mintUserToken(&User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, time.Hour, t0)
	if err != nil {
		t.Fatalf("mintUserToken: %v", err)
	}

	renewed, ok, rerr := a.RenewUserToken(context.Background(), issued, time.Hour, 24*time.Hour)
	if ok || renewed != "" {
		t.Fatalf("a store error must still fail closed; got ok=%v renewed=%q", ok, renewed)
	}
	if !errors.Is(rerr, dbErr) {
		t.Errorf("the cause must survive for the log; got %v", rerr)
	}
	if errors.Is(rerr, ErrInvalidToken) {
		t.Error("a backend failure must not be labeled an invalid token — the handler maps that to 401 and sends the client to log in against the database that is down")
	}
}
