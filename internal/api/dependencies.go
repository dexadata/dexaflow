package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/oidc"
)

// Dependencies bundles everything the HTTP server needs, one group per
// concern. NewServer refuses a value that leaves out a dependency a route
// it registers cannot run without; see Validate.
type Dependencies struct {
	Observability ObservabilityDeps
	Server        ServerDeps
	Auth          AuthDeps
	Issuers       IssuerDeps
	UI            UIDeps
	Execution     ExecutionDeps
	Stores        StoreDeps
}

// ObservabilityDeps are the logger, metrics, tracing and health reporting of
// the server.
type ObservabilityDeps struct {
	// Logger receives one line per request and the server's own warnings.
	// Required.
	Logger   *slog.Logger
	Registry *prometheus.Registry
	Metrics  Metrics
	Tracer   trace.Tracer
	// HealthChecks back /readyz and /api/v2/monitor/health.
	HealthChecks map[string]HealthChecker
	// SchedulerHealth reports the scheduler's heartbeat for /monitor/health.
	// When nil the component reports healthy (single-process role assumption).
	SchedulerHealth Heartbeater
	// ProbeBudget (server.probe_budget) bounds /readyz and the dependency
	// checks of /api/v2/monitor/health. Non-positive means 2s.
	ProbeBudget time.Duration
	// ExecutorInfo is served on /api/v2/monitor/executor.
	ExecutorInfo ExecutorInfo
}

// ServerDeps are the server-wide HTTP settings (server.* in the config) and
// the edition gates.
type ServerDeps struct {
	CORSOrigins []string
	// TrustedProxies is the set of proxy IPs/CIDRs whose X-Forwarded-For header
	// gin will honor when resolving c.ClientIP(). Empty/nil trusts NO proxy, so
	// ClientIP is the direct peer and a spoofed XFF cannot forge the client IP
	// (audit H1). A Pro deployment behind an ingress sets this to the ingress
	// CIDR so per-client rate-limiting and audit see the real client.
	TrustedProxies []string
	// GzipResponses (server.gzip_responses) gzips JSON and NDJSON responses of
	// 1 KB or more for clients that accept it; streams are never compressed.
	// False (the default) sends every body as identity.
	GzipResponses bool
	// MaxPageLimit (server.max_page_limit) caps the limit of every list
	// endpoint and the dag_runs_limit of /ui/dags. Non-positive (the default)
	// leaves them uncapped.
	MaxPageLimit int
	// MaxRequestBodyBytes (server.max_request_body_bytes) and
	// MaxUploadBodyBytes (server.max_upload_body_bytes) cap request bodies,
	// the second on the routes that take a DAG's code or spec (BodyLimit).
	// Non-positive (the default) leaves those routes unlimited.
	MaxRequestBodyBytes int64
	MaxUploadBodyBytes  int64
	// Pools registers the named-pool CRUD as real endpoints (ADR 0053). The
	// server sets it on the cluster edition, the same condition as the
	// scheduler's pool gate; otherwise the Pools screen gets the graceful
	// empty-collection stub.
	Pools bool
	// PoolsReadOnly is server.pools_read_only: the pool API serves reads only and
	// every create, resize and delete answers 403 with PoolsReadOnlyDetail, for
	// every role including tenant admin. False keeps the write:pool-gated CRUD.
	PoolsReadOnly bool
}

