package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/oidc"
)

// testCLIRedirect is where a CLI under test listens. Nothing listens there:
// the tests read the server's redirect instead of following it.
const testCLIRedirect = "http://127.0.0.1:43123/callback"

// cliPKCE is the CLI's half of the PKCE pair. Only the verifier proves
// possession; the challenge travels through the browser.
type cliPKCE struct{ verifier, challenge string }

func newCLIPKCE() cliPKCE {
	v := oauth2.GenerateVerifier()
	return cliPKCE{verifier: v, challenge: oauth2.S256ChallengeFromVerifier(v)}
}

func cliLoginQuery(redirect, state, challenge, method string) string {
	q := url.Values{}
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", method)
	return "/api/v2/auth/oidc/login?" + q.Encode()
}

// startCLILogin starts a CLI-bound login the way the CLI's browser does and
// returns the IdP-side state, as startLogin does for a browser login.
func startCLILogin(t *testing.T, srv *gin.Engine, cliState string, pk cliPKCE) loginState {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		cliLoginQuery(testCLIRedirect, cliState, pk.challenge, "S256"), http.NoBody)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("CLI login = %d %s, want a %d redirect to the IdP", rec.Code, rec.Body.String(), http.StatusFound)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	q := loc.Query()
	if q.Get("code_challenge") == pk.challenge {
		t.Error("the server sent the CLI's challenge to the IdP; it must use its own PKCE pair there")
	}
	var cookie *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == oidcStateCookie {
			cookie = ck
		}
	}
	if cookie == nil {
		t.Fatal("CLI login did not set the state cookie")
	}
	return loginState{state: q.Get("state"), nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), cookie: cookie}
}

// loopbackResult parses the callback's redirect to the CLI.
func loopbackResult(t *testing.T, rec *httptest.ResponseRecorder) url.Values {
	t.Helper()
	if rec.Code != http.StatusFound && rec.Code != http.StatusSeeOther {
		t.Fatalf("answer = %d %s, want a redirect to the CLI", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, testCLIRedirect+"?") {
		t.Fatalf("callback redirected to %q, want the CLI's loopback %s", loc, testCLIRedirect)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	return u.Query()
}

func exchangeCLICode(srv http.Handler, code, verifier, redirect string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"code": code, "code_verifier": verifier, "redirect_uri": redirect})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, cliTokenPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func accessToken(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var resp tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if resp.AccessToken == "" || resp.TokenType != "bearer" || resp.ExpiresIn < 3500 || resp.ExpiresIn > 3600 {
		t.Fatalf("token response = %+v, want the shape /auth/token answers", resp)
	}
	return resp.AccessToken
}

// driveCLILogin runs a CLI-bound login through the callback and returns the
// one-time code the CLI receives.
func driveCLILogin(t *testing.T, srv *gin.Engine, f *fakeIDP, cfg config.OIDCSection, pk cliPKCE) string {
	t.Helper()
	st := startCLILogin(t, srv, "cli-state-0123456789", pk)
	q := loopbackResult(t, approveConsent(t, srv, completeCallback(srv, f, st, f.signIDToken(baseClaims(cfg, st.nonce)))))
	if q.Get("state") != "cli-state-0123456789" {
		t.Fatalf("loopback state = %q, want the CLI's own state echoed", q.Get("state"))
	}
	if q.Get("code") == "" {
		t.Fatal("loopback redirect carries no code")
	}
	return q.Get("code")
}

func TestCLILoginEndsWithCodeAndExchangesForToken(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(f.issuer, "subject-123", &auth.User{ID: "u-1", TenantID: "default", Email: "alice@corp.example"}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)
	pk := newCLIPKCE()

	st := startCLILogin(t, srv, "cli-state-0123456789", pk)
	rec := completeCallback(srv, f, st, f.signIDToken(baseClaims(cfg, st.nonce)))
	if ck := sessionCookie(rec); ck != nil {
		t.Errorf("a CLI login set the browser session cookie %q; the token belongs to the CLI only", ck.Name)
	}
	code := loopbackResult(t, approveConsent(t, srv, rec)).Get("code")
	token := accessToken(t, exchangeCLICode(srv, code, pk.verifier, testCLIRedirect))
	if got := tokenRoles(t, token); len(got) != 1 || got[0] != "editor" {
		t.Errorf("roles = %v, want [editor] from the role mapping", got)
	}
}

// The CLI token is minted from the same resolved user the UI login mints its
// session from, so tenant, subject and roles are identical by construction.
func TestCLILoginTenantAndRolesMatchUILogin(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.RoleMappings = map[string]string{"data-eng": "editor", "ops": "operator"}
	cfg.JITProvisioning = true
	store := newFakeOIDCStore()
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)
	mutate := func(c jwt.MapClaims) { c["groups"] = []string{"ops", "data-eng", "unmapped"} }

	ui := sessionCookie(driveCallback(t, srv, f, cfg, mutate))
	if ui == nil {
		t.Fatal("UI login minted no session")
	}
	pk := newCLIPKCE()
	st := startCLILogin(t, srv, "cli-state-0123456789", pk)
	claims := baseClaims(cfg, st.nonce)
	mutate(claims)
	code := loopbackResult(t, approveConsent(t, srv, completeCallback(srv, f, st, f.signIDToken(claims)))).Get("code")
	cli := accessToken(t, exchangeCLICode(srv, code, pk.verifier, testCLIRedirect))

	uiClaims, cliClaims := parseClaims(t, ui.Value), parseClaims(t, cli)
	for _, k := range []string{"sub", "tenant_id", "email"} {
		if uiClaims[k] != cliClaims[k] {
			t.Errorf("claim %s: CLI %v, UI %v", k, cliClaims[k], uiClaims[k])
		}
	}
	if u, c := tokenRoles(t, ui.Value), tokenRoles(t, cli); strings.Join(u, ",") != strings.Join(c, ",") {
		t.Errorf("roles: CLI %v, UI %v", c, u)
	}
	if roles, ok := store.lastReconcile(cliClaims["sub"].(string)); !ok || strings.Join(roles, ",") != "operator,editor" {
		t.Errorf("CLI login reconciled roles %v (%v), want the IdP-mapped set like the UI", roles, ok)
	}
}

