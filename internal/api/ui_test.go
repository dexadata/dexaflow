package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/ui"
)

func uiServer() *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", Email: "admin@leoflow.local", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		UI:            ui.New(),
	})
}

func TestUIConfigInstanceNameConfigurable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := map[string]string{"": "Leoflow", "Leoflow · DEV": "Leoflow · DEV"}
	for in, want := range cases {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ui/config", http.NoBody)
		uiConfigHandler(in, 30)(c)
		var cfg map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if cfg["instance_name"] != want {
			t.Errorf("instance_name for %q = %v, want %v", in, cfg["instance_name"], want)
		}
	}
}

// TestUIConfigAutoRefreshIntervalConfigurable pins the contract behind the
// "UI tasks finished but tela not updated" Lima feedback: the SPA's auto-
// refresh interval (in seconds) MUST be a server-side setting so Lite can dial
// it down to ~5s for the dev inner loop while Pro keeps the 30s production
// default. A non-positive input falls back to the production default so a
// misconfigured env var cannot accidentally drop the value to 0 (which would
// hammer the DB).
func TestUIConfigAutoRefreshIntervalConfigurable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		in   int
		want float64 // JSON numbers come back as float64
	}{
		{"lite dev loop", 5, 5},
		{"production default", 30, 30},
		{"non-positive falls back to 30", 0, 30},
		{"negative falls back to 30", -1, 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ui/config", http.NoBody)
			uiConfigHandler("Leoflow", tc.in)(c)
			var cfg map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if cfg["auto_refresh_interval"] != tc.want {
				t.Errorf("auto_refresh_interval (in=%d) = %v, want %v", tc.in, cfg["auto_refresh_interval"], tc.want)
			}
		})
	}
}

func TestUIConfigIsPublicAndShaped(t *testing.T) {
	rec := authGet(uiServer(), http.MethodGet, "/ui/config", "") // no token: must still work (public)
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/config = %d, want 200", rec.Code)
	}
	var cfg map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	// Every field the spec marks required must be present, or the SPA may
	// silently misrender. See uiConfigRequiredFields (mirrored from the spec).
	for _, field := range uiConfigRequiredFields {
		if _, ok := cfg[field]; !ok {
			t.Errorf("config missing required field %q", field)
		}
	}
	if cfg["instance_name"] != "Leoflow" {
		t.Errorf("instance_name = %v, want Leoflow", cfg["instance_name"])
	}
	if cfg["auto_refresh_interval"].(float64) != 30 {
		t.Errorf("auto_refresh_interval = %v, want 30", cfg["auto_refresh_interval"])
	}
	// theme is required AND nullable in the spec (Theme object | null); null
	// means "no custom Chakra theme". It is NOT the string "default".
	if v, ok := cfg["theme"]; ok && v != nil {
		t.Errorf("theme = %v, want null", v)
	}
	// is_db_isolation_mode is not part of the 3.2.1 ConfigResponse.
	if _, ok := cfg["is_db_isolation_mode"]; ok {
		t.Errorf("config carries is_db_isolation_mode, not in spec")
	}
}