// AuthDeps are the user sign-in: the request authenticator, the password and
// OIDC login flows, and the session cookie.
type AuthDeps struct {
	// Authenticator verifies every request's session and issues the token of
	// POST /auth/token. Required, also under DevNoAuth, which still serves
	// that route.
	Authenticator auth.Authenticator
	// RateLimiter caps the failed logins of POST /auth/token per client.
	// Required.
	RateLimiter  *auth.RateLimiter
	TokenTTLSecs int
	// TokenRenewer re-mints a still-valid user bearer with a fresh short TTL so a
	// long CLI/dev session need not re-login every TokenTTLSecs (aresta #5). Nil
	// leaves the renew route unregistered (renewal simply unavailable). In practice
	// it is the same *auth.JWTAuthenticator as Authenticator.
	TokenRenewer TokenRenewer
	// TokenMaxLifetimeSecs is the hard ceiling on a renewed session's total age
	// since first login; past it, renewal is refused and the user must
	// re-authenticate. Non-positive disables the ceiling.
	TokenMaxLifetimeSecs int
	// DevNoAuth replaces JWT auth with a dev-only bypass that authenticates every
	// request as an admin (no login). It is for `dexaflow lite` only and must never
	// be set in production. See DevBypassAuth.
	DevNoAuth bool
	// JWTSecret is the HS256 secret the OIDC callback mints the app's _token with.
	JWTSecret string
	// SessionCookieInsecure drops the Secure attribute from the session and OIDC
	// state cookies (auth.session_cookie_insecure). It is stated negatively so the
	// zero value is the hardened one: a caller that forgets the field gets Secure.
	// See cookieSecure for why this is a setting and not derived from the request.
	SessionCookieInsecure bool
	// SessionCookieName is the session cookie's name (auth.session_cookie_name,
	// #1405). Empty is _token, the name every earlier release used.
	SessionCookieName string

	// OIDC wiring (registered only when OIDCFlow is non-nil, provider: oidc).
	// The JWT authenticator above stays the request-path verifier in both modes.
	//
	// OIDCFlow is the discovered Authorization Code + PKCE flow; nil in JWT mode,
	// in which case the /api/v2/auth/oidc/* routes are not registered.
	OIDCFlow *oidc.Flow
	// OIDCEnabled is true when auth.provider is "oidc". It makes the credential
	// path break-glass-only: with OIDC on, an empty break-glass allowlist means
	// SSO-only (every password login rejected), NOT ungated: the secure default.
	OIDCEnabled bool
	// OIDCSettings carries the role mappings, JIT policy, default_role, and
	// break-glass allowlist the login flow and the credential gate read.
	OIDCSettings OIDCSettings
	// ExternalSignInURL and ExternalSignOutURL are auth.external_signin_url and
	// auth.external_signout_url (#1288): the operator's own sign-in and
	// sign-out, used in place of Dexaflow's pages. Empty keeps Dexaflow's.
	ExternalSignInURL  string
	ExternalSignOutURL string
	// OIDCUsers resolves and JIT-provisions OIDC identities (the storage repo).
	OIDCUsers OIDCUserStore
	// CLIDevices keeps the device sign-ins of the CLI (ADR 0081, the storage
	// repo). With single sign-on configured it serves `dexaflow auth login
	// --device`; nil leaves the device sign-in unserved.
	CLIDevices CLIDeviceStore
	// AuthAudit records authentication events (login, tenant-pin rejection, JIT,
	// break-glass, logout) to the audit sink.
	AuthAudit AuthAuditWriter
}

// IssuerDeps are the callers whose credentials come from outside the login
// flow: the trusted issuer, the CI OIDC issuers and the operator service
// token.
type IssuerDeps struct {
	// TrustedIssuer, when set, enables POST /api/v2/auth/session: a token from
	// the operator's trusted issuer opens a UI session for a linked user
	// (#1284). TrustedIssuerUsers resolves those users (the storage repo).
	TrustedIssuer      TrustedIssuer
	TrustedIssuerUsers TrustedIssuerUserStore
	// TrustedIssuerBearer, when set, also accepts the trusted issuer's tokens
	// for a bearer audience as the Authorization bearer of any protected
	// request (#1468). It resolves users through TrustedIssuerUsers.
	TrustedIssuerBearer TrustedIssuerBearer
	// DeployViaIssuer is set when auth.trusted_issuer.deploy_audience is
	// (#1773): the deploy routes and deleting a DAG then take only a
	// TrustedIssuerBearer token of that audience. With no TrustedIssuerBearer every deploy is refused,
	// so a wiring mistake fails closed.
	DeployViaIssuer bool
	// TrustedIssuerOrigins are the only Origins a handoff may be posted from
	// (scheme://host[:port]), so another site cannot sign a browser in.
	TrustedIssuerOrigins []string
	// TrustedIssuerHostPattern is auth.trusted_issuer.host_pattern (#1463),
	// such as "{tenant}.example.com": when set, a handoff and a session cookie
	// are accepted only on the host of their tenant. Empty binds nothing.
	TrustedIssuerHostPattern string
	// CIOIDC, when set, verifies the OIDC tokens of the configured CI
	// issuers (ADR 0092, auth.ci_oidc): POST /api/v2/auth/ciToken exchanges
	// one for a session, and GET /api/v2/deployDefaults lists the caller's
	// tenant's issuers. Nil leaves both off. CIOIDCRoles reads the role a
	// rule names (the storage repo), CIOIDCSessionTTL is the session's
	// lifetime (an hour when zero), and CIOIDCExchanges counts the
	// exchanges (nil: not counted).
	CIOIDC           CIOIDCVerifier
	CIOIDCRoles      auth.RolePermissionStore
	CIOIDCSessionTTL time.Duration
	CIOIDCExchanges  CIOIDCExchangeRecorder
	// ServiceToken, when set, enables the operator service API under
	// /api/v2/service/ (#1283), authenticated by this bearer token instead of a
	// user session. ServiceTenants is its storage (the repo).
	ServiceToken   string
	ServiceTenants ServiceTenantStore
	// ServiceAllowedTenants are the tenants the service API may link issuer
	// users in: auth.trusted_issuer.allowed_tenants, where "*" allows all.
	ServiceAllowedTenants []string
}