func parseClaims(t *testing.T, token string) jwt.MapClaims {
	t.Helper()
	var claims jwt.MapClaims
	if _, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) {
		return []byte(testHS256Secret), nil
	}); err != nil {
		t.Fatalf("parse token: %v", err)
	}
	return claims
}

func TestCLILoginRefusesRedirectOffLoopback(t *testing.T) {
	f := newFakeIDP(t)
	srv := oidcServer(t, f, baseOIDCConfig(f), newFakeOIDCStore(), &fakeAuthAudit{}, nil)
	pk := newCLIPKCE()
	for _, redirect := range []string{
		"http://evil.example:8080/callback",
		"https://127.0.0.1:43123/callback",
		"http://localhost:43123/callback",
		"http://127.0.0.1/callback",
		"http://127.0.0.1:0/callback",
		"http://127.0.0.1:99999/callback",
		"http://user@127.0.0.1:43123/callback",
		"http://127.0.0.1:43123/callback?x=1",
		"http://127.0.0.1:43123/callback#frag",
		"http://127.0.0.1.evil.example:43123/callback",
		"http://10.0.0.1:43123/callback",
		"http://[2001:db8::1]:43123/callback",
		"http://[::ffff:127.0.0.1]:43123/callback",
		"http://127.0.0.1:43123/callback?",
		"http://127.0.0.1:43123/callback#",
		"http://127.0.0.1:43123/call%20back",
		"http://127.0.0.1:43123/a/../callback",
		"http://127.0.0.1:43123/cb.html",
		"http://127.0.0.1:043123/callback",
		"HTTP://127.0.0.1:43123/callback",
		"http://127.0.0.1:43123",
		"http://127.0.0.1:43123/cb\\x",
		"http://:@127.0.0.1:43123/callback",
		"//127.0.0.1:43123/callback",
		"",
	} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
			cliLoginQuery(redirect, "cli-state-0123456789", pk.challenge, "S256"), http.NoBody)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("redirect_uri %q: status %d, want 400", redirect, rec.Code)
		}
		if strings.HasPrefix(rec.Header().Get("Location"), f.issuer) {
			t.Errorf("redirect_uri %q: the browser was sent to the IdP", redirect)
		}
	}
	for _, redirect := range []string{testCLIRedirect, "http://[::1]:43123/callback"} {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
			cliLoginQuery(redirect, "cli-state-0123456789", pk.challenge, "S256"), http.NoBody)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Errorf("loopback redirect_uri %q: status %d %s, want 302", redirect, rec.Code, rec.Body.String())
		}
	}
}

