package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// supportedMenuItems are the Airflow 3.2.1 UI menu sections Dexaflow backs. The
// UI renders only the sections /ui/auth/menus authorizes, so omitting the rest
// (Assets, Pools, Providers, Jobs, XComs, ...) hides them without modifying the
// SPA. Each value must be a real 3.2.1 MenuItem enum member (validMenuItems).
// The set is widened as sections gain real backing: Variables and Connections
// (the Admin panel, #45) and Audit Log (Browse, #37) are implemented, so they
// are advertised; still-stubbed sections stay hidden. See docs/ui-compatibility.md.
var supportedMenuItems = []string{"Dags", "Variables", "Connections", "Audit Log", "Docs"}

// menuItemPermission maps an advertised menu section to the RBAC permission the
// caller needs to use it, so /ui/auth/menus advertises only the sections the
// caller can actually reach. Each mapping mirrors the RequirePermission gate on
// that section's data routes, so the menu never advertises a section that would
// 403 on click. Sections with no backing resource (Docs) are omitted from the
// map and stay baseline-visible to any authenticated user.
var menuItemPermission = map[string]auth.Permission{
	"Dags":        {Action: "read", Resource: "dag"},
	"Variables":   {Action: "read", Resource: "variable"},
	"Connections": {Action: "read", Resource: "connection"},
	"Audit Log":   {Action: "read", Resource: "audit_log"},
}

// authorizedMenuItems filters supportedMenuItems to those the user may use,
// preserving the advertised order. An item with no permission mapping is
// baseline-visible. The advertised SET is unchanged, so an admin (whose
// permission checks short-circuit) still sees every section.
func authorizedMenuItems(user *auth.User) []string {
	items := make([]string, 0, len(supportedMenuItems))
	for _, item := range supportedMenuItems {
		perm, gated := menuItemPermission[item]
		if !gated || user.HasPermission(perm.Action, perm.Resource) {
			items = append(items, item)
		}
	}
	return items
}

// validMenuItems is the Airflow 3.2.1 MenuItem string enum. /ui/auth/menus may
// only advertise values from this set; the SPA ignores unknown items.
var validMenuItems = map[string]bool{
	"Required Actions": true, "Assets": true, "Audit Log": true, "Config": true,
	"Connections": true, "Dags": true, "Docs": true, "Jobs": true,
	"Plugins": true, "Pools": true, "Providers": true, "Variables": true,
	"XComs": true,
}

// uiConfigRequiredFields mirrors the fields the Airflow 3.2.1 ConfigResponse
// schema marks required. The UI may silently misrender if any is absent, so the
// /ui/config payload must always carry every one. The browser E2E is the real
// validation; this list is the cheap unit-test guard. See docs/ui-compatibility.md.
var uiConfigRequiredFields = []string{
	"fallback_page_limit", "auto_refresh_interval", "hide_paused_dags_by_default",
	"instance_name", "enable_swagger_ui", "require_confirmation_dag_change",
	"default_wrap", "test_connection", "dashboard_alert", "show_external_log_redirect",
	"theme", "multi_team",
}

// DefaultUIAutoRefreshIntervalSeconds is the production-safe value returned by
// /ui/config when no explicit override is configured. Lite overrides this to a
// smaller value (typically 5s) for a snappy inner-loop dev experience; Pro
// keeps 30s so the SPA's polling does not hammer a shared metadata DB.
const DefaultUIAutoRefreshIntervalSeconds = 30

// registerUI mounts the Airflow 3.2.1 internal UI API (/ui/*) that the bundled
// React app calls. Unimplemented /ui paths degrade gracefully via uiNoRoute.
// tokenTTLSecs feeds the expires_in_seconds field of /ui/auth/token, and
// autoRefreshIntervalSecs is the SPA's polling cadence for DAG / DagRun /
// task-instance state refresh (non-positive values fall back to the
// production default).
func registerUI(r gin.IRouter, tokenTTLSecs int, instanceName string, autoRefreshIntervalSecs int) {
	r.GET("/ui/config", uiConfigHandler(instanceName, autoRefreshIntervalSecs))
	r.GET("/ui/auth/me", uiMeHandler())
	r.GET("/ui/auth/menus", uiMenusHandler())
	r.POST("/ui/auth/token", uiTokenHandler(tokenTTLSecs))
}

