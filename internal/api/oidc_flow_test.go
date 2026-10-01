package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/oidc"
)

// ─────────────────────────── fake IdP ───────────────────────────

// fakeIDP is an in-process OpenID Provider: it serves discovery + JWKS + a token
// endpoint, and signs ID tokens with an in-test RSA key. The token endpoint
// verifies the PKCE code_verifier against the challenge staged for the code, so
// the tests exercise a real PKCE round-trip.
type fakeIDP struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	kid    string
	issuer string

	mu     sync.Mutex
	staged map[string]stagedCode
}

type stagedCode struct {
	challenge string
	idToken   string
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	f := &fakeIDP{key: key, kid: "test-key-1", staged: map[string]stagedCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.handleDiscovery)
	mux.HandleFunc("/jwks", f.handleJWKS)
	mux.HandleFunc("/token", f.handleToken)
	f.srv = httptest.NewServer(mux)
	f.issuer = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIDP) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                f.issuer,
		"authorization_endpoint":                f.issuer + "/authorize",
		"token_endpoint":                        f.issuer + "/token",
		"jwks_uri":                              f.issuer + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (f *fakeIDP) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	pub := f.key.PublicKey
	writeJSON(w, map[string]any{"keys": []map[string]any{{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": f.kid,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

func (f *fakeIDP) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	code := r.PostForm.Get("code")
	verifier := r.PostForm.Get("code_verifier")
	f.mu.Lock()
	st, ok := f.staged[code]
	f.mu.Unlock()
	if !ok {
		http.Error(w, "unknown code", http.StatusBadRequest)
		return
	}
	// Prove PKCE: the presented verifier must hash to the staged challenge.
	sum := sha256.Sum256([]byte(verifier))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != st.challenge {
		http.Error(w, "pkce mismatch", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": "fake-access-token",
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     st.idToken,
	})
}

func (f *fakeIDP) signIDToken(claims jwt.MapClaims) string {
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = f.kid
	s, err := tok.SignedString(f.key)
	if err != nil {
		panic(err)
	}
	return s
}

func (f *fakeIDP) stage(code, challenge, idToken string) {
	f.mu.Lock()
	f.staged[code] = stagedCode{challenge: challenge, idToken: idToken}
	f.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ─────────────────────────── fakes ───────────────────────────

type fakeOIDCStore struct {
	users        map[string]*auth.User // provider|subject → user
	active       map[string]bool
	roles        map[string]bool
	created      []createdOIDC
	createErr    error
	reconciled   []reconcileCall
	reconcileErr error
	// roleErr is what the lookup itself returns. The real repository resolves the
	// tenant first, so domain.ErrNotFound here is a tenant that does not exist,
	// while any other error is the database failing. The handler audits the two
	// differently and a fake that collapsed them would make that untestable.
	roleErr error
}

type createdOIDC struct {
	tenant, email, provider, subject string
	roles                            []string
}

type reconcileCall struct {
	userID string
	roles  []string
}

func newFakeOIDCStore() *fakeOIDCStore {
	return &fakeOIDCStore{
		users:  map[string]*auth.User{},
		active: map[string]bool{},
		roles:  map[string]bool{"viewer": true, "editor": true, "operator": true, "admin": true},
	}
}

func key(provider, subject string) string { return provider + "|" + subject }

func (s *fakeOIDCStore) seed(provider, subject string, u *auth.User, active bool) {
	s.users[key(provider, subject)] = u
	s.active[key(provider, subject)] = active
}

func (s *fakeOIDCStore) FindUserByOIDCSubject(_ context.Context, provider, subject string) (*auth.User, bool, error) {
	u, ok := s.users[key(provider, subject)]
	if !ok {
		return nil, false, auth.ErrUserNotFound
	}
	return u, s.active[key(provider, subject)], nil
}

func (s *fakeOIDCStore) CreateOIDCUser(_ context.Context, tenant, email, provider, subject string, roles []string) (*auth.User, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	s.created = append(s.created, createdOIDC{tenant, email, provider, subject, roles})
	u := &auth.User{ID: "jit-" + subject, TenantID: tenant, Email: email, Roles: roles}
	s.seed(provider, subject, u, true)
	return u, nil
}

func (s *fakeOIDCStore) RoleExists(_ context.Context, _ string, role string) (bool, error) {
	if s.roleErr != nil {
		return false, s.roleErr
	}
	return s.roles[role], nil
}

func (s *fakeOIDCStore) ReconcileUserRoles(_ context.Context, userID string, roleNames []string) error {
	if s.reconcileErr != nil {
		return s.reconcileErr
	}
	s.reconciled = append(s.reconciled, reconcileCall{userID: userID, roles: append([]string(nil), roleNames...)})
	// Reflect the reconciled set onto the resolvable user so a later
	// FindUserByOIDCSubject (standing in for the per-request reload) observes it.
	for k, u := range s.users {
		if u.ID == userID {
			u.Roles = append([]string(nil), roleNames...)
			s.users[k] = u
		}
	}
	return nil
}

// lastReconcile returns the roles from the most recent reconcile for userID, and
// whether any reconcile happened for it.
func (s *fakeOIDCStore) lastReconcile(userID string) ([]string, bool) {
	for i := len(s.reconciled) - 1; i >= 0; i-- {
		if s.reconciled[i].userID == userID {
			return s.reconciled[i].roles, true
		}
	}
	return nil, false
}

type fakeAuthAudit struct {
	mu     sync.Mutex
	events []authEvent
}

type authEvent struct {
	tenant, userID, action, email, outcome string
	extra                                  map[string]string
}

func (a *fakeAuthAudit) RecordAuthEvent(_ context.Context, tenant, userID, action, email, outcome string, extra map[string]string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, authEvent{tenant, userID, action, email, outcome, extra})
	return nil
}

func (a *fakeAuthAudit) has(action, outcome string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.events {
		if e.action == action && e.outcome == outcome {
			return true
		}
	}
	return false
}

// hasReason reports whether a "denied" event for action carries the given reason
// in its extra metadata.
func (a *fakeAuthAudit) hasReason(action, reason string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.events {
		if e.action == action && e.outcome == "denied" && e.extra["reason"] == reason {
			return true
		}
	}
	return false
}

// ─────────────────────────── harness ───────────────────────────

const testHS256Secret = "oidc-flow-test-secret"

func baseOIDCConfig(f *fakeIDP) config.OIDCSection {
	return config.OIDCSection{
		Issuer:           f.issuer,
		ClientID:         "client-abc",
		ClientSecret:     "client-secret",
		RedirectURL:      "https://app.example/api/v2/auth/oidc/callback",
		Scopes:           []string{"openid", "email", "profile", "groups"},
		GroupsClaim:      "groups",
		RoleMappings:     map[string]string{"data-eng": "editor"},
		TenantClaim:      "tid",
		TenantClaims:     map[string]string{"tenant-guid": "default"},
		ClockSkewSeconds: 60,
	}
}

func oidcServer(t *testing.T, f *fakeIDP, cfg config.OIDCSection, store OIDCUserStore, audit AuthAuditWriter, authn auth.Authenticator) *gin.Engine {
	t.Helper()
	flow, err := oidc.NewFlow(context.Background(), cfg, testHS256Secret)
	if err != nil {
		t.Fatalf("NewFlow: %v", err)
	}
	if authn == nil {
		authn = &fakeAuthn{}
	}
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: authn,
		RateLimiter:   auth.NewRateLimiter(50, time.Minute),
		HealthChecks:  map[string]HealthChecker{},
		CORSOrigins:   []string{"*"},
		TokenTTLSecs:  3600,
		OIDCFlow:      flow,
		OIDCSettings:  cfg,
		OIDCUsers:     store,
		AuthAudit:     audit,
		JWTSecret:     testHS256Secret,
	})
}

type loginState struct {
	state, nonce, challenge string
	cookie                  *http.Cookie
}

func startLogin(t *testing.T, srv *gin.Engine) loginState {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/auth/oidc/login?next=/dags", http.NoBody)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	// Not assertLoginSucceeded: this is the START of the flow, a redirect OUT to
	// the IdP, so there is no session yet. What has to hold here is that it is a
	// redirect and that it is not the bounce back to the login page a refused
	// login now produces.
	if rec.Code != http.StatusFound {
		t.Fatalf("login = %d, want a %d redirect to the IdP", rec.Code, http.StatusFound)
	}
	if strings.Contains(rec.Header().Get("Location"), "sso_error") {
		t.Fatalf("the login route refused before reaching the IdP: %q", rec.Header().Get("Location"))
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q, want S256 (PKCE)", q.Get("code_challenge_method"))
	}
	var cookie *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == oidcStateCookie {
			cookie = ck
		}
	}
	if cookie == nil {
		t.Fatal("login did not set the state cookie")
	}
	if !cookie.HttpOnly {
		t.Error("state cookie is not HttpOnly")
	}
	return loginState{state: q.Get("state"), nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), cookie: cookie}
}