func TestCLILoginRequiresS256ChallengeAndState(t *testing.T) {
	f := newFakeIDP(t)
	srv := oidcServer(t, f, baseOIDCConfig(f), newFakeOIDCStore(), &fakeAuthAudit{}, nil)
	pk := newCLIPKCE()
	cases := map[string]string{
		"plain method":      cliLoginQuery(testCLIRedirect, "cli-state-0123456789", pk.challenge, "plain"),
		"no method":         cliLoginQuery(testCLIRedirect, "cli-state-0123456789", pk.challenge, ""),
		"no challenge":      cliLoginQuery(testCLIRedirect, "cli-state-0123456789", "", "S256"),
		"short challenge":   cliLoginQuery(testCLIRedirect, "cli-state-0123456789", "abc", "S256"),
		"challenge charset": cliLoginQuery(testCLIRedirect, "cli-state-0123456789", strings.Repeat("+", 43), "S256"),
		"no state":          cliLoginQuery(testCLIRedirect, "", pk.challenge, "S256"),
		"short state":       cliLoginQuery(testCLIRedirect, "abc", pk.challenge, "S256"),
	}
	for name, target := range cases {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
}

func TestCLICodeExchangeVerifiesPKCE(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(f.issuer, "subject-123", &auth.User{ID: "u-1", TenantID: "default"}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)
	pk := newCLIPKCE()
	code := driveCLILogin(t, srv, f, cfg, pk)

	if rec := exchangeCLICode(srv, code, newCLIPKCE().verifier, testCLIRedirect); rec.Code != http.StatusBadRequest {
		t.Fatalf("exchange with another verifier = %d, want 400", rec.Code)
	}
	if rec := exchangeCLICode(srv, code, pk.challenge, testCLIRedirect); rec.Code != http.StatusBadRequest {
		t.Fatalf("exchange presenting the challenge as the verifier = %d, want 400", rec.Code)
	}
	if rec := exchangeCLICode(srv, code, pk.verifier, "http://127.0.0.1:43124/callback"); rec.Code != http.StatusBadRequest {
		t.Fatalf("exchange naming another redirect_uri = %d, want 400", rec.Code)
	}
	accessToken(t, exchangeCLICode(srv, code, pk.verifier, testCLIRedirect))
}

func TestCLICodeIsSingleUse(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(f.issuer, "subject-123", &auth.User{ID: "u-1", TenantID: "default"}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)
	pk := newCLIPKCE()
	code := driveCLILogin(t, srv, f, cfg, pk)

	accessToken(t, exchangeCLICode(srv, code, pk.verifier, testCLIRedirect))
	if rec := exchangeCLICode(srv, code, pk.verifier, testCLIRedirect); rec.Code != http.StatusBadRequest {
		t.Fatalf("second exchange of the same code = %d, want 400", rec.Code)
	}
}

// The IdP-side state of a CLI login opens one code. A replayed callback (same
// state cookie, same state) is refused even when the IdP would accept its code
// again, which the fake IdP does.
func TestCLILoginStateIsSingleUse(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(f.issuer, "subject-123", &auth.User{ID: "u-1", TenantID: "default"}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)
	st := startCLILogin(t, srv, "cli-state-0123456789", newCLIPKCE())
	idToken := f.signIDToken(baseClaims(cfg, st.nonce))

	if q := loopbackResult(t, approveConsent(t, srv, completeCallback(srv, f, st, idToken))); q.Get("code") == "" {
		t.Fatal("first callback carried no code")
	}
	q := loopbackResult(t, completeCallback(srv, f, st, idToken))
	if q.Get("code") != "" {
		t.Fatal("a replayed callback produced a second code")
	}
	if q.Get("error") != "access_denied" {
		t.Errorf("replayed callback error = %q, want access_denied", q.Get("error"))
	}
}

func TestCLILoginRefusalReturnsToCLIWithoutReason(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	audit := &fakeAuthAudit{}
	srv := oidcServer(t, f, cfg, newFakeOIDCStore(), audit, nil)
	st := startCLILogin(t, srv, "cli-state-0123456789", newCLIPKCE())
	claims := baseClaims(cfg, st.nonce)
	claims["tid"] = "another-tenant"

	q := loopbackResult(t, completeCallback(srv, f, st, f.signIDToken(claims)))
	if q.Get("code") != "" {
		t.Fatal("a refused login handed the CLI a code")
	}
	if q.Get("error") != "access_denied" || q.Get("state") != "cli-state-0123456789" {
		t.Errorf("refusal = %v, want error=access_denied and the CLI state", q)
	}
	if len(q) != 2 {
		t.Errorf("refusal carries %v; the reason belongs in the audit trail, not the URL", q)
	}
	if !audit.hasReason(auditOIDCTenantReject, "tenant_not_allowed") {
		t.Error("the refusal was not audited with its reason")
	}
}

func TestCLICodeRefusalsLookAlike(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(f.issuer, "subject-123", &auth.User{ID: "u-1", TenantID: "default"}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)
	pk := newCLIPKCE()
	used := driveCLILogin(t, srv, f, cfg, pk)
	accessToken(t, exchangeCLICode(srv, used, pk.verifier, testCLIRedirect))
	pk2 := newCLIPKCE()
	fresh := driveCLILogin(t, srv, f, cfg, pk2)

	refusals := map[string]*httptest.ResponseRecorder{
		"unknown":        exchangeCLICode(srv, "not-a-code", pk.verifier, testCLIRedirect),
		"used":           exchangeCLICode(srv, used, pk.verifier, testCLIRedirect),
		"wrong verifier": exchangeCLICode(srv, fresh, pk.verifier, testCLIRedirect),
		"tampered":       exchangeCLICode(srv, fresh[:len(fresh)-2]+"AA", pk2.verifier, testCLIRedirect),
	}
	var first string
	for name, rec := range refusals {
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
		if first == "" {
			first = rec.Body.String()
		}
		if rec.Body.String() != first {
			t.Errorf("%s: body %s differs from %s; a refusal must not say which check failed", name, rec.Body.String(), first)
		}
	}
}

func TestCLICodeExpires(t *testing.T) {
	now := time.Now()
	codes := newCLICodes(testHS256Secret)
	codes.now = func() time.Time { return now }
	pk := newCLIPKCE()
	code, err := codes.sealCode(cliPending{Token: "minted-token", Redirect: testCLIRedirect, Challenge: pk.challenge})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	r := gin.New()
	r.POST(cliTokenPath, cliTokenHandler(cliTokenDeps{codes: codes, logger: discardLogger(), limiter: auth.NewRateLimiter(100, time.Minute)}))

	now = now.Add(cliCodeTTL + time.Second)
	if rec := exchangeCLICode(r, code, pk.verifier, testCLIRedirect); rec.Code != http.StatusBadRequest {
		t.Fatalf("exchange after the code's lifetime = %d, want 400", rec.Code)
	}
	now = now.Add(-2 * time.Second)
	if rec := exchangeCLICode(r, code, pk.verifier, testCLIRedirect); rec.Code != http.StatusOK {
		t.Fatalf("exchange within the lifetime = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

func TestCLICodeTTLIsShort(t *testing.T) {
	if cliCodeTTL > time.Minute {
		t.Fatalf("cliCodeTTL = %v; the CLI exchanges the code the moment it arrives", cliCodeTTL)
	}
}

func TestCLILoginLogsNoCodeOrToken(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(f.issuer, "subject-123", &auth.User{ID: "u-1", TenantID: "default"}, true)
	var logs bytes.Buffer
	flow, err := oidc.NewFlow(context.Background(), cfg, testHS256Secret)
	if err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger:       slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
			HealthChecks: map[string]HealthChecker{},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{},
			RateLimiter:   auth.NewRateLimiter(50, time.Minute),
			TokenTTLSecs:  3600,
			OIDCFlow:      flow,
			OIDCSettings:  testOIDCSettings(cfg),
			OIDCUsers:     store,
			AuthAudit:     &fakeAuthAudit{},
			JWTSecret:     testHS256Secret,
		},
	})
	pk := newCLIPKCE()
	code := driveCLILogin(t, srv, f, cfg, pk)
	exchangeCLICode(srv, code, newCLIPKCE().verifier, testCLIRedirect)
	token := accessToken(t, exchangeCLICode(srv, code, pk.verifier, testCLIRedirect))
	exchangeCLICode(srv, code, pk.verifier, testCLIRedirect)

	if !strings.Contains(logs.String(), "cli sign-in code refused") {
		t.Fatalf("the refused exchanges were not logged at all; the leak check below would prove nothing:\n%s", logs.String())
	}
	for name, secret := range map[string]string{"code": code, "token": token, "verifier": pk.verifier} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the %s is in the server log", name)
		}
	}
}

