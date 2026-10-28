package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// deployerUser holds the built-in deployer role's grants (migration 044).
func deployerUser() *auth.User {
	return &auth.User{ID: "u-ci", TenantID: "acme", Email: "ci@acme.com", Roles: []string{"deployer"},
		Permissions: []auth.Permission{{Action: "read", Resource: "dag"}, {Action: "deploy", Resource: "dag"}}}
}

func viewerUser() *auth.User {
	return &auth.User{ID: "u-view", TenantID: "acme", Email: "v@acme.com", Roles: []string{"viewer"},
		Permissions: []auth.Permission{{Action: "read", Resource: "dag"}}}
}

// fakeAPITokens records what the token routes ask of the store.
type fakeAPITokens struct {
	created   []auth.APIToken
	hashes    []string
	listOwner []string
	revoked   []string
	revokeErr error
	createErr error
	list      []auth.APIToken
}

func (f *fakeAPITokens) CreateAPIToken(_ context.Context, tenant, userID, name, hash string, scopes []string, expiresAt time.Time) (auth.APIToken, error) {
	if f.createErr != nil {
		return auth.APIToken{}, f.createErr
	}
	tok := auth.APIToken{ID: "tok-1", TenantID: tenant, UserID: userID, Name: name, Scopes: scopes,
		CreatedAt: time.Now(), ExpiresAt: expiresAt}
	f.created = append(f.created, tok)
	f.hashes = append(f.hashes, hash)
	return tok, nil
}

func (f *fakeAPITokens) ListAPITokens(_ context.Context, _, ownerID string, _, _ int) ([]auth.APIToken, int, error) {
	f.listOwner = append(f.listOwner, ownerID)
	return f.list, len(f.list), nil
}

func (f *fakeAPITokens) RevokeAPIToken(_ context.Context, _, id, ownerID string) (auth.APIToken, error) {
	if f.revokeErr != nil {
		return auth.APIToken{}, f.revokeErr
	}
	f.revoked = append(f.revoked, id+"|"+ownerID)
	return auth.APIToken{ID: id, Name: "ci-x", UserID: "u-owner"}, nil
}

func tokenServer(user *auth.User, store *fakeAPITokens, audit *fakeAuthAudit) *gin.Engine {
	gin.SetMode(gin.TestMode)
	deps := Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: user},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Stores: StoreDeps{
			Dags:      &fakeDagRepo{},
			DagRuns:   &fakeRunRepo{},
			Versions:  &fakeVersionRepo{created: true, change: rollbackChange()},
			APITokens: store,
		},
	}
	if audit != nil {
		deps.Auth.AuthAudit = audit
	}
	return newTestServer(deps)
}

// TestDeployerRoleDeploysAndNothingElse: the deployer role registers a
// version, sets the current version and reads DAGs, and is refused deleting,
// pausing or triggering a DAG, whether it calls with a session or with a
// deploy token.
func TestDeployerRoleDeploysAndNothingElse(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		user := deployerUser()
		if scoped {
			user.Scoped, user.Scopes = true, []string{auth.ScopeDeploy}
		}
		srv := tokenServer(user, &fakeAPITokens{}, nil)
		allowed := map[string][3]string{
			"register":    {http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON},
			"set current": {http.MethodPut, "/api/v2/dags/etl/currentVersion", `{"version":"v1"}`},
			"read dag":    {http.MethodGet, "/api/v2/dags/etl", ""},
		}
		for name, c := range allowed {
			if rec := authGet(srv, c[0], c[1], c[2]); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Errorf("scoped=%v %s = %d %s, want allowed", scoped, name, rec.Code, rec.Body.String())
			}
		}
		refused := map[string][3]string{
			"delete dag": {http.MethodDelete, "/api/v2/dags/etl", ""},
			"pause dag":  {http.MethodPatch, "/api/v2/dags/etl", `{"is_paused":true}`},
			"trigger":    {http.MethodPost, "/api/v2/dags/etl/dagRuns", `{}`},
		}
		for name, c := range refused {
			if rec := authGet(srv, c[0], c[1], c[2]); rec.Code != http.StatusForbidden {
				t.Errorf("scoped=%v %s = %d %s, want 403", scoped, name, rec.Code, rec.Body.String())
			}
		}
	}
}

