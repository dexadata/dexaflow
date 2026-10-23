package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/api/apitest"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/oidc"
)

// ssoIDP is a minimal OpenID Provider for the CLI tests. Its authorization
// endpoint signs the user in at once and redirects back with a code, which is
// what a person clicking through the IdP amounts to.
type ssoIDP struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]idpCode
	// deny makes the authorization endpoint answer error=access_denied.
	deny bool
}

type idpCode struct{ challenge, idToken string }

func newSSOIDP(t *testing.T) *ssoIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &ssoIDP{key: key, codes: map[string]idpCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, map[string]any{
			"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks",
			"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/authorize", f.authorize)
	mux.HandleFunc("/token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *ssoIDP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	out := url.Values{"state": {q.Get("state")}}
	if f.deny {
		out.Set("error", "access_denied")
	} else {
		now := time.Now()
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": f.srv.URL, "aud": "cli-test-client", "sub": "subject-42",
			"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": q.Get("nonce"),
			"email": "alice@corp.example", "email_verified": true, "tid": "tenant-guid",
			"groups": []string{"data-eng"},
		})
		tok.Header["kid"] = "k1"
		signed, serr := tok.SignedString(f.key)
		if serr != nil {
			http.Error(w, serr.Error(), http.StatusInternalServerError)
			return
		}
		code := "idp-code-" + q.Get("state")[:8]
		f.mu.Lock()
		f.codes[code] = idpCode{challenge: q.Get("code_challenge"), idToken: signed}
		f.mu.Unlock()
		out.Set("code", code)
	}
	back.RawQuery = out.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (f *ssoIDP) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	c, ok := f.codes[r.PostForm.Get("code")]
	f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
		http.Error(w, "invalid_grant", http.StatusBadRequest)
		return
	}
	writeTestJSON(w, map[string]any{"access_token": "idp-access", "token_type": "Bearer", "id_token": c.idToken})
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ssoUsers is the user store behind the test control plane: one OIDC user,
// provisioned on first sign-in.
type ssoUsers struct {
	mu   sync.Mutex
	user *auth.User
}

func (s *ssoUsers) FindUserByOIDCSubject(context.Context, string, string) (*auth.User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.user == nil {
		return nil, false, auth.ErrUserNotFound
	}
	return s.user, true, nil
}

func (s *ssoUsers) CreateOIDCUser(_ context.Context, tenant, email, _, _ string, roles []string) (*auth.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.user = &auth.User{ID: "user-42", TenantID: tenant, Email: email, Roles: roles}
	return s.user, nil
}

func (s *ssoUsers) RoleExists(context.Context, string, string) (bool, error) { return true, nil }

func (s *ssoUsers) ReconcileUserRoles(context.Context, string, []string) error { return nil }

const ssoTestSecret = "cli-sso-test-secret"

// newSSOControlPlane serves the real API server with single sign-on against
// idp, the same wiring dexaflow-server uses.
func newSSOControlPlane(t *testing.T, idp *ssoIDP) *httptest.Server {
	t.Helper()
	return newSSOControlPlaneWith(t, idp, nil)
}