// UIDeps are the embedded web UI and the Lite editor.
type UIDeps struct {
	// InstanceName is shown in the UI navbar (Airflow's instance_name). Empty
	// falls back to "Dexaflow"; `dexaflow lite` sets it to mark the DEV environment.
	InstanceName string
	// AutoRefreshIntervalSeconds controls the SPA's polling cadence for DAG /
	// DagRun / task-instance state refresh (Airflow's auto_refresh_interval).
	// Non-positive (the zero default) falls back to DefaultUIAutoRefreshIntervalSeconds
	// (30s, production-safe). `dexaflow lite` sets it to ~5s for a snappy inner loop.
	AutoRefreshIntervalSeconds int
	// Theme is the Chakra theme /ui/config hands the UI (Airflow's `[api]
	// theme`: tokens, globalCss, icon, icon_dark_mode), already validated as a
	// JSON object at boot. Nil serves null, the stock look (#1289).
	Theme json.RawMessage
	// ETagRevalidation (ui.etag_revalidation) relaxes no-store to
	// "private, no-cache" with Vary: Authorization, Cookie on the routes that
	// compute an ETag, so the browser can revalidate them and get a 304. False
	// (the default) keeps no-store on every UI route.
	ETagRevalidation bool

	// SPA serves the embedded SPA. When nil the server is API-only.
	SPA UIServer

	// Workspace backs the Lite web editor (ADR 0025). When nil the editor's
	// filesystem API is not registered (Production, or Lite without a workspace).
	Workspace WorkspaceFS

	// MonacoDir is the directory holding the pinned Monaco bundle that
	// `dexaflow setup` fetched; the editor page is served Monaco from it. Empty or
	// missing makes the page show a setup hint instead of a broken editor.
	MonacoDir string

	// ExamplesFS backs the IDE's "Download examples" button, typically the
	// `embed.FS` shipped from the leoflow root package. Nil disables the button.
	ExamplesFS fs.FS
}

// ExecutionDeps are the checks a DAG registration runs against the
// execution and deploy settings, and what its answer reports about them.
type ExecutionDeps struct {
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
	// RequireDigest is deploy.require_digest (#1703): what registering a
	// version, or making one current again, does with an image not pinned by
	// a full sha256 digest. Off (and the zero value) accepts it; warn accepts
	// it with a warning in the answer, a log line and a count; enforce
	// answers 400.
	RequireDigest domain.DigestRequirement
	// UnpinnedImages counts the unpinned images deploy.require_digest warned
	// about or refused. Nil: not counted.
	UnpinnedImages UnpinnedImageRecorder
	// DeployDefaults are what the server advertises from deploy.defaults
	// (config.ServerConfig.AdvertisedDeployDefaults), served on
	// GET /api/v2/deployDefaults (#1704, #1890). The zero value serves empty
	// fields.
	DeployDefaults domain.DeployDefaults
	// ExecutionPolicy is executor.defaults.execution_allowlist (issue #629).
	// When set, registering a DAG whose task sets an execution field (or
	// priority class) outside it answers 400 naming the task and the field.
	// Nil: everything allowed.
	ExecutionPolicy *domain.ExecutionPolicy
	// WarmPolicy is the warm placement decision the dispatcher applies
	// (dispatch.Dispatcher.WarmPolicy, built from the same configuration). The
	// register response reports each task's execution mode with it (#1707).
	// The zero value reports warm pools off.
	WarmPolicy domain.WarmPolicy
	// WarmPools are the operator's warm pool settings (execution.*), read
	// for the pool state of the register response.
	WarmPools domain.WarmPoolDefaults
	// WarmWorkersAbsent says why no warm worker runs although warm pools are
	// on (the subprocess executor starts no warm pool reconciler; neither does
	// a server whose pod dispatch is disabled), "" when they run. The register
	// response then reports every task on a dedicated pod, with this reason.
	WarmWorkersAbsent string
	// TaskPods is how this server starts task pods, which the register dry
	// run reports (#1889): whether they refuse an image that runs as root,
	// and the ServiceAccount each task would run as, looked up. The zero
	// value reports a server that starts no task pods.
	TaskPods TaskPodSettings
}