func TestUIMenusHidesUnsupportedSections(t *testing.T) {
	rec := authGet(uiServer(), http.MethodGet, "/ui/auth/menus", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/auth/menus = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Authorized []string `json:"authorized_menu_items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	has := func(s string) bool {
		for _, m := range body.Authorized {
			if m == s {
				return true
			}
		}
		return false
	}
	// Backed sections are advertised so the SPA renders them: Dags, Docs, the
	// Admin panel (Variables, Connections — #45) and Audit Log (#37).
	for _, want := range []string{"Dags", "Docs", "Variables", "Connections", "Audit Log"} {
		if !has(want) {
			t.Errorf("menus should authorize %q, got %v", want, body.Authorized)
		}
	}
	// Still-stubbed sections stay hidden.
	if has("Pools") || has("XComs") || has("Required Actions") || has("Providers") {
		t.Errorf("menus must hide unsupported sections, got %v", body.Authorized)
	}
	// Every advertised item must be a real 3.2.1 MenuItem enum value.
	for _, m := range body.Authorized {
		if !validMenuItems[m] {
			t.Errorf("menu item %q is not a 3.2.1 MenuItem enum value", m)
		}
	}
}

func TestAuthorizedMenuItemsFiltersByPermission(t *testing.T) {
	// Admin short-circuits every permission check, so it sees the full advertised
	// menu — the Lite bootstrap admin's view is unchanged.
	admin := &auth.User{Roles: []string{"admin"}}
	if got := authorizedMenuItems(admin); !reflect.DeepEqual(got, supportedMenuItems) {
		t.Errorf("admin menu = %v, want the full set %v", got, supportedMenuItems)
	}

	// A viewer holds read on dag/variable/connection but not audit_log, so the
	// Audit Log section (gated on read:audit_log, like its data route) drops out
	// while Docs stays baseline-visible. Order follows the advertised slice.
	viewer := &auth.User{Roles: []string{"viewer"}, Permissions: []auth.Permission{
		{Action: "read", Resource: "dag"},
		{Action: "read", Resource: "variable"},
		{Action: "read", Resource: "connection"},
	}}
	wantViewer := []string{"Dags", "Variables", "Connections", "Docs"}
	if got := authorizedMenuItems(viewer); !reflect.DeepEqual(got, wantViewer) {
		t.Errorf("viewer menu = %v, want %v", got, wantViewer)
	}

	// A principal with no permissions still sees the baseline-visible items only.
	if got := authorizedMenuItems(&auth.User{}); !reflect.DeepEqual(got, []string{"Docs"}) {
		t.Errorf("no-permission menu = %v, want [Docs]", got)
	}
}

func TestUIMeIncludesRoles(t *testing.T) {
	rec := authGet(uiServer(), http.MethodGet, "/ui/auth/me", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/auth/me = %d, want 200", rec.Code)
	}
	var me struct {
		Username string   `json:"username"`
		Roles    []string `json:"roles"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if len(me.Roles) != 1 || me.Roles[0] != "admin" {
		t.Errorf("roles = %v, want [admin]", me.Roles)
	}
}

func TestUIAuthTokenReMintsForAuthenticatedPrincipal(t *testing.T) {
	srv := uiServer()
	// Authenticated principal: re-mint succeeds with the spec response shape.
	rec := authGet(srv, http.MethodPost, "/ui/auth/token", `{"token_type":"api"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/auth/token = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var tok map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"access_token", "token_type", "expires_in_seconds"} {
		if _, ok := tok[f]; !ok {
			t.Errorf("token response missing %q (got %v)", f, tok)
		}
	}
}

func TestUIMeReturnsUser(t *testing.T) {
	rec := authGet(uiServer(), http.MethodGet, "/ui/auth/me", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/auth/me = %d, want 200", rec.Code)
	}
	var me struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me.Username != "admin@leoflow.local" {
		t.Errorf("username = %q", me.Username)
	}
}

func TestUINoRouteDegradesGracefully(t *testing.T) {
	srv := uiServer()
	// Unimplemented /ui read (no explicit stub) -> empty 200, not a 404 white
	// screen. /ui/gantt has no stub, so it exercises the NoRoute catch-all.
	if rec := authGet(srv, http.MethodGet, "/ui/gantt/etl/r1", ""); rec.Code != http.StatusOK || rec.Body.String() != "{}" {
		t.Errorf("unimplemented /ui GET = %d %q, want 200 {}", rec.Code, rec.Body.String())
	}
	// Unimplemented /ui write -> 501 hint.
	if rec := authGet(srv, http.MethodPost, "/ui/backfills", "{}"); rec.Code != http.StatusNotImplemented {
		t.Errorf("unimplemented /ui write = %d, want 501", rec.Code)
	}
	// Unmatched /api path -> 404 (never the SPA shell).
	if rec := authGet(srv, http.MethodGet, "/api/v2/bogus", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unmatched /api GET = %d, want 404", rec.Code)
	}
	// Non-/ui, non-/api GET -> SPA shell (client-side route), Airflow-style.
	rec := authGet(srv, http.MethodGet, "/dags/etl/grid", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<div id=\"root\"") {
		t.Errorf("client-side route = %d, want 200 SPA shell, got %q", rec.Code, rec.Body.String())
	}
	// Non-GET unknown -> 404.
	if rec := authGet(srv, http.MethodDelete, "/whatever", ""); rec.Code != http.StatusNotFound {
		t.Errorf("non-GET unknown = %d, want 404", rec.Code)
	}
}

func TestUnauthenticatedShellRedirectsToLogin(t *testing.T) {
	srv := uiServer()
	anon := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
		rec := httptest.NewRecorder() // no Authorization header, no cookie
		srv.ServeHTTP(rec, req)
		return rec
	}
	// The app shell must NOT render for an unauthenticated visitor: rendering it
	// flashes the whole logged-in UI before bouncing to login (and lights the log
	// with 401s). Shell routes redirect straight to the login page instead.
	for _, p := range []string{"/", "/dags/etl/grid"} {
		rec := anon(p)
		if rec.Code != http.StatusFound {
			t.Errorf("anonymous shell GET %s = %d, want 302 to login", p, rec.Code)
		}
		if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/api/v2/auth/login") {
			t.Errorf("anonymous shell GET %s -> %q, want /api/v2/auth/login", p, loc)
		}
	}
	// Static assets and the pre-login config stay public — the login page and the
	// SPA bundle it loads after sign-in must be fetchable without a token.
	for _, p := range []string{"/static/VERSION", "/ui/config"} {
		if rec := anon(p); rec.Code != http.StatusOK {
			t.Errorf("anonymous GET %s = %d, want 200 (public)", p, rec.Code)
		}
	}
	// Data planes stay gated with 401 (API calls, not a redirect).
	for _, p := range []string{"/api/v2/version", "/ui/auth/me"} {
		if rec := anon(p); rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous GET %s = %d, want 401 (gated data)", p, rec.Code)
		}
	}
	// With a session cookie, the gate lets the shell through.
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dags/etl/grid", http.NoBody)
	req.AddCookie(&http.Cookie{Name: authTokenCookie, Value: "valid"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<div id=\"root\"") {
		t.Errorf("cookie-authenticated shell = %d, want 200 SPA shell", rec.Code)
	}
}

func TestUIServesStaticAndIndexShell(t *testing.T) {
	srv := uiServer()
	// Root serves the SPA shell with the templated base href.
	rec := authGet(srv, http.MethodGet, "/", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<base href=\"/\"") {
		t.Errorf("root shell = %d, body=%q", rec.Code, rec.Body.String())
	}
	// The embedded bundle (placeholder or fetched) serves files under /static.
	// (index.html itself is 301-canonicalized to ./ by http.FileServer, so we
	// probe the VERSION marker, which every bundle carries.)
	rec = authGet(srv, http.MethodGet, "/static/VERSION", "")
	if rec.Code != http.StatusOK {
		t.Errorf("/static/VERSION = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc == "" {
		t.Errorf("/static response missing Cache-Control")
	}
}

// TestShellGateDeniesOnABackendError pins the SECOND caller of
// auth.Authenticate. #1087 changed what Authenticate returns for a store
// failure — a raw backend error instead of a joined ErrInvalidToken — and
// nothing outside internal/auth switches on that sentinel, so the shell gate
// must keep deciding on `err == nil` alone. If it ever grew a "not
// ErrInvalidToken, so let it through" branch, an unreachable user store would
// serve the authenticated SPA shell to anyone holding any string.
func TestShellGateDeniesOnABackendError(t *testing.T) {
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{authErr: errors.New("dial tcp 10.0.0.1:5432: i/o timeout")},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		UI:            ui.New(),
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/dags/etl/grid", http.NoBody)
	req.AddCookie(&http.Cookie{Name: authTokenCookie, Value: "whatever"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("shell GET with an unreachable store = %d, want 302 to login — a store we could not read must never authenticate", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<div id=\"root\"") {
		t.Error("the authenticated SPA shell was served while the user store was unreachable")
	}
}