// newSSOControlPlaneWith is newSSOControlPlane serving the device sign-in
// from devices when it is not nil.
func newSSOControlPlaneWith(t *testing.T, idp *ssoIDP, devices api.CLIDeviceStore) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	base := "http://" + srv.Listener.Addr().String()
	cfg := config.OIDCSection{
		Issuer: idp.srv.URL, ClientID: "cli-test-client", ClientSecret: "s3cret",
		RedirectURL:  base + "/api/v2/auth/oidc/callback",
		Scopes:       []string{"openid", "email"},
		GroupsClaim:  "groups",
		RoleMappings: map[string]string{"data-eng": "editor"},
		TenantClaim:  "tid", TenantClaims: map[string]string{"tenant-guid": "default"},
		JITProvisioning: true, ClockSkewSeconds: 60,
	}
	flow, err := oidc.NewFlow(context.Background(), cfg, ssoTestSecret)
	if err != nil {
		t.Fatalf("NewFlow: %v", err)
	}
	srv.Config.Handler = apitest.NewServer(api.Dependencies{
		Observability: api.ObservabilityDeps{
			Logger:       discardTestLogger(),
			HealthChecks: map[string]api.HealthChecker{},
		},
		Auth: api.AuthDeps{
			Authenticator:         auth.NewJWTAuthenticator(nil, ssoTestSecret, time.Hour),
			RateLimiter:           auth.NewRateLimiter(20, time.Minute),
			TokenTTLSecs:          3600,
			OIDCFlow:              flow,
			OIDCEnabled:           true,
			OIDCSettings:          cfg,
			OIDCUsers:             &ssoUsers{},
			JWTSecret:             ssoTestSecret,
			SessionCookieInsecure: true,
			CLIDevices:            devices,
		},
	})
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// browserVisit is what a person's browser does with the sign-in URL: follow
// every redirect, keeping cookies, until a page answers.
func browserVisit(t *testing.T, target string) {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Error(err)
		return
	}
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Error(err)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Errorf("browser: %v", err)
		return
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	// The person clicks "Sign in the CLI" on the consent page.
	if m := consentCSRF.FindSubmatch(body); m != nil {
		form := url.Values{"csrf": {string(m[1])}, "decision": {"approve"}}
		post, perr := http.NewRequestWithContext(context.Background(), http.MethodPost,
			resp.Request.URL.ResolveReference(&url.URL{Path: "/api/v2/auth/cli/consent"}).String(), strings.NewReader(form.Encode()))
		if perr != nil {
			t.Error(perr)
			return
		}
		post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		post.Header.Set("Sec-Fetch-Site", "same-origin")
		presp, derr := client.Do(post)
		if derr != nil {
			t.Errorf("browser: consent: %v", derr)
			return
		}
		_, _ = io.Copy(io.Discard, presp.Body)
		_ = presp.Body.Close()
	}
}

var consentCSRF = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// fakeBrowser replaces the system browser of the commands run under d and
// records the URLs it was asked to open. visit runs in the background, as a
// real browser does, and defaults to browserVisit.
func fakeBrowser(t *testing.T, d *cliDeps, visit func(string)) *[]string {
	t.Helper()
	var mu sync.Mutex
	opened := &[]string{}
	d.openBrowser = func(u string) error {
		mu.Lock()
		*opened = append(*opened, u)
		mu.Unlock()
		if visit == nil {
			go browserVisit(t, u)
		} else {
			go visit(u)
		}
		return nil
	}
	return opened
}

func savedToken(t *testing.T, cfgPath, server string) string {
	t.Helper()
	store, err := config.ReadUserStore(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	tok, _ := store.Session(server)
	return tok
}

func TestLoginSSOBrowserFlowSavesTheServerToken(t *testing.T) {
	d := testDeps()
	t.Setenv("LEOFLOW_USERNAME", "")
	t.Setenv("LEOFLOW_PASSWORD", "")
	idp := newSSOIDP(t)
	cp := newSSOControlPlane(t, idp)
	opened := fakeBrowser(t, d, nil)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	out, _, err := runDeps(t, d, "auth", "login", "--server", cp.URL, "--config", cfgPath)
	if err != nil {
		t.Fatalf("login: %v\n%s", err, out)
	}
	if len(*opened) != 1 || !strings.HasPrefix((*opened)[0], cp.URL+"/api/v2/auth/oidc/login?") {
		t.Fatalf("browser opened %v, want the server's sign-in", *opened)
	}
	if !strings.Contains(out, (*opened)[0]) {
		t.Errorf("output does not print the sign-in URL for when no browser opens:\n%s", out)
	}
	tok := savedToken(t, cfgPath, cp.URL)
	if tok == "" {
		t.Fatalf("no token saved for %s:\n%s", cp.URL, out)
	}
	var claims jwt.MapClaims
	if _, perr := jwt.ParseWithClaims(tok, &claims, func(*jwt.Token) (any, error) { return []byte(ssoTestSecret), nil }); perr != nil {
		t.Fatalf("the saved token is not a Dexaflow token: %v", perr)
	}
	if claims["sub"] != "user-42" || claims["tenant_id"] != "default" {
		t.Errorf("token claims = %v, want the SSO user in tenant default", claims)
	}
	if cfg, lerr := config.Load(cfgPath, nil); lerr != nil || cfg.Token != tok || cfg.ServerURL != cp.URL {
		t.Errorf("single-server session = %q/%q (%v), want the SSO token saved like a password login", cfg.ServerURL, cfg.Token, lerr)
	}
	if !strings.Contains(strings.ToLower(out), "logged in") {
		t.Errorf("output = %q, want a 'logged in' confirmation", out)
	}
}

// urlWatcher is a command output that hands the first sign-in URL it sees to
// visit, as a person reading the terminal would.
type urlWatcher struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	seen  bool
	visit func(string)
}

