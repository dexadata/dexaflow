---
title: "internal/api"
linkTitle: "internal/api"
weight: 11
---

```go
import "github.com/dexadata/dexaflow/internal/api"
```

Package api implements the Airflow\-compatible HTTP control plane \(ADR 0007\).

## Index

- [Constants](<#constants>)
- [Variables](<#variables>)
- [func AbortProblem\(c \*gin.Context, status int, title, detail string\)](<#AbortProblem>)
- [func CORS\(allowed \[\]string\) gin.HandlerFunc](<#CORS>)
- [func DevBypassAuth\(\) gin.HandlerFunc](<#DevBypassAuth>)
- [func JWTAuth\(authn auth.Authenticator\) gin.HandlerFunc](<#JWTAuth>)
- [func NewServer\(deps Dependencies\) \*gin.Engine](<#NewServer>)
- [func NoStoreOnVolatileRoutes\(\) gin.HandlerFunc](<#NoStoreOnVolatileRoutes>)
- [func ObservabilityHandler\(registry \*prometheus.Registry, checks map\[string\]HealthChecker\) http.Handler](<#ObservabilityHandler>)
- [func Observe\(metrics Metrics, tracer trace.Tracer\) gin.HandlerFunc](<#Observe>)
- [func RequestID\(\) gin.HandlerFunc](<#RequestID>)
- [func RequirePermission\(action, resource string\) gin.HandlerFunc](<#RequirePermission>)
- [func StructuredLogger\(logger \*slog.Logger\) gin.HandlerFunc](<#StructuredLogger>)
- [func UserFromContext\(c \*gin.Context\) \(\*auth.User, bool\)](<#UserFromContext>)
- [type AuditLogReader](<#AuditLogReader>)
- [type AuditWriter](<#AuditWriter>)
- [type AuthAuditWriter](<#AuthAuditWriter>)
- [type ConnectionStore](<#ConnectionStore>)
- [type ConnectionTester](<#ConnectionTester>)
- [type DagLatestRunsReader](<#DagLatestRunsReader>)
- [type DagRepository](<#DagRepository>)
- [type DagRunRepository](<#DagRunRepository>)
- [type DagSpecReader](<#DagSpecReader>)
- [type DagVersionLister](<#DagVersionLister>)
- [type DagVersionRepository](<#DagVersionRepository>)
- [type DashboardStatsReader](<#DashboardStatsReader>)
- [type Dependencies](<#Dependencies>)
- [type ExecutorInfo](<#ExecutorInfo>)
- [type FavoriteStore](<#FavoriteStore>)
- [type HealthChecker](<#HealthChecker>)
- [type Heartbeater](<#Heartbeater>)
- [type ImportErrorStore](<#ImportErrorStore>)
- [type LogReader](<#LogReader>)
- [type Metrics](<#Metrics>)
- [type OIDCUserStore](<#OIDCUserStore>)
- [type PoolStore](<#PoolStore>)
- [type Problem](<#Problem>)
- [type TaskInstanceRepository](<#TaskInstanceRepository>)
- [type TaskSummaryReader](<#TaskSummaryReader>)
- [type TokenRenewer](<#TokenRenewer>)
- [type UIServer](<#UIServer>)
- [type UserAuditWriter](<#UserAuditWriter>)
- [type UserStore](<#UserStore>)
- [type VariableStore](<#VariableStore>)
- [type WorkspaceFS](<#WorkspaceFS>)
- [type XComReader](<#XComReader>)


## Constants

<a name="DefaultUIAutoRefreshIntervalSeconds"></a>DefaultUIAutoRefreshIntervalSeconds is the production\-safe value returned by /ui/config when no explicit override is configured. Lite overrides this to a smaller value \(typically 5s\) for a snappy inner\-loop dev experience; Pro keeps 30s so the SPA's polling does not hammer a shared metadata DB.

```go
const DefaultUIAutoRefreshIntervalSeconds = 30
```

## Variables

<a name="ErrNotFound"></a>ErrNotFound is returned by repositories when a resource does not exist.

```go
var ErrNotFound = domain.ErrNotFound
```

<a name="AbortProblem"></a>
## func [AbortProblem](<https://github.com/dexadata/dexaflow/blob/main/internal/api/problem.go#L16>)

```go
func AbortProblem(c *gin.Context, status int, title, detail string)
```

AbortProblem writes an RFC 7807 problem response and stops the handler chain.

<a name="CORS"></a>
## func [CORS](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L84>)

```go
func CORS(allowed []string) gin.HandlerFunc
```

CORS allows the configured origins \(use "\*" to allow any\).

<a name="DevBypassAuth"></a>
## func [DevBypassAuth](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L142>)

```go
func DevBypassAuth() gin.HandlerFunc
```

DevBypassAuth authenticates EVERY request as a fixed admin user, with no token required. It exists solely for \`leoflow dev\` \(the local, unsandboxed loop\) so a developer reaches the UI without logging in. It must only be wired under the explicit dev opt\-in \(config auth.dev\_no\_auth\); the server logs a prominent warning when it is active. NEVER enable this in production.

<a name="JWTAuth"></a>
## func [JWTAuth](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L151>)

```go
func JWTAuth(authn auth.Authenticator) gin.HandlerFunc
```

JWTAuth validates the bearer token on protected routes and stores the user.

<a name="NewServer"></a>
## func [NewServer](<https://github.com/dexadata/dexaflow/blob/main/internal/api/server.go#L140>)

```go
func NewServer(deps Dependencies) *gin.Engine
```

NewServer builds the gin engine with the full middleware chain, health and metrics endpoints, embedded Scalar docs, and the auth token endpoint.

<a name="NoStoreOnVolatileRoutes"></a>
## func [NoStoreOnVolatileRoutes](<https://github.com/dexadata/dexaflow/blob/main/internal/api/no_store.go#L30>)

```go
func NoStoreOnVolatileRoutes() gin.HandlerFunc
```

NoStoreOnVolatileRoutes stamps every response served from the SPA\-facing JSON surface \(\`/api/v2/\*\` and \`/ui/\*\`\) with \`Cache\-Control: no\-store, must\-revalidate\` so the browser HTTP cache does not return a pre\-mutation payload after a PATCH/POST/DELETE.

Why this exists \(\#211, \#271\): mark\-state PATCH succeeds in single\-digit ms; TanStack Query then invalidates its in\-memory cache and re\-fetches. Without this header, the browser's HTTP cache layer can serve the OLD response to that re\-fetch \(the original GET response had no explicit caching directive, so the browser falls back to heuristic caching\). The SPA renders stale state until the next "natural" refresh — the observable symptom is "marcar como falha demora uma eternidade".

Static assets \(\`/ide/vs/\*\` for the Monaco bundle\) are content\-hashed and SHOULD cache, so they are explicitly excluded.

We deliberately use "no\-store" rather than "no\-cache": no\-store forbids the browser from writing the response anywhere, which is the strongest guarantee we can give a TanStack\-backed SPA. "must\-revalidate" is added for older intermediaries \(proxies / SW\) that may not honor no\-store alone. This is ADR\-0017\-compatible: no SPA changes.

<a name="ObservabilityHandler"></a>
## func [ObservabilityHandler](<https://github.com/dexadata/dexaflow/blob/main/internal/api/observability_handler.go#L26>)

```go
func ObservabilityHandler(registry *prometheus.Registry, checks map[string]HealthChecker) http.Handler
```

ObservabilityHandler builds the handler served on the metrics listener: the Prometheus /metrics endpoint plus the same /healthz and /readyz the full API exposes \(reusing the very same handlers, so the semantics are identical — trivial liveness, dependency\-pinging readiness\).

Roles that do not serve the full API — the ADR 0049 scheduler role — still need a liveness/readiness surface for the kubelet's probes. The metrics listener runs in every role, so mounting health here gives a scheduler\-only pod a probe target without exposing the API, auth, or UI. The full API keeps its own /healthz and /readyz on the HTTP port, so the "all" role is unchanged; this is purely additive on the metrics port.

This handler is intentionally unauthenticated \(probes carry no token\) and does not run the API middleware chain; it is the same trust level as scraping /metrics, which is already public.

<a name="Observe"></a>
## func [Observe](<https://github.com/dexadata/dexaflow/blob/main/internal/api/observe.go#L20>)

```go
func Observe(metrics Metrics, tracer trace.Tracer) gin.HandlerFunc
```

Observe wraps each request in an OTel span and records HTTP metrics \(ADR 0010\). A nil tracer falls back to the global \(no\-op\) tracer; nil metrics are skipped, so the middleware is safe in tests.

<a name="RequestID"></a>
## func [RequestID](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L27>)

```go
func RequestID() gin.HandlerFunc
```

RequestID assigns a request id \(honoring an inbound X\-Request\-Id\) and echoes it.

<a name="RequirePermission"></a>
## func [RequirePermission](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L213>)

```go
func RequirePermission(action, resource string) gin.HandlerFunc
```

RequirePermission enforces an RBAC permission on a route.

<a name="StructuredLogger"></a>
## func [StructuredLogger](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L48>)

```go
func StructuredLogger(logger *slog.Logger) gin.HandlerFunc
```

StructuredLogger logs one structured line per request \(ADR 0010\).

<a name="UserFromContext"></a>
## func [UserFromContext](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L203>)

```go
func UserFromContext(c *gin.Context) (*auth.User, bool)
```

UserFromContext returns the authenticated user stored by JWTAuth.

<a name="AuditLogReader"></a>
## type [AuditLogReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_audit.go#L17-L19>)

AuditLogReader lists recorded actions for the Audit Log view. dagID == "" means no DAG filter.

```go
type AuditLogReader interface {
    ListAuditLogs(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.AuditLogEntry, int, error)
}
```

<a name="AuditWriter"></a>
## type [AuditWriter](<https://github.com/dexadata/dexaflow/blob/main/internal/api/resources.go#L57-L59>)

AuditWriter records task\-level actions \(clear, mark state\) for the Audit Log view, with the acting user and the run/task in the entry.

```go
type AuditWriter interface {
    RecordTaskActionAudit(ctx context.Context, tenant, userID, action, dagID, runID, taskID string, tryNumber int) error
}
```

<a name="AuthAuditWriter"></a>
## type [AuthAuditWriter](<https://github.com/dexadata/dexaflow/blob/main/internal/api/oidc_handler.go#L55-L57>)

AuthAuditWriter records authentication events to the audit sink \(H5\). It is best\-effort: a write error never changes the auth outcome.

```go
type AuthAuditWriter interface {
    RecordAuthEvent(ctx context.Context, tenant, actorUserID, action, email, outcome string, extra map[string]string) error
}
```

<a name="ConnectionStore"></a>
## type [ConnectionStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_connections.go#L76-L81>)

ConnectionStore reads and writes Airflow\-style Connections for the Admin UI. Password is encrypted at rest by the store \(ADR 0019\) and never returned.

```go
type ConnectionStore interface {
    ListConnections(ctx context.Context, tenant string, limit, offset int) ([]domain.Connection, int, error)
    GetConnection(ctx context.Context, tenant, connID string) (domain.Connection, error)
    SetConnectionPatch(ctx context.Context, tenant string, p domain.ConnectionPatch) error
    DeleteConnection(ctx context.Context, tenant, connID string) error
}
```

<a name="ConnectionTester"></a>
## type [ConnectionTester](<https://github.com/dexadata/dexaflow/blob/main/internal/api/connection_probe.go#L25-L27>)

ConnectionTester checks whether a connection is well\-formed. The default implementation validates STRUCTURE only and makes no network call: the Go control plane must never reach out to a user\-configured host \(SSRF / internal port\-scan — go/request\-forgery\), and "reachable from the control plane" is the wrong question anyway, since a connection is used in the task's network scope, not the control plane's. Live reachability/auth is tested where the connection is actually used \(the task/executor\) — tracked as a follow\-up.

```go
type ConnectionTester interface {
    Test(ctx context.Context, c domain.Connection) (ok bool, message string)
}
```

<a name="DagLatestRunsReader"></a>
## type [DagLatestRunsReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_dags.go#L22-L24>)

DagLatestRunsReader fetches the most\-recent runs for a set of DAGs in one query, so /ui/dags can embed run history without an N\+1.

```go
type DagLatestRunsReader interface {
    LatestRunsForDags(ctx context.Context, tenant string, dagIDs []string, perDag int) (map[string][]domain.DagRun, error)
}
```

<a name="DagRepository"></a>
## type [DagRepository](<https://github.com/dexadata/dexaflow/blob/main/internal/api/resources.go#L25-L32>)

DagRepository reads, updates, and deletes registered DAGs.

```go
type DagRepository interface {
    ListDags(ctx context.Context, tenant string, limit, offset int) ([]domain.DAG, int, error)
    GetDag(ctx context.Context, tenant, dagID string) (domain.DAG, error)
    SetPaused(ctx context.Context, tenant, dagID string, paused bool) (domain.DAG, error)
    DeleteDag(ctx context.Context, tenant, dagID string) error
    ClearDagHistory(ctx context.Context, tenant, dagID string) error
    ListDagsFiltered(ctx context.Context, tenant, runState string, paused *bool, limit, offset int) ([]domain.DAG, int, error)
}
```

<a name="DagRunRepository"></a>
## type [DagRunRepository](<https://github.com/dexadata/dexaflow/blob/main/internal/api/resources.go#L35-L41>)

DagRunRepository reads and creates DAG runs.

```go
type DagRunRepository interface {
    ListDagRuns(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, int, error)
    GetDagRun(ctx context.Context, tenant, dagID, runID string) (domain.DagRun, error)
    CreateDagRun(ctx context.Context, tenant, dagID string, run domain.DagRun) (domain.DagRun, error)
    SetDagRunState(ctx context.Context, tenant, dagID, runID, state string) error
    DeleteDagRun(ctx context.Context, tenant, dagID, runID string) error
}
```

<a name="DagSpecReader"></a>
## type [DagSpecReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_structure.go#L15-L17>)

DagSpecReader reads the parsed spec of a DAG's current version, the source of task topology for the grid and graph views.

```go
type DagSpecReader interface {
    GetCurrentSpec(ctx context.Context, tenant, dagID string) (domain.DAGSpec, error)
}
```

<a name="DagVersionLister"></a>
## type [DagVersionLister](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_dagversions.go#L17-L19>)

DagVersionLister lists a DAG's registered versions. The Airflow UI fetches this to resolve a version\_number before requesting version\-scoped structure \(the Graph view\); without it the graph never loads. See docs/ui\-compatibility.md.

```go
type DagVersionLister interface {
    ListDagVersions(ctx context.Context, tenant, dagID string) ([]domain.DagVersion, error)
}
```

<a name="DagVersionRepository"></a>
## type [DagVersionRepository](<https://github.com/dexadata/dexaflow/blob/main/internal/api/versions.go#L13-L18>)

DagVersionRepository registers compiled DAG versions.

```go
type DagVersionRepository interface {
    // RegisterDagVersion upserts the DAG and inserts a version keyed by
    // specHash, reporting whether a new version was created (false if the hash
    // already existed — the push is idempotent).
    RegisterDagVersion(ctx context.Context, tenant string, spec domain.DAGSpec, specHash string) (bool, error)
}
```

<a name="DashboardStatsReader"></a>
## type [DashboardStatsReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_dashboard.go#L15-L18>)

DashboardStatsReader backs the home dashboard widgets with real counts.

```go
type DashboardStatsReader interface {
    DagStats(ctx context.Context, tenant string) (domain.DagStats, error)
    HistoricalMetrics(ctx context.Context, tenant string, since, until time.Time) (domain.HistoricalMetrics, error)
}
```

<a name="Dependencies"></a>
## type [Dependencies](<https://github.com/dexadata/dexaflow/blob/main/internal/api/server.go#L29-L136>)

Dependencies bundles everything the HTTP server needs.

```go
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
    // falls back to "Dexaflow"; `leoflow dev` sets it to mark the DEV environment.
    InstanceName string
    // UIAutoRefreshIntervalSeconds controls the SPA's polling cadence for DAG /
    // DagRun / task-instance state refresh (Airflow's auto_refresh_interval).
    // Non-positive (the zero default) falls back to DefaultUIAutoRefreshIntervalSeconds
    // (30s, production-safe). `dexaflow lite` sets it to ~5s for a snappy inner loop.
    UIAutoRefreshIntervalSeconds int
    // DevNoAuth replaces JWT auth with a dev-only bypass that authenticates every
    // request as an admin (no login). It is for `leoflow dev` only and must never
    // be set in production. See DevBypassAuth.
    DevNoAuth bool
    // Edition marks the running edition ("pro", "lite", or empty). It gates
    // Pro-only surfaces: named-pool CRUD is registered as real endpoints only when
    // Edition == "pro" (ADR 0053), otherwise the Pools screen gets the graceful
    // empty-collection stub, matching how the scheduler's pool gate is Pro-gated.
    Edition string

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
    UI  UIServer

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
    // OIDCUsers resolves and JIT-provisions OIDC identities (the storage repo).
    OIDCUsers OIDCUserStore
    // AuthAudit records authentication events (login, tenant-pin rejection, JIT,
    // break-glass, logout) to the audit sink.
    AuthAudit AuthAuditWriter
    // JWTSecret is the HS256 secret the OIDC callback mints the app's _token with.
    JWTSecret string
}
```

<a name="ExecutorInfo"></a>
## type [ExecutorInfo](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_executor.go#L15-L19>)

ExecutorInfo describes the control plane's execution capacity. It surfaces whether pod dispatch is available — the cluster\-level answer to "why is a task stuck queued" \(\#46/\#47\). The stock Airflow UI has no widget for it, but operators \(curl/monitoring\) and a future custom Cluster Activity view consume it. Cluster Activity in Airflow 3.2 is otherwise the Home dashboard, already backed by /api/v2/monitor/health \(\#33\) and /ui/dashboard/\* \(\#39\).

```go
type ExecutorInfo struct {
    PodDispatchEnabled    bool
    TaskNamespace         string
    AgentControlPlaneAddr string
}
```

<a name="FavoriteStore"></a>
## type [FavoriteStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_favorites.go#L11-L15>)

FavoriteStore persists per\-user DAG favorites \(the DAG\-list star\).

```go
type FavoriteStore interface {
    AddFavorite(ctx context.Context, tenant, userID, dagID string) error
    RemoveFavorite(ctx context.Context, tenant, userID, dagID string) error
    FavoriteDagIDs(ctx context.Context, tenant, userID string) (map[string]bool, error)
}
```

<a name="HealthChecker"></a>
## type [HealthChecker](<https://github.com/dexadata/dexaflow/blob/main/internal/api/health.go#L12-L14>)

HealthChecker reports dependency health for readiness checks.

```go
type HealthChecker interface {
    Ping(ctx context.Context) error
}
```

<a name="Heartbeater"></a>
## type [Heartbeater](<https://github.com/dexadata/dexaflow/blob/main/internal/api/monitor.go#L18-L20>)

Heartbeater reports a long\-running component's liveness and last heartbeat for the monitor health endpoint. The scheduler implements it.

```go
type Heartbeater interface {
    Heartbeat() (healthy bool, last time.Time)
}
```

<a name="ImportErrorStore"></a>
## type [ImportErrorStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/import_errors.go#L19-L23>)

ImportErrorStore reads and writes DAG parse/compile errors that back Airflow's "Import Errors" banner on the home dashboard. The \`leoflow dev\` watcher writes an entry on a failed compile and clears it on the next good compile; the public GET /api/v2/importErrors feed is what the UI polls.

```go
type ImportErrorStore interface {
    ListImportErrors(ctx context.Context, tenant string) ([]domain.ImportError, error)
    SetImportError(ctx context.Context, tenant string, e domain.ImportError) error
    ClearImportError(ctx context.Context, tenant, filename string) error
}
```

<a name="LogReader"></a>
## type [LogReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/logs.go#L20-L23>)

LogReader streams a task attempt's stored logs and, for running tasks, tails new lines live.

```go
type LogReader interface {
    ReadLogs(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (io.ReadCloser, error)
    Tail(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (<-chan string, func(), error)
}
```

<a name="Metrics"></a>
## type [Metrics](<https://github.com/dexadata/dexaflow/blob/main/internal/api/observe.go#L13-L15>)

Metrics records HTTP request metrics. observability.Metrics implements it.

```go
type Metrics interface {
    RecordHTTPRequest(method, path string, status int, dur time.Duration)
}
```

<a name="OIDCUserStore"></a>
## type [OIDCUserStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/oidc_handler.go#L37-L51>)

OIDCUserStore resolves and just\-in\-time\-provisions OIDC identities. storage implements it. The interface lives with its consumer \(the callback handler\).

```go
type OIDCUserStore interface {
    // FindUserByOIDCSubject resolves a returning identity by its (provider,
    // subject) pair, returning the user, whether it is active, and
    // auth.ErrUserNotFound when no row matches.
    FindUserByOIDCSubject(ctx context.Context, provider, subject string) (*auth.User, bool, error)
    // CreateOIDCUser JIT-provisions an OIDC-only user with the given roles.
    CreateOIDCUser(ctx context.Context, tenant, email, provider, subject string, roles []string) (*auth.User, error)
    // RoleExists reports whether a role name exists for the tenant, used to fail
    // closed on a misconfigured default_role or role mapping.
    RoleExists(ctx context.Context, tenant, role string) (bool, error)
    // ReconcileUserRoles sets the user's DB roles to EXACTLY roleNames (the IdP is
    // authoritative). It fails closed on a name that is not a role in the tenant,
    // leaving the prior grants intact.
    ReconcileUserRoles(ctx context.Context, userID string, roleNames []string) error
}
```

<a name="PoolStore"></a>
## type [PoolStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_pools.go#L15-L21>)

PoolStore reads and writes named task pools for the Admin UI \(ADR 0053 Stage 3\). Pools are tenant\-scoped; PoolSlotUsage reports per\-pool occupancy for the Airflow slot fields.

```go
type PoolStore interface {
    ListPools(ctx context.Context, tenant string, limit, offset int) ([]domain.Pool, int, error)
    GetPool(ctx context.Context, tenant, name string) (domain.Pool, error)
    SetPool(ctx context.Context, tenant string, p domain.Pool) error
    DeletePool(ctx context.Context, tenant, name string) error
    PoolSlotUsage(ctx context.Context, tenant string) (map[string]domain.PoolUsage, error)
}
```

<a name="Problem"></a>
## type [Problem](<https://github.com/dexadata/dexaflow/blob/main/internal/api/problem.go#L7-L13>)

Problem is an RFC 7807 problem\-details response body.

```go
type Problem struct {
    Type     string `json:"type"`
    Title    string `json:"title"`
    Status   int    `json:"status"`
    Detail   string `json:"detail,omitempty"`
    Instance string `json:"instance,omitempty"`
}
```

<a name="TaskInstanceRepository"></a>
## type [TaskInstanceRepository](<https://github.com/dexadata/dexaflow/blob/main/internal/api/resources.go#L45-L53>)

TaskInstanceRepository reads task instances, clears them for re\-run, and sets their state directly \(the UI's mark\-success/failed actions\).

```go
type TaskInstanceRepository interface {
    ListTaskInstances(ctx context.Context, tenant, dagID, runID string, limit, offset int) ([]domain.TaskInstance, int, error)
    // ListTaskInstanceAttempts returns every attempt for (run, task), oldest
    // first — the current row UNIONed with the archived history. The UI's
    // /tries endpoint needs all attempts to render its navigable tabs.
    ListTaskInstanceAttempts(ctx context.Context, tenant, dagID, runID, taskID string) ([]domain.TaskInstance, error)
    ClearTaskInstances(ctx context.Context, tenant, dagID, runID string, taskIDs []string, onlyFailed, resetDagRun bool) (int, error)
    SetTaskInstanceState(ctx context.Context, tenant, dagID, runID, taskID, state string) error
}
```

<a name="TaskSummaryReader"></a>
## type [TaskSummaryReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_summaries.go#L20-L22>)

TaskSummaryReader fetches task instances across a set of runs of a DAG, the source for the grid's per\-cell state summaries.

```go
type TaskSummaryReader interface {
    TaskInstancesForRuns(ctx context.Context, tenant, dagID string, runIDs []string) ([]domain.TaskInstance, error)
}
```

<a name="TokenRenewer"></a>
## type [TokenRenewer](<https://github.com/dexadata/dexaflow/blob/main/internal/api/auth_handler.go#L18-L20>)

TokenRenewer re\-mints a still\-valid user bearer with a fresh short TTL, bounded by max\_lifetime since first login. \*auth.JWTAuthenticator implements it via RenewUserToken; the handler depends on this narrow interface so the renew route can be tested without a real signing key.

```go
type TokenRenewer interface {
    RenewUserToken(token string, ttl, maxLifetime time.Duration) (renewed string, ok bool, err error)
}
```

<a name="UIServer"></a>
## type [UIServer](<https://github.com/dexadata/dexaflow/blob/main/internal/api/server.go#L23-L26>)

UIServer serves the embedded single\-page app: static assets and an index.html shell that the SPA's client\-side router falls back to. It is satisfied by internal/ui.Server. When nil, the server runs API\-only and unknown paths return 404 instead of the SPA shell.

```go
type UIServer interface {
    StaticHandler() http.Handler
    Index(w http.ResponseWriter, basePath string)
}
```

<a name="UserAuditWriter"></a>
## type [UserAuditWriter](<https://github.com/dexadata/dexaflow/blob/main/internal/api/users.go#L49-L51>)

UserAuditWriter records account\-creation events for the Audit Log. It is a separate, narrow interface \(not the task\-shaped AuditWriter\) so account management writes a "user" resource entry with the acting admin as owner. The granted roles are passed as a single joined string so the record captures the full set.

```go
type UserAuditWriter interface {
    RecordUserCreatedAudit(ctx context.Context, tenant, actorUserID, createdUserID, email, roles string) error
}
```

<a name="UserStore"></a>
## type [UserStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/users.go#L39-L42>)

UserStore creates control\-plane accounts for the admin create\-user API. The store hashes the plaintext password \(reusing the bootstrap admin's bcrypt scheme\) and returns the created user without any secret. A duplicate email must surface as domain.ErrConflict and an unknown role as domain.ErrValidation.

```go
type UserStore interface {
    CreateUser(ctx context.Context, tenant, email, password string, roles []string) (domain.User, error)
    ListUsers(ctx context.Context, tenant string, limit, offset int) ([]domain.User, int, error)
}
```

<a name="VariableStore"></a>
## type [VariableStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_variables.go#L15-L20>)

VariableStore reads and writes Airflow\-style Variables for the Admin UI.

```go
type VariableStore interface {
    ListVariables(ctx context.Context, tenant string, limit, offset int) ([]domain.Variable, int, error)
    GetVariable(ctx context.Context, tenant, key string) (domain.Variable, error)
    SetVariablePatch(ctx context.Context, tenant string, p domain.VariablePatch) error
    DeleteVariable(ctx context.Context, tenant, key string) error
}
```

<a name="WorkspaceFS"></a>
## type [WorkspaceFS](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ide.go#L27-L34>)

WorkspaceFS is the workspace\-confined filesystem backing the Lite web editor \(ADR 0025\). Every path is relative to the workspace root and confined to it.

```go
type WorkspaceFS interface {
    Tree() ([]workspace.Entry, error)
    Read(rel string) ([]byte, error)
    Write(rel string, data []byte) error
    Create(rel string, dir bool) error
    Move(from, to string) error
    Delete(rel string) error
}
```

<a name="XComReader"></a>
## type [XComReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/xcoms.go#L17-L20>)

XComReader reads stored XCom values and lists a task instance's XCom keys for the read API.

```go
type XComReader interface {
    GetXCom(ctx context.Context, tenant, dagID, runID, taskID, key string) (xcom.Entry, error)
    ListXComEntries(ctx context.Context, tenant, dagID, runID, taskID string) ([]domain.XComEntryMeta, error)
}
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
