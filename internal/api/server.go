package api

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"

	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/oidc"
)

// UIServer serves the embedded single-page app: static assets and an
// index.html shell that the SPA's client-side router falls back to. It is
// satisfied by internal/ui.Server. When nil, the server runs API-only and
// unknown paths return 404 instead of the SPA shell.
type UIServer interface {
	StaticHandler() http.Handler
	Index(w http.ResponseWriter, basePath string)
}

// Dependencies bundles everything the HTTP server needs.
type Dependencies struct {
	Logger        *slog.Logger
	Authenticator auth.Authenticator
	RateLimiter   *auth.RateLimiter
	Registry      *prometheus.Registry
	Metrics       Metrics
	Tracer        trace.Tracer
	HealthChecks  map[string]HealthChecker
	CORSOrigins   []string
	// TrustedProxies is the set of proxy IPs/CIDRs whose X-Forwarded-For header
	// gin will honor when resolving c.ClientIP(). Empty/nil trusts NO proxy, so
	// ClientIP is the direct peer and a spoofed XFF cannot forge the client IP
	// (audit H1). A Pro deployment behind an ingress sets this to the ingress
	// CIDR so per-client rate-limiting and audit see the real client.
	TrustedProxies []string
	TokenTTLSecs   int
	// GzipResponses (server.gzip_responses) gzips JSON and NDJSON responses of
	// 1 KB or more for clients that accept it; streams are never compressed.
	// False (the default) sends every body as identity.
	GzipResponses bool
	// MaxPageLimit (server.max_page_limit) caps the limit of every list
	// endpoint and the dag_runs_limit of /ui/dags. Non-positive (the default)
	// leaves them uncapped.
	MaxPageLimit int
	// TokenRenewer re-mints a still-valid user bearer with a fresh short TTL so a
	// long CLI/dev session need not re-login every TokenTTLSecs (aresta #5). Nil
	// leaves the renew route unregistered (renewal simply unavailable). In practice
	// it is the same *auth.JWTAuthenticator as Authenticator.
	TokenRenewer TokenRenewer
	// TokenMaxLifetimeSecs is the hard ceiling on a renewed session's total age
	// since first login; past it, renewal is refused and the user must
	// re-authenticate. Non-positive disables the ceiling.
	TokenMaxLifetimeSecs int
	// InstanceName is shown in the UI navbar (Airflow's instance_name). Empty
	// falls back to "Dexaflow"; `dexaflow lite` sets it to mark the DEV environment.
	InstanceName string
	// UIAutoRefreshIntervalSeconds controls the SPA's polling cadence for DAG /
	// DagRun / task-instance state refresh (Airflow's auto_refresh_interval).
	// Non-positive (the zero default) falls back to DefaultUIAutoRefreshIntervalSeconds
	// (30s, production-safe). `dexaflow lite` sets it to ~5s for a snappy inner loop.
	UIAutoRefreshIntervalSeconds int
	// UITheme is the Chakra theme /ui/config hands the UI (Airflow's `[api]
	// theme`: tokens, globalCss, icon, icon_dark_mode), already validated as a
	// JSON object at boot. Nil serves null, the stock look (#1289).
	UITheme json.RawMessage
	// UIETagRevalidation (ui.etag_revalidation) relaxes no-store to
	// "private, no-cache" with Vary: Authorization, Cookie on the routes that
	// compute an ETag, so the browser can revalidate them and get a 304. False
	// (the default) keeps no-store on every UI route.
	UIETagRevalidation bool
	// DevNoAuth replaces JWT auth with a dev-only bypass that authenticates every
	// request as an admin (no login). It is for `dexaflow lite` only and must never
	// be set in production. See DevBypassAuth.
	DevNoAuth bool
	// Edition marks the running edition ("pro", "lite", or empty). It gates
	// Pro-only surfaces: named-pool CRUD is registered as real endpoints only when
	// Edition == "pro" (ADR 0053), otherwise the Pools screen gets the graceful
	// empty-collection stub, matching how the scheduler's pool gate is Pro-gated.
	Edition string
	// PoolsReadOnly is server.pools_read_only: the pool API serves reads only and
	// every create, resize and delete answers 403 with PoolsReadOnlyDetail, for
	// every role including tenant admin. False keeps the write:pool-gated CRUD.
	PoolsReadOnly bool
	// ResourceUnit is executor.unit (ADR 0066 §3). When set, registering a DAG
	// whose task declares more than pool_slots x unit answers 400 naming the
	// size it needs (under enforce: warn it is accepted, logged and counted).
	// Nil: no unit, no check.
	ResourceUnit *domain.ResourceUnit
	// UnitMisfits counts a task registered under executor.unit.enforce=warn
	// although it does not fit its size. Nil: not counted.
	UnitMisfits UnitMisfitRecorder
	// SourceModeImage is the runtime image when execution.source_mode is on
	// (ADR 0067 §3), "" when it is off. Registering a version on that image
	// answers 400 when its source is empty or over domain.MaxSourceModeBytes.
	SourceModeImage string

	// Resource repositories. Routes for nil repositories are not registered.
	Dags           DagRepository
	DagRuns        DagRunRepository
	Tasks          TaskInstanceRepository
	Versions       DagVersionRepository
	Xcoms          XComReader
	Logs           LogReader
	Specs          DagSpecReader
	LatestRuns     DagLatestRunsReader
	TaskSummary    TaskSummaryReader
	DagVersions    DagVersionLister
	DashboardStats DashboardStatsReader
	AuditLog       AuditLogReader
	Variables      VariableStore
	Users          UserStore
	UserAudit      UserAuditWriter
	Connections    ConnectionStore
	ConnectionTest ConnectionTester
	Pools          PoolStore
	Favorites      FavoriteStore
	ImportErrors   ImportErrorStore
	Audit          AuditWriter
	ExecutorInfo   ExecutorInfo

	// Workspace backs the Lite web editor (ADR 0025). When nil the editor's
	// filesystem API is not registered (Production, or Lite without a workspace).
	Workspace WorkspaceFS

	// MonacoDir is the directory holding the pinned Monaco bundle that
	// `dexaflow setup` fetched; the editor page is served Monaco from it. Empty or
	// missing makes the page show a setup hint instead of a broken editor.
	MonacoDir string

	// ExamplesFS backs the IDE's "Download examples" button — typically the
	// `embed.FS` shipped from the leoflow root package. Nil disables the button.
	ExamplesFS fs.FS

	// SchedulerHealth reports the scheduler's heartbeat for /monitor/health.
	// When nil the component reports healthy (single-process role assumption).
	SchedulerHealth Heartbeater

	// UI serves the embedded SPA. When nil the server is API-only.
	UI UIServer

	// OIDC wiring (registered only when OIDCFlow is non-nil — provider: oidc).
	// The JWT authenticator above stays the request-path verifier in both modes.
	//
	// OIDCFlow is the discovered Authorization Code + PKCE flow; nil in JWT mode,
	// in which case the /api/v2/auth/oidc/* routes are not registered.
	OIDCFlow *oidc.Flow
	// OIDCEnabled is true when auth.provider is "oidc". It makes the credential
	// path break-glass-only: with OIDC on, an empty break-glass allowlist means
	// SSO-only (every password login rejected), NOT ungated — the secure default.
	OIDCEnabled bool
	// OIDCSettings carries the role mappings, JIT policy, default_role, and
	// break-glass allowlist the login flow and the credential gate read.
	OIDCSettings config.OIDCSection
	// ExternalSignInURL and ExternalSignOutURL are auth.external_signin_url and
	// auth.external_signout_url (#1288): the operator's own sign-in and
	// sign-out, used in place of Dexaflow's pages. Empty keeps Dexaflow's.
	ExternalSignInURL  string
	ExternalSignOutURL string
	// OIDCUsers resolves and JIT-provisions OIDC identities (the storage repo).
	OIDCUsers OIDCUserStore
	// AuthAudit records authentication events (login, tenant-pin rejection, JIT,
	// break-glass, logout) to the audit sink.
	AuthAudit AuthAuditWriter
	// TrustedIssuer, when set, enables POST /api/v2/auth/session: a token from
	// the operator's trusted issuer opens a UI session for a linked user
	// (#1284). TrustedIssuerUsers resolves those users (the storage repo).
	TrustedIssuer      TrustedIssuer
	TrustedIssuerUsers TrustedIssuerUserStore
	// TrustedIssuerBearer, when set, also accepts the trusted issuer's tokens
	// for a bearer audience as the Authorization bearer of any protected
	// request (#1468). It resolves users through TrustedIssuerUsers.
	TrustedIssuerBearer TrustedIssuerBearer
	// TrustedIssuerOrigins are the only Origins a handoff may be posted from
	// (scheme://host[:port]), so another site cannot sign a browser in.
	TrustedIssuerOrigins []string
	// ServiceToken, when set, enables the operator service API under
	// /api/v2/service/ (#1283), authenticated by this bearer token instead of a
	// user session. ServiceTenants is its storage (the repo).
	ServiceToken   string
	ServiceTenants ServiceTenantStore
	// ServiceAllowedTenants are the tenants the service API may link issuer
	// users in: auth.trusted_issuer.allowed_tenants, where "*" allows all.
	ServiceAllowedTenants []string
	// JWTSecret is the HS256 secret the OIDC callback mints the app's _token with.
	JWTSecret string
	// SessionCookieInsecure drops the Secure attribute from the session and OIDC
	// state cookies (auth.session_cookie_insecure). It is stated negatively so the
	// zero value is the hardened one: a caller that forgets the field gets Secure.
	// See cookieSecure for why this is a setting and not derived from the request.
	SessionCookieInsecure bool
}

