package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/issuer"
)

// deployIssuer verifies "deploy:<scopes>" as a bearer of the deploy audience
// (#1773) and "scopes:<scopes>" as a bearer of another bearer audience, both
// for the linked user of tenant acme.
type deployIssuer struct{}

func (deployIssuer) Provider() string { return "issuer:portal" }

func (deployIssuer) VerifyBearer(_ context.Context, raw string) (*issuer.Identity, error) {
	if list, ok := strings.CutPrefix(raw, "deploy:"); ok {
		return &issuer.Identity{Subject: "user-42", Email: "ana@acme.com", Tenant: "acme", Scopes: strings.Fields(list), DeployAudience: true}, nil
	}
	if list, ok := strings.CutPrefix(raw, "scopes:"); ok {
		return &issuer.Identity{Subject: "user-42", Email: "ana@acme.com", Tenant: "acme", Scopes: strings.Fields(list)}, nil
	}
	return nil, fmt.Errorf("%w: bad signature", issuer.ErrInvalidToken)
}

// engineAndAPIToken authenticates "engine" (a password login or session
// token) as an unscoped admin and "dxf_deploy" as an API token of that admin
// with the deploy scope (ADR 0074).
type engineAndAPIToken struct{}

func (engineAndAPIToken) Authenticate(_ context.Context, token string) (*auth.User, error) {
	user := linkedAdmin()
	switch token {
	case "engine":
		return user, nil
	case "dxf_deploy":
		user.Scoped = true
		user.Scopes = []string{auth.ScopeDeploy}
		return user, nil
	}
	return nil, auth.ErrInvalidToken
}

func (engineAndAPIToken) IssueToken(context.Context, auth.Credentials) (string, error) {
	return "", nil
}

func newDeployAudienceServer(t *testing.T, viaIssuer bool) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Auth: AuthDeps{
			Authenticator: engineAndAPIToken{},
			RateLimiter:   auth.NewRateLimiter(5, time.Minute),
			TokenTTLSecs:  900,
			AuthAudit:     &fakeAuthAudit{},
		},
		Issuers: IssuerDeps{
			TrustedIssuerBearer: deployIssuer{},
			TrustedIssuerUsers:  &fakeIssuerUsers{user: linkedAdmin(), active: true},
			DeployViaIssuer:     viaIssuer,
		},
		Stores: StoreDeps{
			Dags:     &fakeDagRepo{},
			DagRuns:  &fakeRunRepo{},
			Versions: &fakeVersionRepo{},
		},
	})
}

var (
	dryRunRegister = scopeCall{"dry run register", http.MethodPost, "/api/v2/dags/etl/versions/dry_run", `{}`}
	setCurrent     = scopeCall{"set current version", http.MethodPut, "/api/v2/dags/etl/currentVersion", `{"version":"v1"}`}
	deployCalls    = []scopeCall{register, dryRunRegister, setCurrent}
)

// sessionCall sends c with token as the session cookie, the way the UI does.
func sessionCall(srv http.Handler, c scopeCall, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), c.method, c.path, strings.NewReader(c.body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "_token", Value: token})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// refusedForIssuer asserts the deploy-audience refusal: 403, a detail that
// says deploys go through the trusted issuer, and the header the CLI reads.
func refusedForIssuer(t *testing.T, who string, c scopeCall, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "deploys go through the trusted issuer") {
		t.Errorf("%s: %s = %d (%s), want 403 saying deploys go through the trusted issuer", who, c.name, rec.Code, rec.Body.String())
		return
	}
	if got := rec.Header().Get(deployViaHeader); got != deployViaIssuer {
		t.Errorf("%s: %s header %s = %q, want %q", who, c.name, deployViaHeader, got, deployViaIssuer)
	}
}

// TestDeployAudienceRefusesEveryOtherCredential: with the deploy audience
// on, a password login token, a session cookie, an API token with
// dexaflow:deploy and a bearer of another audience with dexaflow:deploy are
// all refused on every deploy route, although their roles allow deploying.
func TestDeployAudienceRefusesEveryOtherCredential(t *testing.T) {
	srv := newDeployAudienceServer(t, true)
	for _, c := range deployCalls {
		refusedForIssuer(t, "password token", c, c.do(srv, "engine"))
		refusedForIssuer(t, "session", c, sessionCall(srv, c, "engine"))
		refusedForIssuer(t, "deploy API token", c, c.do(srv, "dxf_deploy"))
		refusedForIssuer(t, "other bearer audience", c, c.do(srv, "scopes:dexaflow:read dexaflow:deploy"))
	}
}

// TestDeployAudienceBearerDeploys: the issuer's bearer of the deploy
// audience with dexaflow:deploy gets past the gates of every deploy route.
func TestDeployAudienceBearerDeploys(t *testing.T) {
	srv := newDeployAudienceServer(t, true)
	for _, c := range deployCalls {
		if rec := c.do(srv, "deploy:dexaflow:deploy"); !passed(rec) {
			t.Errorf("%s with the deploy audience = %d (%s), want past the gates", c.name, rec.Code, rec.Body.String())
		}
	}
}