func TestCLITokenSharesTheOIDCRateLimit(t *testing.T) {
	f := newFakeIDP(t)
	srv := oidcServer(t, f, baseOIDCConfig(f), newFakeOIDCStore(), &fakeAuthAudit{}, nil)
	var last int
	for range oidcRateLimitPerMinute + 1 {
		last = exchangeCLICode(srv, "x", "y", testCLIRedirect).Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("exchange %d in a minute = %d, want 429", oidcRateLimitPerMinute+1, last)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/auth/oidc/login", http.NoBody)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("browser login after the exchange budget is spent = %d, want 429 (one budget for the OIDC routes)", rec.Code)
	}
}

func TestAuthMethodsMatchTheLoginPage(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	read := func(srv http.Handler) authMethods {
		t.Helper()
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, authMethodsPath, http.NoBody)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", authMethodsPath, rec.Code)
		}
		var m authMethods
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if got := read(testServer(&fakeAuthn{})); got != (authMethods{Password: true}) {
		t.Errorf("password-only server = %+v, want password only", got)
	}
	flow, err := oidc.NewFlow(context.Background(), cfg, testHS256Secret)
	if err != nil {
		t.Fatal(err)
	}
	sso := func(breakGlass []string) http.Handler {
		c := cfg
		c.BreakGlassEmails = breakGlass
		return newTestServer(Dependencies{
			Observability: ObservabilityDeps{
				Logger: discardLogger(),
			},
			Auth: AuthDeps{
				Authenticator: &fakeAuthn{},
				RateLimiter:   auth.NewRateLimiter(5, time.Minute),
				OIDCFlow:      flow,
				OIDCEnabled:   true,
				OIDCSettings:  testOIDCSettings(c),
				OIDCUsers:     newFakeOIDCStore(),
				JWTSecret:     testHS256Secret,
			},
		})
	}
	if got := read(sso(nil)); got != (authMethods{SSO: true}) {
		t.Errorf("SSO without break-glass = %+v, want sso only", got)
	}
	if got := read(sso([]string{"root@corp.example"})); got != (authMethods{SSO: true, Password: true}) {
		t.Errorf("SSO with break-glass = %+v, want both", got)
	}
}

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// consentOf reads the consent page a CLI login's callback answers: the
// consent cookie it set and the CSRF token in its form.
func consentOf(t *testing.T, rec *httptest.ResponseRecorder) (cookie *http.Cookie, csrf string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("callback = %d (Location %q), want the 200 consent page", rec.Code, rec.Header().Get("Location"))
	}
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == cliConsentCookie && ck.Value != "" {
			cookie = ck
		}
	}
	if cookie == nil {
		t.Fatal("the consent page set no consent cookie")
	}
	m := csrfField.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("the consent page has no csrf field:\n%s", rec.Body.String())
	}
	return cookie, m[1]
}