func baseClaims(cfg config.OIDCSection, nonce string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":            cfg.Issuer,
		"aud":            cfg.ClientID,
		"sub":            "subject-123",
		"exp":            now.Add(time.Hour).Unix(),
		"iat":            now.Unix(),
		"nonce":          nonce,
		"email":          "alice@corp.example",
		"email_verified": true,
		"tid":            "tenant-guid",
		"groups":         []string{"data-eng"},
	}
}

// completeCallback stages the given raw ID token for a fresh code and invokes the
// callback with the login state's state + cookie.
func completeCallback(srv *gin.Engine, f *fakeIDP, st loginState, idToken string) *httptest.ResponseRecorder {
	code := "code-" + st.state[:12]
	f.stage(code, st.challenge, idToken)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/auth/oidc/callback?code="+code+"&state="+url.QueryEscape(st.state), http.NoBody)
	req.AddCookie(st.cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// driveCallback runs the full login→callback, minting claims from baseClaims with
// the login nonce and an optional mutation.
func driveCallback(t *testing.T, srv *gin.Engine, f *fakeIDP, cfg config.OIDCSection, mutate func(jwt.MapClaims)) *httptest.ResponseRecorder {
	t.Helper()
	st := startLogin(t, srv)
	claims := baseClaims(cfg, st.nonce)
	if mutate != nil {
		mutate(claims)
	}
	return completeCallback(srv, f, st, f.signIDToken(claims))
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == authTokenCookie && ck.Value != "" {
			return ck
		}
	}
	return nil
}

func tokenRoles(t *testing.T, token string) []string {
	t.Helper()
	var claims jwt.MapClaims
	if _, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) {
		return []byte(testHS256Secret), nil
	}); err != nil {
		t.Fatalf("parse minted token: %v", err)
	}
	raw, _ := claims["roles"].([]any)
	roles := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			roles = append(roles, s)
		}
	}
	return roles
}