// TestDeployRoutesStillAcceptWriteOnDags: a role holding write:dag, custom
// roles included, keeps deploying after deploy:dag exists; read alone does not.
func TestDeployRoutesStillAcceptWriteOnDags(t *testing.T) {
	writer := &auth.User{ID: "u-w", TenantID: "acme", Permissions: []auth.Permission{{Action: "write", Resource: "dag"}}}
	srv := tokenServer(writer, &fakeAPITokens{}, nil)
	if rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON); rec.Code != http.StatusCreated {
		t.Errorf("write:dag register = %d %s, want 201", rec.Code, rec.Body.String())
	}
	rec := authGet(tokenServer(viewerUser(), &fakeAPITokens{}, nil), http.MethodPost, "/api/v2/dags/etl/versions", validSpecJSON)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "deploy:dag") {
		t.Errorf("read-only register = %d %s, want 403 naming deploy:dag", rec.Code, rec.Body.String())
	}
}

type createdTokenBody struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	Token     string   `json:"token"`
	ExpiresAt string   `json:"expires_at"`
}

// TestCreateDeployTokenReturnsItOnceAndStoresTheHash: the token is in the
// response, the store gets only its hash, the scopes of a deploy token, and
// the requested expiry; the creation is audited without the token.
func TestCreateDeployTokenReturnsItOnceAndStoresTheHash(t *testing.T) {
	store, audit := &fakeAPITokens{}, &fakeAuthAudit{}
	srv := tokenServer(deployerUser(), store, audit)
	before := time.Now()

	rec := authGet(srv, http.MethodPost, "/api/v2/apiTokens", `{"name":"ci-x","scope":"deploy","expires_in_seconds":7776000}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s, want 201", rec.Code, rec.Body.String())
	}
	var body createdTokenBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !auth.IsAPIToken(body.Token) || body.Name != "ci-x" || body.ID != "tok-1" {
		t.Errorf("response %+v, want the new token", body)
	}
	if len(store.created) != 1 || store.hashes[0] != auth.HashAPIToken(body.Token) {
		t.Fatalf("store got %v, want the hash of the returned token", store.hashes)
	}
	got := store.created[0]
	if got.UserID != "u-ci" || got.TenantID != "acme" || !slices.Equal(got.Scopes, []string{auth.ScopeDeploy}) {
		t.Errorf("stored %+v, want the caller's deploy token in acme", got)
	}
	if d := got.ExpiresAt.Sub(before); d < 90*24*time.Hour || d > 90*24*time.Hour+time.Minute {
		t.Errorf("expiry in %v, want 90 days", d)
	}
	if strings.Contains(rec.Body.String(), store.hashes[0]) {
		t.Error("the response carries the stored hash")
	}
	if len(audit.events) != 1 || audit.events[0].action != "api_token.create" || audit.events[0].extra["token_id"] != "tok-1" {
		t.Fatalf("audit = %+v, want one api_token.create", audit.events)
	}
	for _, v := range audit.events[0].extra {
		if strings.Contains(v, body.Token) {
			t.Error("the audit record carries the token")
		}
	}
}

func TestCreateTokenDefaultsToNinetyDays(t *testing.T) {
	store := &fakeAPITokens{}
	rec := authGet(tokenServer(deployerUser(), store, nil), http.MethodPost, "/api/v2/apiTokens", `{"name":"ci","scope":"deploy"}`)
	if rec.Code != http.StatusCreated || len(store.created) != 1 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	if d := time.Until(store.created[0].ExpiresAt); d < 90*24*time.Hour-time.Minute || d > 90*24*time.Hour {
		t.Errorf("default expiry in %v, want 90 days", d)
	}
}

func TestCreateTokenValidatesItsInput(t *testing.T) {
	cases := map[string]string{
		"no name":         `{"scope":"deploy"}`,
		"blank name":      `{"name":" ","scope":"deploy"}`,
		"name with space": `{"name":"ci x","scope":"deploy"}`,
		"name too long":   `{"name":"` + strings.Repeat("a", 65) + `","scope":"deploy"}`,
		"unicode name":    `{"name":"ci-é","scope":"deploy"}`,
		"leading dash":    `{"name":"-ci","scope":"deploy"}`,
		"unknown scope":   `{"name":"ci","scope":"admin"}`,
		"no scope":        `{"name":"ci"}`,
		"run scope":       `{"name":"ci","scope":"run"}`,
		"ttl too short":   `{"name":"ci","scope":"deploy","expires_in_seconds":60}`,
		"ttl too long":    `{"name":"ci","scope":"deploy","expires_in_seconds":31536001}`,
		"negative ttl":    `{"name":"ci","scope":"deploy","expires_in_seconds":-1}`,
		"unknown field":   `{"name":"ci","scope":"deploy","user_id":"u-admin"}`,
		"not json":        `name=ci`,
	}
	for name, body := range cases {
		store := &fakeAPITokens{}
		rec := authGet(tokenServer(deployerUser(), store, nil), http.MethodPost, "/api/v2/apiTokens", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, rec.Code, rec.Body.String())
		}
		if len(store.created) != 0 {
			t.Errorf("%s reached the store", name)
		}
	}
	ok := `{"name":"` + strings.Repeat("a", 64) + `","scope":"read","expires_in_seconds":3600}`
	if rec := authGet(tokenServer(deployerUser(), &fakeAPITokens{}, nil), http.MethodPost, "/api/v2/apiTokens", ok); rec.Code != http.StatusCreated {
		t.Errorf("64-character name and one-hour ttl = %d %s, want 201", rec.Code, rec.Body.String())
	}
}

// TestCreateTokenNeedsTheGrantItsScopeUses: a token never carries a scope its
// owner could not use, so a viewer cannot mint a deploy token, while it can
// mint a read one.
func TestCreateTokenNeedsTheGrantItsScopeUses(t *testing.T) {
	store := &fakeAPITokens{}
	srv := tokenServer(viewerUser(), store, nil)
	if rec := authGet(srv, http.MethodPost, "/api/v2/apiTokens", `{"name":"ci","scope":"deploy"}`); rec.Code != http.StatusForbidden {
		t.Errorf("viewer deploy token = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if rec := authGet(srv, http.MethodPost, "/api/v2/apiTokens", `{"name":"ci","scope":"read"}`); rec.Code != http.StatusCreated {
		t.Errorf("viewer read token = %d %s, want 201", rec.Code, rec.Body.String())
	}
	if len(store.created) != 1 || !slices.Equal(store.created[0].Scopes, []string{auth.ScopeRead}) {
		t.Errorf("created %+v, want one read token", store.created)
	}
}

// TestAScopedTokenCannotManageTokens: a token cannot mint, list or revoke
// tokens, so a leaked deploy token cannot extend itself.
func TestAScopedTokenCannotManageTokens(t *testing.T) {
	user := deployerUser()
	user.Scoped, user.Scopes = true, []string{auth.ScopeRead, auth.ScopeDeploy}
	store := &fakeAPITokens{}
	srv := tokenServer(user, store, nil)
	for _, c := range [][3]string{
		{http.MethodPost, "/api/v2/apiTokens", `{"name":"ci","scope":"deploy"}`},
		{http.MethodGet, "/api/v2/apiTokens", ""},
		{http.MethodDelete, "/api/v2/apiTokens/tok-1", ""},
	} {
		if rec := authGet(srv, c[0], c[1], c[2]); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with a token = %d, want 403", c[0], c[1], rec.Code)
		}
	}
	if len(store.created)+len(store.listOwner)+len(store.revoked) != 0 {
		t.Error("a scoped token reached the token store")
	}
}

// TestTokenListAndRevokeAreOwnUnlessAdmin: a user lists and revokes its own
// tokens; a user who manages accounts sees and revokes the tenant's.
func TestTokenListAndRevokeAreOwnUnlessAdmin(t *testing.T) {
	last := time.Now()
	store := &fakeAPITokens{list: []auth.APIToken{{ID: "tok-1", Name: "ci-x", UserEmail: "ci@acme.com",
		Scopes: []string{auth.ScopeRead, auth.ScopeDeploy}, LastUsedAt: &last}}}
	srv := tokenServer(deployerUser(), store, nil)
	rec := authGet(srv, http.MethodGet, "/api/v2/apiTokens", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"owner_email":"ci@acme.com"`) ||
		!strings.Contains(rec.Body.String(), `"last_used_at"`) || !strings.Contains(rec.Body.String(), `"total_entries":1`) {
		t.Errorf("list = %d %s", rec.Code, rec.Body.String())
	}
	if rec := authGet(srv, http.MethodDelete, "/api/v2/apiTokens/tok-1", ""); rec.Code != http.StatusNoContent {
		t.Errorf("revoke = %d %s, want 204", rec.Code, rec.Body.String())
	}
	admin := &auth.User{ID: "u-admin", TenantID: "acme", Roles: []string{"admin"}}
	asAdmin := tokenServer(admin, store, nil)
	authGet(asAdmin, http.MethodGet, "/api/v2/apiTokens", "")
	authGet(asAdmin, http.MethodDelete, "/api/v2/apiTokens/tok-2", "")
	if !slices.Equal(store.listOwner, []string{"u-ci", ""}) {
		t.Errorf("list owners = %q, want the caller, then every owner for the admin", store.listOwner)
	}
	if !slices.Equal(store.revoked, []string{"tok-1|u-ci", "tok-2|"}) {
		t.Errorf("revokes = %q, want the caller's own, then any owner's for the admin", store.revoked)
	}
}

