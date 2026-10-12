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

// scopedIssuer verifies the bearer "scopes:<space-separated scopes>" as a
// token of tenant acme carrying those scopes (ADR 0067). "scopes:" is a token
// whose scope claim is present but empty.
type scopedIssuer struct{}

func (scopedIssuer) Provider() string { return "issuer:portal" }

func (scopedIssuer) VerifyBearer(_ context.Context, raw string) (*issuer.Identity, error) {
	list, ok := strings.CutPrefix(raw, "scopes:")
	if !ok {
		return nil, fmt.Errorf("%w: bad signature", issuer.ErrInvalidToken)
	}
	return &issuer.Identity{Subject: "user-42", Email: "ana@acme.com", Tenant: "acme", Scopes: strings.Fields(list)}, nil
}

// engineAdmin authenticates the engine token "engine" as an unscoped admin.
type engineAdmin struct{}

func (engineAdmin) Authenticate(_ context.Context, token string) (*auth.User, error) {
	if token != "engine" {
		return nil, auth.ErrInvalidToken
	}
	return &auth.User{ID: "u-1", TenantID: "acme", Email: "ana@acme.com", Roles: []string{"admin"}}, nil
}

func (engineAdmin) IssueToken(context.Context, auth.Credentials) (string, error) { return "", nil }

// newScopeServer serves every route the scope table names, with the linked
// user's roles set by user.
func newScopeServer(t *testing.T, user *auth.User) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return NewServer(Dependencies{
		Logger:              discardLogger(),
		Authenticator:       engineAdmin{},
		RateLimiter:         auth.NewRateLimiter(5, time.Minute),
		TokenTTLSecs:        900,
		Dags:                &fakeDagRepo{},
		DagRuns:             &fakeRunRepo{},
		Tasks:               &fakeTaskRepo{},
		Versions:            &fakeVersionRepo{},
		Connections:         &fakeConnStore{},
		TrustedIssuerBearer: scopedIssuer{},
		TrustedIssuerUsers:  &fakeIssuerUsers{user: user, active: true},
		AuthAudit:           &fakeAuthAudit{},
	})
}

func linkedAdmin() *auth.User {
	return &auth.User{ID: "u-1", TenantID: "acme", Email: "ana@acme.com", Roles: []string{"admin"}}
}

type scopeCall struct{ name, method, path, body string }

