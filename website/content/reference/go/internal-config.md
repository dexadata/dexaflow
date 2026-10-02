---
title: "internal/config"
linkTitle: "internal/config"
weight: 9
---

```go
import "github.com/dexadata/dexaflow/internal/config"
```

Package config loads Dexaflow configuration from defaults, an optional config file, and LEOFLOW\_\* environment variables, with flags taking precedence.

## Index

- [Constants](<#constants>)
- [func DefaultConfigFile\(\) \(string, error\)](<#DefaultConfigFile>)
- [func PersistSession\(path, serverURL, token string\) error](<#PersistSession>)
- [type AuthSection](<#AuthSection>)
- [type CORSSection](<#CORSSection>)
- [type Config](<#Config>)
  - [func Load\(configFile string, flags \*pflag.FlagSet\) \(\*Config, error\)](<#Load>)
- [type DatabaseSection](<#DatabaseSection>)
- [type DispatchSection](<#DispatchSection>)
- [type ExecutionSection](<#ExecutionSection>)
  - [func \(e ExecutionSection\) EffectiveMinIdle\(dagMinIdle int\) int](<#ExecutionSection.EffectiveMinIdle>)
- [type ExecutorSection](<#ExecutorSection>)
- [type HTTPExecutorSection](<#HTTPExecutorSection>)
- [type JWTSection](<#JWTSection>)
- [type LogsSection](<#LogsSection>)
- [type OIDCSection](<#OIDCSection>)
- [type OTelSection](<#OTelSection>)
- [type ObjectLogSection](<#ObjectLogSection>)
- [type ObservabilitySection](<#ObservabilitySection>)
- [type PlatformDefaultsSection](<#PlatformDefaultsSection>)
- [type RedisSection](<#RedisSection>)
- [type SchedulerSection](<#SchedulerSection>)
- [type SecretsSection](<#SecretsSection>)
- [type ServerConfig](<#ServerConfig>)
  - [func LoadServer\(configFile string, flags \*pflag.FlagSet\) \(\*ServerConfig, error\)](<#LoadServer>)
  - [func \(c \*ServerConfig\) Validate\(\) error](<#ServerConfig.Validate>)
- [type ServerSection](<#ServerSection>)
  - [func \(s ServerSection\) EffectiveRole\(\) string](<#ServerSection.EffectiveRole>)
  - [func \(s ServerSection\) ServesAPI\(\) bool](<#ServerSection.ServesAPI>)
  - [func \(s ServerSection\) ServesScheduler\(\) bool](<#ServerSection.ServesScheduler>)
- [type UISection](<#UISection>)


## Constants

<a name="RoleAll"></a>Server roles \(ADR 0049\).

```go
const (
    // RoleAll runs every component in one process (the default; Lite's only mode).
    RoleAll = "all"
    // RoleAPI runs the HTTP API + UI only (restricted network identity).
    RoleAPI = "api"
    // RoleScheduler runs the reconciler + dispatch + agent gRPC (privileged).
    RoleScheduler = "scheduler"
)
```

<a name="AuthProviderJWT"></a>Auth providers \(auth.provider allowlist\). "jwt" is the default credential authenticator; "oidc" adds the SSO login flow on top of it \(the JWT authenticator stays the request\-path verifier in both modes\).

```go
const (
    // AuthProviderJWT is the default: username/password issues an HS256 token.
    AuthProviderJWT = "jwt"
    // AuthProviderOIDC enables the OIDC/SSO login flow. It is Pro-gated and
    // requires the auth.oidc.* configuration; boot fails closed otherwise.
    AuthProviderOIDC = "oidc"
)
```

<a name="SecretScopingPermissive"></a>Secret policy allowlists \(ADR 0055\). auth.secret\_scoping and auth.secret\_liveness\_mode are validated against these; an unknown value fails boot closed. Empty is valid — serverDefaults sets the safe default for each.

```go
const (
    // SecretScopingPermissive delivers the whole tenant vault (today's behavior),
    // scoping only where a DAG declared; the default.
    SecretScopingPermissive = "permissive"
    // SecretScopingEnforce delivers only the declared subset.
    SecretScopingEnforce = "enforce"
    // SecretScopingOff disables scope-by-declaration entirely.
    SecretScopingOff = "off"
    // SecretLivenessObserve logs a would-have-denied without denying; the default.
    SecretLivenessObserve = "observe"
    // SecretLivenessEnforce denies secret delivery when the caller's TI is not live.
    SecretLivenessEnforce = "enforce"
    // AgentTokenTransportEnvVar sets the agent token as a plaintext env var on the
    // pod spec (today's behavior); the default.
    AgentTokenTransportEnvVar = "envvar"
    // AgentTokenTransportExchange mounts a projected ServiceAccount token the agent
    // exchanges (via TokenReview) for a task-scoped JWT, so no bearer sits in
    // plaintext on the Pod object (ADR 0055 Fix #3). Pro/Kubernetes-executor-only.
    AgentTokenTransportExchange = "exchange"
)
```

<a name="DefaultConfigFile"></a>
## func [DefaultConfigFile](<https://github.com/dexadata/leoflow/blob/main/internal/config/config.go#L69>)

```go
func DefaultConfigFile() (string, error)
```

DefaultConfigFile returns the default configuration file path, \~/.leoflow/config.yaml.

<a name="PersistSession"></a>
## func [PersistSession](<https://github.com/dexadata/leoflow/blob/main/internal/config/persist.go#L16>)

```go
func PersistSession(path, serverURL, token string) error
```

PersistSession writes the control\-plane server URL and auth token into the config file at path, preserving any other keys already there \(e.g. the Lite settings written by \`dexaflow setup\`\). It creates the file and its parent directory when absent, and keeps the file at 0600 because the token is a secret. An empty path is an error: the caller must resolve the target first.

<a name="AuthSection"></a>
## type [AuthSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L379-L438>)

AuthSection configures authentication.

```go
type AuthSection struct {
    Provider string     `mapstructure:"provider"`
    JWT      JWTSection `mapstructure:"jwt"`
    // OIDC configures the OIDC/SSO login flow. It is read only when Provider is
    // "oidc" (Pro-gated); the JWT authenticator remains the request-path verifier
    // in both modes.
    OIDC OIDCSection `mapstructure:"oidc"`
    // DevNoAuth disables authentication entirely, treating every request as an
    // admin. It exists ONLY for `leoflow dev` (local, unsandboxed). It is false by
    // default and the server logs a prominent warning when it is on. NEVER set
    // this in production (LEOFLOW_AUTH_DEV_NO_AUTH).
    DevNoAuth bool `mapstructure:"dev_no_auth"`
    // LoginRateLimitPerMinute caps failed /auth/token attempts per client IP per
    // minute (anti-brute-force). Only failures count, so a successful login never
    // consumes budget. Lite raises this well above the production default because
    // it is a local single-user tool where lockouts are pure friction.
    LoginRateLimitPerMinute int `mapstructure:"login_rate_limit_per_minute"`
    // SecretScoping is the operator scope-by-declaration policy (ADR 0055 D9):
    // "permissive" | "enforce" | "off". permissive (the default) delivers the
    // whole tenant vault when a DAG declares nothing and warns — but still
    // delivers the whole vault — when a DAG declares a narrower set; enforce
    // delivers only the declared subset (empty declaration → nothing); off
    // disables scoping. It is operator-scoped, NEVER author-settable. Empty = the
    // permissive default.
    SecretScoping string `mapstructure:"secret_scoping"`
    // SecretLivenessMode gates secret delivery on task-instance liveness (ADR 0055
    // E2): "observe" | "enforce". observe (the default) logs + audits a
    // would-have-denied when the caller's TI is not live but still delivers;
    // enforce denies with PermissionDenied. Empty = the observe default.
    SecretLivenessMode string `mapstructure:"secret_liveness_mode"`
    // MaxAttemptCredentialLifetime is the hard ceiling on how long a single task
    // attempt's agent credential may be kept alive by heartbeat renewal (ADR 0055
    // Fix #4). Past this age since first dispatch, the control plane stops renewing
    // the token on heartbeat and lets it lapse, bounding a runaway attempt. The
    // short per-attempt TTL still bounds a stolen/finished token independently;
    // this caps the total renewed lifetime. It governs a second guarantee too: the
    // Kubernetes executor floors a task pod's activeDeadlineSeconds with it when
    // the DAG declares no execution_timeout, so a pod whose agent keeps retrying
    // its report through a total control-plane outage is not left Running
    // forever (a declared timeout is never shortened). Generous by default (24h)
    // so no normal task regresses. Bind via
    // LEOFLOW_AUTH_MAX_ATTEMPT_CREDENTIAL_LIFETIME as a duration (e.g. "24h",
    // "90m"). With warm pools enabled it is also the per-attempt watchdog that
    // keeps a wedged attempt from pinning a warm slot (a warm pod has no pod-level
    // deadline; the worker lifetime cap drains between attempts, never
    // mid-attempt). A non-positive value disables the renewal ceiling, the pod
    // deadline floor and that watchdog together — a wedged task then has no
    // wall-clock bound of its own — so boot logs a WARN naming the key.
    MaxAttemptCredentialLifetime time.Duration `mapstructure:"max_attempt_credential_lifetime"`
    // AgentTokenTransport selects how the in-pod agent obtains its control-plane
    // bearer credential (ADR 0055 Fix #3): "envvar" (the default) sets the token as
    // a plaintext LEOFLOW_AGENT_TOKEN env var on the pod spec — today's behavior,
    // byte-identical; "exchange" mounts a projected ServiceAccount token that the
    // agent exchanges once (via a control-plane TokenReview) for the task-scoped
    // JWT, so no bearer sits in plaintext on the Pod object. The exchange path is
    // Pro/Kubernetes-executor-only (the subprocess executor has no pod/SA/TokenReview
    // and ignores this). It is operator-scoped, NEVER author-settable. Empty = the
    // envvar default. Bind via LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT.
    AgentTokenTransport string `mapstructure:"agent_token_transport"`
}
```

<a name="CORSSection"></a>
## type [CORSSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L353-L355>)

CORSSection configures cross\-origin access.

```go
type CORSSection struct {
    AllowedOrigins []string `mapstructure:"allowed_origins"`
}
```

<a name="Config"></a>
## type [Config](<https://github.com/dexadata/leoflow/blob/main/internal/config/config.go#L32-L65>)

Config holds the developer CLI configuration.

```go
type Config struct {
    // ServerURL is the control plane base URL used by push and auth create-token.
    ServerURL string `mapstructure:"server_url"`
    // Token is the JWT bearer token persisted by `leoflow login` and used by
    // push and deploy when no --token flag or LEOFLOW_TOKEN env is set.
    Token string `mapstructure:"token"`
    // LogLevel is reserved for CLI log verbosity (not yet wired).
    LogLevel string `mapstructure:"log_level"`
    // Registry is reserved for the image registry used by image build (ADR 0003).
    Registry string `mapstructure:"registry"`
    // ParserCmd is the command used to invoke the Python parser from compile.
    ParserCmd string `mapstructure:"parser_cmd"`

    // Lite-edition settings written by `dexaflow setup` and read by `dexaflow lite`.
    // Workspace is the default directory holding the user's DAG projects.
    Workspace string `mapstructure:"workspace"`
    // LiteExecutor is the chosen executor: "subprocess" (local) or "k8s" (mini-cluster).
    LiteExecutor string `mapstructure:"lite_executor"`
    // LitePort is the UI/API port for the Lite control plane.
    LitePort int `mapstructure:"lite_port"`
    // AdminEmail is the Lite admin login created at bootstrap.
    AdminEmail string `mapstructure:"admin_email"`
    // AdminPasswordHash is the bcrypt hash of the generated admin password; the
    // plaintext is shown once at setup and never stored (Lite only).
    AdminPasswordHash string `mapstructure:"admin_password_hash"`
    // JWTSecret is the per-install Lite JWT signing secret, generated by
    // `dexaflow setup` (random, 64 hex chars) and persisted alongside the admin
    // hash. Rotating it on every fresh install invalidates browser tokens from a
    // prior install — so a reinstall greets the user with the login screen and
    // the freshly printed credentials actually do something (#121). Empty on
    // legacy installs; the lite runner falls back to the dev-only constant with
    // a warning so the upgrade does not break existing setups.
    JWTSecret string `mapstructure:"jwt_secret"`
}
```

<a name="Load"></a>
### func [Load](<https://github.com/dexadata/leoflow/blob/main/internal/config/config.go#L80>)

```go
func Load(configFile string, flags *pflag.FlagSet) (*Config, error)
```

Load assembles configuration from defaults, the given file \(when non\-empty\), LEOFLOW\_\* environment variables, and the provided flag set, in increasing order of precedence. A nil flag set or empty file path is ignored.

<a name="DatabaseSection"></a>
## type [DatabaseSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L358-L362>)

DatabaseSection configures the Postgres connection pool.

```go
type DatabaseSection struct {
    URL          string `mapstructure:"url"`
    MaxOpenConns int    `mapstructure:"max_open_conns"`
    MaxIdleConns int    `mapstructure:"max_idle_conns"`
}
```

<a name="DispatchSection"></a>
## type [DispatchSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L547-L555>)

DispatchSection sizes the BufferedDispatcher \(\#127\). BufferSize=0 keeps the scheduler tick synchronous with the inner dispatcher — the right shape for Lite \(subprocess fork is microseconds\). BufferSize\>0 enables the worker pool — the right shape for Pro \(Kubernetes API calls add real latency\). The defaults are set per\-edition by configsetup so the user does not have to think about this; an operator can still tune the knobs.

```go
type DispatchSection struct {
    // BufferSize is the depth of the queued-dispatches channel. 0 disables the
    // pool (synchronous passthrough). A full channel returns ErrAtCapacity to
    // the scheduler, which leaves the TI scheduled for the next tick.
    BufferSize int `mapstructure:"buffer_size"`
    // Workers is the number of goroutines draining the queue. Ignored when
    // BufferSize <= 0; otherwise floored to 1.
    Workers int `mapstructure:"workers"`
}
```

<a name="ExecutionSection"></a>
## type [ExecutionSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L194-L234>)

ExecutionSection configures warm worker pools — Pro\-gated N:1 pod reuse \(ADR 0058\). Every field is operator\-set \(never DAG\-author\-set\), consistent with the secret\-scoping stance: whether a pod may be reused across attempts is an operator's security decision, not a DAG author's. All fields default to a byte\-for\-byte no\-op — warm pools OFF means dedicated pod\-per\-task, today's behavior — and are read for runtime behavior only in a later brick; N1a introduces the knobs plus the fail\-closed boot guard \(validateExecution\).

```go
type ExecutionSection struct {
    // WarmPoolsEnabled turns on N:1 pod reuse (ADR 0058). Default false = a
    // dedicated pod per task attempt, today's behavior byte-for-byte. Turning it on
    // is gated at boot on the security prerequisites (token-exchange transport +
    // liveness enforcement) because a warm pod reuses one credential across attempts.
    WarmPoolsEnabled bool `mapstructure:"warm_pools_enabled"`
    // MaxAttemptsPerWorker caps how many attempts a warm worker serves before it is
    // drained and recycled (ADR 0058 D9). Bounds credential-leak and stale-image
    // exposure by forcing a fresh pod periodically. Default 50.
    MaxAttemptsPerWorker int `mapstructure:"max_attempts_per_worker"`
    // MaxWorkerLifetime is the wall-clock cap on a warm worker before it is drained
    // and recycled (ADR 0058 D9), independent of the attempt count. Default 1h. When
    // warm pools are on it MUST be >= auth.max_attempt_credential_lifetime, so a
    // worker is never force-recycled mid-attempt by its token lapsing.
    MaxWorkerLifetime time.Duration `mapstructure:"max_worker_lifetime"`
    // MinIdleWorkers is the number of warm workers kept ready per DAG version
    // (ADR 0058 D6). Default 0 = scale-to-zero, preserving the ADR 0002 zero-idle
    // floor; an operator opts into warmth by raising it.
    MinIdleWorkers int `mapstructure:"min_idle_workers"`
    // WorkerIdleTTL is how long an idle warm worker is kept before it is recycled
    // (ADR 0058 D6). Default 5m.
    WorkerIdleTTL time.Duration `mapstructure:"worker_idle_ttl"`
    // MaxPoolSize caps the total warm workers a single DAG version may hold —
    // registered workers plus in-flight dedicated pods. Default 8, operator-set.
    // N1b1-place records the knob and validates it (>= 1 when warm pools are on)
    // but does NOT enforce the cap yet: defer-at-max needs real pool accounting
    // (registered workers + in-flight pods), which arrives with the worker
    // lifecycle in N1b2/N1d. Today's placer is assign-if-free-else-dedicated.
    MaxPoolSize int `mapstructure:"max_pool_size"`
    // MaxWarmPodsPerTenant caps the TOTAL warm pods a single tenant may hold across
    // ALL its dag_versions on a shared cluster (M4). Where MaxPoolSize bounds one
    // dag_version's pool, this bounds a tenant's aggregate warm footprint so one
    // tenant cannot pin unlimited idle pods and starve neighbors on a shared
    // multi-team cluster. Default 100, operator-set. It is a RESERVE-then-RATION
    // budget, never a starvation lever: a tenant's promised idle floors (the sum of
    // its versions' EffectiveMinIdle) are honored even when they exceed this cap
    // (the reconciler raises the effective budget to the floor sum and meters the
    // misconfiguration), and the cap is enforced only by refusing to CREATE new
    // warm pods — never by deleting a busy worker.
    MaxWarmPodsPerTenant int `mapstructure:"max_warm_pods_per_tenant"`
}
```

<a name="ExecutionSection.EffectiveMinIdle"></a>
### func \(ExecutionSection\) [EffectiveMinIdle](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L248>)

```go
func (e ExecutionSection) EffectiveMinIdle(dagMinIdle int) int
```

EffectiveMinIdle resolves the warm\-worker target for one dag\_version under model A2 \(ADR 0058 N1b2b\): the DAG author declares desired warmth per DAG \(dagMinIdle\), the operator caps and floors it.

- Warm pools OFF =\> always 0. This is what makes a default deploy a byte\-for\-byte no\-op: with warmth gated off no warm pod is ever targeted, so the reconciler \(when it runs at all\) reconciles every pool to zero.
- The DAG author's value wins when set \(\> 0\); when the DAG declares none \(0\) it falls back to the operator's execution.min\_idle\_workers floor.
- The resolved value is clamped to \[0, max\_pool\_size\] so an author can never provision more warmth than the operator's per\-version cap allows, and a nonsensical negative never underflows.

<a name="ExecutorSection"></a>
## type [ExecutorSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L108-L151>)

ExecutorSection configures how tasks are executed.

```go
type ExecutorSection struct {
    HTTP HTTPExecutorSection `mapstructure:"http"`
    // TaskNamespace is the Kubernetes namespace the server creates task pods and
    // per-run staging PVCs in. It MUST match the namespace the Helm chart grants
    // the executor Role in (chart `taskNamespace` → LEOFLOW_EXECUTOR_TASK_NAMESPACE);
    // a mismatch 403s every dispatch (#480). Defaults to "leoflow".
    TaskNamespace string `mapstructure:"task_namespace"`
    // Type selects the pod-path executor: "kubernetes" (default, pod-per-task) or
    // "subprocess" (dev only, runs the agent on the host without isolation, used
    // by `leoflow dev`).
    Type string `mapstructure:"type"`
    // AgentPath is the leoflow-agent binary the subprocess executor runs (dev only).
    AgentPath string `mapstructure:"agent_path"`
    // SubprocessWorkDir is the working directory the subprocess executor runs the
    // agent in, so it can import the project's dag.py (dev only). Empty keeps the
    // server's working directory.
    SubprocessWorkDir string `mapstructure:"subprocess_workdir"`
    // AgentControlPlaneAddr is the gRPC address task pods dial back to. Empty
    // falls back to server.grpc_addr; in a local k3d/kind cluster set it to a
    // host-reachable address such as host.k3d.internal:9091.
    AgentControlPlaneAddr string `mapstructure:"agent_control_plane_addr"`
    // AgentTLSCAConfigMap names a ConfigMap (key ca.crt) mounted into task pods so
    // the agent verifies the control plane's gRPC TLS cert (issue #58). Empty =
    // agents use the insecure channel (dev).
    AgentTLSCAConfigMap string `mapstructure:"agent_tls_ca_configmap"`
    // TaskServiceAccount is the ServiceAccount task pods run as when a DAG's task
    // does not set execution.service_account. The chart wires its taskServiceAccount
    // here, so creating that SA makes keyless work without every DAG opting in.
    // Empty keeps pods on the namespace default SA (an explicit per-task value
    // always wins).
    TaskServiceAccount string `mapstructure:"task_service_account"`
    // TaskSecretName names a Kubernetes Secret mounted (read-only) into every task
    // pod at TaskSecretMountPath. It lets a task read a credential that lives in
    // the cluster's secret store (e.g. a GCP service-account key) referenced by a
    // connection's key_path — so Dexaflow never stores the key itself (ADR 0035).
    // Empty = no secret mounted.
    TaskSecretName string `mapstructure:"task_secret_name"`
    // TaskSecretMountPath is where TaskSecretName is mounted in the task pod.
    TaskSecretMountPath string `mapstructure:"task_secret_mount_path"`
    // Defaults holds per-cluster task defaults applied at dispatch to fill gaps the
    // DAG artifact left empty (ADR 0023, layer L0). They never override a value
    // baked into dag.json, keeping the artifact portable across clusters.
    Defaults PlatformDefaultsSection `mapstructure:"defaults"`
}
```

<a name="HTTPExecutorSection"></a>
## type [HTTPExecutorSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L291-L295>)

HTTPExecutorSection configures HTTP\-related executor knobs.

```go
type HTTPExecutorSection struct {
    // UserAgent is the default User-Agent header for HTTP requests a task image
    // may make on the platform's behalf.
    UserAgent string `mapstructure:"user_agent"`
}
```

<a name="JWTSection"></a>
## type [JWTSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L441-L454>)

JWTSection configures JWT issuance and validation.

```go
type JWTSection struct {
    Secret          string `mapstructure:"secret"`
    TokenTTLSeconds int    `mapstructure:"token_ttl_seconds"`
    // MaxLifetimeSeconds is the hard ceiling on how long a user session may be kept
    // alive by transparent token renewal (aresta #5), measured since first login
    // (the token's oiat claim). Past it, POST /api/v2/auth/token/renew is refused
    // and the user must `dexaflow auth login` again. The short TokenTTLSeconds still
    // bounds a stolen token independently; this only caps the total renewed
    // lifetime, mirroring auth.max_attempt_credential_lifetime for agent tokens.
    // Generous by default (24h) so a normal dev day never re-logs in mid-session; a
    // non-positive value disables the ceiling (renewal never expires the session).
    // Bind via LEOFLOW_AUTH_JWT_MAX_LIFETIME_SECONDS.
    MaxLifetimeSeconds int `mapstructure:"max_lifetime_seconds"`
}
```

<a name="LogsSection"></a>
## type [LogsSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L58-L70>)

LogsSection configures task log shipping.

```go
type LogsSection struct {
    // Dir is the root directory for the disk log sink (the default backend).
    Dir string `mapstructure:"dir"`
    // Backend selects the durable task-log store: "disk" (default) writes files
    // under Dir; "s3" ships each attempt to an S3-compatible bucket (AWS S3,
    // MinIO, Ceph RGW); "gcs" ships to Google Cloud Storage via its native SDK.
    // Object storage is opt-in — Lite and every deployment that does not set this
    // keep the exact on-disk path unchanged.
    Backend string `mapstructure:"backend"`
    // Sink configures the object-store backend; read only when Backend is "s3" or
    // "gcs".
    Sink ObjectLogSection `mapstructure:"sink"`
}
```

<a name="OIDCSection"></a>
## type [OIDCSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L465-L532>)

OIDCSection configures the OIDC/SSO login flow \(Authorization Code \+ PKCE\). It is read only when auth.provider is "oidc", which is Pro\-gated and fails boot closed unless Issuer, ClientID, and RedirectURL are all set.

Verification is keyless: the ID token is validated against the issuer's public JWKS discovered from Issuer, so no secret is stored for the verify path. ClientSecret is used solely for the authorization\-code exchange and is injected via LEOFLOW\_AUTH\_OIDC\_CLIENT\_SECRET \(env, never persisted, never logged\) — the same posture as the JWT secret.

```go
type OIDCSection struct {
    // Issuer is the org's single-tenant issuer URL (https). It is pinned: any ID
    // token whose iss claim differs is rejected (fail-closed tenant pin).
    Issuer string `mapstructure:"issuer"`
    // ClientID is the registered application (client) id; it is the expected
    // audience of every ID token.
    ClientID string `mapstructure:"client_id"`
    // ClientSecret is used only for the code exchange. Set via
    // LEOFLOW_AUTH_OIDC_CLIENT_SECRET; never persist it in a config file.
    ClientSecret string `mapstructure:"client_secret"`
    // RedirectURL is this server's callback URL registered with the IdP
    // (…/api/v2/auth/oidc/callback).
    RedirectURL string `mapstructure:"redirect_url"`
    // Scopes are the OAuth scopes requested; defaults to openid, email, profile.
    // Add the IdP's groups scope here when group→role mapping is used.
    Scopes []string `mapstructure:"scopes"`
    // GroupsClaim is the ID-token claim carrying the user's IdP groups (default
    // "groups"). Its values drive RoleMappings.
    GroupsClaim string `mapstructure:"groups_claim"`
    // RoleMappings maps an IdP group value to an existing Dexaflow role name.
    // Default-DENY: a group with no mapping grants no role. Configure via a YAML
    // config file only. The chart ships none today, so this map has no route
    // through Helm (#1143).
    //
    // Decoded OUT-OF-BAND (mapstructure:"-"), not by viper: viper's "." key
    // delimiter splits a dotted MAP KEY (a dotted IdP group like "app.admins")
    // into nested maps and fails to decode. LoadServer parses this map straight
    // from the raw YAML instead (#826). Env-var binding never applied to maps.
    RoleMappings map[string]string `mapstructure:"-"`
    // DefaultRole softens the default-deny WITHOUT weakening the secure default:
    // when an authenticated user resolves to zero mapped roles and DefaultRole is
    // set, they are granted this single role (operators are advised to use a
    // read-only role such as "viewer"). Empty (the default) keeps strict
    // default-deny — an unmapped user gets no role. It must name an existing DB
    // role for the resolved tenant; an unknown role fails the login closed.
    DefaultRole string `mapstructure:"default_role"`
    // TenantClaim selects which IdP claim identifies the tenant: "tid" (Entra) or
    // "hd" (Google Workspace).
    TenantClaim string `mapstructure:"tenant_claim"`
    // TenantClaims maps a TenantClaim value to a Dexaflow tenant name. A value not
    // present here is rejected (403) — the login never falls back to "default".
    //
    // Decoded OUT-OF-BAND (mapstructure:"-"), not by viper: a Google Workspace
    // `hd` value is a domain (always dotted, e.g. "example.com" or
    // "sub.example.co.uk"), which viper's "." delimiter would split into nested
    // maps and fail to decode — the #826 crash. LoadServer parses this map from
    // the raw YAML instead.
    TenantClaims map[string]string `mapstructure:"-"`
    // AllowedEmailDomains is an install-time, login-level allowlist layered on TOP
    // of the tid/hd tenant pin — it is NOT the pin itself (that stays issuer +
    // tid/hd + email_verified per D6). The check runs only AFTER the pin and
    // email_verified==true have passed, so the email domain is trustworthy at that
    // point. Empty (the default) imposes no domain restriction — the tid/hd pin is
    // the sole boundary. Non-empty admits a login (pre-provisioned OR JIT) only
    // when the verified email's domain is in the list; every other login is
    // rejected 403. It gates EVERY OIDC login, not just auto-provisioning.
    AllowedEmailDomains []string `mapstructure:"allowed_email_domains"`
    // BreakGlassEmails is the allowlist of local password logins permitted while
    // provider is "oidc"; every other password login is rejected (SSO-only).
    BreakGlassEmails []string `mapstructure:"break_glass_emails"`
    // JITProvisioning creates a user row on first OIDC login when no matching one
    // exists. OFF by default (a pre-provisioned user is required); when ON, the new
    // row is granted the roles from RoleMappings.
    JITProvisioning bool `mapstructure:"jit_provisioning"`
    // ClockSkewSeconds is the tolerance applied to the ID token's exp/iat/nbf
    // checks to absorb small clock differences between the IdP and this server.
    // Defaults to 60.
    ClockSkewSeconds int `mapstructure:"clock_skew_seconds"`
}
```

<a name="OTelSection"></a>
## type [OTelSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L565-L568>)

OTelSection configures OpenTelemetry export.

```go
type OTelSection struct {
    Enabled  bool   `mapstructure:"enabled"`
    Endpoint string `mapstructure:"endpoint"`
}
```

<a name="ObjectLogSection"></a>
## type [ObjectLogSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L81-L105>)

ObjectLogSection configures the object\-store log backend for both the "s3" and "gcs" providers. Auth is keyless\-first \(ADR 0035\): leave the credential fields empty to use the ambient chain — IRSA / instance profile for S3, GKE Workload Identity \(ADC\) for GCS. Static keys and credential files are a discouraged escape hatch for dev and clusters without an identity broker.

Bucket and Prefix apply to both providers. Region, Endpoint, ForcePathStyle, AccessKeyID and SecretAccessKey are S3\-only. CredentialsFile is GCS\-only. A field set for the other provider is simply ignored.

```go
type ObjectLogSection struct {
    // Bucket is the target bucket. Required when Backend is "s3" or "gcs".
    Bucket string `mapstructure:"bucket"`
    // Prefix is an optional key prefix under which attempt objects are laid out.
    Prefix string `mapstructure:"prefix"`
    // Region is the S3 store region (e.g. "us-east-1"). Required by AWS S3;
    // ignored by some S3-compatible stores. S3-only.
    Region string `mapstructure:"region"`
    // Endpoint overrides the S3 endpoint for S3-compatible stores (MinIO, Ceph
    // RGW). Empty uses the AWS default endpoint. S3-only — it is NOT the way to
    // reach GCS, which has its own keyless "gcs" backend.
    Endpoint string `mapstructure:"endpoint"`
    // ForcePathStyle uses path-style addressing (bucket in the path, not the
    // host). Required by MinIO and some S3-compatible stores. S3-only.
    ForcePathStyle bool `mapstructure:"force_path_style"`
    // AccessKeyID is a static S3 access key. Empty (recommended) uses the keyless
    // credential chain (ADR 0035). S3-only.
    AccessKeyID string `mapstructure:"access_key_id"`
    // SecretAccessKey pairs with AccessKeyID. Discouraged; prefer keyless. S3-only.
    SecretAccessKey string `mapstructure:"secret_access_key"`
    // CredentialsFile is a path to a GCS service-account JSON key. Empty
    // (recommended) uses Application Default Credentials — GKE Workload Identity
    // keyless. GCS-only.
    CredentialsFile string `mapstructure:"credentials_file"`
}
```

<a name="ObservabilitySection"></a>
## type [ObservabilitySection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L558-L562>)

ObservabilitySection configures logging, metrics, and tracing.

```go
type ObservabilitySection struct {
    OTel      OTelSection `mapstructure:"otel"`
    LogLevel  string      `mapstructure:"log_level"`
    LogFormat string      `mapstructure:"log_format"`
}
```

<a name="PlatformDefaultsSection"></a>
## type [PlatformDefaultsSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L155-L185>)

PlatformDefaultsSection configures the lowest\-precedence \(L0\) task defaults, applied at dispatch to fill gaps the DAG left empty \(ADR 0023\).

```go
type PlatformDefaultsSection struct {
    // StagingSize/StagingStorageClass default the per-run staging volume when the
    // DAG enabled staging without pinning them (e.g. the cluster's RWX class).
    StagingSize         string `mapstructure:"staging_size"`
    StagingStorageClass string `mapstructure:"staging_storage_class"`
    // StagingAccessMode is the PVC access mode for the staging volume. Defaults to
    // ReadWriteMany (multi-node prod); single-node dev (k3d local-path, no RWX)
    // sets ReadWriteOnce, which is sufficient for a run's sequential same-node pods.
    StagingAccessMode string `mapstructure:"staging_access_mode"`
    // ResourcesCPU/ResourcesMemory default a task's request when neither the task
    // override nor the DAG set any (Kubernetes quantities, e.g. "250m"/"256Mi").
    ResourcesCPU    string `mapstructure:"resources_cpu"`
    ResourcesMemory string `mapstructure:"resources_memory"`
    // RunTasksAsNonRoot refuses to start a task container whose image resolves
    // to UID 0, completing Pod Security Admission's `restricted` set. On by
    // default now that the images this repo ships carry a numeric non-root UID:
    // runtime/Dockerfile runs as `USER 65532:65532` and every examples/*/image
    // inherits it, and the executor pairs it with a pod-level fsGroup so the
    // per-run staging PVC stays writable. Turn it off for a cluster whose task
    // images legitimately run as root.
    //
    // Deliberately a cluster setting rather than a DAG field: whether untrusted
    // task code may run as root belongs to whoever operates the cluster, not to
    // whoever authors the DAG.
    RunTasksAsNonRoot bool `mapstructure:"run_tasks_as_non_root"`
    // ReadOnlyTaskRootFilesystem mounts every task container's root filesystem
    // read-only. Off by default because `restricted` does not require it and it
    // breaks ordinary Python tasks (pip cache, /tmp, matplotlib config); turn it
    // on for a fleet of tasks known not to write outside their volumes.
    ReadOnlyTaskRootFilesystem bool `mapstructure:"read_only_task_root_filesystem"`
}
```

<a name="RedisSection"></a>
## type [RedisSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L365-L376>)

RedisSection configures the Redis connection.

```go
type RedisSection struct {
    URL string `mapstructure:"url"`
    // CAFile is the absolute path to a PEM CA bundle the client trusts when
    // negotiating TLS to a `rediss://` URL (#312). Required to reach managed
    // Redis (Memorystore SERVER_AUTHENTICATION, ElastiCache in-transit
    // encryption, Azure Cache for Redis) whose server cert is signed by a
    // provider / per-instance CA that is not in the container's system
    // roots. Empty falls back to the SDK default — system roots only.
    // The Helm chart sets this via LEOFLOW_REDIS_CA_FILE when
    // `redis.caConfigMap` is configured, pointing at the mounted ConfigMap.
    CAFile string `mapstructure:"ca_file"`
}
```

<a name="SchedulerSection"></a>
## type [SchedulerSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L535-L539>)

SchedulerSection configures the scheduler loop.

```go
type SchedulerSection struct {
    LoopIntervalMS int             `mapstructure:"loop_interval_ms"`
    Enabled        bool            `mapstructure:"enabled"`
    Dispatch       DispatchSection `mapstructure:"dispatch"`
}
```

<a name="SecretsSection"></a>
## type [SecretsSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L45-L55>)

SecretsSection configures the external secrets backend \(ADR 0060\). When Backend is set, a Connection/Variable a DAG declares can be resolved pod\-side from the provider store under the pod's keyless identity instead of the leoflow vault. Empty \(the default\) keeps the vault as the only source — byte\-identical to pre\-0060. This is operator\-only config: it is delivered to the pod as LEOFLOW\_SECRETS\_\* env, which an author's task env can never set \(\#828\).

```go
type SecretsSection struct {
    // Backend is the provider secrets-backend class the in-pod resolver drives
    // (e.g. the Airflow AWS SecretsManagerBackend). Empty disables external secrets.
    Backend string `mapstructure:"backend"`
    // BackendKwargs is the provider kwargs as a JSON object string (connections_prefix,
    // variables_prefix, region_name, …); a kind is served iff its `*_prefix` kwarg is
    // present. A JSON string (not a map) so it is settable via a single
    // LEOFLOW_SECRETS_BACKEND_KWARGS env var, matching the env-only control-plane
    // chart. Empty is treated as `{}`. Delivered verbatim to the pod.
    BackendKwargs string `mapstructure:"backend_kwargs"`
}
```

<a name="ServerConfig"></a>
## type [ServerConfig](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L21-L37>)

ServerConfig is the full configuration for the leoflow\-server control plane. It mirrors the nested YAML described in the Phase 2 prompt.

```go
type ServerConfig struct {
    Server        ServerSection        `mapstructure:"server"`
    Database      DatabaseSection      `mapstructure:"database"`
    Redis         RedisSection         `mapstructure:"redis"`
    Auth          AuthSection          `mapstructure:"auth"`
    Scheduler     SchedulerSection     `mapstructure:"scheduler"`
    Executor      ExecutorSection      `mapstructure:"executor"`
    Execution     ExecutionSection     `mapstructure:"execution"`
    Logs          LogsSection          `mapstructure:"logs"`
    Observability ObservabilitySection `mapstructure:"observability"`
    UI            UISection            `mapstructure:"ui"`
    Secrets       SecretsSection       `mapstructure:"secrets"`
    // SecretKey (LEOFLOW_SECRET_KEY) is the 32-byte key encrypting connection
    // secrets at rest (ADR 0019). Raw 32 chars, 64-char hex, or base64. Empty
    // disables connection writes.
    SecretKey string `mapstructure:"secret_key"`
}
```

<a name="LoadServer"></a>
### func [LoadServer](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L727>)

```go
func LoadServer(configFile string, flags *pflag.FlagSet) (*ServerConfig, error)
```

LoadServer assembles the server configuration from defaults, the given file, LEOFLOW\_\* environment variables, and flags, in increasing precedence.

<a name="ServerConfig.Validate"></a>
### func \(\*ServerConfig\) [Validate](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L835>)

```go
func (c *ServerConfig) Validate() error
```

Validate reports configuration errors that must abort startup.

<a name="ServerSection"></a>
## type [ServerSection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L298-L318>)

ServerSection configures the HTTP, metrics, and agent gRPC listeners.

```go
type ServerSection struct {
    // Role selects which components this process runs (ADR 0049): "all" (default;
    // the monolith Lite always runs), "api" (HTTP + UI, restricted identity), or
    // "scheduler" (reconciler + dispatch + agent gRPC, privileged). Splitting is a
    // Pro-only topology; "all" is behavior-identical to the pre-0049 monolith.
    Role        string      `mapstructure:"role"`
    HTTPAddr    string      `mapstructure:"http_addr"`
    MetricsAddr string      `mapstructure:"metrics_addr"`
    GRPCAddr    string      `mapstructure:"grpc_addr"`
    CORS        CORSSection `mapstructure:"cors"`
    // TrustedProxies lists the proxy IPs/CIDRs whose X-Forwarded-For is honored
    // when resolving the client IP. Empty (the default) trusts no proxy, so a
    // spoofed XFF cannot forge the client IP (audit H1); set it to the ingress
    // CIDR when the API runs behind a reverse proxy so rate-limiting and audit
    // see the real client.
    TrustedProxies []string `mapstructure:"trusted_proxies"`
    // GRPCTLSCert/GRPCTLSKey enable TLS on the agent gRPC listener (issue #58).
    // When both are set the channel is encrypted; empty means plaintext (dev).
    GRPCTLSCert string `mapstructure:"grpc_tls_cert"`
    GRPCTLSKey  string `mapstructure:"grpc_tls_key"`
}
```

<a name="ServerSection.EffectiveRole"></a>
### func \(ServerSection\) [EffectiveRole](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L332>)

```go
func (s ServerSection) EffectiveRole() string
```

EffectiveRole returns the configured role, defaulting empty to RoleAll so an unset role \(Lite, and every pre\-0049 deployment\) keeps the monolith behavior.

<a name="ServerSection.ServesAPI"></a>
### func \(ServerSection\) [ServesAPI](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L340>)

```go
func (s ServerSection) ServesAPI() bool
```

ServesAPI reports whether this process runs the HTTP API \+ UI.

<a name="ServerSection.ServesScheduler"></a>
### func \(ServerSection\) [ServesScheduler](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L347>)

```go
func (s ServerSection) ServesScheduler() bool
```

ServesScheduler reports whether this process runs the scheduler, dispatch, and the agent gRPC endpoint.

<a name="UISection"></a>
## type [UISection](<https://github.com/dexadata/leoflow/blob/main/internal/config/server.go#L267-L288>)

UISection configures the embedded Airflow UI.

```go
type UISection struct {
    // InstanceName is shown in the UI navbar (Airflow's instance_name). Empty
    // falls back to "Dexaflow"; `dexaflow lite` sets it to mark the environment.
    InstanceName string `mapstructure:"instance_name"`
    // AutoRefreshIntervalSeconds is the SPA's polling cadence for DAG /
    // DagRun / task-instance state refresh (Airflow's auto_refresh_interval).
    // Zero (the default) falls back to api.DefaultUIAutoRefreshIntervalSeconds
    // (30s, production-safe). `dexaflow lite` sets it to 1s for a snappy inner
    // loop so the SPA reflects state changes almost immediately during dev.
    AutoRefreshIntervalSeconds int `mapstructure:"auto_refresh_interval_seconds"`
    // Edition marks the running edition; "lite" shows the silver LITE badge and
    // "pro" shows the gold PRO badge in the UI shell (independent of the auth
    // mode). Empty/any other value shows no badge — Demo intentionally renders
    // without an edition pill.
    Edition string `mapstructure:"edition"`
    // Workspace is the DAG project directory the Lite web editor edits (ADR 0025).
    // Empty disables the editor (Production, or Lite without one).
    Workspace string `mapstructure:"workspace"`
    // MonacoDir is where the pinned Monaco bundle was fetched by `dexaflow setup`;
    // the editor page is served Monaco from it. Empty shows a setup hint.
    MonacoDir string `mapstructure:"monaco_dir"`
}
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