// TestDeployAudienceBearerStillNeedsTheScope: the audience does not stand
// in for the scope, and the role check still runs first.
func TestDeployAudienceBearerStillNeedsTheScope(t *testing.T) {
	srv := newDeployAudienceServer(t, true)
	refusedForScope(t, register, register.do(srv, "deploy:dexaflow:read"), "dexaflow:deploy")

	reader := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Auth: AuthDeps{
			Authenticator: engineAndAPIToken{},
			RateLimiter:   auth.NewRateLimiter(5, time.Minute),
			TokenTTLSecs:  900,
			AuthAudit:     &fakeAuthAudit{},
		},
		Issuers: IssuerDeps{
			TrustedIssuerBearer: deployIssuer{},
			TrustedIssuerUsers:  &fakeIssuerUsers{user: linkedReader(), active: true},
			DeployViaIssuer:     true,
		},
		Stores: StoreDeps{
			Versions: &fakeVersionRepo{},
		},
	})
	rec := register.do(reader, "deploy:dexaflow:deploy")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "deploy:dag") {
		t.Errorf("viewer with the deploy audience = %d (%s), want 403 for the missing role permission", rec.Code, rec.Body.String())
	}
}

// TestDeployAudienceBearerGetsOnlyItsScopes: a deploy-audience bearer is
// scoped like any bearer, so with only dexaflow:deploy it reads what a
// deploy reads and cannot trigger a run.
func TestDeployAudienceBearerGetsOnlyItsScopes(t *testing.T) {
	srv := newDeployAudienceServer(t, true)
	const token = "deploy:dexaflow:deploy"
	if rec := readDags.do(srv, token); rec.Code != http.StatusOK {
		t.Errorf("list dags with the deploy audience = %d (%s), want 200 (a deploy read)", rec.Code, rec.Body.String())
	}
	refusedForScope(t, triggerRun, triggerRun.do(srv, token), "dexaflow:run")
}

// TestDeployAudienceLeavesReadsAndRunControlAlone: the restriction covers
// the deploy routes only.
func TestDeployAudienceLeavesReadsAndRunControlAlone(t *testing.T) {
	srv := newDeployAudienceServer(t, true)
	for _, c := range []scopeCall{readDags, triggerRun, pauseDag} {
		if rec := c.do(srv, "engine"); !passed(rec) {
			t.Errorf("%s with a password token = %d (%s), want past the gates", c.name, rec.Code, rec.Body.String())
		}
	}
}

// TestDeployAudienceOffChangesNothing: without the setting every credential
// whose roles and scopes allow a deploy still deploys.
func TestDeployAudienceOffChangesNothing(t *testing.T) {
	srv := newDeployAudienceServer(t, false)
	for _, c := range deployCalls {
		for _, token := range []string{"engine", "dxf_deploy", "scopes:dexaflow:deploy"} {
			if rec := c.do(srv, token); !passed(rec) {
				t.Errorf("%s with %s and the setting off = %d (%s), want past the gates", c.name, token, rec.Code, rec.Body.String())
			}
		}
		if rec := sessionCall(srv, c, "engine"); !passed(rec) {
			t.Errorf("%s with a session and the setting off = %d (%s), want past the gates", c.name, rec.Code, rec.Body.String())
		}
	}
}

// TestDeployAudienceGatesDeletingADag: deleting a DAG undeploys it, so with
// the deploy audience on it goes through the issuer as well: every other
// credential is refused with the same detail and header, and the issuer's
// deploy-audience bearer with dexaflow:deploy deletes.
func TestDeployAudienceGatesDeletingADag(t *testing.T) {
	srv := newDeployAudienceServer(t, true)
	refusedForIssuer(t, "password token", deleteDag, deleteDag.do(srv, "engine"))
	refusedForIssuer(t, "session", deleteDag, sessionCall(srv, deleteDag, "engine"))
	refusedForIssuer(t, "deploy API token", deleteDag, deleteDag.do(srv, "dxf_deploy"))
	refusedForIssuer(t, "other bearer audience", deleteDag, deleteDag.do(srv, "scopes:dexaflow:read dexaflow:deploy"))
	if rec := deleteDag.do(srv, "deploy:dexaflow:deploy"); !passed(rec) {
		t.Errorf("delete dag with the deploy audience = %d (%s), want past the gates", rec.Code, rec.Body.String())
	}
	refusedForScope(t, deleteDag, deleteDag.do(srv, "deploy:dexaflow:read"), "dexaflow:deploy")
}

// TestDeployAudienceOffKeepsDeletingADagAsToday: without the setting a
// password token deletes and a scoped token is refused, as before.
func TestDeployAudienceOffKeepsDeletingADagAsToday(t *testing.T) {
	srv := newDeployAudienceServer(t, false)
	if rec := deleteDag.do(srv, "engine"); !passed(rec) {
		t.Errorf("delete dag with a password token = %d (%s), want past the gates", rec.Code, rec.Body.String())
	}
	for _, token := range []string{"dxf_deploy", "deploy:dexaflow:deploy"} {
		rec := deleteDag.do(srv, token)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), scopedRouteRefusal) {
			t.Errorf("delete dag with %s = %d (%s), want 403 for a scoped token", token, rec.Code, rec.Body.String())
		}
	}
}

