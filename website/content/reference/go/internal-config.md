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
- [func HomeDir\(\) \(string, error\)](<#HomeDir>)
- [func HomeDirIn\(userHome string\) \(string, error\)](<#HomeDirIn>)
- [func IsClusterEdition\(edition string\) bool](<#IsClusterEdition>)
- [func NormalizeServer\(url string\) string](<#NormalizeServer>)
- [func PersistSession\(path, serverURL, token string\) error](<#PersistSession>)
- [func UpdateUserStore\(path string, update func\(\*UserStore\) error\) error](<#UpdateUserStore>)
- [func ValidOrigins\(list \[\]string\) bool](<#ValidOrigins>)
- [type APITokensSection](<#APITokensSection>)
- [type AdmissionSection](<#AdmissionSection>)
  - [func \(a AdmissionSection\) Enabled\(\) bool](<#AdmissionSection.Enabled>)
- [type AdmissionWebhookSection](<#AdmissionWebhookSection>)
- [type AgentSection](<#AgentSection>)
- [type AlertsSection](<#AlertsSection>)
- [type AuthSection](<#AuthSection>)
- [type BootstrapSection](<#BootstrapSection>)
  - [func \(b BootstrapSection\) Enabled\(\) bool](<#BootstrapSection.Enabled>)
  - [func \(b BootstrapSection\) GoString\(\) string](<#BootstrapSection.GoString>)
  - [func \(b BootstrapSection\) LogValue\(\) slog.Value](<#BootstrapSection.LogValue>)
  - [func \(b BootstrapSection\) String\(\) string](<#BootstrapSection.String>)
- [type BuildKitSection](<#BuildKitSection>)
- [type CIOIDCIssuerSection](<#CIOIDCIssuerSection>)
- [type CIOIDCSection](<#CIOIDCSection>)
  - [func \(s CIOIDCSection\) CIIssuers\(\) \[\]cioidc.Issuer](<#CIOIDCSection.CIIssuers>)
  - [func \(s CIOIDCSection\) Enabled\(\) bool](<#CIOIDCSection.Enabled>)
  - [func \(s CIOIDCSection\) SessionTTL\(\) time.Duration](<#CIOIDCSection.SessionTTL>)
- [type CORSSection](<#CORSSection>)
- [type CloudBuildSection](<#CloudBuildSection>)
- [type Config](<#Config>)
  - [func Load\(configFile string, flags \*pflag.FlagSet\) \(\*Config, error\)](<#Load>)
- [type DatabaseSection](<#DatabaseSection>)
- [type DeployDefaultsSection](<#DeployDefaultsSection>)
- [type DeploySection](<#DeploySection>)
- [type DispatchSection](<#DispatchSection>)
- [type ExecutionAllowlistSection](<#ExecutionAllowlistSection>)
  - [func \(s ExecutionAllowlistSection\) ExecutionPolicyConfig\(\) domain.ExecutionPolicyConfig](<#ExecutionAllowlistSection.ExecutionPolicyConfig>)
- [type ExecutionSection](<#ExecutionSection>)
  - [func \(e ExecutionSection\) EffectiveMinIdle\(dagMinIdle int\) int](<#ExecutionSection.EffectiveMinIdle>)
  - [func \(e ExecutionSection\) WarmPoolDefaults\(\) domain.WarmPoolDefaults](<#ExecutionSection.WarmPoolDefaults>)
- [type ExecutorSection](<#ExecutorSection>)
- [type ExecutorUnitSection](<#ExecutorUnitSection>)
  - [func \(u ExecutorUnitSection\) ResourceUnitConfig\(\) domain.ResourceUnitConfig](<#ExecutorUnitSection.ResourceUnitConfig>)
- [type HTTPExecutorSection](<#HTTPExecutorSection>)
- [type HelmSection](<#HelmSection>)
- [type HomeLinkSection](<#HomeLinkSection>)
- [type JWTSection](<#JWTSection>)
- [type KubeClientSection](<#KubeClientSection>)
- [type LogTailSection](<#LogTailSection>)
- [type LogsSection](<#LogsSection>)
- [type MetricsSection](<#MetricsSection>)
- [type OIDCSection](<#OIDCSection>)
- [type OTelSection](<#OTelSection>)
- [type ObjectLogSection](<#ObjectLogSection>)
- [type ObservabilitySection](<#ObservabilitySection>)
- [type PlatformDefaultsSection](<#PlatformDefaultsSection>)
- [type PoolsSection](<#PoolsSection>)
- [type RedisSection](<#RedisSection>)
- [type RemoteBuilderSection](<#RemoteBuilderSection>)
- [type RetentionSection](<#RetentionSection>)
  - [func \(r RetentionSection\) Active\(usageRecording bool\) bool](<#RetentionSection.Active>)
  - [func \(r RetentionSection\) Enabled\(\) bool](<#RetentionSection.Enabled>)
  - [func \(r RetentionSection\) Validate\(\) error](<#RetentionSection.Validate>)
  - [func \(r RetentionSection\) ValidateWith\(usageRecording bool\) error](<#RetentionSection.ValidateWith>)
- [type SchedulerSection](<#SchedulerSection>)
- [type SecretsSection](<#SecretsSection>)
- [type ServerConfig](<#ServerConfig>)
  - [func LoadServer\(configFile string, flags \*pflag.FlagSet\) \(\*ServerConfig, error\)](<#LoadServer>)
  - [func \(c \*ServerConfig\) AdvertisedDeployDefaults\(\) domain.DeployDefaults](<#ServerConfig.AdvertisedDeployDefaults>)
  - [func \(c \*ServerConfig\) ClusterEdition\(\) bool](<#ServerConfig.ClusterEdition>)
  - [func \(c \*ServerConfig\) PoolsEnabled\(\) bool](<#ServerConfig.PoolsEnabled>)
  - [func \(c \*ServerConfig\) Validate\(\) error](<#ServerConfig.Validate>)
- [type ServerSection](<#ServerSection>)
  - [func \(s ServerSection\) EffectiveRole\(\) string](<#ServerSection.EffectiveRole>)
  - [func \(s ServerSection\) HTTPTLSEnabled\(\) bool](<#ServerSection.HTTPTLSEnabled>)
  - [func \(s ServerSection\) ServesAPI\(\) bool](<#ServerSection.ServesAPI>)
  - [func \(s ServerSection\) ServesScheduler\(\) bool](<#ServerSection.ServesScheduler>)
- [type Session](<#Session>)
- [type Setting](<#Setting>)
  - [func CLISettings\(\) \[\]Setting](<#CLISettings>)
  - [func ServerSettings\(\) \[\]Setting](<#ServerSettings>)
- [type SourceModeSection](<#SourceModeSection>)
  - [func \(s SourceModeSection\) RuntimeImage\(\) string](<#SourceModeSection.RuntimeImage>)
  - [func \(s SourceModeSection\) WarmPoolsOn\(\) bool](<#SourceModeSection.WarmPoolsOn>)
- [type TrustedIssuerSection](<#TrustedIssuerSection>)
  - [func \(s TrustedIssuerSection\) Enabled\(\) bool](<#TrustedIssuerSection.Enabled>)
- [type UISection](<#UISection>)
- [type UsageEventsSection](<#UsageEventsSection>)
- [type UsageSection](<#UsageSection>)
- [type UserStore](<#UserStore>)
  - [func ReadUserStore\(path string\) \(\*UserStore, error\)](<#ReadUserStore>)
  - [func \(s \*UserStore\) RemoveSession\(server string\) bool](<#UserStore.RemoveSession>)
  - [func \(s \*UserStore\) RemoveTarget\(name string\) bool](<#UserStore.RemoveTarget>)
  - [func \(s \*UserStore\) Session\(server string\) \(string, bool\)](<#UserStore.Session>)
  - [func \(s \*UserStore\) SetSession\(server, token string\)](<#UserStore.SetSession>)
  - [func \(s \*UserStore\) SetTarget\(t UserTarget\)](<#UserStore.SetTarget>)
  - [func \(s \*UserStore\) SetWorkspaceTarget\(root, name string\)](<#UserStore.SetWorkspaceTarget>)
  - [func \(s \*UserStore\) Target\(name string\) \(UserTarget, bool\)](<#UserStore.Target>)
  - [func \(s \*UserStore\) WorkspaceTarget\(root string\) string](<#UserStore.WorkspaceTarget>)
- [type UserTarget](<#UserTarget>)
- [type WorkspaceTarget](<#WorkspaceTarget>)


## Constants

<a name="EditionCluster"></a>The values of ui.edition \(ADR 0027\). The edition says how the server is delivered, not what it may do: the Helm chart on Kubernetes or the installable binary. Both are the same commit and the same license \(ADR 0028\).

```go
const (
    // EditionCluster is the Helm chart on Kubernetes.
    EditionCluster = "cluster"
    // EditionLite is the installable binary (`dexaflow lite`).
    EditionLite = "lite"
    // EditionProAlias is the earlier name of EditionCluster. It is still
    // accepted so a chart that sets it keeps working while the rename rolls
    // out; IsClusterEdition treats both the same.
    EditionProAlias = "pro"
)
```

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

<a name="LogTailPublishAlways"></a>Live\-tail publish modes \(logs.tail.publish\).

```go
const (
    // LogTailPublishAlways publishes every received log line for the live
    // tail as it arrives (the default).
    LogTailPublishAlways = "always"
    // LogTailPublishOnDemand publishes only while a follower is subscribed,
    // probing at most once a second per log stream and replaying the lines
    // held since the last probe that found nobody.
    LogTailPublishOnDemand = "on_demand"
)
```

<a name="VariableEncryptionOff"></a>Variable encryption modes \(variable\_encryption, \#507\).

```go
const (
    // VariableEncryptionOff stores Variable values in plaintext (the default).
    VariableEncryptionOff = "off"
    // VariableEncryptionOn seals Variable values with the secret key.
    VariableEncryptionOn = "on"
    // VariableEncryptionDecrypt restores sealed values to plaintext at boot.
    VariableEncryptionDecrypt = "decrypt"
)
```

<a name="SecretScopingPermissive"></a>Secret policy allowlists \(ADR 0055\). auth.secret\_scoping and auth.secret\_liveness\_mode are validated against these; an unknown value fails boot closed. Empty is valid, serverDefaults sets the safe default for each.

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

<a name="DefaultSessionCookieName"></a>DefaultSessionCookieName is the browser session cookie's name when auth.session\_cookie\_name is unset: the name every earlier release used.

```go
const DefaultSessionCookieName = "_token"
```

<a name="HomeDirName"></a>HomeDirName is the per\-user state directory under the home directory.

```go
const HomeDirName = ".dexaflow"
```

<a name="HostPatternTenant"></a>HostPatternTenant is the placeholder auth.trusted\_issuer.host\_pattern replaces with the session's tenant.

```go
const HostPatternTenant = "{tenant}"
```

<a name="LegacyHomeDirName"></a>LegacyHomeDirName is the state directory's name before the rename.

```go
const LegacyHomeDirName = ".leoflow"
```

<a name="DefaultConfigFile"></a>
## func [DefaultConfigFile](<https://github.com/dexadata/dexaflow/blob/main/internal/config/config.go#L109>)

```go
func DefaultConfigFile() (string, error)
```

DefaultConfigFile returns the default configuration file path, \~/.dexaflow/config.yaml \(see HomeDir for installs from before the rename\).

<a name="HomeDir"></a>
## func [HomeDir](<https://github.com/dexadata/dexaflow/blob/main/internal/config/home.go#L17>)

```go
func HomeDir() (string, error)
```

HomeDir returns the per\-user state directory, \~/.dexaflow. See HomeDirIn.

<a name="HomeDirIn"></a>
## func [HomeDirIn](<https://github.com/dexadata/dexaflow/blob/main/internal/config/home.go#L31>)

```go
func HomeDirIn(userHome string) (string, error)
```

HomeDirIn returns the state directory under userHome. It is \~/.dexaflow, except for an install from before the rename: then \~/.dexaflow is created as a link to the existing \~/.leoflow, so its data stays where it is \(a running Lite holds files and mounts under it\) and both paths reach it. If the link cannot be created, \~/.dexaflow itself is returned, so state is never split between two directories. The directory is not created here.

<a name="IsClusterEdition"></a>
## func [IsClusterEdition](<https://github.com/dexadata/dexaflow/blob/main/internal/config/edition.go#L19>)

```go
func IsClusterEdition(edition string) bool
```

IsClusterEdition reports whether edition is the cluster edition, under its current name or the earlier one.

<a name="NormalizeServer"></a>
## func [NormalizeServer](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L71>)

```go
func NormalizeServer(url string) string
```

NormalizeServer is the form a server URL is compared and stored in: trimmed, without trailing slashes. Anything cleverer \(default ports, host casing\) could make two different control planes share a token.

<a name="PersistSession"></a>
## func [PersistSession](<https://github.com/dexadata/dexaflow/blob/main/internal/config/persist.go#L12>)

```go
func PersistSession(path, serverURL, token string) error
```

PersistSession writes the control\-plane server URL and auth token into the config file at path, preserving any other keys already there \(e.g. the Lite settings written by \`dexaflow setup\`\). It creates the file and its parent directory when absent, and replaces the file atomically at 0600 because the token is a secret. An empty path is an error: the caller must resolve the target first.

<a name="UpdateUserStore"></a>
## func [UpdateUserStore](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L100>)

```go
func UpdateUserStore(path string, update func(*UserStore) error) error
```

UpdateUserStore reads the store at path, lets update change it, and writes the managed keys back \(the per\-server sessions, the targets, the selections and the single\-server server\_url/token pair\). Every other key, the order of the keys and their comments are kept. The file is created when absent and replaced atomically at 0600, whatever its mode was, because it holds tokens.

<a name="ValidOrigins"></a>
## func [ValidOrigins](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L555>)

```go
func ValidOrigins(list []string) bool
```

ValidOrigins reports whether list holds at least one origin and every entry is an http or https scheme://host\[:port\] with nothing after it: the rule auth.trusted\_issuer.allowed\_origins follows, for origins that come from elsewhere than the configuration.

<a name="APITokensSection"></a>
## type [APITokensSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_auth.go#L200-L210>)

APITokensSection configures the API tokens of ADR 0074 \(\#2056\).

```go
type APITokensSection struct {
    // Disabled stops serving the API tokens: /api/v2/apiTokens and the
    // tokens screen are not registered, and every API token is refused as
    // an unknown bearer, a token minted while they were served included (it
    // works again if they are served again before it expires). For a
    // platform in front of the engine that decides who may reach it at each
    // sign-in, so no engine credential outlives that decision. False, the
    // zero value, serves them, so a configuration that never mentions it
    // keeps them.
    Disabled bool `mapstructure:"disabled"`
}
```

<a name="AdmissionSection"></a>
## type [AdmissionSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_admission.go#L16-L27>)

AdmissionSection is execution.admission \(ADR 0100\).

```go
type AdmissionSection struct {
    // Timeout bounds each hook call. Default 500ms (zero means the default),
    // at most 5s. Bind via
    // DEXAFLOW_EXECUTION_ADMISSION_TIMEOUT.
    Timeout time.Duration `mapstructure:"timeout"`
    // FailOpen admits an attempt unchanged when a hook errors, times out or is
    // skipped after repeated failures, instead of holding it. Default false.
    // Bind via DEXAFLOW_EXECUTION_ADMISSION_FAIL_OPEN.
    FailOpen bool `mapstructure:"fail_open"`
    // Webhook asks an operator's endpoint about every attempt.
    Webhook AdmissionWebhookSection `mapstructure:"webhook"`
}
```

<a name="AdmissionSection.Enabled"></a>
### func \(AdmissionSection\) [Enabled](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_admission.go#L44>)

```go
func (a AdmissionSection) Enabled() bool
```

Enabled reports whether any admission hook is configured.

<a name="AdmissionWebhookSection"></a>
## type [AdmissionWebhookSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_admission.go#L30-L41>)

AdmissionWebhookSection is execution.admission.webhook \(ADR 0100 §8\).

```go
type AdmissionWebhookSection struct {
    // URL turns the webhook on. https, unless the host is loopback. Bind via
    // DEXAFLOW_EXECUTION_ADMISSION_WEBHOOK_URL.
    URL string `mapstructure:"url"`
    // CAFile is a PEM bundle the endpoint's certificate must chain to;
    // empty uses the system roots. Bind via
    // DEXAFLOW_EXECUTION_ADMISSION_WEBHOOK_CA_FILE.
    CAFile string `mapstructure:"ca_file"`
    // TokenFile holds a bearer token sent with each request, read on every
    // call. Bind via DEXAFLOW_EXECUTION_ADMISSION_WEBHOOK_TOKEN_FILE.
    TokenFile string `mapstructure:"token_file"`
}
```

<a name="AgentSection"></a>
## type [AgentSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_bootstrap.go#L67-L73>)

AgentSection configures what the server allows on the agent gRPC channel.

```go
type AgentSection struct {
    // AllowInsecureSecrets (DEXAFLOW_AGENT_ALLOW_INSECURE_SECRETS, default
    // false) lets the server deliver secrets to agents over a plaintext gRPC
    // channel. Dev only: `dexaflow lite` sets it for its loopback agents, and
    // the Pro edition refuses to boot with it.
    AllowInsecureSecrets bool `mapstructure:"allow_insecure_secrets"`
}
```

<a name="AlertsSection"></a>
## type [AlertsSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_logs.go#L102-L115>)

AlertsSection guards the destinations of native on\-failure alerts \(\#424\). An alert's URL and headers come from a tenant's connection, so on a shared engine a tenant could otherwise point one at the control plane's own network: loopback, a private service, or the cloud metadata endpoint.

```go
type AlertsSection struct {
    // BlockPrivateDestinations refuses alert requests to loopback, private,
    // link-local (including 169.254.169.254), shared, unspecified, multicast and
    // broadcast addresses. The check runs on the address actually dialed, after
    // DNS resolution and on every redirect, and the guarded client does not use
    // the proxy environment. Off by default, so an existing install that alerts
    // an in-cluster endpoint keeps working.
    BlockPrivateDestinations bool `mapstructure:"block_private_destinations"`
    // AllowedCIDRs exempts these ranges (CIDRs or single addresses) from the
    // block, e.g. an on-premises chat server. Validated at startup even while the
    // block is off, so a typo surfaces before anyone turns it on; applied only
    // while it is on.
    AllowedCIDRs []string `mapstructure:"allowed_cidrs"`
}
```

<a name="AuthSection"></a>
## type [AuthSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_auth.go#L80-L197>)

AuthSection configures authentication.

```go
type AuthSection struct {
    Provider string     `mapstructure:"provider"`
    JWT      JWTSection `mapstructure:"jwt"`
    // OIDC configures the OIDC/SSO login flow. It is read only when Provider is
    // "oidc" (Pro-gated); the JWT authenticator remains the request-path verifier
    // in both modes.
    OIDC OIDCSection `mapstructure:"oidc"`
    // TrustedIssuer lets a platform that already authenticates its users open a
    // UI session for them with a token its own issuer signed (#1284). Empty
    // Issuer disables it.
    TrustedIssuer TrustedIssuerSection `mapstructure:"trusted_issuer"`
    // CIOIDC lets CI pipelines deploy with their provider's OIDC token
    // instead of a stored deploy token (ADR 0092, #1882). No issuer turns
    // it off.
    CIOIDC CIOIDCSection `mapstructure:"ci_oidc"`
    // APITokens can turn the long-lived API tokens (ADR 0074) off.
    APITokens APITokensSection `mapstructure:"api_tokens"`
    // ServiceToken enables the operator service API under /api/v2/service/
    // (#1283), which creates tenants and links users to the trusted issuer. It
    // is the bearer credential for that API: at least 32 characters, kept in a
    // Secret. Empty disables the API.
    ServiceToken string `mapstructure:"service_token"`
    // ExternalSignInURL hands unauthenticated UI visitors to the operator's own
    // sign-in instead of Dexaflow's page, with the requested path in a `next`
    // query parameter (#1288). The operator's flow is expected to return them
    // with a Dexaflow session. Empty keeps Dexaflow's page; `?local=1` reaches it
    // either way.
    ExternalSignInURL string `mapstructure:"external_signin_url"`
    // ExternalSignOutURL is where sign-out lands after clearing the session, so
    // the operator can end their own session too (#1288). Empty returns to
    // Dexaflow's sign-in page.
    ExternalSignOutURL string `mapstructure:"external_signout_url"`
    // DevNoAuth disables authentication entirely, treating every request as an
    // admin. It exists ONLY for `dexaflow lite` (local, unsandboxed). It is false by
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
    // whole tenant vault and warns when a DAG declares a narrower set, an
    // empty set included (#800); enforce
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
    // mid-attempt). An attempt whose agent goes silent after running past the
    // ceiling is failed by the heartbeat reaper as a task failure with the
    // credential_ceiling reason (its retry policy applies), never re-placed as an
    // agent_lost infra loss with a fresh credential (#1461). In Lite, which has no
    // pod deadline, the reaper also fails an attempt still running past the
    // ceiling with that reason and stops its task (#1511). A non-positive value
    // disables the renewal ceiling, the pod deadline floor, that watchdog and the
    // credential_ceiling failure together (a wedged task then has no
    // wall-clock bound of its own), so boot logs a WARN naming the key.
    MaxAttemptCredentialLifetime time.Duration `mapstructure:"max_attempt_credential_lifetime"`
    // AgentTokenTransport selects how the in-pod agent obtains its control-plane
    // bearer credential (ADR 0055 Fix #3): "envvar" (the default) sets the token as
    // a plaintext LEOFLOW_AGENT_TOKEN env var on the pod spec, today's behavior,
    // byte-identical; "exchange" mounts a projected ServiceAccount token that the
    // agent exchanges once (via a control-plane TokenReview) for the task-scoped
    // JWT, so no bearer sits in plaintext on the Pod object. The exchange path is
    // Pro/Kubernetes-executor-only (the subprocess executor has no pod/SA/TokenReview
    // and ignores this). It is operator-scoped, NEVER author-settable. Empty = the
    // envvar default. Bind via LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT.
    AgentTokenTransport string `mapstructure:"agent_token_transport"`
    // SessionCookieInsecure drops the Secure attribute from the browser session
    // cookie (_token) and the OIDC state cookie. It is operator-scoped, NEVER
    // author-settable, and defaults to false, which is the hardened posture.
    //
    // There is exactly one reason to set it: a deployment served over plain http
    // to something that is not a loopback address. A browser refuses a Secure
    // cookie from such an origin outright, so the login page would post valid
    // credentials, get a 200, and land back on itself with no error anywhere. A
    // loopback deployment (localhost, 127.0.0.1) needs nothing: browsers treat it
    // as trustworthy and accept the Secure cookie over http.
    //
    // It cannot be derived from the request. Behind a TLS-terminating ingress the
    // server sees plain http while the browser sees https, so request-derived
    // Secure would strip it from the deployment that most needs it. Boot logs a
    // WARN when it is on. Bind via LEOFLOW_AUTH_SESSION_COOKIE_INSECURE.
    SessionCookieInsecure bool `mapstructure:"session_cookie_insecure"`
    // SessionCookieName is the name of the browser session cookie (#1405).
    // The default, _token, is what every earlier release used, so an existing
    // install keeps its sessions. A name starting with __Host- makes the
    // browser refuse the cookie unless it is Secure, has Path=/ and no Domain,
    // which is how the session cookie is always set: a page on a sibling host
    // under the same parent domain can then no longer plant a session here.
    // Recommended for any deployment that serves several hosts under one
    // parent domain. A __Host- or __Secure- name with SessionCookieInsecure
    // fails boot, because the browser would drop every session cookie.
    // Empty means _token. Bind via LEOFLOW_AUTH_SESSION_COOKIE_NAME.
    SessionCookieName string `mapstructure:"session_cookie_name"`
}
```

<a name="BootstrapSection"></a>
## type [BootstrapSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_bootstrap.go#L21-L33>)

BootstrapSection configures the admin user the server creates or reconciles at boot in tenant \`default\`. Nothing happens unless Password or PasswordHash is set.

```go
type BootstrapSection struct {
    // Email (DEXAFLOW_BOOTSTRAP_EMAIL) is the address of the bootstrap admin.
    // Empty (the default) means admin@dexaflow.local, resolved to the legacy
    // admin@leoflow.local on an install whose admin already has that address
    // (auth.ResolveAdminEmail).
    Email string `mapstructure:"email"`
    // Password (DEXAFLOW_BOOTSTRAP_PASSWORD) is the plaintext password of the
    // bootstrap admin, used only when PasswordHash is empty. Never logged.
    Password string `mapstructure:"password"`
    // PasswordHash (DEXAFLOW_BOOTSTRAP_PASSWORD_HASH) is a bcrypt hash of the
    // bootstrap admin's password. It wins over Password. Never logged.
    PasswordHash string `mapstructure:"password_hash"`
}
```

<a name="BootstrapSection.Enabled"></a>
### func \(BootstrapSection\) [Enabled](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_bootstrap.go#L36>)

```go
func (b BootstrapSection) Enabled() bool
```

Enabled reports whether a bootstrap credential is set.

<a name="BootstrapSection.GoString"></a>
### func \(BootstrapSection\) [GoString](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_bootstrap.go#L47>)

```go
func (b BootstrapSection) GoString() string
```

GoString is String for the %\#v verb.

<a name="BootstrapSection.LogValue"></a>
### func \(BootstrapSection\) [LogValue](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_bootstrap.go#L50>)

```go
func (b BootstrapSection) LogValue() slog.Value
```

LogValue keeps the credentials out of structured logs.

<a name="BootstrapSection.String"></a>
### func \(BootstrapSection\) [String](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_bootstrap.go#L42>)

```go
func (b BootstrapSection) String() string
```

String keeps the credentials out of anything that formats the section, the whole ServerConfig included.

<a name="BuildKitSection"></a>
## type [BuildKitSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_deploy.go#L70-L75>)

BuildKitSection is deploy.defaults.remote\_builder.buildkit, the in\-cluster builder the chart runs \(builder.enabled\): the fields of domain.BuildKitConfig a server can know. The kubeconfig context is the client's own. Every field empty advertises none.

```go
type BuildKitSection struct {
    // Namespace is the namespace of the builder.
    Namespace string `mapstructure:"namespace"`
    // PodSelector is the label selector of the builder pod.
    PodSelector string `mapstructure:"pod_selector"`
}
```

<a name="CIOIDCIssuerSection"></a>
## type [CIOIDCIssuerSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/ci_oidc.go#L45-L62>)

CIOIDCIssuerSection is one entry of auth.ci\_oidc.issuers.

```go
type CIOIDCIssuerSection struct {
    // Name identifies the entry in logs, metrics, sessions and `ci init`.
    Name string `yaml:"name" json:"name"`
    // Provider is github, gitlab, cloudbuild or other.
    Provider string `yaml:"provider" json:"provider"`
    // Issuer is the exact iss of the provider's tokens.
    Issuer string `yaml:"issuer" json:"issuer"`
    // JWKSURL is where the provider's keys are; empty discovers them.
    JWKSURL string `yaml:"jwks_url" json:"jwks_url"`
    // Audience is the aud the tokens must carry.
    Audience string `yaml:"audience" json:"audience"`
    // Tenant is the only tenant the entry's tokens act in.
    Tenant string `yaml:"tenant" json:"tenant"`
    // MaxLifetimeSeconds caps exp - iat; 0 means 3600, at most 86400.
    MaxLifetimeSeconds int `yaml:"max_lifetime_seconds" json:"max_lifetime_seconds"`
    // Rules map claims to a role; the first that matches decides.
    Rules []cioidc.Rule `yaml:"rules" json:"rules"`
}
```

<a name="CIOIDCSection"></a>
## type [CIOIDCSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/ci_oidc.go#L32-L42>)

CIOIDCSection configures the CI OIDC issuers whose job tokens the control plane exchanges for a deploy session \(ADR 0092, \#1882\). With no issuer the exchange route is not registered and nothing changes.

```go
type CIOIDCSection struct {
    // SessionTTLSeconds is how long a session from the exchange lives, 300 to
    // 43200; 3600 by default. It is not renewed: a later job exchanges again.
    SessionTTLSeconds int `mapstructure:"session_ttl_seconds"`
    // Issuers are the trusted CI issuers, each with its audience, its one
    // tenant and its rules. Configure them in the server configuration file
    // or, as a JSON array of the same objects, in
    // DEXAFLOW_AUTH_CI_OIDC_ISSUERS. Decoded out of band (mapstructure:"-"),
    // because a claim name may contain dots that viper would split.
    Issuers []CIOIDCIssuerSection `mapstructure:"-" yaml:"issuers"`
}
```

<a name="CIOIDCSection.CIIssuers"></a>
### func \(CIOIDCSection\) [CIIssuers](<https://github.com/dexadata/dexaflow/blob/main/internal/config/ci_oidc.go#L77>)

```go
func (s CIOIDCSection) CIIssuers() []cioidc.Issuer
```

CIIssuers returns the entries as the verifier takes them.

<a name="CIOIDCSection.Enabled"></a>
### func \(CIOIDCSection\) [Enabled](<https://github.com/dexadata/dexaflow/blob/main/internal/config/ci_oidc.go#L65>)

```go
func (s CIOIDCSection) Enabled() bool
```

Enabled reports whether any CI OIDC issuer is configured.

<a name="CIOIDCSection.SessionTTL"></a>
### func \(CIOIDCSection\) [SessionTTL](<https://github.com/dexadata/dexaflow/blob/main/internal/config/ci_oidc.go#L69>)

```go
func (s CIOIDCSection) SessionTTL() time.Duration
```

SessionTTL is SessionTTLSeconds as a duration; zero \(a section built in code rather than loaded\) is the default hour.

<a name="CORSSection"></a>
## type [CORSSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L211-L213>)

CORSSection configures cross\-origin access.

```go
type CORSSection struct {
    AllowedOrigins []string `mapstructure:"allowed_origins"`
}
```

<a name="CloudBuildSection"></a>
## type [CloudBuildSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_deploy.go#L79-L90>)

CloudBuildSection is deploy.defaults.remote\_builder.cloud\_build, the fields of domain.CloudBuildConfig. Every field empty advertises none.

```go
type CloudBuildSection struct {
    // Project is the Google Cloud project the build runs in.
    Project string `mapstructure:"project"`
    // Region is the Cloud Build region, such as us-central1.
    Region string `mapstructure:"region"`
    // WorkerPool is a private pool,
    // projects/<project>/locations/<region>/workerPools/<name>.
    WorkerPool string `mapstructure:"worker_pool"`
    // ServiceAccount is the account the build runs as,
    // projects/<project>/serviceAccounts/<email>.
    ServiceAccount string `mapstructure:"service_account"`
}
```

<a name="Config"></a>
## type [Config](<https://github.com/dexadata/dexaflow/blob/main/internal/config/config.go#L31-L89>)

Config holds the developer CLI configuration.

```go
type Config struct {
    // ServerURL is the control plane base URL used by push and auth create-token.
    ServerURL string `mapstructure:"server_url"`
    // Token is the JWT bearer token persisted by `dexaflow auth login` and used by
    // push and deploy when no --token flag or DEXAFLOW_TOKEN env is set.
    Token string `mapstructure:"token"`
    // LogLevel is reserved for CLI log verbosity (not yet wired).
    LogLevel string `mapstructure:"log_level"`
    // Registry is reserved for the image registry used by image build (ADR 0003).
    Registry string `mapstructure:"registry"`
    // ParserCmd is the command used to invoke the Python parser from compile.
    ParserCmd string `mapstructure:"parser_cmd"`

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
    // hash.
    //
    // Rotating it on every fresh install invalidates browser tokens from a
    // prior install — so a reinstall greets the user with the login screen and
    // the freshly printed credentials actually do something (#121). Empty on
    // legacy installs; the lite runner falls back to the dev-only constant with
    // a warning so the upgrade does not break existing setups.
    JWTSecret string `mapstructure:"jwt_secret"`
    // SecretKey is the per-install key encrypting connection passwords and extra
    // at rest, generated by `dexaflow setup` (random, 64 hex chars). Lite reads it
    // from this file only: an exported DEXAFLOW_SECRET_KEY does not replace it.
    //
    // Lite used to
    // encrypt with a constant compiled into this repository, identical on every
    // install, so anyone holding a Lite database file read every credential in it
    // (#486). Empty on a legacy install; the lite runner then encrypts with that
    // constant, warns on every start, and names `dexaflow lite migrate-key`, the
    // only path that moves existing rows onto a key of the install's own (ADR
    // 0065). Lite reads the key from the file alone (readLiteKeyConfig in
    // internal/cli), never through this loader, whose environment overlay would
    // let an exported LEOFLOW_SECRET_KEY replace it.
    SecretKey string `mapstructure:"secret_key"`
    // SecretKeyPrevious is a comma-separated list of decrypt-only predecessors.
    // `dexaflow lite migrate-key` writes it while a migration is unfinished and
    // removes it once every stored secret was moved and re-checked; an operator
    // may also set it by hand.
    //
    // With it set, Lite passes every key to the server,
    // which reads with any and writes with the first (#486).
    SecretKeyPrevious string `mapstructure:"secret_key_previous"`
}
```

<a name="Load"></a>
### func [Load](<https://github.com/dexadata/dexaflow/blob/main/internal/config/config.go#L120>)

```go
func Load(configFile string, flags *pflag.FlagSet) (*Config, error)
```

Load assembles configuration from defaults, the given file \(when non\-empty\), LEOFLOW\_\* environment variables, and the provided flag set, in increasing order of precedence. A nil flag set or empty file path is ignored.

<a name="DatabaseSection"></a>
## type [DatabaseSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L216-L268>)

DatabaseSection configures the Postgres connection pool.

```go
type DatabaseSection struct {
    URL          string `mapstructure:"url"`
    MaxOpenConns int    `mapstructure:"max_open_conns"`
    MaxIdleConns int    `mapstructure:"max_idle_conns"`
    // SchedulerMaxConns, when positive, gives the scheduler loop, its reapers
    // and its janitors a pool of their own with this many connections, so API
    // traffic that saturates the main pool cannot stall a scheduler tick. Only
    // a process with scheduler.enabled opens it. 0 (the default) keeps them on
    // the main pool.
    SchedulerMaxConns int `mapstructure:"scheduler_max_conns"`
    // AgentMaxConns, when positive, gives the agent gRPC handlers' execution
    // store (heartbeats, state reports, reschedules and the attempt liveness
    // check) a pool of their own with this many connections; XCom, connection
    // and secret reads stay on the main pool. API traffic that saturates the
    // main pool then cannot block a heartbeat write while the reapers judge
    // liveness from last_heartbeat_at (#1624). Only a process that serves the
    // agent endpoint (a scheduler role) opens it. 0 (the default) keeps them
    // on the main pool.
    AgentMaxConns int `mapstructure:"agent_max_conns"`
    // StatementTimeoutMS, when positive, sets statement_timeout on every
    // connection of the main pool, which serves the API. It is never applied to
    // the leader election pool (its session holds the scheduler's advisory
    // lock), the health pool or the scheduler pool, and the few writes that
    // cascade over a DAG's history lift it for their own transaction. Without a
    // scheduler pool the scheduler shares the main pool and so the timeout too.
    // 0 (the default) sets nothing.
    StatementTimeoutMS int `mapstructure:"statement_timeout_ms"`
    // ConnMaxLifetimeJitterMS, when positive, adds up to this much random time
    // to each connection's lifetime in the main, scheduler and health pools, so
    // replicas started together do not all reconnect at the same moment. 0 (the
    // default) leaves the pgx default, or what the DSN sets.
    ConnMaxLifetimeJitterMS int `mapstructure:"conn_max_lifetime_jitter_ms"`
    // InstallationID (DEXAFLOW_DATABASE_INSTALLATION_ID) is the installation
    // id of the database this release was installed on (ADR 0090 decision 3).
    // The chart reads it from the ConfigMap the migration Job keeps. When set,
    // the server serves only a database that carries it: another id, none, or
    // no schema at all is a replaced database, which boot waits on and which
    // makes a running server exit. Empty (the default, and Lite) checks
    // nothing.
    InstallationID string `mapstructure:"installation_id"`
    // MigrateOnBoot (DEXAFLOW_DATABASE_MIGRATE_ON_BOOT, default false; Helm
    // migrations.onBoot) applies the embedded migrations at boot when the
    // schema is absent or behind, never when it is ahead, dirty or a replaced
    // database (ADR 0090 decision 4). It needs a DSN that may run DDL, and the
    // api role refuses it.
    MigrateOnBoot bool `mapstructure:"migrate_on_boot"`
    // RowLevelSecurity (DEXAFLOW_DATABASE_ROW_LEVEL_SECURITY, default false)
    // binds each main pool connection a request uses to the request's tenant,
    // so the row level security policies of migration 059 hide every other
    // tenant's rows from it (ADR 0104). Boot fails unless row level security
    // is on for the tenant tables (`dexaflow-migrate rls on`).
    RowLevelSecurity bool `mapstructure:"row_level_security"`
}
```

<a name="DeployDefaultsSection"></a>
## type [DeployDefaultsSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_deploy.go#L32-L55>)

DeployDefaultsSection is deploy.defaults: the values served as they are, and the switch that adds the source mode runtime image to them.

```go
type DeployDefaultsSection struct {
    domain.DeployDefaults `mapstructure:",squash"`
    // SourceMode (deploy.defaults.source_mode, default false) advertises
    // execution.source_mode.image as source_mode_image, so a deploy to this
    // cluster that names no runtime image of its own registers dag.py on it
    // with no build (#1772). It requires execution.source_mode.enabled.
    SourceMode bool `mapstructure:"source_mode"`
    // RemoteBuilder (deploy.defaults.remote_builder.cloud_build.*, #1890, or
    // deploy.defaults.remote_builder.buildkit.*, #1883) is the remote builder
    // a deploy to this cluster uses for builds no daemonless path covers,
    // when the project, its target and its workspace name none (ADR 0083).
    // Unset (the default) advertises none.
    RemoteBuilder RemoteBuilderSection `mapstructure:"remote_builder"`
    // AdvertiseTaskServiceAccount (deploy.defaults.task_service_account,
    // default false, #1890) serves executor.task_service_account as
    // task_service_account, so a deploy says which account its tasks run as
    // when they name none. It requires executor.task_service_account.
    AdvertiseTaskServiceAccount bool `mapstructure:"task_service_account"`
    // AdvertiseWarmPools (deploy.defaults.warm_pools, default false, #1890)
    // serves execution.warm_pools_enabled, execution.min_idle_workers and
    // execution.max_pool_size as warm_pools, so a deploy says how many idle
    // workers its DAG gets.
    AdvertiseWarmPools bool `mapstructure:"warm_pools"`
}
```

<a name="DeploySection"></a>
## type [DeploySection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_deploy.go#L13-L28>)

DeploySection is what the server tells \`dexaflow deploy\` \(\#1704\).

```go
type DeploySection struct {
    // Defaults (deploy.defaults.*) are the registry prefix, build platforms,
    // base image and remote builder a deploy to this cluster uses for
    // whatever the project, its workspace, its target and the person's config
    // leave unset, and the switches that advertise the source mode runtime
    // image, the task service account and the warm pools. Served on GET
    // /api/v2/deployDefaults; unset (the default) advertises nothing, so
    // every deploy resolves as it did before. None of them is a secret.
    Defaults DeployDefaultsSection `mapstructure:"defaults"`
    // RequireDigest (deploy.require_digest, #1703) is what registering a
    // version, or making one current again, does with an image not pinned by
    // a full sha256 digest: off (the default) accepts it, warn accepts it with
    // a warning, a log line and a count, enforce refuses it. DEXAFLOW_DEPLOY_REQUIRE_DIGEST (legacy
    // LEOFLOW_DEPLOY_REQUIRE_DIGEST).
    RequireDigest domain.DigestRequirement `mapstructure:"require_digest"`
}
```

<a name="DispatchSection"></a>
## type [DispatchSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L331-L350>)

DispatchSection sizes the BufferedDispatcher \(\#127\). BufferSize=0 keeps the scheduler tick synchronous with the inner dispatcher, the right shape for Lite \(subprocess fork is microseconds\). BufferSize\>0 enables the worker pool, the right shape for Pro \(Kubernetes API calls add real latency\). The defaults are set per\-edition by configsetup so the user does not have to think about this; an operator can still tune the knobs.

```go
type DispatchSection struct {
    // BufferSize is the depth of the queued-dispatches channel. 0 disables the
    // pool (synchronous passthrough). A full channel returns ErrAtCapacity to
    // the scheduler, which leaves the TI scheduled for the next tick.
    BufferSize int `mapstructure:"buffer_size"`
    // Workers is the number of goroutines draining the queue. Ignored when
    // BufferSize <= 0; otherwise floored to 1.
    Workers int `mapstructure:"workers"`
    // TickBudget bounds the wall time one scheduler tick spends dispatching
    // (#1596). Once it is spent the tick stops dispatching, the remaining ready
    // tasks stay scheduled with no attempt charged, and the next tick starts at
    // the run after the one that was cut, so one wide fan-out under the
    // Kubernetes client rate limit cannot hold every other run, and cron run
    // creation, for minutes. 0 disables it; negative fails boot.
    TickBudget time.Duration `mapstructure:"tick_budget"`
    // MaxPerTenantPerTick caps the tasks one tenant may dispatch in one tick,
    // so a tenant with a wide fan-out or many runs leaves dispatch room for the
    // others. 0 (default) means no cap; negative fails boot.
    MaxPerTenantPerTick int `mapstructure:"max_per_tenant_per_tick"`
}
```

<a name="ExecutionAllowlistSection"></a>
## type [ExecutionAllowlistSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L165-L180>)

ExecutionAllowlistSection is executor.defaults.execution\_allowlist. A DAG that sets a field \(or priority class\) outside it is refused at registration and, for a DAG registered before the list changed, at dispatch.

```go
type ExecutionAllowlistSection struct {
    // Fields lists the execution fields a DAG may set, by their dag.json names
    // (domain.ExecutionPolicyFields). "*" (the default) allows all of them,
    // "none" or an explicitly empty list allows none.
    Fields []string `mapstructure:"fields"`
    // PriorityClasses, when not empty, lists the only priority_class_name values
    // a DAG may set. Empty (the default) allows any.
    PriorityClasses []string `mapstructure:"priority_classes"`
    // ServiceAccounts, when not empty, lists the only execution.service_account
    // values a DAG may name (issue #537 C2). A task that names none runs as
    // executor.task_service_account, which is not checked. Empty allows any.
    ServiceAccounts []string `mapstructure:"service_accounts"`
    // Images, when not empty, lists the DAG images that may run: exact
    // references or prefixes ending in "*" (issue #537 C2). Empty allows any.
    Images []string `mapstructure:"images"`
}
```

<a name="ExecutionAllowlistSection.ExecutionPolicyConfig"></a>
### func \(ExecutionAllowlistSection\) [ExecutionPolicyConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L183>)

```go
func (s ExecutionAllowlistSection) ExecutionPolicyConfig() domain.ExecutionPolicyConfig
```

ExecutionPolicyConfig is the section as the domain parser takes it.

<a name="ExecutionSection"></a>
## type [ExecutionSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L197-L273>)

ExecutionSection configures warm worker pools, Pro\-gated N:1 pod reuse \(ADR 0058\). Every field is operator\-set \(never DAG\-author\-set\), consistent with the secret\-scoping stance: whether a pod may be reused across attempts is an operator's security decision, not a DAG author's. All fields default to a byte\-for\-byte no\-op, warm pools OFF means dedicated pod\-per\-task, today's behavior, and are read for runtime behavior only in a later brick; N1a introduces the knobs plus the fail\-closed boot guard \(validateExecution\).

```go
type ExecutionSection struct {
    // WarmPoolsEnabled turns on N:1 pod reuse (ADR 0058). Default false = a
    // dedicated pod per task attempt, today's behavior byte-for-byte. Turning it on
    // is gated at boot on the security prerequisites (token-exchange transport +
    // liveness enforcement) because a warm pod reuses one credential across attempts.
    WarmPoolsEnabled bool `mapstructure:"warm_pools_enabled"`
    // SourceMode runs a version on the operator's runtime image from the dag.py
    // it was registered with, with no image build (ADR 0067 §3). Off by default.
    SourceMode SourceModeSection `mapstructure:"source_mode"`
    // Admission configures the dispatch admission hooks (ADR 0100).
    Admission AdmissionSection `mapstructure:"admission"`
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
    // MaxPoolSize caps the total warm workers a single DAG version may hold,
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
    // budget, never a starvation lever: the promised idle floors of a tenant's
    // versions with an active run (the sum of their EffectiveMinIdle) are honored
    // even when they exceed this cap (the reconciler raises the effective budget
    // to the floor sum and meters the misconfiguration). A prewarm-only version
    // (ADR 0082) never raises it: the prewarm floors share what the cap leaves,
    // by DAG id, and a prewarm pool above its share shrinks with the worker idle
    // TTL. The floors of versions with an active run are created at once on top
    // of such a pool, so the tenant exceeds the cap until it has shrunk (at
    // most one idle TTL after each share drop). The cap is otherwise enforced
    // only by refusing to CREATE new warm pods, never by deleting a busy worker.
    MaxWarmPodsPerTenant int `mapstructure:"max_warm_pods_per_tenant"`
    // WarmReadOnlyRootFilesystem mounts every warm worker's root filesystem read
    // only and gives each attempt its own HOME and XDG dirs inside the scratch the
    // worker wipes between attempts, plus a sweep of the shared /tmp emptyDir and
    // /dev/shm before each attempt and after it ends. It closes X3.2: on a
    // writable root a file one attempt plants on the image (a module on the
    // working directory's sys.path, a ~/.local site-packages entry) is executed
    // by the next attempt on the same worker. Default false keeps
    // today's writable root, since a task that writes outside $HOME, $TMPDIR, /tmp
    // and /dev/shm would fail with it on. It applies to warm pods created after it
    // is turned on. Dedicated task pods are not affected; they follow
    // executor.defaults.read_only_task_root_filesystem.
    WarmReadOnlyRootFilesystem bool `mapstructure:"warm_read_only_root_filesystem"`
    // WarmRequireAttemptHomeSweep refuses a warm worker that does not confirm,
    // when it registers, that it empties /tmp and /dev/shm between attempts
    // (#1678), so its DAG version's attempts run on dedicated pods. It applies
    // only with WarmReadOnlyRootFilesystem on. Default false logs and meters
    // such a worker and keeps it: agents from runtime v0.5.1 sweep but only the
    // ones built after the report was added can confirm it.
    WarmRequireAttemptHomeSweep bool `mapstructure:"warm_require_attempt_home_sweep"`
    // WarmPoolEventRefill makes warm-pool refill event driven: the reconciler
    // reads the fleet from a dedicated warm-pod informer instead of a LIST per
    // tick, reacts at once to a worker being deleted, failing or claimed, and
    // issues replacement creates concurrently (bounded). Default false keeps
    // today's 30s polling with a LIST and serial creates. The failover reapers
    // and the drained-anchor delete keep their live reads either way.
    WarmPoolEventRefill bool `mapstructure:"warm_pool_event_refill"`
}
```

<a name="ExecutionSection.EffectiveMinIdle"></a>
### func \(ExecutionSection\) [EffectiveMinIdle](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L282>)

```go
func (e ExecutionSection) EffectiveMinIdle(dagMinIdle int) int
```

EffectiveMinIdle resolves the warm\-worker target for one dag\_version \(domain.WarmPoolDefaults.EffectiveMinIdle\).

<a name="ExecutionSection.WarmPoolDefaults"></a>
### func \(ExecutionSection\) [WarmPoolDefaults](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L276>)

```go
func (e ExecutionSection) WarmPoolDefaults() domain.WarmPoolDefaults
```

WarmPoolDefaults is e's warm pool settings as the domain carries them.

<a name="ExecutorSection"></a>
## type [ExecutorSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L11-L77>)

ExecutorSection configures how tasks are executed.

```go
type ExecutorSection struct {
    HTTP HTTPExecutorSection `mapstructure:"http"`
    // Policy is the raw YAML of executor.policy, the pod fields the operator
    // forces on every task pod (ADR 0063), parsed and validated at boot by
    // dispatch.ParseForcePolicy. It is read straight from the config file named
    // by DEXAFLOW_CONFIG, because a node selector key such as
    // "kubernetes.io/os" carries dots that viper would split, and tolerations
    // cannot travel as environment variables. Empty means no policy.
    Policy []byte `mapstructure:"-" yaml:"policy"`
    // TaskNamespace is the Kubernetes namespace the server creates task pods and
    // per-run staging PVCs in. It MUST match the namespace the Helm chart grants
    // the executor Role in (chart `taskNamespace` → LEOFLOW_EXECUTOR_TASK_NAMESPACE);
    // a mismatch 403s every dispatch (#480). Defaults to "leoflow".
    TaskNamespace string `mapstructure:"task_namespace"`
    // Type selects the pod-path executor: "kubernetes" (default, pod-per-task) or
    // "subprocess" (dev only, runs the agent on the host without isolation, used
    // by `dexaflow lite`).
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
    // connection's key_path, so Dexaflow never stores the key itself (ADR 0035).
    // Empty = no secret mounted.
    TaskSecretName string `mapstructure:"task_secret_name"`
    // TaskSecretMountPath is where TaskSecretName is mounted in the task pod.
    TaskSecretMountPath string `mapstructure:"task_secret_mount_path"`
    // Defaults holds per-cluster task defaults applied at dispatch to fill gaps the
    // DAG artifact left empty (ADR 0023, layer L0). They never override a value
    // baked into dag.json, keeping the artifact portable across clusters.
    Defaults PlatformDefaultsSection `mapstructure:"defaults"`
    // CollectSettledRunPods deletes a settled run's finished task pods as soon
    // as the reconciler has recorded every outcome, in one DeleteCollection by
    // the run's label instead of one delete per pod after the grace period. It
    // needs the deletecollection verb on pods (the chart grants it only when this
    // is on) and falls back to per-pod deletes without it. Off by default:
    // finished pods stay for the grace period, so they can be inspected with
    // kubectl.
    CollectSettledRunPods bool `mapstructure:"collect_settled_run_pods"`
    // KubeClient sets the client-side rate limits of the control plane's
    // Kubernetes clients.
    KubeClient KubeClientSection `mapstructure:"kube_client"`
    // Unit is the resource unit one pool slot stands for (ADR 0066). Unset (the
    // default) changes nothing. Set, every task pod is sized pool_slots x unit
    // where the task leaves cpu or memory out, and a task that declares more
    // than that is refused at registration and at dispatch.
    Unit ExecutorUnitSection `mapstructure:"unit"`
}
```

<a name="ExecutorUnitSection"></a>
## type [ExecutorUnitSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L83-L88>)

ExecutorUnitSection is executor.unit: the CPU and memory of one pool slot, as Kubernetes quantities \(both or neither\), how a task that does not fit its size is treated \(refuse or warn\), and the largest pool\_slots a task may have while the unit is set.

```go
type ExecutorUnitSection struct {
    CPU     string `mapstructure:"cpu"`
    Memory  string `mapstructure:"memory"`
    Enforce string `mapstructure:"enforce"`
    MaxSize int    `mapstructure:"max_size"`
}
```

<a name="ExecutorUnitSection.ResourceUnitConfig"></a>
### func \(ExecutorUnitSection\) [ResourceUnitConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L91>)

```go
func (u ExecutorUnitSection) ResourceUnitConfig() domain.ResourceUnitConfig
```

ResourceUnitConfig is the section as the domain parser takes it.

<a name="HTTPExecutorSection"></a>
## type [HTTPExecutorSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L288-L292>)

HTTPExecutorSection configures HTTP\-related executor knobs.

```go
type HTTPExecutorSection struct {
    // UserAgent is the default User-Agent header for HTTP requests a task image
    // may make on the platform's behalf.
    UserAgent string `mapstructure:"user_agent"`
}
```

<a name="HelmSection"></a>
## type [HelmSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_helm.go#L8-L27>)

HelmSection is the Helm release a Pro control plane was installed by. The chart sets it so the not\-ready reason and the boot log can name the exact command that migrates this release's database \(ADR 0090 decision 2\). It changes nothing else: an install without it gets the same hint with placeholders.

```go
type HelmSection struct {
    // Release (DEXAFLOW_HELM_RELEASE) is the Helm release name. Empty means
    // the process was not installed by the chart.
    Release string `mapstructure:"release"`
    // Namespace (DEXAFLOW_HELM_NAMESPACE) is the release namespace.
    Namespace string `mapstructure:"namespace"`
    // ChartVersion (DEXAFLOW_HELM_CHART_VERSION) is the version of the chart
    // that rendered this pod, so the hint upgrades to the same chart and does
    // not pick up a newer one.
    ChartVersion string `mapstructure:"chart_version"`
    // MigrationsEnabled (DEXAFLOW_HELM_MIGRATIONS_ENABLED, default true) is the
    // chart's migrations.enabled. When false the chart runs no migration Job,
    // so `helm upgrade` would not migrate anything and the hint names the
    // operator's own migration step instead.
    MigrationsEnabled bool `mapstructure:"migrations_enabled"`
    // MigrateImage (DEXAFLOW_HELM_MIGRATE_IMAGE) is the dexaflow-migrate
    // image and tag of this release, named by the hint when
    // MigrationsEnabled is false.
    MigrateImage string `mapstructure:"migrate_image"`
}
```

<a name="HomeLinkSection"></a>
## type [HomeLinkSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_ui.go#L59-L64>)

HomeLinkSection is the operator's way back from the UI: a label and the absolute http\(s\) URL it opens, in the same tab. Both are set or neither.

```go
type HomeLinkSection struct {
    // Label is the link text, for example the operator's portal name.
    Label string `mapstructure:"label"`
    // URL is where the link goes. It must be an absolute http:// or https:// URL.
    URL string `mapstructure:"url"`
}
```

<a name="JWTSection"></a>
## type [JWTSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_auth.go#L213-L226>)

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

<a name="KubeClientSection"></a>
## type [KubeClientSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L102-L113>)

KubeClientSection sets the client\-side rate limits \(client\-go token buckets\) of the control plane's Kubernetes clients. The dispatch client creates task pods; the agent token exchange builds its own client with the same limits. Maintenance work \(pod informer, reconciler, reapers, staging GC, warm pool reconciler\) shares the dispatch client unless MaintenanceQPS is set, in which case it gets a separate client and token bucket so a maintenance burst cannot starve pod creation.

```go
type KubeClientSection struct {
    // QPS and Burst limit the dispatch client. Defaults are client-go's own
    // (5 and 10); a non-positive value falls back to them.
    QPS   float64 `mapstructure:"qps"`
    Burst int     `mapstructure:"burst"`
    // MaintenanceQPS and MaintenanceBurst limit a separate maintenance client.
    // 0 (default) keeps maintenance on the dispatch client, one shared budget as
    // before. A non-positive burst with a positive QPS falls back to client-go's
    // default burst.
    MaintenanceQPS   float64 `mapstructure:"maintenance_qps"`
    MaintenanceBurst int     `mapstructure:"maintenance_burst"`
}
```

<a name="LogTailSection"></a>
## type [LogTailSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_logs.go#L41-L54>)

LogTailSection configures the live\-tail fan\-out.

```go
type LogTailSection struct {
    // Publish is "always" (default: every line is published, as before) or
    // "on_demand" (lines are published only while someone follows the attempt;
    // a new follower may see its first live lines up to about a second late).
    // Bind via DEXAFLOW_LOGS_TAIL_PUBLISH (legacy LEOFLOW_LOGS_TAIL_PUBLISH).
    Publish string `mapstructure:"publish"`
    // ReplayBudgetBytes bounds what the on_demand gates of all log streams hold
    // for replay together (#1621). Each stream holds at most 1 MiB, so without
    // it a thousand silent streams can hold about 500 MiB; over the budget the
    // longest silent stream's held lines are dropped first, and a follower of
    // that stream reads them from the stored log. 0 (the default) bounds
    // nothing, as before. Read only with Publish "on_demand".
    ReplayBudgetBytes int64 `mapstructure:"replay_budget_bytes"`
}
```

<a name="LogsSection"></a>
## type [LogsSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_logs.go#L8-L27>)

LogsSection configures task log shipping.

```go
type LogsSection struct {
    // Dir is the root directory for the disk log sink (the default backend).
    Dir string `mapstructure:"dir"`
    // Backend selects the durable task-log store: "disk" (default) writes files
    // under Dir; "s3" ships each attempt to an S3-compatible bucket (AWS S3,
    // MinIO, Ceph RGW); "gcs" ships to Google Cloud Storage via its native SDK.
    // Object storage is opt-in, Lite and every deployment that does not set this
    // keep the exact on-disk path unchanged.
    Backend string `mapstructure:"backend"`
    // PersistenceEnabled indicates whether the log sink storage is backed by
    // durable persistence (a PVC in Kubernetes or host disk in Lite). When false
    // with backend "disk", logs are kept in an ephemeral emptyDir and will not
    // survive a pod restart.
    PersistenceEnabled bool `mapstructure:"persistence_enabled"`
    // Sink configures the object-store backend; read only when Backend is "s3" or
    // "gcs".
    Sink ObjectLogSection `mapstructure:"sink"`
    // Tail configures the live-tail fan-out of task log lines.
    Tail LogTailSection `mapstructure:"tail"`
}
```

<a name="MetricsSection"></a>
## type [MetricsSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L302-L307>)

MetricsSection configures the Prometheus scrape.

```go
type MetricsSection struct {
    // DropLegacyNames stops publishing every dexaflow_* family a second time
    // under its pre-rename leoflow_* name. Off by default (ADR 0062 gate), so
    // dashboards and alerts written against the old names keep working.
    DropLegacyNames bool `mapstructure:"drop_legacy_names"`
}
```

<a name="OIDCSection"></a>
## type [OIDCSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_auth.go#L237-L319>)

OIDCSection configures the OIDC/SSO login flow \(Authorization Code \+ PKCE\). It is read only when auth.provider is "oidc", which is Pro\-gated and fails boot closed unless Issuer, ClientID, and RedirectURL are all set.

Verification is keyless: the ID token is validated against the issuer's public JWKS discovered from Issuer, so no secret is stored for the verify path. ClientSecret is used solely for the authorization\-code exchange and is injected via LEOFLOW\_AUTH\_OIDC\_CLIENT\_SECRET \(env, never persisted, never logged\), the same posture as the JWT secret.

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
    RoleMappings map[string]string `mapstructure:"-" yaml:"role_mappings"`
    // DefaultRole softens the default-deny WITHOUT weakening the secure default:
    // when an authenticated user resolves to zero mapped roles and DefaultRole is
    // set, they are granted this single role (operators are advised to use a
    // read-only role such as "viewer"). Empty (the default) keeps strict
    // default-deny, an unmapped user gets no role. It must name an existing DB
    // role for the resolved tenant; an unknown role fails the login closed.
    DefaultRole string `mapstructure:"default_role"`
    // TenantClaim selects which IdP claim identifies the tenant: "tid" (Entra) or
    // "hd" (Google Workspace).
    TenantClaim string `mapstructure:"tenant_claim"`
    // TenantClaims maps a TenantClaim value to a Dexaflow tenant name. A value not
    // present here is rejected (403), the login never falls back to "default".
    //
    // Decoded OUT-OF-BAND (mapstructure:"-"), not by viper: a Google Workspace
    // `hd` value is a domain (always dotted, e.g. "example.com" or
    // "sub.example.co.uk"), which viper's "." delimiter would split into nested
    // maps and fail to decode, the #826 crash. LoadServer parses this map from
    // the raw YAML instead.
    TenantClaims map[string]string `mapstructure:"-" yaml:"tenant_claims"`
    // AllowedEmailDomains is an install-time, login-level allowlist layered on TOP
    // of the tid/hd tenant pin, it is NOT the pin itself (that stays issuer +
    // tid/hd + email_verified per D6). The check runs only AFTER the pin and
    // email_verified==true have passed, so the email domain is trustworthy at that
    // point. Empty (the default) imposes no domain restriction, the tid/hd pin is
    // the sole boundary. Non-empty admits a login (pre-provisioned OR JIT) only
    // when the verified email's domain is in the list; every other login is
    // rejected 403. It gates EVERY OIDC login, not just auto-provisioning.
    AllowedEmailDomains []string `mapstructure:"allowed_email_domains"`
    // BreakGlassEmails is the allowlist of local password logins permitted while
    // provider is "oidc"; every other password login is rejected (SSO-only).
    BreakGlassEmails []string `mapstructure:"break_glass_emails"`
    // JITProvisioning creates a user row on first OIDC login when no matching one
    // exists; the new row is granted the roles from RoleMappings. OFF by default.
    //
    // OFF denies every first login. A login matches a user only by
    // (oidc_provider, oidc_subject) and CreateOIDCUser, reached only from this
    // path, is the sole statement that writes those columns, so no API, CLI or
    // migration can pre-create an OIDC identity (ADR 0057, amendment on D4).
    // internal/serverapp warns about this at boot.
    JITProvisioning bool `mapstructure:"jit_provisioning"`
    // AutoRedirect starts the login flow on the sign-in page instead of rendering
    // it, for a deployment where that page is a screen to acknowledge for nothing
    // (an edge proxy has already authenticated, or SSO is the only way in).
    //
    // OFF by default: turning it on for everyone would remove the sign-in page
    // from deployments that rely on it. It is suppressed on a refused sign-on and
    // behind an explicit ?local=1, so a denial still lands somewhere readable and
    // a break-glass account can always reach the form.
    AutoRedirect bool `mapstructure:"auto_redirect"`
    // ClockSkewSeconds is the tolerance applied to the ID token's exp/iat/nbf
    // checks to absorb small clock differences between the IdP and this server.
    // Defaults to 60.
    ClockSkewSeconds int `mapstructure:"clock_skew_seconds"`
}
```

<a name="OTelSection"></a>
## type [OTelSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L310-L321>)

OTelSection configures OpenTelemetry export.

```go
type OTelSection struct {
    Enabled  bool   `mapstructure:"enabled"`
    Endpoint string `mapstructure:"endpoint"`
    // SampleRatio is the share of request traces kept (1 keeps every trace).
    // No propagator is installed, so an incoming traceparent is ignored and
    // every request starts a new root trace; spans within a request follow
    // its root's decision.
    SampleRatio float64 `mapstructure:"sample_ratio"`
    // SkipProbeSpans drops spans for /healthz, /readyz and /static/*. Off by
    // default (ADR 0062), so every request is traced as before.
    SkipProbeSpans bool `mapstructure:"skip_probe_spans"`
}
```

<a name="ObjectLogSection"></a>
## type [ObjectLogSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_logs.go#L65-L96>)

ObjectLogSection configures the object\-store log backend for both the "s3" and "gcs" providers. Auth is keyless\-first \(ADR 0035\): leave the credential fields empty to use the ambient chain, IRSA / instance profile for S3, GKE Workload Identity \(ADC\) for GCS. Static keys and credential files are a discouraged escape hatch for dev and clusters without an identity broker.

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
    // RGW). Empty uses the AWS default endpoint. S3-only, it is NOT the way to
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
    // (recommended) uses Application Default Credentials, GKE Workload Identity
    // keyless. GCS-only.
    CredentialsFile string `mapstructure:"credentials_file"`
    // Layout selects how new attempts are written to the bucket: "single"
    // (default) keeps one object per attempt at {try}.log, rewritten on every
    // flush; "segmented" writes numbered segments under {try}.log.d/
    // ({try}.e{epoch}.log.d/ for a later execution of the try) so a flush
    // uploads only the open segment. Both layouts are always readable. Turn
    // segmented on only once every replica runs a version that reads it.
    Layout string `mapstructure:"layout"`
}
```

<a name="ObservabilitySection"></a>
## type [ObservabilitySection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L290-L299>)

ObservabilitySection configures logging, metrics, and tracing.

```go
type ObservabilitySection struct {
    OTel      OTelSection    `mapstructure:"otel"`
    LogLevel  string         `mapstructure:"log_level"`
    LogFormat string         `mapstructure:"log_format"`
    Metrics   MetricsSection `mapstructure:"metrics"`
    // UsageMetrics advances dexaflow_task_attempts_total and
    // dexaflow_task_slot_seconds_total from the usage events (ADR 0096), one
    // series per tenant. Off by default; it needs usage.events.enabled.
    UsageMetrics bool `mapstructure:"usage_metrics"`
}
```

<a name="PlatformDefaultsSection"></a>
## type [PlatformDefaultsSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L117-L160>)

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
    // LimitsCPU/LimitsMemory are the default task limits (#537): the CPU and
    // memory limit a task pod gets in a dimension its resources leave without
    // one, including a task that declares requests only. Empty (the default)
    // sets no limit, as before. Either may be set alone. Not applied with
    // executor.unit, which always sets both limits.
    LimitsCPU    string `mapstructure:"limits_cpu"`
    LimitsMemory string `mapstructure:"limits_memory"`
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
    // ExecutionAllowlist restricts which task execution (placement) fields a DAG
    // author may set, and optionally which priority classes (issue #629). The
    // default allows everything. Like the two settings above it is a cluster
    // setting: on a shared cluster these fields reach other teams' nodes and
    // preemption order.
    ExecutionAllowlist ExecutionAllowlistSection `mapstructure:"execution_allowlist"`
}
```

<a name="PoolsSection"></a>
## type [PoolsSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L317-L323>)

PoolsSection is scheduler.pools \(\#2173\).

```go
type PoolsSection struct {
    // Enabled turns named pools on (ADR 0053): the cross-DAG slot gate of the
    // tick and the pools API. Unset, it follows the edition: on for the
    // cluster edition, off otherwise, as before the key existed. Read it
    // through ServerConfig.PoolsEnabled.
    Enabled *bool `mapstructure:"enabled"`
}
```

<a name="RedisSection"></a>
## type [RedisSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L271-L287>)

RedisSection configures the Redis connection.

```go
type RedisSection struct {
    URL string `mapstructure:"url"`
    // CAFile is the absolute path to a PEM CA bundle the client trusts when
    // negotiating TLS to a `rediss://` URL (#312). Required to reach managed
    // Redis (Memorystore SERVER_AUTHENTICATION, ElastiCache in-transit
    // encryption, Azure Cache for Redis) whose server cert is signed by a
    // provider / per-instance CA that is not in the container's system
    // roots. Empty falls back to the SDK default, system roots only.
    // The Helm chart sets this via LEOFLOW_REDIS_CA_FILE when
    // `redis.caConfigMap` is configured, pointing at the mounted ConfigMap.
    CAFile string `mapstructure:"ca_file"`
    // RequireNoEviction refuses to boot unless Redis runs maxmemory-policy
    // noeviction, or when the policy cannot be read (#2354). Off, an unsafe
    // policy only logs a warning and sets dexaflow_redis_eviction_policy_safe
    // to 0.
    RequireNoEviction bool `mapstructure:"require_noeviction"`
}
```

<a name="RemoteBuilderSection"></a>
## type [RemoteBuilderSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_deploy.go#L59-L64>)

RemoteBuilderSection is deploy.defaults.remote\_builder: the backends of domain.RemoteBuilderConfig as server keys. At most one is set.

```go
type RemoteBuilderSection struct {
    // CloudBuild is deploy.defaults.remote_builder.cloud_build.
    CloudBuild CloudBuildSection `mapstructure:"cloud_build"`
    // BuildKit is deploy.defaults.remote_builder.buildkit.
    BuildKit BuildKitSection `mapstructure:"buildkit"`
}
```

<a name="RetentionSection"></a>
## type [RetentionSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L357-L383>)

RetentionSection configures the leader\-only retention janitor, which deletes metadata rows past a per\-class age. Runs and the audit log are kept by default \(0 days\); the usage events keep 30 days, and they exist only once an operator turns their recording on. The pacing knobs keep each statement small and each cycle bounded.

```go
type RetentionSection struct {
    // DagRunsDays deletes finished (success or failed) dag runs, with their task
    // instances, attempt history and XCom index rows, once they ended more
    // than this many days ago. 0 (default) keeps every run.
    DagRunsDays int `mapstructure:"dag_runs_days"`
    // AuditLogDays deletes audit log entries older than this many days. 0
    // (default) keeps the whole audit log.
    AuditLogDays int `mapstructure:"audit_log_days"`
    // UsageEventsDays deletes usage events (ADR 0096) recorded more than this
    // many days ago, oldest first in the order the service API serves them.
    // Default 30; 0 keeps every event.
    UsageEventsDays int `mapstructure:"usage_events_days"`
    // DryRun only counts the rows a cycle would delete, logs the counts and
    // exports them as gauges, and deletes nothing.
    DryRun bool `mapstructure:"dry_run"`
    // Interval is how often a cycle runs. Default 1h.
    Interval time.Duration `mapstructure:"interval"`
    // BatchSize caps the rows one DELETE statement removes, except the task
    // instances of one run larger than it, which go together. Default 1000.
    BatchSize int `mapstructure:"batch_size"`
    // BatchPause is the sleep between two batches, so the janitor never holds
    // the database busy. Default 100ms.
    BatchPause time.Duration `mapstructure:"batch_pause"`
    // MaxRowsPerCycle stops a cycle from starting another batch once this many
    // rows were deleted; the rest waits for the next cycle. Default 100000.
    MaxRowsPerCycle int `mapstructure:"max_rows_per_cycle"`
}
```

<a name="RetentionSection.Active"></a>
### func \(RetentionSection\) [Active](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L399>)

```go
func (r RetentionSection) Active(usageRecording bool) bool
```

Active reports whether the janitor has work: a run or audit class, or the usage events window while recording is on. With recording off the table only holds what an earlier recording left, pruned whenever the janitor runs for another class.

<a name="RetentionSection.Enabled"></a>
### func \(RetentionSection\) [Enabled](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L391>)

```go
func (r RetentionSection) Enabled() bool
```

Enabled reports whether the runs or the audit log class is configured.

<a name="RetentionSection.Validate"></a>
### func \(RetentionSection\) [Validate](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L404>)

```go
func (r RetentionSection) Validate() error
```

Validate is ValidateWith for an install that does not record usage events.

<a name="RetentionSection.ValidateWith"></a>
### func \(RetentionSection\) [ValidateWith](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L411>)

```go
func (r RetentionSection) ValidateWith(usageRecording bool) error
```

ValidateWith rejects a negative window always, and pacing that would make the janitor unbounded or a busy loop when it has work \(Active\): a run or audit class, or the usage events window while usageRecording is on. So the default 30 day window checks nothing new on an install that does not record.

<a name="SchedulerSection"></a>
## type [SchedulerSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_execution.go#L295-L314>)

SchedulerSection configures the scheduler loop.

```go
type SchedulerSection struct {
    LoopIntervalMS int  `mapstructure:"loop_interval_ms"`
    Enabled        bool `mapstructure:"enabled"`
    // EagerPromotion cuts the latency between a task settling and its
    // downstreams being dispatched. Within a tick, the scheduler re-plans a run
    // after its own state changes (a newly scheduled task is queued at once, a
    // skip cascades), a bounded number of times; and a task's terminal report
    // triggers an early tick instead of waiting for the next interval, which
    // stays the upper bound. Off by default (ADR 0062): the tick then promotes
    // one step per interval, as it always has.
    EagerPromotion bool            `mapstructure:"eager_promotion"`
    Dispatch       DispatchSection `mapstructure:"dispatch"`
    Alerts         AlertsSection   `mapstructure:"alerts"`
    Pools          PoolsSection    `mapstructure:"pools"`
    // PoolStarvationThreshold is how long a task held only by its pool waits
    // before the pool is reserved for it, so tasks of fewer slots cannot keep
    // a larger one out forever (ADR 0066 §4). 0 disables reservations. Read
    // only while pools are on (see PoolsSection).
    PoolStarvationThreshold time.Duration `mapstructure:"pool_starvation_threshold"`
}
```

<a name="SecretsSection"></a>
## type [SecretsSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_secrets.go#L23-L33>)

SecretsSection configures the external secrets backend \(ADR 0060\). When Backend is set, a Connection/Variable a DAG declares can be resolved pod\-side from the provider store under the pod's keyless identity instead of the leoflow vault. Empty \(the default\) keeps the vault as the only source, byte\-identical to pre\-0060. This is operator\-only config: it is delivered to the pod as LEOFLOW\_SECRETS\_\* env, which an author's task env can never set \(\#828\).

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
## type [ServerConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L22-L81>)

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
    Retention     RetentionSection     `mapstructure:"retention"`
    Usage         UsageSection         `mapstructure:"usage"`
    Deploy        DeploySection        `mapstructure:"deploy"`
    Bootstrap     BootstrapSection     `mapstructure:"bootstrap"`
    Agent         AgentSection         `mapstructure:"agent"`
    Helm          HelmSection          `mapstructure:"helm"`
    // SecretKey (LEOFLOW_SECRET_KEY) encrypts connection secrets at rest (ADR
    // 0019). Raw 32 chars, 64-char hex, or base64. Empty disables connection
    // writes.
    //
    // A COMMA-SEPARATED LIST rotates the key: the first entry encrypts and
    // decrypts, every later entry only decrypts. Nothing is ever written under a
    // later entry. This is the shape Airflow's `fernet_key` uses, so an operator
    // coming from Airflow already knows to put the new key first and the old
    // ones after (#486).
    //
    // Trying keys in order is safe only because AES-GCM is authenticated: a
    // wrong key fails to open rather than returning plausible garbage.
    SecretKey string `mapstructure:"secret_key"`
    // SecretKeyReencryptOnBoot (LEOFLOW_SECRET_KEY_REENCRYPT_ON_BOOT, default
    // true) runs the ADR 0019 boot sweep that moves stored secrets onto the
    // first key of SecretKey. `dexaflow lite` sets it to false: a Lite install
    // migrates only through the explicit `dexaflow lite migrate-key`, which
    // records every key before touching a row and verifies before it commits
    // (ADR 0065). Pro keeps the default.
    SecretKeyReencryptOnBoot bool `mapstructure:"secret_key_reencrypt_on_boot"`
    // SecretKeyMigrationLock (LEOFLOW_SECRET_KEY_MIGRATION_LOCK, default false)
    // makes the server hold the key-migration advisory lock shared for its whole
    // life, refuse to start while a migration holds it, and exit if it loses it.
    // `dexaflow lite` sets it to true (ADR 0065 section 3).
    SecretKeyMigrationLock bool `mapstructure:"secret_key_migration_lock"`
    // VariableEncryption (DEXAFLOW_VARIABLE_ENCRYPTION, default "off") is how
    // Variable values are stored (#507). "off" writes plaintext, as every
    // earlier release did, and still reads values sealed before. "on" seals
    // every write with SecretKey and seals the stored plaintext rows at boot;
    // it requires a usable SecretKey. "decrypt" writes plaintext and restores
    // every sealed row at boot: the step before rolling back to a release that
    // cannot read sealed values.
    VariableEncryption string `mapstructure:"variable_encryption"`
    // DagsArePausedAtCreation (DEXAFLOW_DAGS_ARE_PAUSED_AT_CREATION, default
    // true) is Airflow's [core] dags_are_paused_at_creation: a DAG registered
    // for the first time starts paused, so its schedule creates no run until
    // someone unpauses it. A DAG's own is_paused_upon_creation wins over it.
    // It applies only on creation: a redeploy never pauses or unpauses a DAG
    // (#1705). `dexaflow lite` sets it to false, so a DAG run locally starts
    // active as it always did.
    DagsArePausedAtCreation bool `mapstructure:"dags_are_paused_at_creation"`
}
```

<a name="LoadServer"></a>
### func [LoadServer](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L325>)

```go
func LoadServer(configFile string, flags *pflag.FlagSet) (*ServerConfig, error)
```

LoadServer assembles the server configuration from defaults, the given file, LEOFLOW\_\* environment variables, and flags, in increasing precedence.

<a name="ServerConfig.AdvertisedDeployDefaults"></a>
### func \(\*ServerConfig\) [AdvertisedDeployDefaults](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_deploy.go#L119>)

```go
func (c *ServerConfig) AdvertisedDeployDefaults() domain.DeployDefaults
```

AdvertisedDeployDefaults are the deploy defaults GET /api/v2/deployDefaults serves: deploy.defaults, with the runtime image of source mode when deploy.defaults.source\_mode asks for it and the mode is on, the remote builder when deploy.defaults.remote\_builder sets one, and the task service account and warm pools of this server when their switches are on \(\#1890\).

<a name="ServerConfig.ClusterEdition"></a>
### func \(\*ServerConfig\) [ClusterEdition](<https://github.com/dexadata/dexaflow/blob/main/internal/config/edition.go#L24>)

```go
func (c *ServerConfig) ClusterEdition() bool
```

ClusterEdition reports whether the server runs as the cluster edition.

<a name="ServerConfig.PoolsEnabled"></a>
### func \(\*ServerConfig\) [PoolsEnabled](<https://github.com/dexadata/dexaflow/blob/main/internal/config/edition.go#L30>)

```go
func (c *ServerConfig) PoolsEnabled() bool
```

PoolsEnabled reports whether named pools are on: scheduler.pools.enabled when it is set, otherwise the cluster edition \(\#2173\).

<a name="ServerConfig.Validate"></a>
### func \(\*ServerConfig\) [Validate](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L480>)

```go
func (c *ServerConfig) Validate() error
```

Validate reports configuration errors that must abort startup.

<a name="ServerSection"></a>
## type [ServerSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L84-L170>)

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
    // GzipResponses gzips JSON and NDJSON responses of 1 KB or more on the API
    // and UI surfaces for clients that accept it. Log routes and anything that
    // flushes (live tails, SSE) stay uncompressed. Routes that return secrets,
    // tokens or code (variables, connections, XComs, auth and the UI session
    // token, IDE files, DAG sources and specs, a single task or task
    // instance) also stay uncompressed:
    // compressing a secret next to reflected input lets its length leak the
    // secret (BREACH). Other JSON can still echo request input next to private
    // data, which is the trade-off of turning this on. Off by default (ADR 0062).
    GzipResponses bool `mapstructure:"gzip_responses"`
    // MaxPageLimit caps the `limit` a list endpoint accepts, and the
    // `dag_runs_limit` of /ui/dags; a larger value is served as the cap, like
    // Airflow's [api] maximum_page_limit. 0 (the default, ADR 0062 gate) keeps
    // today's behavior: no cap.
    MaxPageLimit int `mapstructure:"max_page_limit"`
    // MaxRequestBodyBytes caps the body of a request to the API and UI
    // listener; a larger one is refused with 413 (#1613). The routes that take
    // a DAG's code or spec (registering a version, its dry run, writing an IDE
    // file) use MaxUploadBodyBytes instead. 0 (the default, ADR 0062 gate)
    // means no limit, as before.
    MaxRequestBodyBytes int64 `mapstructure:"max_request_body_bytes"`
    // MaxUploadBodyBytes caps the body of the routes that take a DAG's code or
    // spec. 0 (the default) leaves them unlimited even when
    // MaxRequestBodyBytes is set, so a cap sized for JSON calls cannot refuse
    // a deploy.
    MaxUploadBodyBytes int64 `mapstructure:"max_upload_body_bytes"`
    // ReadTimeout bounds reading a whole request, headers and body, on the API
    // and metrics listeners: a slow client cannot hold a connection open by
    // trickling a body. Set it above the slowest legitimate upload. It never
    // limits a response: net/http lifts the read deadline once the body is
    // read, so live log tails are unaffected. 0 (the default, ADR 0062 gate)
    // means no limit, as before.
    ReadTimeout time.Duration `mapstructure:"read_timeout"`
    // IdleTimeout closes a keep-alive connection that has been idle this long.
    // 0 (the default) keeps idle connections open, as before, even when
    // ReadTimeout is set (net/http alone would fall back to ReadTimeout).
    IdleTimeout time.Duration `mapstructure:"idle_timeout"`
    // ProbeBudget bounds the whole /readyz handler, every dependency check
    // sharing one deadline, so the server answers 503 naming the slow
    // dependency before the kubelet's own timeout cuts the probe off. The
    // chart renders it as probes.readiness.timeoutSeconds minus one second
    // (#1050). The default, 2s, is the budget every earlier release used, and
    // 0 means the same; a negative value fails boot.
    ProbeBudget time.Duration `mapstructure:"probe_budget"`
    // LivenessSchedulerStall fails /healthz, so the kubelet restarts the pod,
    // when this process leads the scheduler and its loop has not started an
    // iteration for this long (#539). 0, the default, leaves /healthz
    // unconditional, as before; a negative value fails boot.
    LivenessSchedulerStall time.Duration `mapstructure:"liveness_scheduler_stall"`
    // HTTPTLSCert/HTTPTLSKey serve the API and UI listener (http_addr) over
    // HTTPS with an operator-supplied PEM certificate and key (#638), so a load
    // balancer can re-encrypt to the pod. Both must be set together; both empty
    // (the default) keeps plain HTTP. The files are re-read when they change, so
    // a rotated certificate is picked up without a restart. The metrics listener
    // is not affected.
    HTTPTLSCert string `mapstructure:"http_tls_cert"`
    HTTPTLSKey  string `mapstructure:"http_tls_key"`
    // PoolsReadOnly makes the tenant-facing pool API (/api/v2/pools) serve reads
    // only: create, resize and delete answer 403 for every role, tenant admin
    // included. It is for an engine shared by many tenants, where the platform
    // operator sizes each tenant's pools out of band and a tenant must not be
    // able to raise its own slot budget. It also makes the scheduler admit a task
    // naming a pool its tenant has not defined against default_pool, since a
    // tenant cannot create pools then. Default false keeps pools writable under
    // write:pool and undefined pools unlimited, today's behavior.
    PoolsReadOnly bool `mapstructure:"pools_read_only"`
}
```

<a name="ServerSection.EffectiveRole"></a>
### func \(ServerSection\) [EffectiveRole](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L190>)

```go
func (s ServerSection) EffectiveRole() string
```

EffectiveRole returns the configured role, defaulting empty to RoleAll so an unset role \(Lite, and every pre\-0049 deployment\) keeps the monolith behavior.

<a name="ServerSection.HTTPTLSEnabled"></a>
### func \(ServerSection\) [HTTPTLSEnabled](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L174>)

```go
func (s ServerSection) HTTPTLSEnabled() bool
```

HTTPTLSEnabled reports whether the API listener serves HTTPS, which needs both server.http\_tls\_cert and server.http\_tls\_key.

<a name="ServerSection.ServesAPI"></a>
### func \(ServerSection\) [ServesAPI](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L198>)

```go
func (s ServerSection) ServesAPI() bool
```

ServesAPI reports whether this process runs the HTTP API \+ UI.

<a name="ServerSection.ServesScheduler"></a>
### func \(ServerSection\) [ServesScheduler](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server.go#L205>)

```go
func (s ServerSection) ServesScheduler() bool
```

ServesScheduler reports whether this process runs the scheduler, dispatch, and the agent gRPC endpoint.

<a name="Session"></a>
## type [Session](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L57-L60>)

Session is a token saved for one control plane.

```go
type Session struct {
    Server string `yaml:"server"`
    Token  string `yaml:"token"`
}
```

<a name="Setting"></a>
## type [Setting](<https://github.com/dexadata/dexaflow/blob/main/internal/config/reference.go#L18-L35>)

Setting describes one configuration key as the loaders read it. It is the raw material of the generated configuration reference page \(internal/tools/configref\): every field is derived from the structs and the default maps the loaders use, so the page cannot name a key, a variable or a default the code does not have.

```go
type Setting struct {
    // Key is the dotted key in the YAML config file, e.g. "auth.oidc.issuer".
    Key string
    // Env is the DEXAFLOW_* variable that sets the key, or "" when only the
    // config file can set it.
    Env string
    // LegacyEnv is the LEOFLOW_* variable from before the rename, read when Env
    // is unset; "" when Env is.
    LegacyEnv string
    // Type is a short type name: string, int, bool, float, duration, list or map.
    Type string
    // Default is the built-in value, rendered as text ("" when empty).
    Default string
    // Owner is the Go struct type that holds the field, e.g. "OIDCSection".
    Owner string
    // Field is the Go field name in Owner, e.g. "Issuer".
    Field string
}
```

<a name="CLISettings"></a>
### func [CLISettings](<https://github.com/dexadata/dexaflow/blob/main/internal/config/reference.go#L45>)

```go
func CLISettings() []Setting
```

CLISettings lists every key of the developer CLI Config in struct order, with the defaults Load applies.

<a name="ServerSettings"></a>
### func [ServerSettings](<https://github.com/dexadata/dexaflow/blob/main/internal/config/reference.go#L39>)

```go
func ServerSettings() []Setting
```

ServerSettings lists every key of ServerConfig in struct order, with the defaults LoadServer applies.

<a name="SourceModeSection"></a>
## type [SourceModeSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_deploy.go#L192-L209>)

SourceModeSection is execution.source\_mode \(ADR 0067 §3\).

```go
type SourceModeSection struct {
    // Enabled turns source mode on. Default false: Pro ignores a version's source.
    // Bind via DEXAFLOW_EXECUTION_SOURCE_MODE_ENABLED (legacy
    // LEOFLOW_EXECUTION_SOURCE_MODE_ENABLED).
    Enabled bool `mapstructure:"enabled"`
    // Image is the runtime image, pinned by a full sha256 digest, that
    // source-mode versions name as their image. Bind via
    // DEXAFLOW_EXECUTION_SOURCE_MODE_IMAGE (legacy LEOFLOW_EXECUTION_SOURCE_MODE_IMAGE).
    Image string `mapstructure:"image"`
    // WarmPools lets a source-mode version run on warm workers, which receive
    // its dag.py with each assignment instead of in the pod spec (ADR 0095).
    // Default false: source-mode versions run on dedicated pods. Turn it on
    // once the runtime image carries an agent from 0.5.7 or later; a worker
    // on an older agent never gets a source-mode attempt, which then takes a
    // dedicated pod. Bind via DEXAFLOW_EXECUTION_SOURCE_MODE_WARM_POOLS
    // (legacy LEOFLOW_EXECUTION_SOURCE_MODE_WARM_POOLS).
    WarmPools bool `mapstructure:"warm_pools"`
}
```

<a name="SourceModeSection.RuntimeImage"></a>
### func \(SourceModeSection\) [RuntimeImage](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_deploy.go#L217>)

```go
func (s SourceModeSection) RuntimeImage() string
```

RuntimeImage returns the runtime image when source mode is on, and "" when it is off, the form the dispatcher and the register check take.

<a name="SourceModeSection.WarmPoolsOn"></a>
### func \(SourceModeSection\) [WarmPoolsOn](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_deploy.go#L213>)

```go
func (s SourceModeSection) WarmPoolsOn() bool
```

WarmPoolsOn reports whether source\-mode versions may run on warm workers: source mode is on and so is its warm\_pools setting.

<a name="TrustedIssuerSection"></a>
## type [TrustedIssuerSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_auth.go#L17-L70>)

TrustedIssuerSection configures one trusted external issuer \(\#1284\). Its tokens are verified against its published JWKS and name an existing user, linked by \(issuer:\<name\>, subject\), in an allowed tenant; they never create users or grant roles.

```go
type TrustedIssuerSection struct {
    // Name identifies the issuer; its users are linked under "issuer:<name>".
    // Lowercase letters, digits and '-'. Keep it stable once users exist.
    Name string `mapstructure:"name"`
    // Issuer is the exact `iss` the tokens carry.
    Issuer string `mapstructure:"issuer"`
    // JWKSURL is where the issuer publishes its public signing keys: https, or
    // http on a loopback host for local development.
    JWKSURL string `mapstructure:"jwks_url"`
    // Audience is the `aud` the tokens must carry for this Dexaflow.
    Audience string `mapstructure:"audience"`
    // TenantClaim names the string claim carrying the Dexaflow tenant name.
    TenantClaim string `mapstructure:"tenant_claim"`
    // AllowedTenants lists the tenants the issuer may sign in to; "*" allows
    // every tenant.
    AllowedTenants []string `mapstructure:"allowed_tenants"`
    // MaxLifetimeSeconds caps exp - iat of a token, the replay window of a
    // handoff. Zero uses the 120-second default; at most 600.
    MaxLifetimeSeconds int `mapstructure:"max_lifetime_seconds"`
    // AllowedOrigins are the origins (scheme://host[:port]) whose pages may
    // post a handoff. Any other Origin, or none, is refused, so another site
    // cannot sign a visitor in (login CSRF). Required.
    AllowedOrigins []string `mapstructure:"allowed_origins"`
    // BearerAudiences turns on the bearer mode (#1468): a token whose `aud` is
    // one of these is accepted as the bearer of any /api/v2 request, reused
    // within its lifetime, the way a remote MCP client authenticates (ADR 0050
    // D9). Each must differ from Audience, so a handoff token is never a
    // bearer. Empty (the default) leaves the mode off.
    BearerAudiences []string `mapstructure:"bearer_audiences"`
    // BearerMaxLifetimeSeconds caps exp - iat of a bearer. Zero uses the
    // 900-second default; at most 3600.
    BearerMaxLifetimeSeconds int `mapstructure:"bearer_max_lifetime_seconds"`
    // DeployAudience makes the issuer the only way to deploy (#1773). When
    // set, registering a DAG version (and its dry run) and setting the current
    // version, and deleting a DAG, take only a bearer of this issuer whose
    // `aud` is this audience alone and that carries dexaflow:deploy, besides
    // the role check; a token that also names another audience, a session, a
    // password login token, an API token and a bearer of another audience
    // are refused with 403. It is a bearer audience of its own: it turns the
    // bearer verifier on without BearerAudiences, and a token for it gets
    // the scopes it carries, like any bearer. It must differ from Audience
    // and from every BearerAudiences entry, so a handoff or another client's
    // bearer is never a deploy token. Empty (the default) changes nothing.
    DeployAudience string `mapstructure:"deploy_audience"`
    // HostPattern binds a browser session to its tenant's host (#1463), for
    // an operator that serves each tenant on its own host in front of one
    // engine, for example "{tenant}.example.com". When set, a handoff is
    // accepted only on the host the pattern names for the token's tenant, and
    // a request that authenticates with the session cookie only on the host
    // for the session's tenant. The host is the request's Host, or
    // X-Forwarded-Host from a peer in server.trusted_proxies. A bearer in the
    // Authorization header is not bound. Empty (the default) binds nothing.
    HostPattern string `mapstructure:"host_pattern"`
}
```

<a name="TrustedIssuerSection.Enabled"></a>
### func \(TrustedIssuerSection\) [Enabled](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_auth.go#L77>)

```go
func (s TrustedIssuerSection) Enabled() bool
```

Enabled reports whether a trusted issuer is configured.

<a name="UISection"></a>
## type [UISection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_ui.go#L12-L55>)

UISection configures the embedded Airflow UI.

```go
type UISection struct {
    // InstanceName is shown in the UI navbar (Airflow's instance_name). Empty
    // falls back to "Dexaflow"; `dexaflow lite` sets it to mark the environment.
    InstanceName string `mapstructure:"instance_name"`
    // AutoRefreshIntervalSeconds is the SPA's polling cadence for DAG /
    // DagRun / task-instance state refresh (Airflow's auto_refresh_interval).
    // Zero (the default) falls back to api.DefaultUIAutoRefreshIntervalSeconds
    // (5s; 30s before v0.5.9). `dexaflow lite` sets it to 1s for a snappy inner
    // loop so the SPA reflects state changes almost immediately during dev.
    AutoRefreshIntervalSeconds int `mapstructure:"auto_refresh_interval_seconds"`
    // Edition marks the running edition (ADR 0027): EditionCluster for the Helm
    // chart, EditionLite for the binary; EditionProAlias is the earlier name of
    // EditionCluster and means the same. Lite shows the silver LITE badge and
    // cluster the gold CLUSTER badge (independent of the auth mode). Empty or
    // any other value shows no badge; Demo intentionally renders without one.
    Edition string `mapstructure:"edition"`
    // Workspace is the DAG project directory the Lite web editor edits (ADR 0025).
    // Empty disables the editor (Production, or Lite without one).
    Workspace string `mapstructure:"workspace"`
    // MonacoDir is where the pinned Monaco bundle was fetched by `dexaflow setup`;
    // the editor page is served Monaco from it. Empty shows a setup hint.
    MonacoDir string `mapstructure:"monaco_dir"`
    // HomeLink is an optional, persistent link from the UI back to the platform
    // the operator serves Dexaflow from (#1290). Empty shows no link.
    HomeLink HomeLinkSection `mapstructure:"home_link"`
    // Theme is a JSON object in the shape of Airflow's `[api] theme` (#1289):
    // `tokens` (Chakra design tokens, such as colors.brand and fonts),
    // `globalCss`, `icon` and `icon_dark_mode`. The UI applies it through its
    // own theming. Empty keeps the stock look.
    Theme string `mapstructure:"theme"`
    // FaviconURL replaces the UI's favicon. It must be http(s) or root-relative.
    FaviconURL string `mapstructure:"favicon_url"`
    // StylesheetURLs are extra stylesheets loaded by every UI page, typically
    // the web fonts a theme's fonts tokens name. Each must be http(s) or
    // root-relative.
    StylesheetURLs []string `mapstructure:"stylesheet_urls"`
    // ETagRevalidation lets the browser revalidate the UI routes that compute
    // an ETag (the grid's task summaries) with "private, no-cache" instead of
    // no-store, so an unchanged grid poll is answered 304. The browser then
    // keeps the last grid body in its private cache after logout, revalidated
    // before any use. Off by default (ADR 0062 gate): every UI route keeps
    // no-store.
    ETagRevalidation bool `mapstructure:"etag_revalidation"`
}
```

<a name="UsageEventsSection"></a>
## type [UsageEventsSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_usage.go#L9-L14>)

UsageEventsSection configures the task attempt usage events.

```go
type UsageEventsSection struct {
    // Enabled turns on the recording of one usage event per finished task
    // attempt. The scheduler leader writes it to the database, where the
    // trigger of migration 055 reads it, so every replica records the same way.
    Enabled bool `mapstructure:"enabled"`
}
```

<a name="UsageSection"></a>
## type [UsageSection](<https://github.com/dexadata/dexaflow/blob/main/internal/config/server_usage.go#L4-L6>)

UsageSection configures the per\-tenant usage records of ADR 0096.

```go
type UsageSection struct {
    Events UsageEventsSection `mapstructure:"events"`
}
```

<a name="UserStore"></a>
## type [UserStore](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L20-L42>)

UserStore is the per\-user CLI state kept in the config file \(\~/.dexaflow/config.yaml\): the single\-server session written before named targets existed, the user's own targets, which target is selected, and one saved token per control plane.

Tokens are keyed by the server URL they were issued for, never by a target name: a target is a label that a shared workspace file can point anywhere, so a token is only ever sent to the server that issued it.

```go
type UserStore struct {
    // ServerURL and Token are the session `dexaflow auth login` wrote before
    // per-server sessions existed. They are read, and kept, as they are.
    ServerURL string `yaml:"server_url,omitempty"`
    Token     string `yaml:"token,omitempty"`
    // Registry is the user's default image registry prefix, the lowest-ranked
    // source a deploy reads one from.
    Registry string `yaml:"registry,omitempty"`
    // CurrentTarget is the target used outside a workspace (and inside one
    // that selects none).
    CurrentTarget string `yaml:"current_target,omitempty"`
    // Targets are the targets the user defined outside any workspace.
    Targets []UserTarget `yaml:"targets,omitempty"`
    // Sessions holds one saved token per control plane.
    Sessions []Session `yaml:"sessions,omitempty"`
    // WorkspaceTargets records `dexaflow target use` inside a workspace: the
    // user's own choice, which leaves the shared workspace file untouched.
    WorkspaceTargets []WorkspaceTarget `yaml:"workspace_targets,omitempty"`
    // TrustedWorkspaces lists workspace roots whose workspace file is used
    // even though another user owns it ("*" trusts every one). The CLI only
    // reads it; the user writes it.
    TrustedWorkspaces []string `yaml:"trusted_workspaces,omitempty"`
}
```

<a name="ReadUserStore"></a>
### func [ReadUserStore](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L77>)

```go
func ReadUserStore(path string) (*UserStore, error)
```

ReadUserStore reads the user store from the config file at path. A missing file is an empty store.

<a name="UserStore.RemoveSession"></a>
### func \(\*UserStore\) [RemoveSession](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L279>)

```go
func (s *UserStore) RemoveSession(server string) bool
```

RemoveSession forgets the token saved for server and reports whether there was one.

<a name="UserStore.RemoveTarget"></a>
### func \(\*UserStore\) [RemoveTarget](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L313>)

```go
func (s *UserStore) RemoveTarget(name string) bool
```

RemoveTarget removes the user target called name and reports whether it existed.

<a name="UserStore.Session"></a>
### func \(\*UserStore\) [Session](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L255>)

```go
func (s *UserStore) Session(server string) (string, bool)
```

Session returns the token saved for server, if any.

<a name="UserStore.SetSession"></a>
### func \(\*UserStore\) [SetSession](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L266>)

```go
func (s *UserStore) SetSession(server, token string)
```

SetSession saves token for server, replacing the one saved before.

<a name="UserStore.SetTarget"></a>
### func \(\*UserStore\) [SetTarget](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L301>)

```go
func (s *UserStore) SetTarget(t UserTarget)
```

SetTarget adds t, or replaces the user target of the same name.

<a name="UserStore.SetWorkspaceTarget"></a>
### func \(\*UserStore\) [SetWorkspaceTarget](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L337>)

```go
func (s *UserStore) SetWorkspaceTarget(root, name string)
```

SetWorkspaceTarget records name as the user's target for the workspace at root; an empty name clears the selection.

<a name="UserStore.Target"></a>
### func \(\*UserStore\) [Target](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L291>)

```go
func (s *UserStore) Target(name string) (UserTarget, bool)
```

Target returns the user target called name.

<a name="UserStore.WorkspaceTarget"></a>
### func \(\*UserStore\) [WorkspaceTarget](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L325>)

```go
func (s *UserStore) WorkspaceTarget(root string) string
```

WorkspaceTarget returns the target the user selected for the workspace at root, or "".

<a name="UserTarget"></a>
## type [UserTarget](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L45-L54>)

UserTarget is a named control plane defined in the user's config file.

```go
type UserTarget struct {
    Name      string   `yaml:"name"`
    Server    string   `yaml:"server"`
    Registry  string   `yaml:"registry,omitempty"`
    Platforms []string `yaml:"platforms,omitempty"`
    BaseImage string   `yaml:"base_image,omitempty"`
    // SourceModeImage is the runtime image deploys to this target register
    // dag.py on, with no build (source mode, #1772).
    SourceModeImage string `yaml:"source_mode_image,omitempty"`
}
```

<a name="WorkspaceTarget"></a>
## type [WorkspaceTarget](<https://github.com/dexadata/dexaflow/blob/main/internal/config/userstore.go#L63-L66>)

WorkspaceTarget is the target the user selected for one workspace root.

```go
type WorkspaceTarget struct {
    Workspace string `yaml:"workspace"`
    Target    string `yaml:"target"`
}
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