func TestRevokeUnknownTokenIsNotFoundAndAudited(t *testing.T) {
	audit := &fakeAuthAudit{}
	srv := tokenServer(deployerUser(), &fakeAPITokens{revokeErr: domain.ErrNotFound}, audit)
	if rec := authGet(srv, http.MethodDelete, "/api/v2/apiTokens/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoke unknown = %d, want 404", rec.Code)
	}
	if len(audit.events) != 0 {
		t.Errorf("a failed revoke was audited as done: %+v", audit.events)
	}
	ok := &fakeAuthAudit{}
	authGet(tokenServer(deployerUser(), &fakeAPITokens{}, ok), http.MethodDelete, "/api/v2/apiTokens/tok-1", "")
	if len(ok.events) != 1 || ok.events[0].action != "api_token.revoke" || ok.events[0].extra["token_id"] != "tok-1" ||
		ok.events[0].extra["token_owner_id"] != "u-owner" {
		t.Errorf("revoke audit = %+v", ok.events)
	}
}

func TestCreateTokenMapsStoreRefusals(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{domain.ErrConflict, http.StatusConflict},
		{domain.Safef(domain.ErrValidation, "an account holds at most 50 live API tokens; revoke one first"), http.StatusBadRequest},
	} {
		rec := authGet(tokenServer(deployerUser(), &fakeAPITokens{createErr: tc.err}, nil), http.MethodPost, "/api/v2/apiTokens", `{"name":"ci","scope":"deploy"}`)
		if rec.Code != tc.want {
			t.Errorf("store %v = %d %s, want %d", tc.err, rec.Code, rec.Body.String(), tc.want)
		}
	}
}

