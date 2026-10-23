package api

import (
	"net/http"

	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// UIServer serves the embedded single-page app: static assets and an
// index.html shell that the SPA's client-side router falls back to. It is
// satisfied by internal/ui.Server. When nil, the server runs API-only and
// unknown paths return 404 instead of the SPA shell.
type UIServer interface {
	StaticHandler() http.Handler
	Index(w http.ResponseWriter, basePath string)
}

// sessionCookie returns the session cookie every handler and the auth
// middleware share, so setting, clearing and reading it agree on its name.
func (d Dependencies) sessionCookie() sessionCookieSpec {
	return sessionCookieSpec{name: d.Auth.SessionCookieName, insecure: d.Auth.SessionCookieInsecure}
}

// newIssuerBearerAuth wires the trusted-issuer bearer from deps, or returns nil
// when it is off.
func newIssuerBearerAuth(deps Dependencies) *issuerBearerAuth {
	if deps.Issuers.TrustedIssuerBearer == nil {
		return nil
	}
	return &issuerBearerAuth{issuer: deps.Issuers.TrustedIssuerBearer, users: deps.Issuers.TrustedIssuerUsers, audit: deps.Auth.AuthAudit, logger: deps.Observability.Logger}
}

// NewServer builds the gin engine with the full middleware chain, health and
// metrics endpoints, embedded Scalar docs, and the auth token endpoint. It
// returns an error wrapping ErrMissingDependency, and builds nothing, when
// deps leaves out a required dependency (see Dependencies.Validate).
func NewServer(deps Dependencies) (*gin.Engine, error) {
	if err := deps.Validate(); err != nil {
		return nil, err
	}
	return newServer(deps), nil
}

// newServer builds the engine NewServer returns from validated deps.
func newServer(deps Dependencies) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Trust only the explicitly-configured proxies for X-Forwarded-For; the empty
	// default trusts none, so c.ClientIP() is the direct peer and a spoofed XFF
	// cannot forge it (audit H1). An invalid CIDR fails SECURE — trust none — not
	// open.
	trustedProxies := deps.Server.TrustedProxies
	if err := r.SetTrustedProxies(deps.Server.TrustedProxies); err != nil {
		deps.Observability.Logger.Error("invalid trusted_proxies; trusting no proxy", "error", err)
		trustedProxies = nil
		if resetErr := r.SetTrustedProxies(nil); resetErr != nil {
			deps.Observability.Logger.Error("resetting trusted proxies to none failed", "error", resetErr)
		}
	}
	// The host binding (#1463) reads X-Forwarded-Host from the same proxies
	// the router trusts, and from none when that list was refused.
	binding := newHostBinding(deps.Issuers.TrustedIssuerHostPattern, trustedProxies)
	// Disable auto-redirect on trailing slash (#291): the 301 it writes
	// bypasses NoStoreOnVolatileRoutes, so the browser can cache the bare
	// 301 and short-circuit the next request. Bare paths register explicit
	// routes alongside their *action wildcards.
	r.RedirectTrailingSlash = false
	r.Use(gin.Recovery())
	r.Use(RequestID())
	r.Use(Observe(deps.Observability.Metrics, deps.Observability.Tracer))
	r.Use(StructuredLogger(deps.Observability.Logger))
	r.Use(RejectEncodedPathSeparators())
	r.Use(CORS(deps.Server.CORSOrigins))
	r.Use(NoStoreOnVolatileRoutes())
	if deps.Server.MaxPageLimit > 0 {
		r.Use(maxPageLimit(deps.Server.MaxPageLimit))
	}
	if deps.Server.MaxRequestBodyBytes > 0 || deps.Server.MaxUploadBodyBytes > 0 {
		r.Use(BodyLimit(deps.Server.MaxRequestBodyBytes, deps.Server.MaxUploadBodyBytes))
	}
	if deps.Server.GzipResponses {
		r.Use(GzipJSON())
	}
	if deps.Auth.DevNoAuth {
		r.Use(DevBypassAuth())
	} else {
		r.Use(jwtAuth(deps.Auth.Authenticator, newIssuerBearerAuth(deps), deps.sessionCookie(), binding))
	}

	r.GET("/healthz", livenessHandler)
	r.GET("/readyz", readinessHandler(deps.Observability.HealthChecks))
	// /metrics is intentionally NOT served here (audit H2): scraping lives on the
	// dedicated observability listener (ObservabilityHandler on the metrics port),
	// which every role runs, so metrics can be firewalled separately from the
	// public API/UI surface. deps.Registry is retained for that listener's wiring.
	registerDocs(r)

	// Under OIDC the credential path is break-glass-only (D8): every non-allowlisted
	// password login is rejected and audited, and an EMPTY allowlist means SSO-only
	// (all password logins rejected) — not ungated. In JWT mode the credential path
	// is the primary auth, so newBreakGlass returns nil (unchanged).
	bg := newBreakGlass(deps.Auth.OIDCSettings.BreakGlassEmails, deps.Auth.AuthAudit, deps.Auth.OIDCEnabled)
	r.POST("/auth/token", authTokenHandler(deps.Auth.Authenticator, deps.Auth.RateLimiter, deps.Auth.TokenTTLSecs, bg, deps.sessionCookie()))
	// Transparent renewal (aresta #5): a still-valid bearer is re-minted with a
	// fresh short TTL, bounded by max_lifetime. Under the public /api/v2/auth/
	// prefix like login, it is self-gating — only a valid signed bearer can be
	// renewed. Registered only when a renewer is wired. Rate-limited per client
	// IP on its own limiter (#801), never the login one, so renewal traffic
	// cannot spend an address's password-login budget.
	if deps.Auth.TokenRenewer != nil {
		renewLimiter := auth.NewRateLimiter(renewRateLimitPerMinute, time.Minute)
		r.POST("/api/v2/auth/token/renew", rateLimitByIP(renewLimiter), renewTokenHandler(deps.Auth.TokenRenewer, deps.Auth.TokenTTLSecs, deps.Auth.TokenMaxLifetimeSecs, deps.sessionCookie()))
	}
	// The Airflow UI redirects unauthenticated users to GET /api/v2/auth/login.
	r.GET("/api/v2/auth/logout", logoutHandler(deps.sessionCookie(), deps.Auth.ExternalSignOutURL))
	pageOpts := loginPageOpts{
		sso:            deps.Auth.OIDCFlow != nil,
		breakGlass:     len(deps.Auth.OIDCSettings.BreakGlassEmails) > 0,
		autoRedirect:   deps.Auth.OIDCSettings.AutoRedirect,
		externalSignIn: deps.Auth.ExternalSignInURL,
	}
	r.GET("/api/v2/auth/login", loginPageHandler(pageOpts))
	// What the CLI reads before it chooses how to sign in (#1704): the same
	// facts the sign-in page renders from.
	device := newCLIDeviceDeps(deps)
	r.GET(authMethodsPath, authMethodsHandler(pageOpts, deps.Auth.OIDCEnabled, device != nil))
	// Operator service API (#1283): registered only when a service token is
	// configured. /api/v2/service/ is outside the user-session middleware's
	// scope (alwaysPublic) because the group authenticates its own callers.
	if deps.Issuers.ServiceToken != "" {
		registerService(r, deps)
	}
	// Trusted-issuer handoff (#1284): registered only when an issuer is
	// configured, on its own per-IP limiter like the OIDC routes.
	if deps.Issuers.TrustedIssuer != nil {
		issuerLimiter := auth.NewRateLimiter(30, time.Minute)
		// Refusals, the rate limit's included, go back to the external sign-in
		// when one is configured (see issuerSessionDeps.refuse).
		handoff := issuerSessionDeps{
			issuer:    deps.Issuers.TrustedIssuer,
			users:     deps.Issuers.TrustedIssuerUsers,
			origins:   deps.Issuers.TrustedIssuerOrigins,
			audit:     deps.Auth.AuthAudit,
			jwtSecret: deps.Auth.JWTSecret,
			tokenTTL:  time.Duration(deps.Auth.TokenTTLSecs) * time.Second,
			logger:    deps.Observability.Logger,
			session:   deps.sessionCookie(),
			hosts:     binding,
			signIn:    issuerSignInTarget(deps.Auth.ExternalSignInURL),
		}
		r.POST("/api/v2/auth/session", rateLimitByIPWith(issuerLimiter, handoff.refuseRateLimited), issuerSessionHandler(handoff))
	}
	// OIDC/SSO login flow (D1): registered only when a provider was discovered at
	// boot. Both routes sit under the public /api/v2/auth/ prefix.
	if deps.Auth.OIDCFlow != nil {
		// Rate-limit the OIDC endpoints on their own per-IP limiter (separate from
		// the /auth/token budget), bounding state-generation / callback spam.
		// The CLI code exchange (#1704) spends the same budget.
		oidcLimiter := auth.NewRateLimiter(oidcRateLimitPerMinute, time.Minute)
		codes := newCLICodes(deps.Auth.JWTSecret)
		r.GET("/api/v2/auth/oidc/login", rateLimitByIP(oidcLimiter), oidcLoginHandler(deps.Auth.OIDCFlow, deps.Observability.Logger, deps.Auth.SessionCookieInsecure))
		// The token route spends the OIDC budget on a code exchange and the
		// device sign-in's own poll budget on a device poll, so it picks the
		// limiter once it has read the grant.
		if device != nil {
			device.codes = codes
			registerCLIDevice(r, device, oidcLimiter)
		}
		r.POST(cliTokenPath, cliTokenHandler(cliTokenDeps{codes: codes, logger: deps.Observability.Logger, audit: deps.Auth.AuthAudit, limiter: oidcLimiter, device: device}))
		r.POST(cliConsentPath, rateLimitByIP(oidcLimiter), cliConsentHandler(cliConsentDeps{
			codes: codes, audit: deps.Auth.AuthAudit, logger: deps.Observability.Logger, insecure: deps.Auth.SessionCookieInsecure,
		}))
		r.GET("/api/v2/auth/oidc/callback", rateLimitByIP(oidcLimiter), oidcCallbackHandler(oidcDeps{
			cliCodes:  codes,
			flow:      deps.Auth.OIDCFlow,
			users:     deps.Auth.OIDCUsers,
			audit:     deps.Auth.AuthAudit,
			cfg:       deps.Auth.OIDCSettings,
			jwtSecret: deps.Auth.JWTSecret,
			tokenTTL:  time.Duration(deps.Auth.TokenTTLSecs) * time.Second,
			logger:    deps.Observability.Logger,

			session: deps.sessionCookie(),
		}))
	}
	// Any signed-in user may read these; a scoped token needs dexaflow:read
	// (ADR 0067). The MCP's health resource reads all three.
	r.GET("/api/v2/monitor/health", RequireScope(auth.ScopeRead), monitorHealthHandler(deps.Observability.HealthChecks, deps.Observability.SchedulerHealth))
	r.GET("/api/v2/monitor/executor", RequireScope(auth.ScopeRead), monitorExecutorHandler(deps.Observability.ExecutorInfo))
	// What a deploy reads before it builds (#1704): the gate of the other
	// deploy reads, so a dexaflow:deploy token reaches it (ADR 0074).
	r.GET("/api/v2/deployDefaults", RequireDeployRead(), deployDefaultsHandler(deps.Execution.DeployDefaults, deps.Issuers.CIOIDC))
	// CI OIDC exchange (ADR 0092): registered only when an issuer is
	// configured, under the public /api/v2/auth/ prefix.
	if ci := newCITokenDeps(deps); ci != nil {
		r.POST(ciTokenPath, ciTokenHandler(ci))
	}

	registerResources(r, deps)
	registerUI(r, deps.Auth.TokenTTLSecs, deps.UI.InstanceName, deps.UI.AutoRefreshIntervalSeconds, deps.UI.Theme, deps.sessionCookie())
	registerUIViews(r, deps)
	registerUIStructure(r, deps.Stores.Specs)
	registerUISummaries(r, deps.Stores.TaskSummary, deps.UI.ETagRevalidation)
	registerUIGantt(r, deps.Stores.Tasks)
	registerUITasks(r, deps.Stores.Specs, deps.Stores.DagSources)
	registerUIDashboard(r, deps.Stores.DashboardStats)
	registerUIAudit(r, deps.Stores.AuditLog)
	registerUIVariables(r, deps.Stores.Variables)
	registerUsers(r, deps.Stores.Users, deps.Stores.UserAudit)
	registerAPITokens(r, deps.Stores.APITokens, deps.Auth.AuthAudit)
	registerAPITokensPage(r, deps, binding)
	r.GET("/api/v2/plugins", pluginsHandler(deps.Stores.APITokens != nil))
	registerUIConnections(r, deps.Stores.Connections, deps.Stores.ConnectionTest)
	registerUIPools(r, deps.Stores.Pools, deps.Server.Edition == "pro", deps.Server.PoolsReadOnly)
	registerUIFavorites(r, deps.Stores.Favorites)
	registerImportErrors(r, deps.Stores.ImportErrors)
	registerDagTags(r, deps.Stores.Dags)
	registerIDE(r, deps.UI.Workspace, deps.UI.MonacoDir, deps.UI.ExamplesFS)
	registerUIStubs(r)
	registerAPIStubs(r)
	registerAirflow33Unserved(r)
	if deps.UI.SPA != nil {
		static := gin.WrapH(http.StripPrefix("/static", deps.UI.SPA.StaticHandler()))
		r.GET("/static/*filepath", static)
		r.HEAD("/static/*filepath", static)
	}
	r.NoRoute(uiNoRoute(deps.UI.SPA, deps.Auth.Authenticator, deps.Auth.DevNoAuth, deps.sessionCookie(), binding))

	return r
}