var signInURL = regexp.MustCompile(`http://\S+/api/v2/auth/oidc/login\?\S+`)

func (w *urlWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	if m := signInURL.FindString(w.buf.String()); m != "" && !w.seen {
		w.seen = true
		go w.visit(m)
	}
	return n, err
}

func (w *urlWatcher) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestLoginSSONoBrowserPrintsTheURL(t *testing.T) {
	d := testDeps()
	idp := newSSOIDP(t)
	cp := newSSOControlPlane(t, idp)
	opened := fakeBrowser(t, d, func(string) {})
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out := &urlWatcher{visit: func(u string) { browserVisit(t, u) }}

	root := NewRootCommand()
	root.SetOut(out)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"auth", "login", "--sso", "--no-browser", "--server", cp.URL, "--config", cfgPath})
	if err := root.ExecuteContext(testCtx(d)); err != nil {
		t.Fatalf("login: %v\n%s", err, out.String())
	}
	if len(*opened) != 0 {
		t.Errorf("--no-browser opened a browser: %v", *opened)
	}
	if savedToken(t, cfgPath, cp.URL) == "" {
		t.Fatalf("no token saved:\n%s", out.String())
	}
}

// fakeCLIServer is a control plane that offers SSO and answers the code
// exchange with exchangeStatus, for the cases the real server only reaches
// with time or tampering.
func fakeCLIServer(t *testing.T, exchangeStatus int) (srv *httptest.Server, exchanges *int) {
	t.Helper()
	exchanges = new(int)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/methods", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, map[string]bool{"sso": true, "password": false})
	})
	mux.HandleFunc("/api/v2/auth/cli/token", func(w http.ResponseWriter, _ *http.Request) {
		*exchanges++
		w.WriteHeader(exchangeStatus)
		if exchangeStatus == http.StatusOK {
			_, _ = io.WriteString(w, `{"access_token":"jwt-sso","token_type":"bearer","expires_in":3600}`)
		}
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, exchanges
}

// redirectWith is a browser that skips the IdP and lands straight on the
// CLI's loopback with the given query, state included unless overridden.
func redirectWith(t *testing.T, q url.Values) func(string) {
	return func(signIn string) {
		u, err := url.Parse(signIn)
		if err != nil {
			t.Error(err)
			return
		}
		back := u.Query().Get("redirect_uri")
		if q.Get("state") == "" {
			q.Set("state", u.Query().Get("state"))
		}
		browserVisit(t, back+"?"+q.Encode())
	}
}

// A redirect with another state is not the sign-in the CLI waits for: it is
// answered 400 and ignored, and the genuine one that follows still signs in.
func TestLoginSSOIgnoresAWrongStateAndKeepsWaiting(t *testing.T) {
	d := testDeps()
	srv, exchanges := fakeCLIServer(t, http.StatusOK)
	fakeBrowser(t, d, func(signIn string) {
		u, err := url.Parse(signIn)
		if err != nil {
			t.Error(err)
			return
		}
		back := u.Query().Get("redirect_uri")
		forged := httpGetStatus(t, back+"?"+url.Values{"code": {"forged"}, "state": {"forged-state-0123456789"}}.Encode())
		if forged != http.StatusBadRequest {
			t.Errorf("forged redirect = %d, want 400", forged)
		}
		aborting := httpGetStatus(t, back+"?"+url.Values{"error": {"access_denied"}, "state": {"forged-state-0123456789"}}.Encode())
		if aborting != http.StatusBadRequest {
			t.Errorf("forged error redirect = %d, want 400", aborting)
		}
		browserVisit(t, back+"?"+url.Values{"code": {"genuine"}, "state": {u.Query().Get("state")}}.Encode())
	})
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	if _, _, err := runDeps(t, d, "auth", "login", "--server", srv.URL, "--config", cfgPath, "--timeout", "10s"); err != nil {
		t.Fatalf("login after a forged redirect: %v", err)
	}
	if *exchanges != 1 || savedToken(t, cfgPath, srv.URL) != "jwt-sso" {
		t.Errorf("exchanges = %d, token %q; want the genuine code exchanged once", *exchanges, savedToken(t, cfgPath, srv.URL))
	}
}

func httpGetStatus(t *testing.T, target string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Error(err)
		return 0
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Error(err)
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestLoginSSOWithOnlyAForgedStateNeverExchanges(t *testing.T) {
	d := testDeps()
	srv, exchanges := fakeCLIServer(t, http.StatusOK)
	fakeBrowser(t, d, redirectWith(t, url.Values{"code": {"c"}, "state": {"forged-state-0123456789"}}))
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	_, _, err := runDeps(t, d, "auth", "login", "--server", srv.URL, "--config", cfgPath, "--timeout", "500ms")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("login with only a forged state: err = %v, want it to keep waiting until the timeout", err)
	}
	if *exchanges != 0 || savedToken(t, cfgPath, srv.URL) != "" {
		t.Error("the CLI exchanged or saved something from a forged redirect")
	}
}

func TestLoginSSOReportsARefusedCode(t *testing.T) {
	d := testDeps()
	srv, exchanges := fakeCLIServer(t, http.StatusBadRequest)
	fakeBrowser(t, d, redirectWith(t, url.Values{"code": {"expired-code"}}))
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	_, _, err := runDeps(t, d, "auth", "login", "--server", srv.URL, "--config", cfgPath)
	if err == nil || !strings.Contains(err.Error(), "refused the sign-in code") {
		t.Fatalf("login with an expired code: err = %v, want the refusal", err)
	}
	if *exchanges != 1 {
		t.Errorf("exchanges = %d, want 1", *exchanges)
	}
	if savedToken(t, cfgPath, srv.URL) != "" {
		t.Error("a token was saved")
	}
}

func TestLoginSSOReportsARefusedSignIn(t *testing.T) {
	d := testDeps()
	idp := newSSOIDP(t)
	idp.deny = true
	cp := newSSOControlPlane(t, idp)
	fakeBrowser(t, d, nil)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	_, _, err := runDeps(t, d, "auth", "login", "--server", cp.URL, "--config", cfgPath)
	if err == nil || !strings.Contains(err.Error(), "single sign-on was refused") {
		t.Fatalf("login refused at the IdP: err = %v, want the refusal reported", err)
	}
	if savedToken(t, cfgPath, cp.URL) != "" {
		t.Error("a token was saved")
	}
}

func TestLoginSSOTimesOut(t *testing.T) {
	d := testDeps()
	srv, _ := fakeCLIServer(t, http.StatusOK)
	fakeBrowser(t, d, func(string) {})
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	_, _, err := runDeps(t, d, "auth", "login", "--server", srv.URL, "--config", cfgPath, "--timeout", "200ms")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

// A username or password means a password login, even on a server that
// offers single sign-on (the break-glass path).
func TestLoginWithCredentialsUsesPasswordEvenWithSSO(t *testing.T) {
	d := testDeps()
	var methodsRead bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/methods", func(w http.ResponseWriter, _ *http.Request) {
		methodsRead = true
		writeTestJSON(w, map[string]bool{"sso": true, "password": true})
	})
	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"jwt-password"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	opened := fakeBrowser(t, d, func(string) {})
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	if _, _, err := runDeps(t, d, "auth", "login", "--server", srv.URL, "--username", "root@corp.example", "--password", "pw", "--config", cfgPath, "--timeout", "2s"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if len(*opened) != 0 || methodsRead {
		t.Errorf("a password login opened a browser (%v) or asked for the methods (%v)", *opened, methodsRead)
	}
	if got := savedToken(t, cfgPath, srv.URL); got != "jwt-password" {
		t.Errorf("token = %q, want jwt-password", got)
	}
}

// A server that predates the methods route, or does not offer single
// sign-on, keeps the password login with its prompts and errors.
func TestLoginFallsBackToPasswordWithoutSSO(t *testing.T) {
	d := testDeps()
	t.Setenv("LEOFLOW_USERNAME", "")
	t.Setenv("LEOFLOW_PASSWORD", "")
	for name, methods := range map[string]http.HandlerFunc{
		"older server": func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) },
		"no sso": func(w http.ResponseWriter, _ *http.Request) {
			writeTestJSON(w, map[string]bool{"sso": false, "password": true})
		},
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v2/auth/methods", methods)
		srv := httptest.NewServer(mux)
		opened := fakeBrowser(t, d, func(string) {})
		_, _, err := runDeps(t, d, "auth", "login", "--server", srv.URL, "--config", filepath.Join(t.TempDir(), "c.yaml"), "--timeout", "2s")
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "username required") {
			t.Errorf("%s: err = %v, want the password login's missing-username error", name, err)
		}
		if len(*opened) != 0 {
			t.Errorf("%s: a browser was opened", name)
		}
	}
}

