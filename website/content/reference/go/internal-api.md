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
- [func AbortProblemCause\(c \*gin.Context, status int, title, detail string, cause error\)](<#AbortProblemCause>)
- [func BodyLimit\(requestMax, uploadMax int64\) gin.HandlerFunc](<#BodyLimit>)
- [func CORS\(allowed \[\]string\) gin.HandlerFunc](<#CORS>)
- [func DevBypassAuth\(\) gin.HandlerFunc](<#DevBypassAuth>)
- [func GzipJSON\(\) gin.HandlerFunc](<#GzipJSON>)
- [func JWTAuth\(authn auth.Authenticator\) gin.HandlerFunc](<#JWTAuth>)
- [func NewServer\(deps Dependencies\) \(\*gin.Engine, error\)](<#NewServer>)
- [func NoStoreOnVolatileRoutes\(\) gin.HandlerFunc](<#NoStoreOnVolatileRoutes>)
- [func ObservabilityHandler\(registry \*prometheus.Registry, checks map\[string\]HealthChecker, opts ...ObservabilityOption\) http.Handler](<#ObservabilityHandler>)
- [func Observe\(metrics Metrics, tracer trace.Tracer\) gin.HandlerFunc](<#Observe>)
- [func RejectEncodedPathSeparators\(\) gin.HandlerFunc](<#RejectEncodedPathSeparators>)
- [func RequestID\(\) gin.HandlerFunc](<#RequestID>)
- [func RequireDeployPermission\(viaIssuer bool\) gin.HandlerFunc](<#RequireDeployPermission>)
- [func RequireDeployRead\(\) gin.HandlerFunc](<#RequireDeployRead>)
- [func RequirePermission\(action, resource string\) gin.HandlerFunc](<#RequirePermission>)
- [func RequireScope\(scope string\) gin.HandlerFunc](<#RequireScope>)
- [func RequireScopedPermission\(action, resource, scope string\) gin.HandlerFunc](<#RequireScopedPermission>)
- [func RequireUndeployPermission\(viaIssuer bool\) gin.HandlerFunc](<#RequireUndeployPermission>)
- [func SchemaWaitHandler\(registry \*prometheus.Registry, reason func\(\) string, opts ...ObservabilityOption\) http.Handler](<#SchemaWaitHandler>)
- [func StructuredLogger\(logger \*slog.Logger\) gin.HandlerFunc](<#StructuredLogger>)
- [func UserFromContext\(c \*gin.Context\) \(\*auth.User, bool\)](<#UserFromContext>)
- [type AuditLogPageReader](<#AuditLogPageReader>)
- [type AuditLogReader](<#AuditLogReader>)
- [type AuditWriter](<#AuditWriter>)
- [type AuthAuditWriter](<#AuthAuditWriter>)
- [type AuthDeps](<#AuthDeps>)
- [type CIOIDCExchangeRecorder](<#CIOIDCExchangeRecorder>)
- [type CIOIDCVerifier](<#CIOIDCVerifier>)
- [type CLIDeviceStore](<#CLIDeviceStore>)
- [type ConnectionStore](<#ConnectionStore>)
- [type ConnectionTester](<#ConnectionTester>)
- [type DagLatestRunsReader](<#DagLatestRunsReader>)
- [type DagRepository](<#DagRepository>)
- [type DagRunPageReader](<#DagRunPageReader>)
- [type DagRunRepository](<#DagRunRepository>)
- [type DagSourceReader](<#DagSourceReader>)
- [type DagSpecReader](<#DagSpecReader>)
- [type DagVersionLister](<#DagVersionLister>)
- [type DagVersionRepository](<#DagVersionRepository>)
- [type DashboardStatsReader](<#DashboardStatsReader>)
- [type Dependencies](<#Dependencies>)
  - [func \(d Dependencies\) Validate\(\) error](<#Dependencies.Validate>)
- [type ExecutionDeps](<#ExecutionDeps>)
- [type ExecutorInfo](<#ExecutorInfo>)
- [type FavoriteStore](<#FavoriteStore>)
- [type HealthChecker](<#HealthChecker>)
- [type Heartbeater](<#Heartbeater>)
- [type ImportErrorStore](<#ImportErrorStore>)
- [type IssuerDeps](<#IssuerDeps>)
- [type LivenessCheck](<#LivenessCheck>)
- [type LogReader](<#LogReader>)
- [type Metrics](<#Metrics>)
- [type OIDCSettings](<#OIDCSettings>)
- [type OIDCUserStore](<#OIDCUserStore>)
- [type ObservabilityDeps](<#ObservabilityDeps>)
- [type ObservabilityOption](<#ObservabilityOption>)
  - [func WithLivenessCheck\(check LivenessCheck\) ObservabilityOption](<#WithLivenessCheck>)
  - [func WithProbeBudget\(d time.Duration\) ObservabilityOption](<#WithProbeBudget>)
  - [func WithoutLegacyMetricNames\(\) ObservabilityOption](<#WithoutLegacyMetricNames>)
- [type PoolStore](<#PoolStore>)
- [type Problem](<#Problem>)
- [type ResourceAuditWriter](<#ResourceAuditWriter>)
- [type RunVersionLabeler](<#RunVersionLabeler>)
- [type SchemaChecker](<#SchemaChecker>)
- [type ServerDeps](<#ServerDeps>)
- [type ServiceAccountReader](<#ServiceAccountReader>)
- [type ServiceTenantStore](<#ServiceTenantStore>)
- [type StoreDeps](<#StoreDeps>)
- [type TaskInstanceRepository](<#TaskInstanceRepository>)
- [type TaskPodSettings](<#TaskPodSettings>)
- [type TaskSummaryReader](<#TaskSummaryReader>)
- [type TokenRenewer](<#TokenRenewer>)
- [type TokenStore](<#TokenStore>)
- [type TrustedIssuer](<#TrustedIssuer>)
- [type TrustedIssuerBearer](<#TrustedIssuerBearer>)
- [type TrustedIssuerUserStore](<#TrustedIssuerUserStore>)
- [type UIDeps](<#UIDeps>)
- [type UIServer](<#UIServer>)
- [type UnitMisfitRecorder](<#UnitMisfitRecorder>)
- [type UnpinnedImageRecorder](<#UnpinnedImageRecorder>)
- [type UsageEventReader](<#UsageEventReader>)
- [type UserActivator](<#UserActivator>)
- [type UserAuditWriter](<#UserAuditWriter>)
- [type UserStore](<#UserStore>)
- [type VariableStore](<#VariableStore>)
- [type WorkspaceFS](<#WorkspaceFS>)
- [type XComReader](<#XComReader>)


## Constants

<a name="DefaultUIAutoRefreshIntervalSeconds"></a>DefaultUIAutoRefreshIntervalSeconds is the value returned by /ui/config when no explicit override is configured. Lite overrides it to 1s for a snappy inner loop. 5s is what a team watching runs progress expects, and one api process serves about 1,400 open tabs of a page with a run in progress at that rate \(Experiment 4, \#1196\); it was 30s before v0.5.9.

```go
const DefaultUIAutoRefreshIntervalSeconds = 5
```

<a name="PoolsReadOnlyDetail"></a>PoolsReadOnlyDetail is the stable problem detail of the 403 every pool write gets when server.pools\_read\_only is on. Clients may match on it to tell the lock apart from a missing permission.

```go
const PoolsReadOnlyDetail = "pools are read-only on this server: their slots are managed by the platform operator"
```

## Variables

<a name="ErrMissingDependency"></a>ErrMissingDependency is wrapped by the error NewServer returns for a Dependencies value that leaves out a required dependency.

```go
var ErrMissingDependency = errors.New("missing required dependency")
```

<a name="ErrNotFound"></a>ErrNotFound is returned by repositories when a resource does not exist.

```go
var ErrNotFound = domain.ErrNotFound
```

<a name="AbortProblem"></a>
## func [AbortProblem](<https://github.com/dexadata/dexaflow/blob/main/internal/api/problem.go#L39>)

```go
func AbortProblem(c *gin.Context, status int, title, detail string)
```

AbortProblem writes an RFC 7807 problem response and stops the handler chain.

detail is sent to the client as\-is, so it must never carry a driver, filesystem, or network error's text. Where the real cause must stay server\-side, use AbortProblemCause.

<a name="AbortProblemCause"></a>
## func [AbortProblemCause](<https://github.com/dexadata/dexaflow/blob/main/internal/api/problem.go#L27>)

```go
func AbortProblemCause(c *gin.Context, status int, title, detail string, cause error)
```

AbortProblemCause writes an RFC 7807 problem whose detail is safe to show the caller, while handing the real error to the request log.

It exists because the two audiences need different text. A storage failure carries the driver's own message, for Postgres, "severity: message \(SQLSTATE code\)" with our constraint, table and column names inside, and a tenant of a multi\-tenant control plane must not receive that \(CWE\-209\). The operator, on the other hand, cannot diagnose the failure without it. Redacting the response and dropping the cause would trade one defect for another, so cause is logged verbatim under its own field.

cause may be nil, which makes this identical to AbortProblem.

<a name="BodyLimit"></a>
## func [BodyLimit](<https://github.com/dexadata/dexaflow/blob/main/internal/api/body_limit.go#L33>)

```go
func BodyLimit(requestMax, uploadMax int64) gin.HandlerFunc
```

BodyLimit caps the size of request bodies \(\#1613\): requestMax for every route, uploadMax for the routes in uploadRoutes. A limit of 0 leaves those routes unlimited, so with both at 0 \(the default\) nothing changes and the middleware is not wired.

A body whose Content\-Length is over the limit is refused with 413 before the handler runs. A body without one \(chunked\) is cut at the limit while the handler reads it; the handler then fails to decode it and answers a 4xx, which is replaced by the same 413 problem, so the client sees one answer whichever way the body arrived.

<a name="CORS"></a>
## func [CORS](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L110>)

```go
func CORS(allowed []string) gin.HandlerFunc
```

CORS allows the configured origins \(use "\*" to allow any\).

<a name="DevBypassAuth"></a>
## func [DevBypassAuth](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L173>)

```go
func DevBypassAuth() gin.HandlerFunc
```

DevBypassAuth authenticates EVERY request as a fixed admin user, with no token required. It exists solely for \`dexaflow lite\` \(the local, unsandboxed loop\) so a developer reaches the UI without logging in. It must only be wired under the explicit dev opt\-in \(config auth.dev\_no\_auth\); the server logs a prominent warning when it is active. NEVER enable this in production.

<a name="GzipJSON"></a>
## func [GzipJSON](<https://github.com/dexadata/dexaflow/blob/main/internal/api/gzip.go#L46>)

```go
func GzipJSON() gin.HandlerFunc
```

GzipJSON compresses JSON and NDJSON responses on the API and UI surfaces \(/api/v2/\*, /ui/\*\) when the client accepts gzip and the body reaches 1 KB. It is wired only when server.gzip\_responses is on.

Streams are never compressed: log routes are skipped by path, and a handler that flushes before 1 KB is buffered \(a live tail, an SSE feed\) is served as identity from then on, so every line still reaches the client the moment it is flushed. Static assets are precompressed by their own handler and are not on these prefixes.

Routes that return secrets, tokens or code \(variables, connections, XComs, auth and the session token, IDE files, DAG sources, a task instance with its rendered fields\) are never compressed either: with a secret and attacker\-reflected input in one compressed body, the response length can leak the secret \(BREACH\). Only a full 200 or 201 body is compressed.

A handler panic is passed on untouched: the held\-back response is dropped so gin.Recovery can still answer 500.

<a name="JWTAuth"></a>
## func [JWTAuth](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L183>)

```go
func JWTAuth(authn auth.Authenticator) gin.HandlerFunc
```

JWTAuth validates the bearer token on protected routes and stores the user.

<a name="NewServer"></a>
## func [NewServer](<https://github.com/dexadata/dexaflow/blob/main/internal/api/server.go#L41>)

```go
func NewServer(deps Dependencies) (*gin.Engine, error)
```

NewServer builds the gin engine with the full middleware chain, health and metrics endpoints, embedded Scalar docs, and the auth token endpoint. It returns an error wrapping ErrMissingDependency, and builds nothing, when deps leaves out a required dependency \(see Dependencies.Validate\).

<a name="NoStoreOnVolatileRoutes"></a>
## func [NoStoreOnVolatileRoutes](<https://github.com/dexadata/dexaflow/blob/main/internal/api/no_store.go#L34>)

```go
func NoStoreOnVolatileRoutes() gin.HandlerFunc
```

NoStoreOnVolatileRoutes stamps every response served from the SPA\-facing JSON surface \(\`/api/v2/\*\` and \`/ui/\*\`\) with \`Cache\-Control: no\-store, must\-revalidate\` so the browser HTTP cache does not return a pre\-mutation payload after a PATCH/POST/DELETE.

Why this exists \(\#211, \#271\): mark\-state PATCH succeeds in single\-digit ms; TanStack Query then invalidates its in\-memory cache and re\-fetches. Without this header, the browser's HTTP cache layer can serve the OLD response to that re\-fetch \(the original GET response had no explicit caching directive, so the browser falls back to heuristic caching\). The SPA renders stale state until the next "natural" refresh — the observable symptom is "marcar como falha demora uma eternidade".

Static assets \(\`/ide/vs/\*\` for the Monaco bundle\) are content\-hashed and SHOULD cache, so they are explicitly excluded.

We deliberately use "no\-store" rather than "no\-cache": no\-store forbids the browser from writing the response anywhere, which is the strongest guarantee we can give a TanStack\-backed SPA. "must\-revalidate" is added for older intermediaries \(proxies / SW\) that may not honor no\-store alone. This is ADR\-0017\-compatible: no SPA changes.

A handler that computes an ETag and authorizes every request may replace the header with "private, no\-cache" when ui.etag\_revalidation is on, so the browser revalidates instead of refetching \(tiSummariesHandler\).

<a name="ObservabilityHandler"></a>
## func [ObservabilityHandler](<https://github.com/dexadata/dexaflow/blob/main/internal/api/observability_handler.go#L28>)

```go
func ObservabilityHandler(registry *prometheus.Registry, checks map[string]HealthChecker, opts ...ObservabilityOption) http.Handler
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

<a name="RejectEncodedPathSeparators"></a>
## func [RejectEncodedPathSeparators](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L498>)

```go
func RejectEncodedPathSeparators() gin.HandlerFunc
```

RejectEncodedPathSeparators refuses a request whose path carries a percent\-encoded slash \(%2F\) or backslash \(%5C\) with 400. gin routes on the decoded path, so /auth%2Ftoken is served as /auth/token, while a reverse proxy or load balancer in front of the server matches its rules on the raw path and would let it through a rule written for /auth/token. Refusing the encoded form keeps both sides reading the same path. The XCom key in the task instance route is the one exception \(see xcomEntriesSegment\).

<a name="RequestID"></a>
## func [RequestID](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L42>)

```go
func RequestID() gin.HandlerFunc
```

RequestID assigns a request id \(honoring an inbound X\-Request\-Id\) and echoes it.

<a name="RequireDeployPermission"></a>
## func [RequireDeployPermission](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L371>)

```go
func RequireDeployPermission(viaIssuer bool) gin.HandlerFunc
```

RequireDeployPermission guards the routes that deploy a DAG: registering a version and setting the current version \(ADR 0074\). deploy:dag, the deployer role's grant, allows them, and so does write:dag, so a role that deployed before deploy:dag existed, a custom one included, still does. A scoped token also needs dexaflow:deploy. With viaIssuer \(auth.trusted\_issuer.deploy\_audience, \#1773\) the principal must also be a trusted\-issuer bearer of the deploy audience: a session, a password login token, an API token or a bearer of another audience is refused with 403, whatever its roles and scopes.

<a name="RequireDeployRead"></a>
## func [RequireDeployRead](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L404>)

```go
func RequireDeployRead() gin.HandlerFunc
```

RequireDeployRead guards a DAG read that a deploy needs: the DAG list, a DAG, its details and its versions \(ADR 0074\). It needs read:dag, and a scoped token needs dexaflow:read or dexaflow:deploy, so a deploy token reads what it deploys and none of the other reads its owner may have.

<a name="RequirePermission"></a>
## func [RequirePermission](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L339>)

```go
func RequirePermission(action, resource string) gin.HandlerFunc
```

RequirePermission enforces an RBAC permission on a route. For a principal whose token carries scopes \(ADR 0067\), a read route also needs dexaflow:read, and any other route is refused: a write a scoped token may call names its scope with RequireScopedPermission, so a write nobody mapped fails closed.

<a name="RequireScope"></a>
## func [RequireScope](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L358>)

```go
func RequireScope(scope string) gin.HandlerFunc
```

RequireScope is the scope check alone, for a protected route that checks no role permission \(any signed\-in user may call it\) and that a scoped token may reach when it carries scope. Without it, jwtAuth refuses a scoped token on the route.

<a name="RequireScopedPermission"></a>
## func [RequireScopedPermission](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L350>)

```go
func RequireScopedPermission(action, resource, scope string) gin.HandlerFunc
```

RequireScopedPermission is RequirePermission for a write route that a scoped token may call when it carries scope \(ADR 0067\). The role check still runs first: a scope narrows what roles allow and never widens it.

<a name="RequireUndeployPermission"></a>
## func [RequireUndeployPermission](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L381>)

```go
func RequireUndeployPermission(viaIssuer bool) gin.HandlerFunc
```

RequireUndeployPermission guards deleting a DAG, which undeploys it. It needs write:dag. Without viaIssuer a scoped token is refused, as on every write no scope grants. With viaIssuer \(\#1773\) it is a deploy decision like RequireDeployPermission: only a trusted\-issuer bearer of the deploy audience with dexaflow:deploy may call it, so no undeploy skips the platform that owns deploys.

<a name="SchemaWaitHandler"></a>
## func [SchemaWaitHandler](<https://github.com/dexadata/dexaflow/blob/main/internal/api/schema_wait_handler.go#L22>)

```go
func SchemaWaitHandler(registry *prometheus.Registry, reason func() string, opts ...ObservabilityOption) http.Handler
```

SchemaWaitHandler is what a booting process serves while it waits for its database schema \(ADR 0090 decision 1\), on the API port and on the metrics port. /healthz answers 200, so liveness and the startup probe leave the pod up for diagnosis instead of restarting it into a crash loop. /readyz answers 503 with reason\(\), the fixed reason and what to do, which never carries the DSN or a host since the endpoint is unauthenticated. /metrics is served when registry is set, so the wait is visible. Every other request gets the same 503: no API route, and so no write path, exists before the schema is current.

<a name="StructuredLogger"></a>
## func [StructuredLogger](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L63>)

```go
func StructuredLogger(logger *slog.Logger) gin.HandlerFunc
```

StructuredLogger logs one structured line per request \(ADR 0010\).

<a name="UserFromContext"></a>
## func [UserFromContext](<https://github.com/dexadata/dexaflow/blob/main/internal/api/middleware.go#L325>)

```go
func UserFromContext(c *gin.Context) (*auth.User, bool)
```

UserFromContext returns the authenticated user stored by JWTAuth.

<a name="AuditLogPageReader"></a>
## type [AuditLogPageReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/keyset_pagination.go#L36-L38>)

AuditLogPageReader is the keyset form of AuditLogReader.ListAuditLogs.

```go
type AuditLogPageReader interface {
    ListAuditLogsAfter(ctx context.Context, tenant, dagID string, after domain.PageCursor, limit int) ([]domain.AuditLogEntry, int, error)
}
```

<a name="AuditLogReader"></a>
## type [AuditLogReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_audit.go#L17-L19>)

AuditLogReader lists recorded actions for the Audit Log view. dagID == "" means no DAG filter.

```go
type AuditLogReader interface {
    ListAuditLogs(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.AuditLogEntry, int, error)
}
```

<a name="AuditWriter"></a>
## type [AuditWriter](<https://github.com/dexadata/dexaflow/blob/main/internal/api/resources.go#L69-L74>)

AuditWriter records task\-level actions \(clear, mark state\) for the Audit Log view, with the acting user and the run/task in the entry.

```go
type AuditWriter interface {
    RecordTaskActionAudit(ctx context.Context, tenant, userID, action, dagID, runID, taskID string, tryNumber int) error
    // RecordRunActionAudit logs a run-level action with extra metadata
    // fields, such as the state a PATCH asked for when it stored another.
    RecordRunActionAudit(ctx context.Context, tenant, userID, action, dagID, runID string, metadata map[string]string) error
}
```

<a name="AuthAuditWriter"></a>
## type [AuthAuditWriter](<https://github.com/dexadata/dexaflow/blob/main/internal/api/oidc_handler.go#L59-L61>)

AuthAuditWriter records authentication events to the audit sink \(H5\). It is best\-effort: a write error never changes the auth outcome.

```go
type AuthAuditWriter interface {
    RecordAuthEvent(ctx context.Context, tenant, actorUserID, action, email, outcome string, extra map[string]string) error
}
```

<a name="AuthDeps"></a>
## type [AuthDeps](<https://github.com/dexadata/dexaflow/blob/main/internal/api/dependencies.go#L94-L159>)

AuthDeps are the user sign\-in: the request authenticator, the password and OIDC login flows, and the session cookie.

```go
type AuthDeps struct {
    // Authenticator verifies every request's session and issues the token of
    // POST /auth/token. Required, also under DevNoAuth, which still serves
    // that route.
    Authenticator auth.Authenticator
    // RateLimiter caps the failed logins of POST /auth/token per client.
    // Required.
    RateLimiter *auth.RateLimiter
    // SharedRateLimits, when set, is the counter every API replica reaches
    // (Redis): the limiters the server builds for its other sign-in routes
    // count through it, so N replicas allow the configured limit and not N
    // times it (#2357). Nil keeps them in process.
    SharedRateLimits auth.SharedCounter
    TokenTTLSecs     int
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
```

<a name="CIOIDCExchangeRecorder"></a>
## type [CIOIDCExchangeRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ci_oidc.go#L60-L62>)

CIOIDCExchangeRecorder counts exchanges by issuer entry and outcome. observability.Metrics implements it.

```go
type CIOIDCExchangeRecorder interface {
    RecordCIOIDCExchange(issuer, outcome string)
}
```

<a name="CIOIDCVerifier"></a>
## type [CIOIDCVerifier](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ci_oidc.go#L51-L56>)

CIOIDCVerifier checks CI provider tokens against the configured issuers and lists them per tenant. cioidc.Verifier implements it.

```go
type CIOIDCVerifier interface {
    // Verify returns what the token may act as, or a *cioidc.Error.
    Verify(ctx context.Context, raw string) (*cioidc.Identity, error)
    // Advertised returns the entries of tenant for GET /api/v2/deployDefaults.
    Advertised(tenant string) []domain.CIOIDCIssuer
}
```

<a name="CLIDeviceStore"></a>
## type [CLIDeviceStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/cli_device.go#L195-L210>)

CLIDeviceStore keeps the device sign\-ins of the CLI \(ADR 0081\). storage implements it. Every method decides on the database's clock and keys a sign\-in by the HMAC of its codes, never the codes.

```go
type CLIDeviceStore interface {
    // CreateCLIDeviceAuthorization starts a sign-in as req describes. It is
    // auth.ErrDeviceSignInsFull or auth.ErrDeviceSignInsFromRequester over a
    // cap, and domain.ErrConflict when the user code is taken.
    CreateCLIDeviceAuthorization(ctx context.Context, req auth.DeviceAuthorizationRequest) (auth.DeviceAuthorization, error)
    // FindCLIDeviceByUserCode returns the pending, unexpired sign-in of a
    // user code, or domain.ErrNotFound.
    FindCLIDeviceByUserCode(ctx context.Context, userCodeHash string) (auth.DeviceAuthorization, error)
    // DecideCLIDeviceAuthorization approves a pending sign-in as user, or
    // cancels it. It is domain.ErrNotFound when the sign-in is not pending,
    // expired, or the user is not in user.TenantID.
    DecideCLIDeviceAuthorization(ctx context.Context, id string, approve bool, user auth.User) error
    // PollCLIDeviceAuthorization answers one poll; an approved sign-in is
    // redeemed by exactly one.
    PollCLIDeviceAuthorization(ctx context.Context, deviceCodeHash string) (auth.DevicePoll, error)
}
```

<a name="ConnectionStore"></a>
## type [ConnectionStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_connections.go#L76-L82>)

ConnectionStore reads and writes Airflow\-style Connections for the Admin UI. Password is encrypted at rest by the store \(ADR 0019\) and never returned.

```go
type ConnectionStore interface {
    ListConnections(ctx context.Context, tenant string, limit, offset int) ([]domain.Connection, int, error)
    ListConnectionsFiltered(ctx context.Context, tenant, pattern, prefixPattern string, limit, offset int) ([]domain.Connection, int, error)
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
## type [DagLatestRunsReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_dags.go#L23-L25>)

DagLatestRunsReader fetches the most\-recent runs for a set of DAGs in one query, so /ui/dags can embed run history without an N\+1.

```go
type DagLatestRunsReader interface {
    LatestRunsForDags(ctx context.Context, tenant string, dagIDs []string, perDag int) (map[string][]domain.DagRun, error)
}
```

<a name="DagRepository"></a>
## type [DagRepository](<https://github.com/dexadata/dexaflow/blob/main/internal/api/resources.go#L26-L37>)

DagRepository reads, updates, and deletes registered DAGs.

```go
type DagRepository interface {
    ListDags(ctx context.Context, tenant string, limit, offset int) ([]domain.DAG, int, error)
    GetDag(ctx context.Context, tenant, dagID string) (domain.DAG, error)
    // SetPausedBy sets is_paused on behalf of the user actorUserID, who a
    // change is audited for.
    SetPausedBy(ctx context.Context, tenant, actorUserID, dagID string, paused bool) (domain.DAG, error)
    DeleteDag(ctx context.Context, tenant, dagID string) error
    ClearDagHistory(ctx context.Context, tenant, dagID string) error
    ListDagsFiltered(ctx context.Context, tenant, runState string, paused *bool, limit, offset int) ([]domain.DAG, int, error)
    ListDagsWithFilter(ctx context.Context, tenant string, filter domain.DagFilter) ([]domain.DAG, int, error)
    ListDagTags(ctx context.Context, tenant, pattern, prefixPattern string, limit, offset int) (tags []string, total int, err error)
}
```

<a name="DagRunPageReader"></a>
## type [DagRunPageReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/keyset_pagination.go#L31-L33>)

DagRunPageReader is the keyset form of DagRunRepository.ListDagRuns.

```go
type DagRunPageReader interface {
    ListDagRunsAfter(ctx context.Context, tenant, dagID string, states []string, after domain.PageCursor, limit int) ([]domain.DagRun, int, error)
}
```

<a name="DagRunRepository"></a>
## type [DagRunRepository](<https://github.com/dexadata/dexaflow/blob/main/internal/api/resources.go#L40-L47>)

DagRunRepository reads and creates DAG runs.

```go
type DagRunRepository interface {
    ListDagRuns(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, int, error)
    ListDagRunsWithFilter(ctx context.Context, tenant, dagID string, filter domain.DagRunFilter) ([]domain.DagRun, int, error)
    GetDagRun(ctx context.Context, tenant, dagID, runID string) (domain.DagRun, error)
    CreateDagRun(ctx context.Context, tenant, dagID string, run domain.DagRun) (domain.DagRun, error)
    SetDagRunState(ctx context.Context, tenant, dagID, runID, state string) error
    DeleteDagRun(ctx context.Context, tenant, dagID, runID string) error
}
```

<a name="DagSourceReader"></a>
## type [DagSourceReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_tasks.go#L258-L260>)

DagSourceReader reads one version of a DAG and the spec registered with it: the version numbered versionNumber \(the version\_number of the dagVersions listing\), or the newest version when versionNumber is 0. It returns domain.ErrNotFound when the caller's tenant has no such version. The spec may be shared with a cache: callers only read it.

```go
type DagSourceReader interface {
    GetDagVersionSpec(ctx context.Context, tenant, dagID string, versionNumber int) (domain.DagVersion, domain.DAGSpec, error)
}
```

<a name="DagSpecReader"></a>
## type [DagSpecReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_structure.go#L16-L18>)

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
## type [DagVersionRepository](<https://github.com/dexadata/dexaflow/blob/main/internal/api/versions.go#L17-L28>)

DagVersionRepository registers compiled DAG versions and chooses which one is current.

```go
type DagVersionRepository interface {
    // RegisterDagVersionAs upserts the DAG and inserts a version keyed by
    // reg.SpecHash on behalf of reg.ActorUserID, reporting whether a new
    // version was created (false if the hash already existed: the push is
    // idempotent) and the DAG's is_paused afterwards. reg.IsPaused, when set,
    // is the DAG's state after the register.
    RegisterDagVersionAs(ctx context.Context, tenant string, reg domain.DagRegistration) (domain.DagRegistered, error)
    // SetCurrentDagVersionAs makes the version req.Selector names current,
    // after req.Admit accepts its spec, on behalf of req.ActorUserID. Runs
    // that already exist keep their version; is_paused is never changed.
    SetCurrentDagVersionAs(ctx context.Context, tenant string, req domain.DagSetCurrent) (domain.CurrentVersionChange, error)
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
## type [Dependencies](<https://github.com/dexadata/dexaflow/blob/main/internal/api/dependencies.go#L23-L31>)

Dependencies bundles everything the HTTP server needs, one group per concern. NewServer refuses a value that leaves out a dependency a route it registers cannot run without; see Validate.

```go
type Dependencies struct {
    Observability ObservabilityDeps
    Server        ServerDeps
    Auth          AuthDeps
    Issuers       IssuerDeps
    UI            UIDeps
    Execution     ExecutionDeps
    Stores        StoreDeps
}
```

<a name="Dependencies.Validate"></a>
### func \(Dependencies\) [Validate](<https://github.com/dexadata/dexaflow/blob/main/internal/api/dependencies.go#L347>)

```go
func (d Dependencies) Validate() error
```

Validate reports the dependencies d leaves out that the server cannot run without: the ones every server needs \(a logger, an authenticator, a login rate limiter\), and the ones a feature that is turned on reads on every request it serves. It names each missing one by its field path, such as Auth.Authenticator. A nil error means NewServer can build the server.

<a name="ExecutionDeps"></a>
## type [ExecutionDeps](<https://github.com/dexadata/dexaflow/blob/main/internal/api/dependencies.go#L249-L299>)

ExecutionDeps are the checks a DAG registration runs against the execution and deploy settings, and what its answer reports about them.

```go
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
## type [HealthChecker](<https://github.com/dexadata/dexaflow/blob/main/internal/api/health.go#L16-L18>)

HealthChecker reports dependency health for readiness checks.

```go
type HealthChecker interface {
    Ping(ctx context.Context) error
}
```

<a name="Heartbeater"></a>
## type [Heartbeater](<https://github.com/dexadata/dexaflow/blob/main/internal/api/monitor.go#L19-L21>)

Heartbeater reports a long\-running component's liveness and last heartbeat for the monitor health endpoint. The scheduler implements it.

```go
type Heartbeater interface {
    Heartbeat() (healthy bool, last time.Time)
}
```

<a name="ImportErrorStore"></a>
## type [ImportErrorStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/import_errors.go#L20-L25>)

ImportErrorStore reads and writes DAG parse/compile errors that back Airflow's "Import Errors" banner on the home dashboard. The \`dexaflow lite\` watcher writes an entry on a failed compile and clears it on the next good compile; the public GET /api/v2/importErrors feed is what the UI polls.

```go
type ImportErrorStore interface {
    ListImportErrors(ctx context.Context, tenant string) ([]domain.ImportError, error)
    ListImportErrorsFiltered(ctx context.Context, tenant, filename, bundleName string, limit, offset int) ([]domain.ImportError, int, error)
    SetImportError(ctx context.Context, tenant string, e domain.ImportError) error
    ClearImportError(ctx context.Context, tenant, filename string) error
}
```

<a name="IssuerDeps"></a>
## type [IssuerDeps](<https://github.com/dexadata/dexaflow/blob/main/internal/api/dependencies.go#L164-L208>)

IssuerDeps are the callers whose credentials come from outside the login flow: the trusted issuer, the CI OIDC issuers and the operator service token.

```go
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
    // ServiceUsage serves GET /api/v2/service/usage (ADR 0096); nil leaves
    // the route absent.
    ServiceUsage UsageEventReader
}
```

<a name="LivenessCheck"></a>
## type [LivenessCheck](<https://github.com/dexadata/dexaflow/blob/main/internal/api/health.go#L69>)

LivenessCheck is an opt\-in liveness condition: a non\-nil error fails /healthz with 503 and the error's text, so the kubelet restarts the pod. Only a fault a restart cures belongs here, never a dependency \(see livenessHandler\).

```go
type LivenessCheck func() error
```

<a name="LogReader"></a>
## type [LogReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/logs.go#L23-L26>)

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

<a name="OIDCSettings"></a>
## type [OIDCSettings](<https://github.com/dexadata/dexaflow/blob/main/internal/api/oidc_handler.go#L65-L84>)

OIDCSettings are the auth.oidc settings the login flow and the credential gate read; the ID token checks live in the oidc.Flow.

```go
type OIDCSettings struct {
    // Issuer is auth.oidc.issuer, the pinned issuer of every ID token.
    Issuer string
    // RedirectURL is auth.oidc.redirect_url, this server's callback URL.
    RedirectURL string
    // RoleMappings is auth.oidc.role_mappings: an IdP group to a role name.
    RoleMappings map[string]string
    // DefaultRole is auth.oidc.default_role, granted to a user no mapping
    // gives a role; empty grants none.
    DefaultRole string
    // BreakGlassEmails is auth.oidc.break_glass_emails, the local password
    // logins permitted while provider is "oidc".
    BreakGlassEmails []string
    // JITProvisioning is auth.oidc.jit_provisioning: create a user on first
    // OIDC login.
    JITProvisioning bool
    // AutoRedirect is auth.oidc.auto_redirect: start the login flow on the
    // sign-in page instead of rendering it.
    AutoRedirect bool
}
```

<a name="OIDCUserStore"></a>
## type [OIDCUserStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/oidc_handler.go#L41-L55>)

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

<a name="ObservabilityDeps"></a>
## type [ObservabilityDeps](<https://github.com/dexadata/dexaflow/blob/main/internal/api/dependencies.go#L35-L55>)

ObservabilityDeps are the logger, metrics, tracing and health reporting of the server.

```go
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
    // Liveness, when set, can fail /healthz (server.liveness_scheduler_stall).
    // Nil keeps /healthz unconditional.
    Liveness LivenessCheck
    // ProbeBudget (server.probe_budget) bounds /readyz and the dependency
    // checks of /api/v2/monitor/health. Non-positive means 2s.
    ProbeBudget time.Duration
    // ExecutorInfo is served on /api/v2/monitor/executor.
    ExecutorInfo ExecutorInfo
}
```

<a name="ObservabilityOption"></a>
## type [ObservabilityOption](<https://github.com/dexadata/dexaflow/blob/main/internal/api/observability_handler.go#L50>)

ObservabilityOption adjusts ObservabilityHandler.

```go
type ObservabilityOption func(*observabilityOptions)
```

<a name="WithLivenessCheck"></a>
### func [WithLivenessCheck](<https://github.com/dexadata/dexaflow/blob/main/internal/api/observability_handler.go#L60>)

```go
func WithLivenessCheck(check LivenessCheck) ObservabilityOption
```

WithLivenessCheck lets check fail /healthz on the metrics listener, the probe target of a scheduler\-only pod.

<a name="WithProbeBudget"></a>
### func [WithProbeBudget](<https://github.com/dexadata/dexaflow/blob/main/internal/api/observability_handler.go#L66>)

```go
func WithProbeBudget(d time.Duration) ObservabilityOption
```

WithProbeBudget bounds /readyz on the metrics listener with the same server.probe\_budget as the API listener's. Without it the budget is 2s.

<a name="WithoutLegacyMetricNames"></a>
### func [WithoutLegacyMetricNames](<https://github.com/dexadata/dexaflow/blob/main/internal/api/observability_handler.go#L74>)

```go
func WithoutLegacyMetricNames() ObservabilityOption
```

WithoutLegacyMetricNames serves each metric family once, under its dexaflow\_ name only \(observability.metrics.drop\_legacy\_names\). It halves the scrape and the per\-scrape copy of every family. It is an opt\-in for installs that do not need the leoflow\_ names; the default keeps them.

<a name="PoolStore"></a>
## type [PoolStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_pools.go#L17-L23>)

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

<a name="ResourceAuditWriter"></a>
## type [ResourceAuditWriter](<https://github.com/dexadata/dexaflow/blob/main/internal/api/resource_audit.go#L15-L17>)

ResourceAuditWriter records an accepted admin write to a named resource of the tenant: a variable, a connection or a pool \(\#2228\). metadata names the fields the request set and never carries a secret value.

```go
type ResourceAuditWriter interface {
    RecordResourceAudit(ctx context.Context, tenant, userID, action, resourceType, resourceID string, metadata map[string]string) error
}
```

<a name="RunVersionLabeler"></a>
## type [RunVersionLabeler](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_summaries.go#L206-L208>)

RunVersionLabeler reads the version label of many runs of one DAG in one query. The storage repository implements it; a DagRunRepository without it falls back to one GetDagRun per run.

```go
type RunVersionLabeler interface {
    DagRunVersionLabels(ctx context.Context, tenant, dagID string, runIDs []string) (map[string]string, error)
}
```

<a name="SchemaChecker"></a>
## type [SchemaChecker](<https://github.com/dexadata/dexaflow/blob/main/internal/api/health.go#L31-L33>)

SchemaChecker is an optional capability a HealthChecker may also implement: asserting that the dependency's SCHEMA, not just its connection, is the one this binary requires. \*storage.Postgres implements it; Redis and other schema\-less dependencies do not, and keep the Ping\-only contract.

It exists because a Ping is not evidence of a usable database \(\#1023\). The invariant is enforced at boot, where the server waits, not ready, until schema\_migrations is present and current \(ADR 0090\); but a boot check says nothing about a database that changes UNDER a running pod, which is exactly what an ephemeral\-volume recycle, a restore from an older backup, a failover to a lagging replica, or a repointed database.url does.

```go
type SchemaChecker interface {
    SchemaReady(ctx context.Context) error
}
```

<a name="ServerDeps"></a>
## type [ServerDeps](<https://github.com/dexadata/dexaflow/blob/main/internal/api/dependencies.go#L59-L90>)

ServerDeps are the server\-wide HTTP settings \(server.\* in the config\) and the edition gates.

```go
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
    // server sets it from scheduler.pools.enabled, the same condition as the
    // scheduler's pool gate; otherwise the Pools screen gets the graceful
    // empty-collection stub.
    Pools bool
    // PoolsReadOnly is server.pools_read_only: the pool API serves reads only and
    // every create, resize and delete answers 403 with PoolsReadOnlyDetail, for
    // every role including tenant admin. False keeps the write:pool-gated CRUD.
    PoolsReadOnly bool
}
```

<a name="ServiceAccountReader"></a>
## type [ServiceAccountReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/task_pods.go#L23-L28>)

ServiceAccountReader reads the ServiceAccounts of the namespace task pods run in. It lives here, next to the dry run that consumes it; the executor's Kubernetes client implements it.

```go
type ServiceAccountReader interface {
    // ImagePullSecrets returns how many imagePullSecrets the ServiceAccount
    // name carries, and found false when it does not exist. An error means
    // the lookup could not tell.
    ImagePullSecrets(ctx context.Context, name string) (count int, found bool, err error)
}
```

<a name="ServiceTenantStore"></a>
## type [ServiceTenantStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/service.go#L24-L38>)

ServiceTenantStore is what the service API needs from storage \(\#1283\). storage.Repository implements it.

```go
type ServiceTenantStore interface {
    // EnsureTenant creates a tenant with the built-in roles and default pool,
    // or fills in what is missing; created reports whether it was new.
    // limits sets the tenant limits it carries and leaves the others.
    EnsureTenant(ctx context.Context, name, displayName string, defaultPoolSlots int, limits domain.TenantLimitsUpdate) (created bool, err error)
    // EnsureIssuerUser makes sure a passwordless user linked to (provider,
    // subject) exists in tenant with exactly roles.
    EnsureIssuerUser(ctx context.Context, tenant, email, provider, subject string, roles []string) (*auth.User, bool, error)
    // SuspendTenant suspends tenant or changes its suspension (#2181);
    // ResumeTenant lifts it, and TenantSuspension reads it (the zero value is
    // a tenant that is not suspended). An unknown tenant is ErrNotFound.
    SuspendTenant(ctx context.Context, tenant string, s domain.TenantSuspension) error
    ResumeTenant(ctx context.Context, tenant string) error
    TenantSuspension(ctx context.Context, tenant string) (domain.TenantSuspension, error)
}
```

<a name="StoreDeps"></a>
## type [StoreDeps](<https://github.com/dexadata/dexaflow/blob/main/internal/api/dependencies.go#L303-L336>)

StoreDeps are the resource repositories. Routes for nil repositories are not registered.

```go
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
```

<a name="TaskInstanceRepository"></a>
## type [TaskInstanceRepository](<https://github.com/dexadata/dexaflow/blob/main/internal/api/resources.go#L51-L65>)

TaskInstanceRepository reads task instances, clears them for re\-run, and sets their state directly \(the UI's mark\-success/failed actions\).

```go
type TaskInstanceRepository interface {
    ListTaskInstances(ctx context.Context, tenant, dagID, runID string, limit, offset int) ([]domain.TaskInstance, int, error)
    ListTaskInstancesWithFilter(ctx context.Context, tenant, dagID, runID string, filter domain.TaskInstanceFilter) ([]domain.TaskInstance, int, error)
    // ListTaskInstanceAttempts returns every attempt for (run, task), oldest
    // first — the current row UNIONed with the archived history. The UI's
    // /tries endpoint needs all attempts to render its navigable tabs.
    ListTaskInstanceAttempts(ctx context.Context, tenant, dagID, runID, taskID string) ([]domain.TaskInstance, error)
    ClearTaskInstances(ctx context.Context, tenant, dagID, runID string, taskIDs []string, onlyFailed bool, opts domain.ClearOptions) (int, error)
    // RunsKeepingVersion lists, without writing, the runs among runIDs that a
    // clear with opts asks to move to the DAG's current version and that keep
    // their own, with the reason (ADR 0086); domain.ErrNotFound for a run the
    // tenant does not have.
    RunsKeepingVersion(ctx context.Context, tenant, dagID string, runIDs []string, opts domain.ClearOptions) ([]domain.ClearVersionKept, error)
    SetTaskInstanceState(ctx context.Context, tenant, dagID, runID, taskID, state string) error
}
```

<a name="TaskPodSettings"></a>
## type [TaskPodSettings](<https://github.com/dexadata/dexaflow/blob/main/internal/api/task_pods.go#L32-L46>)

TaskPodSettings is how this server starts task pods, as the register dry run reports it. The zero value is a server that starts no task pods.

```go
type TaskPodSettings struct {
    // Enabled is true when this server dispatches tasks to pods.
    Enabled bool
    // RunAsNonRoot is executor.run_tasks_as_non_root: task pods refuse an
    // image that runs as root, or as a user that is not a number.
    RunAsNonRoot bool
    // DefaultServiceAccount is executor.task_service_account, the account
    // of a task that names none; "" runs it as the namespace's default.
    DefaultServiceAccount string
    // ServiceAccounts reads the task ServiceAccounts; nil reports each one
    // unchecked, with Unread as the reason.
    ServiceAccounts ServiceAccountReader
    // Unread says why ServiceAccounts is nil.
    Unread string
}
```

<a name="TaskSummaryReader"></a>
## type [TaskSummaryReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_summaries.go#L18-L20>)

TaskSummaryReader fetches task instances across a set of runs of a DAG, the source for the grid's per\-cell state summaries.

```go
type TaskSummaryReader interface {
    TaskInstancesForRuns(ctx context.Context, tenant, dagID string, runIDs []string) ([]domain.TaskInstance, error)
}
```

<a name="TokenRenewer"></a>
## type [TokenRenewer](<https://github.com/dexadata/dexaflow/blob/main/internal/api/auth_handler.go#L22-L24>)

TokenRenewer re\-mints a still\-valid user bearer with a fresh short TTL, bounded by max\_lifetime since first login. \*auth.JWTAuthenticator implements it via RenewUserToken; the handler depends on this narrow interface so the renew route can be tested without a real signing key. It takes a context because renewal re\-proves the principal against the user store, so it must be canceled with the request.

```go
type TokenRenewer interface {
    RenewUserToken(ctx context.Context, token string, ttl, maxLifetime time.Duration) (renewed string, ok bool, err error)
}
```

<a name="TokenStore"></a>
## type [TokenStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/api_tokens.go#L39-L43>)

TokenStore keeps the API tokens of a tenant \(ADR 0074\). The store sees only the hash of a token. A user outside the tenant, or one holding too many live tokens, is domain.ErrValidation; a live token of the same owner and name is domain.ErrConflict; a token not found under the owner limit is domain.ErrNotFound.

```go
type TokenStore interface {
    CreateAPIToken(ctx context.Context, tenant, userID, name, hash string, scopes []string, expiresAt time.Time) (auth.APIToken, error)
    ListAPITokens(ctx context.Context, tenant, ownerID string, limit, offset int) ([]auth.APIToken, int, error)
    RevokeAPIToken(ctx context.Context, tenant, id, ownerID string) (auth.APIToken, error)
}
```

<a name="TrustedIssuer"></a>
## type [TrustedIssuer](<https://github.com/dexadata/dexaflow/blob/main/internal/api/issuer_session.go#L43>)

TrustedIssuer verifies handoff tokens from the operator's trusted issuer. issuer.Verifier implements it; the contract is public \(ADR 0099\).

```go
type TrustedIssuer = authn.TrustedIssuer
```

<a name="TrustedIssuerBearer"></a>
## type [TrustedIssuerBearer](<https://github.com/dexadata/dexaflow/blob/main/internal/api/issuer_bearer.go#L21-L27>)

TrustedIssuerBearer verifies bearer tokens from the operator's trusted issuer \(\#1468, ADR 0050 D9\). issuer.Verifier implements it.

```go
type TrustedIssuerBearer interface {
    // Provider is the key the issuer's users are linked under.
    Provider() string
    // VerifyBearer checks a token sent as a request bearer and returns the
    // identity it carries.
    VerifyBearer(ctx context.Context, raw string) (*issuer.Identity, error)
}
```

<a name="TrustedIssuerUserStore"></a>
## type [TrustedIssuerUserStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/issuer_session.go#L47-L49>)

TrustedIssuerUserStore resolves a linked user. storage implements it; it is the same lookup a returning SSO login uses.

```go
type TrustedIssuerUserStore interface {
    FindUserByOIDCSubject(ctx context.Context, provider, subject string) (*auth.User, bool, error)
}
```

<a name="UIDeps"></a>
## type [UIDeps](<https://github.com/dexadata/dexaflow/blob/main/internal/api/dependencies.go#L211-L245>)

UIDeps are the embedded web UI and the Lite editor.

```go
type UIDeps struct {
    // InstanceName is shown in the UI navbar (Airflow's instance_name). Empty
    // falls back to "Dexaflow"; `dexaflow lite` sets it to mark the DEV environment.
    InstanceName string
    // AutoRefreshIntervalSeconds controls the SPA's polling cadence for DAG /
    // DagRun / task-instance state refresh (Airflow's auto_refresh_interval).
    // Non-positive (the zero default) falls back to DefaultUIAutoRefreshIntervalSeconds
    // (5s; 30s before v0.5.9). `dexaflow lite` sets it to 1s for a snappy inner loop.
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
```

<a name="UIServer"></a>
## type [UIServer](<https://github.com/dexadata/dexaflow/blob/main/internal/api/server.go#L17-L20>)

UIServer serves the embedded single\-page app: static assets and an index.html shell that the SPA's client\-side router falls back to. It is satisfied by internal/ui.Server. When nil, the server runs API\-only and unknown paths return 404 instead of the SPA shell.

```go
type UIServer interface {
    StaticHandler() http.Handler
    Index(w http.ResponseWriter, basePath string)
}
```

<a name="UnitMisfitRecorder"></a>
## type [UnitMisfitRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/api/versions.go#L65-L67>)

UnitMisfitRecorder counts a task that does not fit the resource unit but is let through under executor.unit.enforce=warn, by stage. observability.Metrics satisfies it.

```go
type UnitMisfitRecorder interface {
    RecordUnitMisfit(stage string)
}
```

<a name="UnpinnedImageRecorder"></a>
## type [UnpinnedImageRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/api/versions.go#L114-L116>)

UnpinnedImageRecorder counts an image not pinned by digest that deploy.require\_digest warned about \(outcome "warned"\) or refused \("refused"\), by stage. observability.Metrics satisfies it.

```go
type UnpinnedImageRecorder interface {
    RecordUnpinnedImage(stage, outcome string)
}
```

<a name="UsageEventReader"></a>
## type [UsageEventReader](<https://github.com/dexadata/dexaflow/blob/main/internal/api/service_usage.go#L18-L23>)

UsageEventReader reads the task attempt usage events of ADR 0096.

```go
type UsageEventReader interface {
    // ReadEvents returns up to limit events after the cursor in serving
    // order, or a page that says recording is off or that the cursor is
    // older than the events kept.
    ReadEvents(ctx context.Context, after domain.UsageCursor, limit int) (domain.UsagePage, error)
}
```

<a name="UserActivator"></a>
## type [UserActivator](<https://github.com/dexadata/dexaflow/blob/main/internal/api/users_active.go#L21-L23>)

UserActivator activates and deactivates accounts for the admin user API \(\#960\). The store records user.activate or user.deactivate with the change, in one transaction, with actorUserID as the acting admin. A user that is not in tenant is domain.ErrNotFound.

```go
type UserActivator interface {
    SetUserActive(ctx context.Context, tenant, actorUserID, userID string, active bool) (domain.User, error)
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
## type [VariableStore](<https://github.com/dexadata/dexaflow/blob/main/internal/api/ui_variables.go#L15-L21>)

VariableStore reads and writes Airflow\-style Variables for the Admin UI.

```go
type VariableStore interface {
    ListVariables(ctx context.Context, tenant string, limit, offset int) ([]domain.Variable, int, error)
    ListVariablesFiltered(ctx context.Context, tenant, pattern, prefixPattern string, limit, offset int) ([]domain.Variable, int, error)
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