// uiConfigHandler returns the UI ConfigResponse (Airflow 3.2.1 shape). It keeps
// every spec-required field present; values stay minimal for the MVP (Phase 5.3
// tunes them). theme is null — required-but-nullable in the spec — meaning "no
// custom Chakra theme". is_db_isolation_mode is intentionally absent: it is not
// part of the 3.2.1 ConfigResponse.
//
// autoRefreshIntervalSecs controls the SPA's polling cadence for DAG / DagRun
// state. A non-positive value falls back to DefaultUIAutoRefreshIntervalSeconds
// so a misconfigured env var cannot accidentally drop it to 0 (which would
// hammer the DB). See docs/configuration.md.
func uiConfigHandler(instanceName string, autoRefreshIntervalSecs int) gin.HandlerFunc {
	if instanceName == "" {
		instanceName = "Dexaflow"
	}
	if autoRefreshIntervalSecs <= 0 {
		autoRefreshIntervalSecs = DefaultUIAutoRefreshIntervalSeconds
	}
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"fallback_page_limit":             50,
			"auto_refresh_interval":           autoRefreshIntervalSecs,
			"hide_paused_dags_by_default":     false,
			"instance_name":                   instanceName,
			"enable_swagger_ui":               true,
			"require_confirmation_dag_change": false,
			"default_wrap":                    false,
			"test_connection":                 "Enabled",
			"dashboard_alert":                 []any{},
			"show_external_log_redirect":      false,
			"external_log_name":               nil,
			"theme":                           nil,
			"multi_team":                      false,
		})
	}
}

// uiTokenHandler implements POST /ui/auth/token: it re-mints a bearer token for
// an already-authenticated principal. Per the 3.2.1 spec the body carries no
// credentials (only an optional token_type), so this is NOT the login endpoint
// — credential login is the simple-auth-manager POST /auth/token. Without a
// valid bearer it returns 401. The response is GenerateTokenResponse
// (access_token, token_type, expires_in_seconds). See docs/ui-compatibility.md.
func uiTokenHandler(tokenTTLSecs int) gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := UserFromContext(c); !ok {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "no authenticated user")
			return
		}
		token := bearerToken(c.GetHeader("Authorization"))
		if token == "" {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "no bearer token")
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"access_token":       token,
			"token_type":         "bearer",
			"expires_in_seconds": tokenTTLSecs,
		})
	}
}

// uiMeHandler returns the authenticated user (AuthenticatedMeResponse).
func uiMeHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := UserFromContext(c)
		if !ok {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "no authenticated user")
			return
		}
		roles := user.Roles
		if roles == nil {
			roles = []string{}
		}
		c.JSON(http.StatusOK, gin.H{"id": user.ID, "username": user.Email, "roles": roles})
	}
}

// uiMenusHandler returns the menu sections Dexaflow backs, filtered to those the
// current user is authorized for, so the UI hides both unbacked sections and
// sections the caller lacks permission to use (MenuItemCollectionResponse).
func uiMenusHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := UserFromContext(c)
		if !ok {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "no authenticated user")
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"authorized_menu_items": authorizedMenuItems(user),
			"extra_menu_items":      []any{},
		})
	}
}

// uiNoRoute is the engine's NoRoute handler, mirroring Airflow's catch-all. An
// unimplemented /ui path degrades gracefully (empty body for reads, 501 hint for
// writes). An unmatched /api path is a 404. Any other GET falls back to the SPA
// shell so the React router can handle client-side routes; without a UI server,
// or for non-GET, it is a 404.
func uiNoRoute(uiSrv UIServer, authn auth.Authenticator, devNoAuth bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		switch {
		case strings.HasPrefix(path, "/ui/"):
			if c.Request.Method == http.MethodGet {
				c.JSON(http.StatusOK, gin.H{})
				return
			}
			AbortProblem(c, http.StatusNotImplemented, "not implemented",
				"this action is not available in Dexaflow yet")
			return
		case strings.HasPrefix(path, "/api/"):
			AbortProblem(c, http.StatusNotFound, "not found", "API route not found")
			return
		}
		if uiSrv != nil && c.Request.Method == http.MethodGet {
			// Gate the SPA shell: an unauthenticated visitor must NOT see the
			// app rendered behind the login screen. Without this, the SPA shell
			// loads, mounts the authenticated layout, and fires its data calls
			// (which 401) before redirecting — flashing the whole UI and lighting
			// the log with 401s. Redirecting here keeps the shell off-screen until
			// there is a valid session. Skipped under dev no-auth.
			if !devNoAuth && !shellSessionValid(c, authn) {
				c.Redirect(http.StatusFound, "/api/v2/auth/login?next="+url.QueryEscape(path))
				return
			}
			uiSrv.Index(c.Writer, "/")
			return
		}
		AbortProblem(c, http.StatusNotFound, "not found", "no such resource")
	}
}

// shellSessionValid reports whether the request carries a token (bearer or the
// _token cookie) that authenticates — the same check JWTAuth applies to data
// routes, reused to gate the SPA shell so the gate and the data plane agree on
// what "logged in" means.
func shellSessionValid(c *gin.Context, authn auth.Authenticator) bool {
	if authn == nil {
		return false
	}
	for _, token := range candidateTokens(c) {
		if _, err := authn.Authenticate(c.Request.Context(), token); err == nil {
			return true
		}
	}
	return false
}