// postConsent posts the consent form as the browser would.
func postConsent(srv http.Handler, cookie *http.Cookie, csrf, decision, fetchSite string) *httptest.ResponseRecorder {
	form := url.Values{"decision": {decision}}
	if csrf != "" {
		form.Set("csrf", csrf)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, cliConsentPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if fetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", fetchSite)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// approveConsent clicks "Sign in the CLI" on the consent page.
func approveConsent(t *testing.T, srv http.Handler, rec *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	cookie, csrf := consentOf(t, rec)
	return postConsent(srv, cookie, csrf, "approve", "same-origin")
}

func seededCLIServer(t *testing.T, audit *fakeAuthAudit) (*gin.Engine, *fakeIDP, config.OIDCSection) {
	t.Helper()
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(f.issuer, "subject-123", &auth.User{ID: "u-1", TenantID: "default", Email: "alice@corp.example"}, true)
	return oidcServer(t, f, cfg, store, audit, nil), f, cfg
}

func TestCLILoginAsksForConsentBeforeAnyCode(t *testing.T) {
	srv, f, cfg := seededCLIServer(t, &fakeAuthAudit{})
	st := startCLILogin(t, srv, "cli-state-0123456789", newCLIPKCE())
	rec := completeCallback(srv, f, st, f.signIDToken(baseClaims(cfg, st.nonce)))

	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("the callback redirected to %q; a CLI login must stop at the consent page", loc)
	}
	body := rec.Body.String()
	for _, want := range []string{"alice@corp.example", "43123", "Sign in the CLI", "Cancel"} {
		if !strings.Contains(body, want) {
			t.Errorf("consent page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "code=") || strings.Contains(body, testCLIRedirect+"?") {
		t.Error("the consent page carries the loopback redirect or a code")
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Error("the consent page can be framed")
	}
	cookie, _ := consentOf(t, rec)
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != cliConsentCookiePath {
		t.Errorf("consent cookie = %+v, want HttpOnly, SameSite=Strict, path %s", cookie, cliConsentCookiePath)
	}
}

// A page that can only navigate the browser (GETs and redirects, no click)
// never gets the code to the loopback.
func TestCLILoginGETChainNeverReachesLoopback(t *testing.T) {
	srv, f, cfg := seededCLIServer(t, &fakeAuthAudit{})
	st := startCLILogin(t, srv, "cli-state-0123456789", newCLIPKCE())
	rec := completeCallback(srv, f, st, f.signIDToken(baseClaims(cfg, st.nonce)))
	cookie, csrf := consentOf(t, rec)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		cliConsentPath+"?decision=approve&csrf="+url.QueryEscape(csrf), http.NoBody)
	req.AddCookie(cookie)
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, req)
	if strings.HasPrefix(got.Header().Get("Location"), testCLIRedirect) {
		t.Fatalf("a GET of the consent route reached the loopback: %q", got.Header().Get("Location"))
	}
}

// forgedToken returns token with its first character replaced by a
// different one, so the forgery never equals the real token.
func forgedToken(token string) string {
	first := byte('a')
	if token[0] == first {
		first = 'b'
	}
	return string(first) + token[1:]
}

func TestCLIConsentRefusesAForgedPost(t *testing.T) {
	srv, f, cfg := seededCLIServer(t, &fakeAuthAudit{})
	st := startCLILogin(t, srv, "cli-state-0123456789", newCLIPKCE())
	cookie, csrf := consentOf(t, completeCallback(srv, f, st, f.signIDToken(baseClaims(cfg, st.nonce))))

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"no csrf":    postConsent(srv, cookie, "", "approve", "same-origin"),
		"wrong csrf": postConsent(srv, cookie, forgedToken(csrf), "approve", "same-origin"),
		"no cookie":  postConsent(srv, nil, csrf, "approve", "same-origin"),
		"cross-site": postConsent(srv, cookie, csrf, "approve", "cross-site"),
		"same-site":  postConsent(srv, cookie, csrf, "approve", "same-site"),
	} {
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, want a refusal", name, rec.Code)
		}
		if strings.HasPrefix(rec.Header().Get("Location"), testCLIRedirect) {
			t.Errorf("%s: reached the loopback", name)
		}
	}
	// The genuine click still works after the forgeries.
	if q := loopbackResult(t, postConsent(srv, cookie, csrf, "approve", "same-origin")); q.Get("code") == "" {
		t.Fatal("the genuine approval carried no code")
	}
}