var (
	readDags    = scopeCall{"list dags", http.MethodGet, "/api/v2/dags", ""}
	triggerRun  = scopeCall{"trigger", http.MethodPost, "/api/v2/dags/etl/dagRuns", `{}`}
	setRunState = scopeCall{"set run state", http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1", `{"state":"failed"}`}
	clearTasks  = scopeCall{"clear", http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", `{"dag_run_id":"r1"}`}
	markTask    = scopeCall{"mark task", http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskInstances/load", `{"new_state":"success"}`}
	markMapped  = scopeCall{"mark mapped task", http.MethodPatch, "/api/v2/dags/etl/dagRuns/r1/taskInstances/load/0", `{"new_state":"success"}`}
	pauseDag    = scopeCall{"pause", http.MethodPatch, "/api/v2/dags/etl", `{"is_paused":true}`}
	register    = scopeCall{"register version", http.MethodPost, "/api/v2/dags/etl/versions", `{}`}
	deleteDag   = scopeCall{"delete dag", http.MethodDelete, "/api/v2/dags/etl", ""}
	deleteRun   = scopeCall{"delete run", http.MethodDelete, "/api/v2/dags/etl/dagRuns/r1", ""}
	addConn     = scopeCall{"create connection", http.MethodPost, "/api/v2/connections", `{"connection_id":"c","conn_type":"http"}`}
	runCalls    = []scopeCall{triggerRun, setRunState, clearTasks, markTask, markMapped, pauseDag}
	closedCalls = []scopeCall{deleteDag, deleteRun, addConn}
)

func (c scopeCall) do(srv http.Handler, bearer string) *httptest.ResponseRecorder {
	var body *strings.Reader
	if c.body == "" {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(c.body)
	}
	req := httptest.NewRequestWithContext(context.Background(), c.method, c.path, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// passed reports that the auth and scope gates let the call through: the
// handler, not the middleware, decided the answer.
func passed(rec *httptest.ResponseRecorder) bool {
	return rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden
}

func refusedForScope(t *testing.T, c scopeCall, rec *httptest.ResponseRecorder, scope string) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Errorf("%s = %d (%s), want 403", c.name, rec.Code, rec.Body.String())
		return
	}
	if !strings.Contains(rec.Body.String(), scope) {
		t.Errorf("%s: 403 body %s does not name %q", c.name, rec.Body.String(), scope)
	}
}

// TestReadScopeTokenCannotWrite: a read-only token reads, and gets 403 on
// trigger and on version register (#1473).
func TestReadScopeTokenCannotWrite(t *testing.T) {
	srv := newScopeServer(t, linkedAdmin())
	const token = "scopes:dexaflow:read"

	if rec := readDags.do(srv, token); rec.Code != http.StatusOK {
		t.Errorf("list dags = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	for _, c := range runCalls {
		refusedForScope(t, c, c.do(srv, token), "dexaflow:run")
	}
	refusedForScope(t, register, register.do(srv, token), "dexaflow:deploy")
}

// TestRunScopeTokenCanTriggerButNotRegister: run grants every run-state
// write, and nothing that deploys.
func TestRunScopeTokenCanTriggerButNotRegister(t *testing.T) {
	srv := newScopeServer(t, linkedAdmin())
	const token = "scopes:dexaflow:read dexaflow:run"

	for _, c := range runCalls {
		if rec := c.do(srv, token); !passed(rec) {
			t.Errorf("%s with dexaflow:run = %d (%s), want past the scope gate", c.name, rec.Code, rec.Body.String())
		}
	}
	refusedForScope(t, register, register.do(srv, token), "dexaflow:deploy")
}

func TestDeployScopeTokenCanRegister(t *testing.T) {
	srv := newScopeServer(t, linkedAdmin())
	const token = "scopes:dexaflow:deploy"

	if rec := register.do(srv, token); !passed(rec) {
		t.Errorf("register with dexaflow:deploy = %d (%s), want past the scope gate", rec.Code, rec.Body.String())
	}
	refusedForScope(t, triggerRun, triggerRun.do(srv, token), "dexaflow:run")
}

// TestReadNeedsTheReadScope: no scope implies another, so a token with only
// run cannot read.
func TestReadNeedsTheReadScope(t *testing.T) {
	srv := newScopeServer(t, linkedAdmin())
	for _, token := range []string{"scopes:dexaflow:run", "scopes:"} {
		refusedForScope(t, readDags, readDags.do(srv, token), "dexaflow:read")
	}
}

// TestScopedTokenCannotReachUnmappedWrites: the routes the scope table does
// not name fail closed, whatever the token's scopes.
func TestScopedTokenCannotReachUnmappedWrites(t *testing.T) {
	srv := newScopeServer(t, linkedAdmin())
	const token = "scopes:dexaflow:read dexaflow:run dexaflow:deploy"
	for _, c := range closedCalls {
		rec := c.do(srv, token)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "a scoped token cannot call this route") {
			t.Errorf("%s with every scope = %d (%s), want 403 saying no scope grants it", c.name, rec.Code, rec.Body.String())
		}
	}
}

// TestScopesNeverWiden: the role check still applies, so a viewer's token
// with dexaflow:run cannot trigger.
func TestScopesNeverWiden(t *testing.T) {
	srv := newScopeServer(t, linkedReader())
	rec := triggerRun.do(srv, "scopes:dexaflow:read dexaflow:run")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "execute:dag") {
		t.Errorf("viewer trigger = %d (%s), want 403 for the missing role permission", rec.Code, rec.Body.String())
	}
}

// TestEngineTokenIsNotScoped: an engine JWT carries no scope claim and keeps
// today's behavior, unmapped writes included.
func TestEngineTokenIsNotScoped(t *testing.T) {
	srv := newScopeServer(t, linkedAdmin())
	for _, c := range append(append([]scopeCall{readDags, register}, runCalls...), closedCalls...) {
		if rec := c.do(srv, "engine"); !passed(rec) {
			t.Errorf("%s with an engine token = %d (%s), want past the gates", c.name, rec.Code, rec.Body.String())
		}
	}
}

// TestScopeDoesNotLeakIntoTheUserRow: the scopes ride on the request's
// principal, never on the user record the store returned.
func TestScopeDoesNotLeakIntoTheUserRow(t *testing.T) {
	user := linkedAdmin()
	srv := newScopeServer(t, user)
	_ = readDags.do(srv, "scopes:dexaflow:read")
	if user.Scoped || user.Scopes != nil {
		t.Errorf("store's user row was mutated: %+v", user)
	}
}