// ─────────────────────────── happy path ───────────────────────────

func TestOIDCHappyPathResolvesUserAndMintsSession(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	// Pre-provisioned user, resolved by the immutable (issuer, subject) pair.
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Email: "alice@corp.example", Roles: []string{"editor"}}, true)
	audit := &fakeAuthAudit{}
	srv := oidcServer(t, f, cfg, store, audit, nil)

	rec := driveCallback(t, srv, f, cfg, nil)

	assertLoginSucceeded(t, rec, "callback")
	if loc := rec.Header().Get("Location"); loc != "/dags" {
		t.Errorf("redirect = %q, want /dags (sanitized next)", loc)
	}
	ck := sessionCookie(rec)
	if ck == nil {
		t.Fatal("callback did not set the _token session cookie")
	}
	if !ck.HttpOnly || !ck.Secure {
		t.Errorf("session cookie must be HttpOnly+Secure; got HttpOnly=%v Secure=%v", ck.HttpOnly, ck.Secure)
	}
	// D5: the minted token carries the group-mapped role.
	if roles := tokenRoles(t, ck.Value); len(roles) != 1 || roles[0] != "editor" {
		t.Errorf("token roles = %v, want [editor] (mapped from group data-eng)", roles)
	}
	if !audit.has(auditOIDCLoginSuccess, "success") {
		t.Error("login success was not audited")
	}
}

// A returning user whose STORED tenant differs from the claim-derived tenant must
// be rejected — never mint a session against a tenant the stored row does not
// belong to. Not reachable under a pinned single-tenant issuer, but an explicit
// reject is defense-in-depth (the returning path previously trusted the
// claim-derived tenant and discarded the stored one).
func TestOIDCReturningUserTenantMismatchRejected(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "other-tenant", Email: "alice@corp.example", Roles: []string{"editor"}}, true)
	audit := &fakeAuthAudit{}
	srv := oidcServer(t, f, cfg, store, audit, nil)

	rec := driveCallback(t, srv, f, cfg, nil)

	// This read a bare 302 as "a session was minted". A refused login is a 302
	// too now, so the inference no longer holds and the helper states the shape.
	assertLoginDenied(t, rec, "tenant-mismatched returning user")
	if !audit.hasReason(auditOIDCLoginFailure, "tenant_mismatch") {
		t.Error("tenant mismatch was not audited with reason=tenant_mismatch")
	}
}

