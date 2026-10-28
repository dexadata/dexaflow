package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

const (
	contextKeyUser      = "leoflow.user"
	contextKeyRequestID = "leoflow.request_id"
	// contextKeyProblemDetail carries the AbortProblem detail so StructuredLogger
	// can surface WHY a 4xx/5xx happened — otherwise a failing request is logged
	// only as a status code, with no cause (an observability blind spot).
	contextKeyProblemDetail = "leoflow.problem_detail"
	// contextKeyProblemCause carries the real error behind a redacted detail.
	// The response says "the request could not be completed"; this says which
	// SQLSTATE, on which operation, so the operator is not left guessing.
	contextKeyProblemCause = "leoflow.problem_cause"
	// contextKeyRefusalStatus carries the status a refusal would have answered
	// when it answers with a redirect instead (the trusted-issuer handoff with an
	// external sign-in). StructuredLogger logs it and logs at its level, so the
	// redirect does not turn a 403 or 500 into an INFO line.
	contextKeyRefusalStatus = "leoflow.refusal_status"
	headerRequestID         = "X-Request-Id"
)

// RequestID assigns a request id (honoring an inbound X-Request-Id) and echoes it.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(headerRequestID)
		if id == "" {
			id = newRequestID()
		}
		c.Set(contextKeyRequestID, id)
		c.Header(headerRequestID, id)
		c.Next()
	}
}

func newRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}

// StructuredLogger logs one structured line per request (ADR 0010).
func StructuredLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		// FullPath is the matched route template; it is empty for unmatched
		// routes, so fall back to the raw URL path to keep 404s diagnosable.
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}
		status := c.Writer.Status()
		attrs := []any{
			"method", c.Request.Method,
			"path", path,
			"status", status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", c.GetString(contextKeyRequestID),
		}
		// Surface the cause on failures so a 4xx/5xx is diagnosable from the log,
		// not just a status code. 5xx is a server fault (ERROR); 4xx is a rejected
		// request (WARN). Below 400 stays INFO.
		if detail := c.GetString(contextKeyProblemDetail); detail != "" {
			attrs = append(attrs, "detail", detail)
		}
		// The detail is what the CLIENT was told, which for a storage failure is
		// a deliberately vague phrase. The cause is the error itself; without it
		// the log would say only that something went wrong.
		if cause := c.GetString(contextKeyProblemCause); cause != "" {
			attrs = append(attrs, "cause", cause)
		}
		level := status
		if refused := c.GetInt(contextKeyRefusalStatus); refused != 0 {
			attrs = append(attrs, "refusal_status", refused)
			level = refused
		}
		switch {
		case level >= 500:
			logger.Error("http request", attrs...)
		case level >= 400:
			logger.Warn("http request", attrs...)
		default:
			logger.Info("http request", attrs...)
		}
	}
}

