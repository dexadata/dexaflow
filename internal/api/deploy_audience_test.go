package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

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
	return NewServer(Dependencies{
		Logger:              discardLogger(),
		Authenticator:       engineAndAPIToken{},
		RateLimiter:         auth.NewRateLimiter(5, time.Minute),
		TokenTTLSecs:        900,
		Dags:                &fakeDagRepo{},
		DagRuns:             &fakeRunRepo{},
		Versions:            &fakeVersionRepo{},
		TrustedIssuerBearer: deployIssuer{},
		TrustedIssuerUsers:  &fakeIssuerUsers{user: linkedAdmin(), active: true},
		AuthAudit:           &fakeAuthAudit{},
		DeployViaIssuer:     viaIssuer,
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

	reader := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: engineAndAPIToken{}, RateLimiter: auth.NewRateLimiter(5, time.Minute),
		TokenTTLSecs: 900, Versions: &fakeVersionRepo{}, TrustedIssuerBearer: deployIssuer{},
		TrustedIssuerUsers: &fakeIssuerUsers{user: linkedReader(), active: true}, AuthAudit: &fakeAuthAudit{},
		DeployViaIssuer: true,
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