// The OIDC login endpoint is rate-limited per client IP (its own limiter, so it
// never contaminates the /auth/token budget). The 31st request within the window
// is refused with 429.
func TestOIDCLoginRateLimited(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	srv := oidcServer(t, f, cfg, newFakeOIDCStore(), &fakeAuthAudit{}, nil)

	var last int
	for i := 0; i < 31; i++ {
		last = do(srv, http.MethodGet, "/api/v2/auth/oidc/login", "").Code
		if i == 0 && last == http.StatusTooManyRequests {
			t.Fatal("first OIDC login request was rate-limited")
		}
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("31st OIDC login request = %d, want 429", last)
	}
}

// ─────────────────────────── H1: tenant pin ───────────────────────────

func TestOIDCTenantPinFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(jwt.MapClaims)
	}{
		{"issuer mismatch", func(c jwt.MapClaims) { c["iss"] = "https://evil.example" }},
		{"tid not in tenant_claims", func(c jwt.MapClaims) { c["tid"] = "some-other-tenant" }},
		{"tid absent", func(c jwt.MapClaims) { delete(c, "tid") }},
		{"email_verified false", func(c jwt.MapClaims) { c["email_verified"] = false }},
		{"email_verified absent", func(c jwt.MapClaims) { delete(c, "email_verified") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIDP(t)
			cfg := baseOIDCConfig(f)
			store := newFakeOIDCStore()
			store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default"}, true)
			audit := &fakeAuthAudit{}
			srv := oidcServer(t, f, cfg, store, audit, nil)

			rec := driveCallback(t, srv, f, cfg, tc.mutate)

			assertLoginDenied(t, rec, tc.name)
			if sessionCookie(rec) != nil {
				t.Error("a rejected login must NOT mint a session cookie")
			}
			// Never a fallback to the default tenant/identity.
			if len(store.created) != 0 {
				t.Error("a rejected login must NOT provision a user")
			}
		})
	}
}

func TestOIDCTenantPinRejectionIsAudited(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	audit := &fakeAuthAudit{}
	srv := oidcServer(t, f, cfg, newFakeOIDCStore(), audit, nil)

	driveCallback(t, srv, f, cfg, func(c jwt.MapClaims) { c["tid"] = "rogue-tenant" })

	if !audit.has(auditOIDCTenantReject, "denied") {
		t.Error("tenant-pin rejection was not audited as a tenant-pin rejection")
	}
}

// ─────────────────────────── H4: token verification ───────────────────────────

func TestOIDCTokenVerificationFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(jwt.MapClaims)
	}{
		{"wrong audience", func(c jwt.MapClaims) { c["aud"] = "some-other-client" }},
		{"wrong nonce", func(c jwt.MapClaims) { c["nonce"] = "not-the-cookie-nonce" }},
		{"absent nonce", func(c jwt.MapClaims) { delete(c, "nonce") }},
		{"expired beyond skew", func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-5 * time.Minute).Unix() }},
		{"azp mismatch", func(c jwt.MapClaims) { c["azp"] = "some-other-client" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIDP(t)
			cfg := baseOIDCConfig(f)
			store := newFakeOIDCStore()
			store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default"}, true)
			srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

			rec := driveCallback(t, srv, f, cfg, tc.mutate)

			assertLoginDenied(t, rec, tc.name)
			if sessionCookie(rec) != nil {
				t.Errorf("%s: a rejected token must NOT mint a session", tc.name)
			}
		})
	}
}

func TestOIDCTamperedSignatureRejected(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default"}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	st := startLogin(t, srv)
	idToken := f.signIDToken(baseClaims(cfg, st.nonce))
	// Corrupt the signature segment.
	parts := strings.Split(idToken, ".")
	sig := []byte(parts[2])
	if sig[0] == 'A' {
		sig[0] = 'B'
	} else {
		sig[0] = 'A'
	}
	parts[2] = string(sig)
	rec := completeCallback(srv, f, st, strings.Join(parts, "."))

	assertLoginDenied(t, rec, "tampered signature")
	if sessionCookie(rec) != nil {
		t.Error("a tampered token must NOT mint a session")
	}
}