func TestCLIConsentIsSingleUse(t *testing.T) {
	srv, f, cfg := seededCLIServer(t, &fakeAuthAudit{})
	st := startCLILogin(t, srv, "cli-state-0123456789", newCLIPKCE())
	cookie, csrf := consentOf(t, completeCallback(srv, f, st, f.signIDToken(baseClaims(cfg, st.nonce))))
	if q := loopbackResult(t, postConsent(srv, cookie, csrf, "approve", "same-origin")); q.Get("code") == "" {
		t.Fatal("first approval carried no code")
	}
	if rec := postConsent(srv, cookie, csrf, "approve", "same-origin"); strings.HasPrefix(rec.Header().Get("Location"), testCLIRedirect) {
		t.Fatal("a replayed approval reached the loopback again")
	}
}

func TestCLIConsentDeclineTellsTheCLI(t *testing.T) {
	audit := &fakeAuthAudit{}
	srv, f, cfg := seededCLIServer(t, audit)
	st := startCLILogin(t, srv, "cli-state-0123456789", newCLIPKCE())
	cookie, csrf := consentOf(t, completeCallback(srv, f, st, f.signIDToken(baseClaims(cfg, st.nonce))))
	q := loopbackResult(t, postConsent(srv, cookie, csrf, "cancel", "same-origin"))
	if q.Get("code") != "" || q.Get("error") != "access_denied" || q.Get("state") != "cli-state-0123456789" {
		t.Fatalf("decline = %v, want error=access_denied with the CLI state and no code", q)
	}
	if !audit.hasReason(auditCLIConsent, "declined") {
		t.Error("the decline was not audited")
	}
}