// newIssuerBearerAuth wires the trusted-issuer bearer from deps, or returns nil
// when it is off.
func newIssuerBearerAuth(deps Dependencies) *issuerBearerAuth {
	if deps.TrustedIssuerBearer == nil {
		return nil
	}
	return &issuerBearerAuth{issuer: deps.TrustedIssuerBearer, users: deps.TrustedIssuerUsers, audit: deps.AuthAudit, logger: deps.Logger}
}

// NewServer builds the gin engine with the full middleware chain, health and
// metrics endpoints, embedded Scalar docs, and the auth token endpoint.
func NewServer(deps Dependencies) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	// Trust only the explicitly-configured proxies for X-Forwarded-For; the empty
	// default trusts none, so c.ClientIP() is the direct peer and a spoofed XFF
	// cannot forge it (audit H1). An invalid CIDR fails SECURE — trust none — not
	// open.
	if err := r.SetTrustedProxies(deps.TrustedProxies); err != nil {
		deps.Logger.Error("invalid trusted_proxies; trusting no proxy", "error", err)
		if resetErr := r.SetTrustedProxies(nil); resetErr != nil {
			deps.Logger.Error("resetting trusted proxies to none failed", "error", resetErr)
		}
	}
	// Disable auto-redirect on trailing slash (#291): the 301 it writes
	// bypasses NoStoreOnVolatileRoutes, so the browser can cache the bare
	// 301 and short-circuit the next request. Bare paths register explicit
	// routes alongside their *action wildcards.
	r.RedirectTrailingSlash = false
	r.Use(gin.Recovery())
	r.Use(RequestID())
	r.Use(Observe(deps.Metrics, deps.Tracer))
	r.Use(StructuredLogger(deps.Logger))
	r.Use(RejectEncodedPathSeparators())
	r.Use(CORS(deps.CORSOrigins))
	r.Use(NoStoreOnVolatileRoutes())
	if deps.MaxPageLimit > 0 {
		r.Use(maxPageLimit(deps.MaxPageLimit))
	}
	if deps.GzipResponses {
		r.Use(GzipJSON())
	}
	if deps.DevNoAuth {
		r.Use(DevBypassAuth())
	} else {
		r.Use(jwtAuth(deps.Authenticator, newIssuerBearerAuth(deps)))
	}

	r.GET("/healthz", livenessHandler)
	r.GET("/readyz", readinessHandler(deps.HealthChecks))
	// /metrics is intentionally NOT served here (audit H2): scraping lives on the
	// dedicated observability listener (ObservabilityHandler on the metrics port),
	// which every role runs, so metrics can be firewalled separately from the
	// public API/UI surface. deps.Registry is retained for that listener's wiring.
	registerDocs(r)

	// Under OIDC the credential path is break-glass-only (D8): every non-allowlisted
	// password login is rejected and audited, and an EMPTY allowlist means SSO-only
	// (all password logins rejected) — not ungated. In JWT mode the credential path
	// is the primary auth, so newBreakGlass returns nil (unchanged).
	bg := newBreakGlass(deps.OIDCSettings.BreakGlassEmails, deps.AuthAudit, deps.OIDCEnabled)
	r.POST("/auth/token", authTokenHandler(deps.Authenticator, deps.RateLimiter, deps.TokenTTLSecs, bg, deps.SessionCookieInsecure))
	// Transparent renewal (aresta #5): a still-valid bearer is re-minted with a
	// fresh short TTL, bounded by max_lifetime. Under the public /api/v2/auth/
	// prefix like login, it is self-gating — only a valid signed bearer can be
	// renewed. Registered only when a renewer is wired. Rate-limited per client
	// IP on its own limiter (#801), never the login one, so renewal traffic
	// cannot spend an address's password-login budget.
	if deps.TokenRenewer != nil {
		renewLimiter := auth.NewRateLimiter(renewRateLimitPerMinute, time.Minute)
		r.POST("/api/v2/auth/token/renew", rateLimitByIP(renewLimiter), renewTokenHandler(deps.TokenRenewer, deps.TokenTTLSecs, deps.TokenMaxLifetimeSecs))
	}
	// The Airflow UI redirects unauthenticated users to GET /api/v2/auth/login.
	r.GET("/api/v2/auth/logout", logoutHandler(deps.SessionCookieInsecure, deps.ExternalSignOutURL))
	r.GET("/api/v2/auth/login", loginPageHandler(loginPageOpts{
		sso:            deps.OIDCFlow != nil,
		breakGlass:     len(deps.OIDCSettings.BreakGlassEmails) > 0,
		autoRedirect:   deps.OIDCSettings.AutoRedirect,
		externalSignIn: deps.ExternalSignInURL,
	}))
	// Operator service API (#1283): registered only when a service token is
	// configured. /api/v2/service/ is outside the user-session middleware's
	// scope (alwaysPublic) because the group authenticates its own callers.
	if deps.ServiceToken != "" {
		registerService(r, deps)
	}
	// Trusted-issuer handoff (#1284): registered only when an issuer is
	// configured, on its own per-IP limiter like the OIDC routes.
	if deps.TrustedIssuer != nil {
		issuerLimiter := auth.NewRateLimiter(30, time.Minute)
		// Refusals, the rate limit's included, go back to the external sign-in
		// when one is configured (see issuerSessionDeps.refuse).
		handoff := issuerSessionDeps{
			issuer:          deps.TrustedIssuer,
			users:           deps.TrustedIssuerUsers,
			origins:         deps.TrustedIssuerOrigins,
			audit:           deps.AuthAudit,
			jwtSecret:       deps.JWTSecret,
			tokenTTL:        time.Duration(deps.TokenTTLSecs) * time.Second,
			logger:          deps.Logger,
			insecureCookies: deps.SessionCookieInsecure,
			signIn:          issuerSignInTarget(deps.ExternalSignInURL),
		}
		r.POST("/api/v2/auth/session", rateLimitByIPWith(issuerLimiter, handoff.refuseRateLimited), issuerSessionHandler(handoff))
	}
	// OIDC/SSO login flow (D1): registered only when a provider was discovered at
	// boot. Both routes sit under the public /api/v2/auth/ prefix.
	if deps.OIDCFlow != nil {
		// Rate-limit the OIDC endpoints on their own per-IP limiter (separate from
		// the /auth/token budget), bounding state-generation / callback spam.
		oidcLimiter := auth.NewRateLimiter(30, time.Minute)
		r.GET("/api/v2/auth/oidc/login", rateLimitByIP(oidcLimiter), oidcLoginHandler(deps.OIDCFlow, deps.Logger, deps.SessionCookieInsecure))
		r.GET("/api/v2/auth/oidc/callback", rateLimitByIP(oidcLimiter), oidcCallbackHandler(oidcDeps{
			flow:      deps.OIDCFlow,
			users:     deps.OIDCUsers,
			audit:     deps.AuthAudit,
			cfg:       deps.OIDCSettings,
			jwtSecret: deps.JWTSecret,
			tokenTTL:  time.Duration(deps.TokenTTLSecs) * time.Second,
			logger:    deps.Logger,

			insecureCookies: deps.SessionCookieInsecure,
		}))
	}
	r.GET("/api/v2/monitor/health", monitorHealthHandler(deps.HealthChecks, deps.SchedulerHealth))
	r.GET("/api/v2/monitor/executor", monitorExecutorHandler(deps.ExecutorInfo))

	registerResources(r, deps)
	registerUI(r, deps.TokenTTLSecs, deps.InstanceName, deps.UIAutoRefreshIntervalSeconds, deps.UITheme)
	registerUIViews(r, deps)
	registerUIStructure(r, deps.Specs)
	registerUISummaries(r, deps.TaskSummary, deps.UIETagRevalidation)
	registerUITasks(r, deps.Specs)
	registerUIDashboard(r, deps.DashboardStats)
	registerUIAudit(r, deps.AuditLog)
	registerUIVariables(r, deps.Variables)
	registerUsers(r, deps.Users, deps.UserAudit)
	registerUIConnections(r, deps.Connections, deps.ConnectionTest)
	registerUIPools(r, deps.Pools, deps.Edition == "pro", deps.PoolsReadOnly)
	registerUIFavorites(r, deps.Favorites)
	registerImportErrors(r, deps.ImportErrors)
	registerIDE(r, deps.Workspace, deps.MonacoDir, deps.ExamplesFS)
	registerUIStubs(r)
	registerAPIStubs(r)
	if deps.UI != nil {
		static := gin.WrapH(http.StripPrefix("/static", deps.UI.StaticHandler()))
		r.GET("/static/*filepath", static)
		r.HEAD("/static/*filepath", static)
	}
	r.NoRoute(uiNoRoute(deps.UI, deps.Authenticator, deps.DevNoAuth))

	return r
}
