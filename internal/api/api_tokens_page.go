package api

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// The API tokens screen (#1886). The embedded Airflow UI cannot be changed,
// so the screen is a page of its own that the UI frames: GET /api/v2/plugins
// offers it as an external view of a built-in plugin (Airflow 3's extension
// point for extra screens), which puts "API tokens" in the UI's user menu and
// renders the page in a same-origin frame at /plugin/api-tokens. The page
// reads and revokes through the token API (ADR 0074), with the session
// cookie, so it holds no data of its own and adds no permission rule.

// apiTokensPagePath is where the token screen is served.
const apiTokensPagePath = "/dexaflow/api-tokens"

// apiTokensSPARoute is where the Airflow UI shows the screen in its frame,
// the route a sign-in returns to.
const apiTokensSPARoute = "/plugin/api-tokens"

// apiTokensScript is the screen's only script. The CSP allows exactly this
// script by its hash (apiTokensScriptHash), never inline script in general.
//
//go:embed api_tokens_page.js
var apiTokensScript string

//go:embed api_tokens_page.html
var apiTokensPageTemplate string

// apiTokensPage is the served page: the template with the script inlined.
var apiTokensPage = []byte(strings.Replace(apiTokensPageTemplate, "{{SCRIPT}}", apiTokensScript, 1))

// apiTokensScriptHash is the CSP source that allows apiTokensScript.
var apiTokensScriptHash = func() string {
	sum := sha256.Sum256([]byte(apiTokensScript))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

// apiTokensPageCSP lets the page run its own script, style itself, and call
// only its own origin; it may be framed only by its own origin (the Airflow
// UI) and has no form to post anywhere.
var apiTokensPageCSP = "default-src 'none'; script-src " + apiTokensScriptHash +
	"; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'self'; form-action 'none'; base-uri 'none'"

// registerAPITokensPage mounts the token screen when the token API is
// mounted. Like the UI shell, the page is refused to a browser without a
// session, which is sent to the sign-in page and back to the framed screen,
// and a session the store could not check gets the outage page (#1099).
func registerAPITokensPage(r gin.IRouter, deps Dependencies, hosts *hostBinding) {
	if deps.Stores.APITokens == nil {
		return
	}
	r.GET(apiTokensPagePath, apiTokensPageHandler(deps, hosts))
}

func apiTokensPageHandler(deps Dependencies, hosts *hostBinding) gin.HandlerFunc {
	session := deps.sessionCookie()
	return func(c *gin.Context) {
		if !deps.Auth.DevNoAuth {
			if verdict := shellSession(c, deps.Auth.Authenticator, session.cookieName(), hosts); verdict != shellVerified {
				refuseShell(c, verdict, session, hosts, apiTokensSPARoute)
				return
			}
		}
		c.Header("Cache-Control", "no-store")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "same-origin")
		c.Header("Content-Security-Policy", apiTokensPageCSP)
		c.Data(http.StatusOK, "text/html; charset=utf-8", apiTokensPage)
	}
}

// pluginsHandler answers GET /api/v2/plugins in Airflow 3's
// PluginCollectionResponse shape. With the token API mounted it lists one
// built-in plugin whose external view is the token screen; otherwise none.
func pluginsHandler(tokens bool) gin.HandlerFunc {
	plugins := []gin.H{}
	if tokens {
		plugins = append(plugins, dexaflowPlugin())
	}
	body := gin.H{"plugins": plugins, "total_entries": len(plugins)}
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, body)
	}
}

// dexaflowPlugin is the built-in plugin that carries Dexaflow's own screens
// into the Airflow UI. Every PluginResponse field is present, as the UI's
// generated client expects.
func dexaflowPlugin() gin.H {
	none := []any{}
	return gin.H{
		"name": "dexaflow", "source": "dexaflow",
		"macros": none, "flask_blueprints": none, "fastapi_apps": none, "fastapi_root_middlewares": none,
		"react_apps": none, "appbuilder_views": none, "appbuilder_menu_items": none,
		"global_operator_extra_links": none, "operator_extra_links": none, "listeners": none, "timetables": none,
		"external_views": []gin.H{{
			"name": "API tokens", "href": apiTokensPagePath, "destination": "nav", "category": "user",
			"url_route": "api-tokens", "icon": nil, "icon_dark_mode": nil,
		}},
	}
}