// A callback that fails before the user is verified does not use up the
// state, so minting state cookies and replaying failed callbacks cannot
// fill the set of answered states.
func TestCLILoginStateIsClaimedOnlyAfterVerification(t *testing.T) {
	srv, f, cfg := seededCLIServer(t, &fakeAuthAudit{})
	st := startCLILogin(t, srv, "cli-state-0123456789", newCLIPKCE())
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v2/auth/oidc/callback?code=never-staged&state="+url.QueryEscape(st.state), http.NoBody)
	req.AddCookie(st.cookie)
	bad := httptest.NewRecorder()
	srv.ServeHTTP(bad, req)
	if q := loopbackResult(t, bad); q.Get("error") != "access_denied" {
		t.Fatalf("failed exchange = %v, want access_denied", q)
	}
	consentOf(t, completeCallback(srv, f, st, f.signIDToken(baseClaims(cfg, st.nonce))))
}

func TestCLICodeExchangeIsAudited(t *testing.T) {
	audit := &fakeAuthAudit{}
	srv, f, cfg := seededCLIServer(t, audit)
	pk := newCLIPKCE()
	code := driveCLILogin(t, srv, f, cfg, pk)
	exchangeCLICode(srv, code, newCLIPKCE().verifier, testCLIRedirect)
	accessToken(t, exchangeCLICode(srv, code, pk.verifier, testCLIRedirect))
	exchangeCLICode(srv, "garbage", pk.verifier, testCLIRedirect)

	var redeemed, wrongVerifier, unknown bool
	audit.mu.Lock()
	for _, e := range audit.events {
		if e.action != auditCLICode {
			continue
		}
		switch {
		case e.outcome == "success" && e.tenant == "default" && e.userID == "u-1":
			redeemed = true
		case e.outcome == "denied" && e.tenant == "default" && e.userID == "u-1" && e.extra["reason"] == "verifier_mismatch":
			wrongVerifier = true
		case e.outcome == "denied" && e.tenant == "" && e.extra["reason"] == "not_authentic":
			unknown = true
		}
	}
	audit.mu.Unlock()
	if !redeemed || !wrongVerifier || !unknown {
		t.Errorf("audit: redeemed=%v wrong-verifier=%v unknown=%v, want all three rows (%+v)", redeemed, wrongVerifier, unknown, audit.events)
	}
}

func TestCLICodeExpiresInIsTheTokenRemainingLifetime(t *testing.T) {
	codes := newCLICodes(testHS256Secret)
	pk := newCLIPKCE()
	token, err := auth.MintUserToken(testHS256Secret, time.Hour, auth.User{ID: "u-1", TenantID: "default"})
	if err != nil {
		t.Fatal(err)
	}
	code, err := codes.sealCode(cliPending{Token: token, TokenExpires: time.Now().Add(time.Hour).Unix(), Redirect: testCLIRedirect, Challenge: pk.challenge})
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(40 * time.Second)
	codes.now = func() time.Time { return later }
	r := gin.New()
	r.POST(cliTokenPath, cliTokenHandler(cliTokenDeps{codes: codes, logger: discardLogger(), limiter: auth.NewRateLimiter(100, time.Minute)}))
	rec := exchangeCLICode(r, code, pk.verifier, testCLIRedirect)
	var resp tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("exchange = %d %s", rec.Code, rec.Body.String())
	}
	if resp.ExpiresIn > 3561 || resp.ExpiresIn < 3555 {
		t.Errorf("expires_in = %d, want about 3560 (the hour minus the 40s already spent)", resp.ExpiresIn)
	}
}

