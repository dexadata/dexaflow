package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
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
	headerRequestID        = "X-Request-Id"
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
		switch {
		case status >= 500:
			logger.Error("http request", attrs...)
		case status >= 400:
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

// authTokenCookie is the cookie the Airflow 3.2.1 UI carries the JWT in (set by
// the login flow). JWTAuth accepts it as a fallback to the Authorization header.
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
	return func(c *gin.Context) {
		if isPublic(c.Request.URL.Path) {
			c.Next()
			return
		}
		tokens := candidateTokens(c)
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
		var unavailable, tenantless error
		for _, token := range tokens {
			user, err := authn.Authenticate(c.Request.Context(), token)
			if err == nil && user.TenantID == "" {
				// A principal that names no tenant cannot be scoped. The
				// authenticator already refuses one; this guard keeps any other
				// Authenticator implementation to the same rule.
				err = errors.Join(auth.ErrInvalidToken, auth.ErrTenantlessToken)
			}
			if errors.Is(err, auth.ErrTenantlessToken) {
				tenantless = err
			}
			if err == nil {
				c.Set(contextKeyUser, user)
				c.Next()
				return
			}
			if !errors.Is(err, auth.ErrInvalidToken) {
				unavailable = err
			}
		}
		if unavailable != nil {
			// We could not DETERMINE whether the token is valid — the store was
			// unreachable. Answering 401 says something false about the caller and
			// is actively harmful: a well-behaved client reads 401 as "re-authenticate",
			// so it hammers /auth/token against the same dead database, and a wall of
			// 401s reads to whoever is on call as an auth incident rather than the
			// outage it is. 503 is the same answer the login path already gives for
			// the same cause (#843), and the cause stays in the log (#1087).
			AbortProblemCause(c, http.StatusServiceUnavailable, "service unavailable", "authentication temporarily unavailable", unavailable)
			return
		}
		// The client hears the same "invalid token" either way; the log keeps the
		// tenantless cause so an operator can tell it from a bad signature.
		AbortProblemCause(c, http.StatusUnauthorized, "unauthorized", "invalid token", tenantless)
	}
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if strings.HasPrefix(header, prefix) {
		return strings.TrimPrefix(header, prefix)
	}
	return ""
}

// candidateTokens returns the JWTs to try authenticating, in order: the
// Authorization bearer first, then the Airflow UI's _token cookie. Both are
// returned (not just the first present) so JWTAuth can fall back to the cookie
// when the bearer is stale/invalid — the SPA-refresh case — rather than failing
// on the bearer alone.
func candidateTokens(c *gin.Context) []string {
	var tokens []string
	if t := bearerToken(c.GetHeader("Authorization")); t != "" {
		tokens = append(tokens, t)
	}
	if cookie, err := c.Request.Cookie(authTokenCookie); err == nil && cookie.Value != "" {
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

// RequirePermission enforces an RBAC permission on a route.
func RequirePermission(action, resource string) gin.HandlerFunc {
	return func(c *gin.Context) {
		user, ok := UserFromContext(c)
		if !ok {
			AbortProblem(c, http.StatusUnauthorized, "unauthorized", "no authenticated user")
			return
		}
		if !user.HasPermission(action, resource) {
			AbortProblem(c, http.StatusForbidden, "forbidden", "missing permission "+action+":"+resource)
			return
		}
		c.Next()
	}
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