func TestLoginSSOFlagNeedsAServerThatOffersIt(t *testing.T) {
	d := testDeps()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/methods", func(w http.ResponseWriter, _ *http.Request) {
		writeTestJSON(w, map[string]bool{"sso": false, "password": true})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	fakeBrowser(t, d, func(string) {})
	_, _, err := runDeps(t, d, "auth", "login", "--sso", "--server", srv.URL, "--config", filepath.Join(t.TempDir(), "c.yaml"), "--timeout", "2s")
	if err == nil || !strings.Contains(err.Error(), "does not offer single sign-on") {
		t.Fatalf("err = %v, want a clear refusal", err)
	}
}

func TestLoginSSOFlagConflictsWithPasswordFlags(t *testing.T) {
	_, _, err := run(t, "auth", "login", "--sso", "--password", "pw", "--server", "http://127.0.0.1:1", "--config", filepath.Join(t.TempDir(), "c.yaml"))
	if err == nil || !strings.Contains(err.Error(), "--sso") {
		t.Fatalf("err = %v, want --sso and the password flags refused together", err)
	}
}

func discardTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// The loopback takes one redirect: a second one, from a replayed or forged
// link, is refused and never replaces the first result.
func TestLoopbackAcceptsOneRedirect(t *testing.T) {
	t.Parallel()
	results := make(chan loopbackResult, 1)
	h := loopbackHandler("the-state-0123456789", results)
	get := func(q string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, ssoCallbackPath+"?"+q, http.NoBody))
		return rec.Code
	}
	if code := get("state=the-state-0123456789&code=first"); code != http.StatusOK {
		t.Fatalf("first redirect = %d, want 200", code)
	}
	if code := get("state=the-state-0123456789&code=second"); code != http.StatusBadRequest {
		t.Fatalf("second redirect = %d, want 400", code)
	}
	if r := <-results; r.code != "first" {
		t.Errorf("result = %+v, want the first code", r)
	}
	select {
	case r := <-results:
		t.Errorf("a second result was delivered: %+v", r)
	default:
	}
}