// TestDeployScopeReadsOnlyWhatDeployNeeds: a token that carries only
// dexaflow:deploy, which is what a deploy token carries, reads DAGs and their
// versions and nothing else its owner could read, even when the owner is an
// admin: variables, connections, logs, XComs, users and the event log answer
// 403 naming the missing scope.
func TestDeployScopeReadsOnlyWhatDeployNeeds(t *testing.T) {
	srv := newFullScopeServer(t)
	const token = "scopes:dexaflow:deploy"
	for _, path := range []string{
		"/api/v2/dags", "/api/v2/dags/etl", "/api/v2/dags/etl/details",
		"/api/v2/dags/etl/dagVersions", "/api/v2/dags/etl/dagVersions/1",
	} {
		c := scopeCall{path, http.MethodGet, path, ""}
		if rec := c.do(srv, token); scopeRefused(rec) {
			t.Errorf("%s with dexaflow:deploy = %d %s, want past the scope checks", path, rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{
		"/api/v2/variables", "/api/v2/connections", "/api/v2/users", "/api/v2/eventLogs",
		"/api/v2/dags/etl/dagRuns", "/api/v2/dags/etl/dagRuns/r1/taskInstances/t/logs/1",
		"/api/v2/xcoms/etl/r1/t/k", "/api/v2/pools",
	} {
		c := scopeCall{path, http.MethodGet, path, ""}
		rec := c.do(srv, token)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "missing scope") {
			t.Errorf("%s with dexaflow:deploy = %d %s, want 403 missing scope", path, rec.Code, rec.Body.String())
		}
	}
}

// TestAPITokenIsNotAcceptedFromTheSessionCookie: an API token authenticates
// only from the Authorization header. In the session cookie it is ignored, so
// a token planted in a browser cookie never opens a session.
func TestAPITokenIsNotAcceptedFromTheSessionCookie(t *testing.T) {
	srv := tokenServer(deployerUser(), &fakeAPITokens{}, nil)
	call := func(cookie string) int {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
		req.AddCookie(&http.Cookie{Name: authTokenCookie, Value: cookie})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := call(auth.APITokenPrefix + "planted"); code != http.StatusUnauthorized {
		t.Errorf("API token in the session cookie = %d, want 401", code)
	}
	if code := call("eyJhbGciOiJIUzI1NiJ9.session.sig"); code != http.StatusOK {
		t.Errorf("session JWT in the cookie = %d, want 200", code)
	}
}