func TestOIDCWithinSkewAccepted(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Roles: []string{"editor"}}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	// Expired 30s ago, inside the 60s skew → still accepted.
	rec := driveCallback(t, srv, f, cfg, func(c jwt.MapClaims) {
		c["exp"] = time.Now().Add(-30 * time.Second).Unix()
	})
	if rec.Code != http.StatusFound {
		t.Fatalf("within-skew token = %d, want 302 (accepted)", rec.Code)
	}
}

// ─────────────────────────── state / CSRF ───────────────────────────

func TestOIDCCallbackRejectsMissingStateCookie(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	srv := oidcServer(t, f, cfg, newFakeOIDCStore(), &fakeAuthAudit{}, nil)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/auth/oidc/callback?code=x&state=y", http.NoBody)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assertLoginDenied(t, rec, "missing state cookie")
}

func TestOIDCCallbackRejectsStateMismatch(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	srv := oidcServer(t, f, cfg, newFakeOIDCStore(), &fakeAuthAudit{}, nil)

	st := startLogin(t, srv)
	// Present a different state query param than the one sealed in the cookie.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/auth/oidc/callback?code=x&state=forged-state", http.NoBody)
	req.AddCookie(st.cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assertLoginDenied(t, rec, "state mismatch")
}

func TestOIDCCallbackRejectsIssParamMismatch(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	srv := oidcServer(t, f, cfg, newFakeOIDCStore(), &fakeAuthAudit{}, nil)

	st := startLogin(t, srv)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v2/auth/oidc/callback?code=x&state="+url.QueryEscape(st.state)+"&iss=https://evil.example", http.NoBody)
	req.AddCookie(st.cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assertLoginDenied(t, rec, "RFC 9207 iss mismatch")
}

// ─────────────────────────── D4: JIT provisioning ───────────────────────────

func TestOIDCJITOffRejectsUnknownSubject(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f) // JITProvisioning defaults off
	store := newFakeOIDCStore()
	audit := &fakeAuthAudit{}
	srv := oidcServer(t, f, cfg, store, audit, nil)

	rec := driveCallback(t, srv, f, cfg, nil)

	assertLoginDenied(t, rec, "JIT-off unknown subject")
	if len(store.created) != 0 {
		t.Error("JIT off must not create a user")
	}
}

func TestOIDCJITOnCreatesUserWithMappedRoles(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.JITProvisioning = true
	store := newFakeOIDCStore()
	audit := &fakeAuthAudit{}
	srv := oidcServer(t, f, cfg, store, audit, nil)

	rec := driveCallback(t, srv, f, cfg, nil)

	assertLoginSucceeded(t, rec, "JIT-on")
	if len(store.created) != 1 {
		t.Fatalf("JIT on created %d users, want 1", len(store.created))
	}
	got := store.created[0]
	if got.email != "alice@corp.example" || got.tenant != "default" || got.subject != "subject-123" {
		t.Errorf("created = %+v, unexpected", got)
	}
	if len(got.roles) != 1 || got.roles[0] != "editor" {
		t.Errorf("created roles = %v, want [editor]", got.roles)
	}
	if !audit.has(auditOIDCJITProvision, "success") {
		t.Error("JIT provisioning was not audited")
	}
}

// ─────────────────────────── D5 + default_role ───────────────────────────

func TestOIDCUnmappedGroupGrantsNoRole(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.JITProvisioning = true
	store := newFakeOIDCStore()
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	rec := driveCallback(t, srv, f, cfg, func(c jwt.MapClaims) { c["groups"] = []string{"unmapped-group"} })
	assertLoginSucceeded(t, rec, "unmapped-group login")
	if len(store.created) != 1 || len(store.created[0].roles) != 0 {
		t.Errorf("unmapped group should grant no role (default-deny); created = %+v", store.created)
	}
}

func TestOIDCDefaultRoleFallback(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.JITProvisioning = true
	cfg.DefaultRole = "viewer"
	store := newFakeOIDCStore()
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	rec := driveCallback(t, srv, f, cfg, func(c jwt.MapClaims) { c["groups"] = []string{"unmapped-group"} })
	assertLoginSucceeded(t, rec, "default_role login")
	if len(store.created) != 1 || len(store.created[0].roles) != 1 || store.created[0].roles[0] != "viewer" {
		t.Errorf("unmapped + default_role=viewer should grant [viewer]; created = %+v", store.created)
	}
}

func TestOIDCDefaultRoleEmptyKeepsDeny(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.JITProvisioning = true
	cfg.DefaultRole = "" // secure default
	store := newFakeOIDCStore()
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	rec := driveCallback(t, srv, f, cfg, func(c jwt.MapClaims) { c["groups"] = []string{"unmapped-group"} })
	if rec.Code != http.StatusFound || len(store.created) != 1 || len(store.created[0].roles) != 0 {
		t.Errorf("empty default_role must keep default-deny; code=%d created=%+v", rec.Code, store.created)
	}
}

func TestOIDCDefaultRoleNonexistentRejected(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.JITProvisioning = true
	cfg.DefaultRole = "wizard" // not a real role
	store := newFakeOIDCStore()
	audit := &fakeAuthAudit{}
	srv := oidcServer(t, f, cfg, store, audit, nil)

	rec := driveCallback(t, srv, f, cfg, func(c jwt.MapClaims) { c["groups"] = []string{"unmapped-group"} })
	assertLoginDenied(t, rec, "nonexistent default_role")
	if len(store.created) != 0 {
		t.Error("a login with an unknown default_role must not create a user")
	}
	if !audit.has(auditOIDCLoginFailure, "denied") {
		t.Error("the rejection was not audited")
	}
}

// ─────────────────────────── allowed_email_domains ───────────────────────────

func TestOIDCEmailDomainAllowlist(t *testing.T) {
	seededUser := func(cfg config.OIDCSection) *fakeOIDCStore {
		s := newFakeOIDCStore()
		s.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Roles: []string{"editor"}}, true)
		return s
	}

	t.Run("domain in allowlist, pre-existing user → ok", func(t *testing.T) {
		f := newFakeIDP(t)
		cfg := baseOIDCConfig(f)
		cfg.AllowedEmailDomains = []string{"corp.example"}
		srv := oidcServer(t, f, cfg, seededUser(cfg), &fakeAuthAudit{}, nil)
		if rec := driveCallback(t, srv, f, cfg, nil); rec.Code != http.StatusFound {
			t.Fatalf("in-allowlist login = %d, want 302", rec.Code)
		}
	})

	t.Run("domain NOT in allowlist, pre-existing user → 403", func(t *testing.T) {
		f := newFakeIDP(t)
		cfg := baseOIDCConfig(f)
		cfg.AllowedEmailDomains = []string{"trusted.example"}
		store := seededUser(cfg)
		srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)
		rec := driveCallback(t, srv, f, cfg, nil) // email alice@corp.example
		assertLoginDenied(t, rec, "out-of-allowlist pre-existing user")
		if sessionCookie(rec) != nil {
			t.Error("out-of-allowlist login must not mint a session")
		}
	})

	t.Run("domain NOT in allowlist, JIT-on → 403, no row", func(t *testing.T) {
		f := newFakeIDP(t)
		cfg := baseOIDCConfig(f)
		cfg.JITProvisioning = true
		cfg.AllowedEmailDomains = []string{"trusted.example"}
		store := newFakeOIDCStore()
		srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)
		rec := driveCallback(t, srv, f, cfg, nil)
		assertLoginDenied(t, rec, "out-of-allowlist JIT")
		if len(store.created) != 0 {
			t.Error("out-of-allowlist login must not provision a user")
		}
	})

	t.Run("empty allowlist → no domain restriction", func(t *testing.T) {
		f := newFakeIDP(t)
		cfg := baseOIDCConfig(f) // AllowedEmailDomains empty
		srv := oidcServer(t, f, cfg, seededUser(cfg), &fakeAuthAudit{}, nil)
		if rec := driveCallback(t, srv, f, cfg, nil); rec.Code != http.StatusFound {
			t.Fatalf("empty allowlist login = %d, want 302", rec.Code)
		}
	})

	t.Run("unverified email is rejected before the domain check", func(t *testing.T) {
		f := newFakeIDP(t)
		cfg := baseOIDCConfig(f)
		cfg.AllowedEmailDomains = []string{"corp.example"} // domain WOULD pass
		srv := oidcServer(t, f, cfg, seededUser(cfg), &fakeAuthAudit{}, nil)
		rec := driveCallback(t, srv, f, cfg, func(c jwt.MapClaims) { c["email_verified"] = false })
		assertLoginDenied(t, rec, "unverified email fails at email_verified, before the domain check")
	})
}