// Concurrent redirects with the right state complete the login once: one is
// answered as the sign-in, the others as already completed, and nothing
// blocks or panics.
func TestLoopbackCompletesOnceUnderConcurrentRedirects(t *testing.T) {
	t.Parallel()
	results := make(chan loopbackResult, 1)
	h := loopbackHandler("the-state-0123456789", results)
	const n = 32
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet,
				ssoCallbackPath+"?state=the-state-0123456789&code=c"+strconv.Itoa(i), http.NoBody))
			codes <- rec.Code
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent redirects blocked")
	}
	close(codes)
	ok := 0
	for c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	if ok != 1 || len(results) != 1 {
		t.Errorf("%d answered as the sign-in, %d results; want exactly one", ok, len(results))
	}
}

func TestLoginSaysWhichMethodAndWhy(t *testing.T) {
	d := testDeps()
	idp := newSSOIDP(t)
	cp := newSSOControlPlane(t, idp)
	fakeBrowser(t, d, nil)
	out, _, err := runDeps(t, d, "auth", "login", "--server", cp.URL, "--config", filepath.Join(t.TempDir(), "c.yaml"))
	if err != nil || !strings.Contains(out, "Signing in with single sign-on (the server offers it") {
		t.Errorf("SSO login said %q (%v), want the method and why", out, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"jwt-password"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	out, _, err = runDeps(t, d, "auth", "login", "--server", srv.URL, "--username", "root@corp.example", "--password", "pw", "--config", filepath.Join(t.TempDir(), "c.yaml"))
	if err != nil || !strings.Contains(out, "Signing in with a password") || !strings.Contains(out, "break-glass") {
		t.Errorf("password login said %q (%v), want the method, why, and the break-glass note", out, err)
	}
}

func TestLoginRefusesAPasswordOverPlainHTTPToAnotherHost(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "c.yaml")
	_, _, err := run(t, "auth", "login", "--server", "http://dexaflow.example.invalid:8080", "--username", "a@b.c", "--password", "pw", "--config", cfgPath)
	if err == nil || !strings.Contains(err.Error(), "--allow-insecure-password") {
		t.Fatalf("err = %v, want a refusal naming --allow-insecure-password", err)
	}
	_, _, err = run(t, "auth", "login", "--server", "http://dexaflow.example.invalid:8080", "--username", "a@b.c", "--password", "pw", "--allow-insecure-password", "--config", cfgPath)
	if err == nil || strings.Contains(err.Error(), "--allow-insecure-password") {
		t.Fatalf("with the flag: err = %v, want the request attempted (and failing on the unknown host)", err)
	}
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]", "dexaflow.localhost"} {
		if err := checkPasswordTransport("http://"+host+":8080", false); err != nil {
			t.Errorf("%s: %v, want a loopback server allowed", host, err)
		}
	}
	if err := checkPasswordTransport("https://dexaflow.example.invalid", false); err != nil {
		t.Errorf("https: %v", err)
	}
}

func TestSystemOpenBrowserRefusesNonWebURLs(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"file:///etc/passwd", `C:\Windows\System32\calc.exe`, "javascript:alert(1)", "http:///nohost"} {
		if isWebURL(target) {
			t.Errorf("isWebURL(%q) = true, want a refusal", target)
		}
		if err := systemOpenBrowser(target); err == nil {
			t.Errorf("systemOpenBrowser(%q) = nil, want a refusal", target)
		}
	}
	if !isWebURL("https://flow.example/api/v2/auth/oidc/login?x=1") {
		t.Error("isWebURL refused a sign-in URL")
	}
}
