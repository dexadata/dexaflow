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
)

// pageGet requests path the way a browser does: no Authorization header,
// the session in the cookie when session is not empty.
func pageGet(t *testing.T, srv http.Handler, path, session string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	if session != "" {
		r.AddCookie(&http.Cookie{Name: authTokenCookie, Value: session})
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

// TestAPITokensPageIsServedToASignedInUser: the token screen (#1886) is a
// page of its own, framed by the Airflow UI. A signed-in browser gets it with
// headers that allow only its own script and only a same-origin frame; a
// browser without a session is sent to the sign-in page and back.
func TestAPITokensPageIsServedToASignedInUser(t *testing.T) {
	srv := tokenServer(deployerUser(), &fakeAPITokens{}, nil)

	rec := pageGet(t, srv, apiTokensPagePath, "session-jwt")
	if rec.Code != http.StatusOK {
		t.Fatalf("page = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src " + apiTokensScriptHash, "connect-src 'self'", "frame-ancestors 'self'", "form-action 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP = %q, missing %q", csp, want)
		}
	}
	for _, d := range strings.Split(csp, ";") {
		if strings.HasPrefix(strings.TrimSpace(d), "script-src") && strings.Contains(d, "'unsafe-inline'") {
			t.Errorf("CSP allows inline script in general: %q", csp)
		}
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<script>"+apiTokensScript+"</script>") {
		t.Error("the page does not carry the script its CSP hash allows")
	}
	if !strings.Contains(body, "/api/v2/apiTokens") {
		t.Error("the page does not read the token API")
	}

	anon := pageGet(t, srv, apiTokensPagePath, "")
	if anon.Code != http.StatusFound || anon.Header().Get("Location") != "/api/v2/auth/login?next=%2Fplugin%2Fapi-tokens" {
		t.Errorf("page without a session = %d Location %q, want 302 to the sign-in page", anon.Code, anon.Header().Get("Location"))
	}
}

// TestAPITokensPageNeedsTheTokenStore: without a token store the API is not
// mounted, so neither is its screen nor its menu entry.
func TestAPITokensPageNeedsTheTokenStore(t *testing.T) {
	srv := apiStubServer(nil)
	if rec := pageGet(t, srv, apiTokensPagePath, "session-jwt"); rec.Code != http.StatusNotFound {
		t.Errorf("page without a token store = %d, want 404", rec.Code)
	}
}

// TestPluginsListTheAPITokensView: the Airflow UI builds its menus from
// GET /api/v2/plugins, so the token screen is offered there as an external
// view of a built-in plugin, in Airflow 3's PluginResponse shape, under the
// user menu.
func TestPluginsListTheAPITokensView(t *testing.T) {
	srv := tokenServer(deployerUser(), &fakeAPITokens{}, nil)
	rec := authGet(srv, http.MethodGet, "/api/v2/plugins", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("plugins = %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Plugins      []map[string]json.RawMessage `json:"plugins"`
		TotalEntries int                          `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TotalEntries != 1 || len(body.Plugins) != 1 {
		t.Fatalf("plugins = %s, want one", rec.Body.String())
	}
	p := body.Plugins[0]
	for _, field := range []string{"macros", "flask_blueprints", "fastapi_apps", "fastapi_root_middlewares", "react_apps",
		"appbuilder_views", "appbuilder_menu_items", "global_operator_extra_links", "operator_extra_links", "listeners", "timetables"} {
		if string(p[field]) != "[]" {
			t.Errorf("plugin %s = %s, want []", field, p[field])
		}
	}
	if string(p["name"]) != `"dexaflow"` || string(p["source"]) != `"dexaflow"` {
		t.Errorf("plugin name/source = %s/%s", p["name"], p["source"])
	}
	var views []map[string]any
	if err := json.Unmarshal(p["external_views"], &views); err != nil || len(views) != 1 {
		t.Fatalf("external_views = %s", p["external_views"])
	}
	want := map[string]any{"name": "API tokens", "href": apiTokensPagePath, "destination": "nav",
		"category": "user", "url_route": "api-tokens", "icon": nil, "icon_dark_mode": nil}
	for k, v := range want {
		if views[0][k] != v {
			t.Errorf("external view %s = %v, want %v", k, views[0][k], v)
		}
	}
}

// tenantRecordingTokens is a token store that records the tenant of every
// call, to prove the handlers scope the store to the caller's tenant.
type tenantRecordingTokens struct {
	fakeAPITokens
	tenants []string
}

func (f *tenantRecordingTokens) ListAPITokens(ctx context.Context, tenant, ownerID string, limit, offset int) ([]auth.APIToken, int, error) {
	f.tenants = append(f.tenants, "list:"+tenant)
	return f.fakeAPITokens.ListAPITokens(ctx, tenant, ownerID, limit, offset)
}

func (f *tenantRecordingTokens) RevokeAPIToken(ctx context.Context, tenant, id, ownerID string) (auth.APIToken, error) {
	f.tenants = append(f.tenants, "revoke:"+tenant)
	return f.fakeAPITokens.RevokeAPIToken(ctx, tenant, id, ownerID)
}

// TestTokenRoutesAreScopedToTheCallersTenant: the list and the revoke the
// screen calls reach the store with the caller's tenant and nothing the
// request names, for an admin too.
func TestTokenRoutesAreScopedToTheCallersTenant(t *testing.T) {
	store := &tenantRecordingTokens{}
	admin := &auth.User{ID: "u-admin", TenantID: "acme", Roles: []string{"admin"}}
	srv := tokenServerWith(admin, store).(*gin.Engine)
	authGet(srv, http.MethodGet, "/api/v2/apiTokens?tenant=other", "")
	authGet(srv, http.MethodDelete, "/api/v2/apiTokens/tok-9?tenant=other", "")
	if !slices.Equal(store.tenants, []string{"list:acme", "revoke:acme"}) {
		t.Errorf("store calls = %q, want both in the caller's tenant", store.tenants)
	}
}

// tokenServerWith is tokenServer over any token store.
func tokenServerWith(user *auth.User, store TokenStore) http.Handler {
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: user},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Stores: StoreDeps{
			APITokens: store,
		},
	})
}