func TestCLICodeVerifierMustBeWellFormed(t *testing.T) {
	audit := &fakeAuthAudit{}
	srv, f, cfg := seededCLIServer(t, audit)
	pk := newCLIPKCE()
	code := driveCLILogin(t, srv, f, cfg, pk)
	for _, v := range []string{"short", strings.Repeat("a", 129), pk.verifier[:42] + "+", pk.verifier[:42] + " "} {
		if rec := exchangeCLICode(srv, code, v, testCLIRedirect); rec.Code != http.StatusBadRequest {
			t.Errorf("verifier %q: status %d, want 400", v, rec.Code)
		}
	}
	malformed := 0
	audit.mu.Lock()
	for _, e := range audit.events {
		if e.action == auditCLICode && e.extra["reason"] == reasonCodeMalformed {
			malformed++
		}
	}
	audit.mu.Unlock()
	if malformed != 4 {
		t.Errorf("%d exchanges refused as malformed, want all 4 refused before the code is opened", malformed)
	}
	accessToken(t, exchangeCLICode(srv, code, pk.verifier, testCLIRedirect))
}

// A consent cookie is sealed for another purpose and is never a code.
func TestCLIConsentCannotBeRedeemedAsCode(t *testing.T) {
	srv, f, cfg := seededCLIServer(t, &fakeAuthAudit{})
	pk := newCLIPKCE()
	st := startCLILogin(t, srv, "cli-state-0123456789", pk)
	cookie, _ := consentOf(t, completeCallback(srv, f, st, f.signIDToken(baseClaims(cfg, st.nonce))))
	if rec := exchangeCLICode(srv, cookie.Value, pk.verifier, testCLIRedirect); rec.Code != http.StatusBadRequest {
		t.Fatalf("the consent cookie redeemed as a code: %d %s", rec.Code, rec.Body.String())
	}
}

func TestCLILoopbackRedirectIsCanonical(t *testing.T) {
	for _, raw := range []string{testCLIRedirect, "http://[::1]:43123/callback", "http://127.0.0.1:1/"} {
		got, err := canonicalLoopback(raw)
		if err != nil || got != raw {
			t.Errorf("canonicalLoopback(%q) = %q, %v; want it unchanged", raw, got, err)
		}
	}
}

var consentScript = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

// The approve button starts disabled (double-clickjacking), and the only
// script that can enable it is the one the CSP allows by hash.
func TestCLIConsentButtonStartsDisabledAndItsScriptIsPinned(t *testing.T) {
	srv, f, cfg := seededCLIServer(t, &fakeAuthAudit{})
	st := startCLILogin(t, srv, "cli-state-0123456789", newCLIPKCE())
	rec := completeCallback(srv, f, st, f.signIDToken(baseClaims(cfg, st.nonce)))
	body := rec.Body.String()
	if !regexp.MustCompile(`<button[^>]*value="approve"[^>]*\bdisabled\b`).MatchString(body) {
		t.Errorf("the approve button is not disabled:\n%s", body)
	}
	m := consentScript.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("the consent page has no script to enable the button")
	}
	sum := sha256.Sum256([]byte(m[1]))
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src "+hash) {
		t.Errorf("CSP %q does not allow the rendered script by its hash %s", csp, hash)
	}
	if strings.Contains(csp, "unsafe-inline'") && strings.Contains(csp, "script-src 'unsafe-inline'") {
		t.Error("the CSP allows inline script in general")
	}
	for _, want := range []string{"visibilityState", "pointermove", "keydown", "1000"} {
		if !strings.Contains(m[1], want) {
			t.Errorf("the script does not check %s", want)
		}
	}
}