// CORS allows the configured origins (use "*" to allow any).
func CORS(allowed []string) gin.HandlerFunc {
	set := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		set[o] = true
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (set["*"] || set[origin]) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type")
			// Paging headers a cross-origin client needs to walk a list.
			c.Header("Access-Control-Expose-Headers", "Link,"+nextCursorHeader)
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// alwaysPublic are non-data paths reachable without a token: the login/logout
// endpoints, health/docs, and the pre-login UI config (read before
// authentication). "/api/v2/auth/" covers the Airflow UI's login + logout, which
// must be reachable precisely when the user has no token yet. /metrics is not
// listed: it is not served on this listener at all (audit H2) — scraping lives on
// the dedicated observability listener. "/api/v2/service/" is the operator
// service API (#1283): it is not open, it authenticates with the service token
// in its own route group instead of a user session.
var alwaysPublic = []string{"/auth/", "/api/v2/auth/", "/api/v2/service/", "/healthz", "/readyz", "/docs", "/openapi", "/ui/config"}

// authTokenCookie is the default name of the cookie the UI carries the JWT in
// (set by the login flow); auth.session_cookie_name renames it (#1405). JWTAuth
// accepts it as a fallback to the Authorization header.
const authTokenCookie = "_token"

// dataPlanePrefixes require a bearer token. Everything outside them — the static
// SPA bundle (/static/*) and the index.html shell served on client-side routes
// — is public: it carries no data, and the APIs it calls enforce auth. Without
// this, an unauthenticated first visit to "/" or "/static/*.js" would 401 and
// the browser could never load the app to reach the login screen.
var dataPlanePrefixes = []string{"/api/", "/ui/"}

func isPublic(path string) bool {
	for _, p := range alwaysPublic {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	for _, p := range dataPlanePrefixes {
		if strings.HasPrefix(path, p) {
			return false
		}
	}
	return true
}

// DevBypassAuth authenticates EVERY request as a fixed admin user, with no token
// required. It exists solely for `dexaflow lite` (the local, unsandboxed loop) so a
// developer reaches the UI without logging in. It must only be wired under the
// explicit dev opt-in (config auth.dev_no_auth); the server logs a prominent
// warning when it is active. NEVER enable this in production.
func DevBypassAuth() gin.HandlerFunc {
	devUser := &auth.User{ID: "leoflow-dev", TenantID: "default", Email: "dev@leoflow.local", Roles: []string{"admin"}}
	return func(c *gin.Context) {
		c.Set(contextKeyUser, devUser)
		c.Next()
	}
}

// JWTAuth validates the bearer token on protected routes and stores the user.
func JWTAuth(authn auth.Authenticator) gin.HandlerFunc {
	return jwtAuth(authn, nil, sessionCookieSpec{}, nil)
}

// jwtAuth is JWTAuth with an optional trusted-issuer bearer (#1468): a bearer
// in the Authorization header that is not a valid engine token is then also
// checked against the trusted issuer. A nil bearer leaves JWTAuth unchanged.
//
// session names the session cookie it reads (#1405) and, with hosts, clears.
// hosts, when not nil, binds a session cookie to its tenant's host (#1463): a
// cookie whose principal belongs to another tenant than the host names is
// refused with 401 and cleared. The Authorization header is not bound.
func jwtAuth(authn auth.Authenticator, bearer *issuerBearerAuth, session sessionCookieSpec, hosts *hostBinding) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isPublic(c.Request.URL.Path) {
			c.Next()
			return
		}
		tokens := candidateTokens(c, session.cookieName())
		if len(tokens) == 0 {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		// Try each candidate (bearer, then cookie) and accept the first that
		// validates. The cookie is a real fallback even when a bearer is present:
		// on a full-page refresh the Airflow UI loses its in-memory token and may
		// send a stale/empty bearer, but a valid _token cookie still authenticates
		// — otherwise the invalid bearer 401s and the UI bounces to login.
		// unavailable holds a backend failure seen while checking a candidate. A
		// later candidate may still authenticate, so it only decides the answer
		// once every candidate has been tried.
		fromHeader := bearerToken(c.GetHeader("Authorization")) != ""
		var unavailable, tenantless error
		wrongHost := false
		for i, token := range tokens {
			header := fromHeader && i == 0
			user, err := authenticateCandidate(c.Request.Context(), authn, bearer, token, header)
			if err == nil && user.TenantID == "" {
				// A principal that names no tenant cannot be scoped. The
				// authenticator already refuses one; this guard keeps any other
				// Authenticator implementation to the same rule.
				err = errors.Join(auth.ErrInvalidToken, auth.ErrTenantlessToken)
			}
			if errors.Is(err, auth.ErrTenantlessToken) {
				tenantless = err
			}
			if err == nil && !header && !hosts.allows(c, user.TenantID) {
				wrongHost = true
				continue
			}
			if err == nil {
				if user.Scoped && !scopeGated(c) {
					// ADR 0067: scopes are checked by the route's permission gate,
					// so a route without one is closed to scoped tokens.
					AbortProblem(c, http.StatusForbidden, "forbidden", scopedRouteRefusal)
					return
				}
				c.Set(contextKeyUser, user)
				c.Next()
				return
			}
			if authUnavailable(err) {
				unavailable = err
			}
		}
		refuseCandidates(c, session, unavailable, tenantless, wrongHost)
	}
}

// refuseCandidates answers a request none of whose candidate tokens
// authenticated: 503 when a backend failure kept a token from being checked,
// 401 clearing the cookie when only a session of another tenant's host was
// seen (#1463), 401 otherwise.
func refuseCandidates(c *gin.Context, session sessionCookieSpec, unavailable, tenantless error, wrongHost bool) {
	if unavailable != nil {
		// We could not DETERMINE whether the token is valid: the store was
		// unreachable. Answering 401 says something false about the caller and
		// is actively harmful: a well-behaved client reads 401 as "re-authenticate",
		// so it hammers /auth/token against the same dead database, and a wall of
		// 401s reads to whoever is on call as an auth incident rather than the
		// outage it is. 503 is the same answer the login path already gives for
		// the same cause (#843), and the cause stays in the log (#1087).
		// Retry-After gives the client a wait instead of a guess (#1100).
		abortAuthUnavailable(c, unavailable)
		return
	}
	if wrongHost {
		// A valid session of another tenant than this host's: clear it, so
		// the browser signs in again here instead of retrying a cookie that
		// can never work on this host.
		clearSessionCookie(c, session)
		AbortProblem(c, http.StatusUnauthorized, "unauthorized", "the session is not valid on this host")
		return
	}
	// The client hears the same "invalid token" either way; the log keeps the
	// tenantless cause so an operator can tell it from a bad signature.
	AbortProblemCause(c, http.StatusUnauthorized, "unauthorized", "invalid token", tenantless)
}

// authenticateCandidate authenticates one candidate token: as an engine token
// first, then, only for the Authorization header and only when the engine
// refused it as invalid, as a trusted-issuer bearer. The session cookie is
// always the engine's own, so it never reaches the issuer.
func authenticateCandidate(ctx context.Context, authn auth.Authenticator, bearer *issuerBearerAuth, token string, fromHeader bool) (*auth.User, error) {
	user, err := authn.Authenticate(ctx, token)
	if err == nil || bearer == nil || !fromHeader || !errors.Is(err, auth.ErrInvalidToken) {
		return user, err
	}
	return bearer.authenticate(ctx, token)
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(header, prefix) {
		return strings.TrimPrefix(header, prefix)
	}
	return ""
}

// candidateTokens returns the JWTs to try authenticating, in order: the
// Authorization bearer first, then the session cookie named cookieName. Both are
// returned (not just the first present) so JWTAuth can fall back to the cookie
// when the bearer is stale/invalid — the SPA-refresh case — rather than failing
// on the bearer alone. A cookie holding an API token or a CI session is skipped.
func candidateTokens(c *gin.Context, cookieName string) []string {
	var tokens []string
	if t := bearerToken(c.GetHeader("Authorization")); t != "" {
		tokens = append(tokens, t)
	}
	// An API token (ADR 0074) and a CI session (ADR 0092) are bearer
	// credentials for clients, never a browser session: in the cookie they
	// are ignored.
	if cookie, err := c.Request.Cookie(cookieName); err == nil && cookie.Value != "" && !auth.IsAPIToken(cookie.Value) && !auth.IsCISession(cookie.Value) {
		tokens = append(tokens, cookie.Value)
	}
	return tokens
}

// UserFromContext returns the authenticated user stored by JWTAuth.
func UserFromContext(c *gin.Context) (*auth.User, bool) {
	v, ok := c.Get(contextKeyUser)
	if !ok {
		return nil, false
	}
	u, ok := v.(*auth.User)
	return u, ok
}

// RequirePermission enforces an RBAC permission on a route. For a principal
// whose token carries scopes (ADR 0067), a read route also needs
// dexaflow:read, and any other route is refused: a write a scoped token may
// call names its scope with RequireScopedPermission, so a write nobody mapped
// fails closed.
func RequirePermission(action, resource string) gin.HandlerFunc {
	scope := ""
	if action == "read" {
		scope = auth.ScopeRead
	}
	return requirePermission(action, resource, scope)
}

// RequireScopedPermission is RequirePermission for a write route that a
// scoped token may call when it carries scope (ADR 0067). The role check
// still runs first: a scope narrows what roles allow and never widens it.
func RequireScopedPermission(action, resource, scope string) gin.HandlerFunc {
	return requirePermission(action, resource, scope)
}

// RequireScope is the scope check alone, for a protected route that checks no
// role permission (any signed-in user may call it) and that a scoped token
// may reach when it carries scope. Without it, jwtAuth refuses a scoped
// token on the route.
func RequireScope(scope string) gin.HandlerFunc {
	return permissionGate{scope: scope}.check
}

// RequireDeployPermission guards the routes that deploy a DAG: registering a
// version and setting the current version (ADR 0074). deploy:dag, the
// deployer role's grant, allows them, and so does write:dag, so a role that
// deployed before deploy:dag existed, a custom one included, still does. A
// scoped token also needs dexaflow:deploy. With viaIssuer
// (auth.trusted_issuer.deploy_audience, #1773) the principal must also be a
// trusted-issuer bearer of the deploy audience: a session, a password login
// token, an API token or a bearer of another audience is refused with 403,
// whatever its roles and scopes.
func RequireDeployPermission(viaIssuer bool) gin.HandlerFunc {
	return permissionGate{action: "deploy", alt: "write", resource: "dag", scope: auth.ScopeDeploy, viaIssuer: viaIssuer}.check
}

// RequireUndeployPermission guards deleting a DAG, which undeploys it. It
// needs write:dag. Without viaIssuer a scoped token is refused, as on every
// write no scope grants. With viaIssuer (#1773) it is a deploy decision like
// RequireDeployPermission: only a trusted-issuer bearer of the deploy
// audience with dexaflow:deploy may call it, so no undeploy skips the
// platform that owns deploys.
func RequireUndeployPermission(viaIssuer bool) gin.HandlerFunc {
	scope := ""
	if viaIssuer {
		scope = auth.ScopeDeploy
	}
	return permissionGate{action: "write", resource: "dag", scope: scope, viaIssuer: viaIssuer}.check
}

// The header a deploy refused for not coming through the trusted issuer
// carries (#1773), so a client tells it from a missing role or scope
// without parsing the detail.
const (
	deployViaHeader = "Dexaflow-Deploy-Via"
	deployViaIssuer = "trusted-issuer"
)

// deployViaIssuerRefusal is the detail of that refusal.
const deployViaIssuerRefusal = "deploys go through the trusted issuer: this server accepts a deploy only from a bearer token the trusted issuer minted for its deploy audience, with the dexaflow:deploy scope"

// RequireDeployRead guards a DAG read that a deploy needs: the DAG list, a
// DAG, its details and its versions (ADR 0074). It needs read:dag, and a
// scoped token needs dexaflow:read or dexaflow:deploy, so a deploy token
// reads what it deploys and none of the other reads its owner may have.
func RequireDeployRead() gin.HandlerFunc {
	return permissionGate{action: "read", resource: "dag", scope: auth.ScopeRead, altScope: auth.ScopeDeploy}.check
}

func requirePermission(action, resource, scope string) gin.HandlerFunc {
	return permissionGate{action: action, resource: resource, scope: scope}.check
}

// permissionGate checks the role permission (when action is set; alt, when
// set, is a second action on the same resource that also passes), then, for
// a scoped principal, the route's scope (or altScope, when set); an empty
// scope means no scope grants the route. Its check is a method value rather
// than a closure so jwtAuth can find it in a route's handler chain
// (scopeGated). viaIssuer, when set, also requires a trusted-issuer bearer
// of the deploy audience (RequireDeployPermission).
type permissionGate struct {
	action, alt, resource, scope, altScope string
	viaIssuer                              bool
}

// scopeAllows reports whether a scoped user's token carries the route's scope.
func (g permissionGate) scopeAllows(user *auth.User) bool {
	return user.HasScope(g.scope) || (g.altScope != "" && user.HasScope(g.altScope))
}

// allows reports whether user holds the gate's permission.
func (g permissionGate) allows(user *auth.User) bool {
	if user.HasPermission(g.action, g.resource) {
		return true
	}
	return g.alt != "" && user.HasPermission(g.alt, g.resource)
}

// missing names the permission a refused user lacks.
func (g permissionGate) missing() string {
	if g.alt == "" {
		return g.action + ":" + g.resource
	}
	return g.action + ":" + g.resource + " or " + g.alt + ":" + g.resource
}

func (g permissionGate) check(c *gin.Context) {
	user, ok := UserFromContext(c)
	if !ok {
		AbortProblem(c, http.StatusUnauthorized, "unauthorized", "no authenticated user")
		return
	}
	if g.action != "" && !g.allows(user) {
		AbortProblem(c, http.StatusForbidden, "forbidden", "missing permission "+g.missing())
		return
	}
	if g.viaIssuer && !user.DeployAudience {
		c.Header(deployViaHeader, deployViaIssuer)
		AbortProblem(c, http.StatusForbidden, "forbidden", deployViaIssuerRefusal)
		return
	}
	if user.Scoped && g.scope == "" {
		AbortProblem(c, http.StatusForbidden, "forbidden", scopedRouteRefusal)
		return
	}
	if user.Scoped && !g.scopeAllows(user) {
		AbortProblem(c, http.StatusForbidden, "forbidden", "missing scope "+g.scope)
		return
	}
	c.Next()
}

// scopedRouteRefusal is the detail of a 403 for a route no scope grants.
const scopedRouteRefusal = "a scoped token cannot call this route"

// permissionGateName is the name gin reports for permissionGate.check in a
// route's handler chain.
var permissionGateName = runtime.FuncForPC(reflect.ValueOf(permissionGate{}.check).Pointer()).Name()

// scopeGated reports whether the matched route runs a permissionGate, which
// is where a scoped principal's scopes are checked. jwtAuth refuses a scoped
// principal on any other route, so a route registered without a permission
// check fails closed for scoped tokens instead of ignoring their scopes.
func scopeGated(c *gin.Context) bool {
	return slices.Contains(c.HandlerNames(), permissionGateName)
}

// xcomEntriesSegment marks where the task instance route's XCom key starts.
// The Airflow UI percent-encodes the key, and a key may legitimately hold a
// slash, so an encoded separator after this segment is part of the key.
const xcomEntriesSegment = "/xcomEntries/"

// RejectEncodedPathSeparators refuses a request whose path carries a
// percent-encoded slash (%2F) or backslash (%5C) with 400. gin routes on the
// decoded path, so /auth%2Ftoken is served as /auth/token, while a reverse
// proxy or load balancer in front of the server matches its rules on the raw
// path and would let it through a rule written for /auth/token. Refusing the
// encoded form keeps both sides reading the same path. The XCom key in the task
// instance route is the one exception (see xcomEntriesSegment).
func RejectEncodedPathSeparators() gin.HandlerFunc {
	return func(c *gin.Context) {
		if hasEncodedPathSeparator(c.Request.URL.EscapedPath()) {
			AbortProblem(c, http.StatusBadRequest, "bad request", "the request path must not contain an encoded slash or backslash")
			return
		}
		c.Next()
	}
}

// hasEncodedPathSeparator reports whether escaped, a request's escaped path,
// holds %2F or %5C (any case) outside the XCom key of the task instance route.
func hasEncodedPathSeparator(escaped string) bool {
	checked := escaped
	if strings.HasPrefix(escaped, "/api/v2/dags/") {
		if i := strings.Index(escaped, xcomEntriesSegment); i >= 0 && strings.Contains(escaped[:i], "/taskInstances/") {
			checked = escaped[:i+len(xcomEntriesSegment)]
		}
	}
	lower := strings.ToLower(checked)
	return strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c")
}