// TestDeployAudienceRefusesDevNoAuth: the dev bypass principal is no issuer
// bearer, so it cannot deploy either.
func TestDeployAudienceRefusesDevNoAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Auth: AuthDeps{
			Authenticator: engineAndAPIToken{},
			RateLimiter:   auth.NewRateLimiter(5, time.Minute),
			TokenTTLSecs:  900,
			DevNoAuth:     true,
		},
		Issuers: IssuerDeps{
			DeployViaIssuer: true,
		},
		Stores: StoreDeps{
			Dags:     &fakeDagRepo{},
			Versions: &fakeVersionRepo{},
		},
	})
	for _, c := range append([]scopeCall{deleteDag}, deployCalls...) {
		refusedForIssuer(t, "dev no auth", c, c.do(srv, ""))
	}
}

// TestDeployAudienceWithoutABearerVerifierFailsClosed: a wiring mistake that
// sets DeployViaIssuer without the verifier refuses every deploy.
func TestDeployAudienceWithoutABearerVerifierFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Auth: AuthDeps{
			Authenticator: engineAndAPIToken{},
			RateLimiter:   auth.NewRateLimiter(5, time.Minute),
			TokenTTLSecs:  900,
		},
		Issuers: IssuerDeps{
			DeployViaIssuer: true,
		},
		Stores: StoreDeps{
			Dags:     &fakeDagRepo{},
			Versions: &fakeVersionRepo{},
		},
	})
	for _, c := range deployCalls {
		refusedForIssuer(t, "password token", c, c.do(srv, "engine"))
		if rec := c.do(srv, "deploy:dexaflow:deploy"); passed(rec) {
			t.Errorf("%s with an unverifiable bearer = %d (%s), want refused", c.name, rec.Code, rec.Body.String())
		}
	}
}

// TestDeployAudienceInvalidBearerDoesNotFallBackToADeploy: a bad header
// bearer falls back to the session cookie, which is still no deploy token.
func TestDeployAudienceInvalidBearerDoesNotFallBackToADeploy(t *testing.T) {
	srv := newDeployAudienceServer(t, true)
	for _, c := range deployCalls {
		req := httptest.NewRequestWithContext(context.Background(), c.method, c.path, strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer forged")
		req.AddCookie(&http.Cookie{Name: "_token", Value: "engine"})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		refusedForIssuer(t, "bad bearer and a session", c, rec)
	}
}

// realIssuerKeys serves an RSA key as a JWKS and signs bearers with it, so a
// test drives the real issuer.Verifier behind the API.
type realIssuerKeys struct {
	key *rsa.PrivateKey
	url string
}

func newRealIssuerKeys(t *testing.T) *realIssuerKeys {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(srv.Close)
	return &realIssuerKeys{key: key, url: srv.URL}
}

func (k *realIssuerKeys) bearer(t *testing.T, aud any) string {
	t.Helper()
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://portal.example.com", "aud": aud, "sub": "user-42", "tenant_id": "acme",
		"scope": "dexaflow:deploy", "iat": now.Add(-10 * time.Second).Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	})
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(k.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestDeployAudienceMixedAudienceTokenCannotDeploy drives the real verifier:
// a token for the deploy audience and the MCP audience is an MCP bearer, so
// it gets the deploy 403, while one for the deploy audience alone deploys.
func TestDeployAudienceMixedAudienceTokenCannotDeploy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	keys := newRealIssuerKeys(t)
	verifier := issuer.New(context.Background(), issuer.Config{
		Name: "portal", Issuer: "https://portal.example.com", JWKSURL: keys.url, Audience: "dexaflow-engine",
		TenantClaim: "tenant_id", AllowedTenants: []string{"acme"}, BearerAudiences: []string{"leoflow-mcp"},
		DeployAudience: "dexaflow-deploy",
	})
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Auth: AuthDeps{
			Authenticator: engineAndAPIToken{},
			RateLimiter:   auth.NewRateLimiter(5, time.Minute),
			TokenTTLSecs:  900,
			AuthAudit:     &fakeAuthAudit{},
		},
		Issuers: IssuerDeps{
			TrustedIssuerBearer: verifier,
			TrustedIssuerUsers:  &fakeIssuerUsers{user: linkedAdmin(), active: true},
			DeployViaIssuer:     true,
		},
		Stores: StoreDeps{
			Dags:     &fakeDagRepo{},
			Versions: &fakeVersionRepo{},
		},
	})
	mixed := keys.bearer(t, []string{"leoflow-mcp", "dexaflow-deploy"})
	for _, c := range append([]scopeCall{deleteDag}, deployCalls...) {
		refusedForIssuer(t, "mixed audience", c, c.do(srv, mixed))
	}
	if rec := register.do(srv, keys.bearer(t, "dexaflow-deploy")); !passed(rec) {
		t.Errorf("register with the deploy audience alone = %d (%s), want past the gates", rec.Code, rec.Body.String())
	}
}