// StoreDeps are the resource repositories. Routes for nil repositories are
// not registered.
type StoreDeps struct {
	Dags     DagRepository
	DagRuns  DagRunRepository
	Tasks    TaskInstanceRepository
	Versions DagVersionRepository
	Xcoms    XComReader
	Logs     LogReader
	// EphemeralLogs indicates that task logs reside on an ephemeral storage volume
	// (such as an emptyDir PVC fallback) that is lost on pod restart. When true,
	// missing logs for terminal task instances explain that logs did not survive
	// a control plane restart.
	EphemeralLogs bool
	Specs         DagSpecReader
	// DagSources reads one version of a DAG with its spec, for the Code view
	// (GET /api/v2/dagSources/{dag_id}). Nil leaves that route unregistered.
	DagSources     DagSourceReader
	LatestRuns     DagLatestRunsReader
	TaskSummary    TaskSummaryReader
	DagVersions    DagVersionLister
	DashboardStats DashboardStatsReader
	AuditLog       AuditLogReader
	Variables      VariableStore
	Users          UserStore
	UserAudit      UserAuditWriter
	// APITokens keeps the long-lived API tokens (ADR 0074). Nil leaves the
	// /api/v2/apiTokens routes unregistered.
	APITokens      TokenStore
	Connections    ConnectionStore
	ConnectionTest ConnectionTester
	Pools          PoolStore
	Favorites      FavoriteStore
	ImportErrors   ImportErrorStore
	Audit          AuditWriter
}

// ErrMissingDependency is wrapped by the error NewServer returns for a
// Dependencies value that leaves out a required dependency.
var ErrMissingDependency = errors.New("missing required dependency")

// Validate reports the dependencies d leaves out that the server cannot run
// without: the ones every server needs (a logger, an authenticator, a login
// rate limiter), and the ones a feature that is turned on reads on every
// request it serves. It names each missing one by its field path, such as
// Auth.Authenticator. A nil error means NewServer can build the server.
func (d Dependencies) Validate() error {
	var missing []string
	seen := map[string]bool{}
	need := func(ok bool, field, why string) {
		if !ok && !seen[field] {
			seen[field] = true
			missing = append(missing, field+" ("+why+")")
		}
	}
	need(d.Observability.Logger != nil, "Observability.Logger", "every request is logged")
	need(d.Auth.Authenticator != nil, "Auth.Authenticator", "POST /auth/token and every protected route use it")
	need(d.Auth.RateLimiter != nil, "Auth.RateLimiter", "POST /auth/token counts failed logins on it")
	if d.Auth.OIDCFlow != nil {
		need(d.Auth.OIDCUsers != nil, "Auth.OIDCUsers", "the OIDC callback resolves users with it")
		need(d.Auth.JWTSecret != "", "Auth.JWTSecret", "the OIDC callback signs sessions with it")
	}
	d.Issuers.validate(d.Auth.JWTSecret, need)
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("api: %w: %s", ErrMissingDependency, strings.Join(missing, ", "))
}

// validate adds the missing dependencies of the issuer features that are
// turned on.
func (d IssuerDeps) validate(jwtSecret string, need func(ok bool, field, why string)) {
	if d.TrustedIssuer != nil {
		need(d.TrustedIssuerUsers != nil, "Issuers.TrustedIssuerUsers", "the trusted-issuer handoff resolves users with it")
		need(jwtSecret != "", "Auth.JWTSecret", "the trusted-issuer handoff signs sessions with it")
	}
	if d.TrustedIssuerBearer != nil {
		need(d.TrustedIssuerUsers != nil, "Issuers.TrustedIssuerUsers", "the trusted-issuer bearer resolves users with it")
	}
	if d.CIOIDC != nil && d.CIOIDCRoles != nil {
		need(jwtSecret != "", "Auth.JWTSecret", "the CI OIDC exchange signs sessions with it")
	}
	if d.ServiceToken != "" {
		need(d.ServiceTenants != nil, "Issuers.ServiceTenants", "the service API stores tenants and users in it")
	}
}
