package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dexadata/dexaflow/internal/egress"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

// ServerConfig is the full configuration for the leoflow-server control plane.
// It mirrors the nested YAML described in the Phase 2 prompt.
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
}

// SecretsSection configures the external secrets backend (ADR 0060). When Backend
// is set, a Connection/Variable a DAG declares can be resolved pod-side from the
// provider store under the pod's keyless identity instead of the leoflow vault.
// Empty (the default) keeps the vault as the only source — byte-identical to
// pre-0060. This is operator-only config: it is delivered to the pod as
// LEOFLOW_SECRETS_* env, which an author's task env can never set (#828).
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

// LogsSection configures task log shipping.
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

// ObjectLogSection configures the object-store log backend for both the "s3" and
// "gcs" providers. Auth is keyless-first (ADR 0035): leave the credential fields
// empty to use the ambient chain — IRSA / instance profile for S3, GKE Workload
// Identity (ADC) for GCS. Static keys and credential files are a discouraged
// escape hatch for dev and clusters without an identity broker.
//
// Bucket and Prefix apply to both providers. Region, Endpoint, ForcePathStyle,
// AccessKeyID and SecretAccessKey are S3-only. CredentialsFile is GCS-only. A
// field set for the other provider is simply ignored.
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
	// Layout selects how new attempts are written to the bucket: "single"
	// (default) keeps one object per attempt at {try}.log, rewritten on every
	// flush; "segmented" writes numbered segments under {try}.log.d/ so a flush
	// uploads only the open segment. Both layouts are always readable. Turn
	// segmented on only once every replica runs a version that reads it.
	Layout string `mapstructure:"layout"`
}

// ExecutorSection configures how tasks are executed.
type ExecutorSection struct {
	HTTP HTTPExecutorSection `mapstructure:"http"`
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
	// connection's key_path — so Dexaflow never stores the key itself (ADR 0035).
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
}

// PlatformDefaultsSection configures the lowest-precedence (L0) task defaults,
// applied at dispatch to fill gaps the DAG left empty (ADR 0023).
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

// ExecutionSection configures warm worker pools — Pro-gated N:1 pod reuse
// (ADR 0058). Every field is operator-set (never DAG-author-set), consistent
// with the secret-scoping stance: whether a pod may be reused across attempts is
// an operator's security decision, not a DAG author's. All fields default to a
// byte-for-byte no-op — warm pools OFF means dedicated pod-per-task, today's
// behavior — and are read for runtime behavior only in a later brick; N1a
// introduces the knobs plus the fail-closed boot guard (validateExecution).
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
}

// EffectiveMinIdle resolves the warm-worker target for one dag_version under
// model A2 (ADR 0058 N1b2b): the DAG author declares desired warmth per DAG
// (dagMinIdle), the operator caps and floors it.
//
//   - Warm pools OFF => always 0. This is what makes a default deploy a
//     byte-for-byte no-op: with warmth gated off no warm pod is ever targeted, so
//     the reconciler (when it runs at all) reconciles every pool to zero.
//   - The DAG author's value wins when set (> 0); when the DAG declares none (0)
//     it falls back to the operator's execution.min_idle_workers floor.
//   - The resolved value is clamped to [0, max_pool_size] so an author can never
//     provision more warmth than the operator's per-version cap allows, and a
//     nonsensical negative never underflows.
func (e ExecutionSection) EffectiveMinIdle(dagMinIdle int) int {
	if !e.WarmPoolsEnabled {
		return 0
	}
	target := dagMinIdle
	if target == 0 {
		// The author declared no warmth; inherit the operator's floor.
		target = e.MinIdleWorkers
	}
	if target < 0 {
		target = 0
	}
	if e.MaxPoolSize > 0 && target > e.MaxPoolSize {
		target = e.MaxPoolSize
	}
	return target
}

// UISection configures the embedded Airflow UI.
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

// HomeLinkSection is the operator's way back from the UI: a label and the
// absolute http(s) URL it opens, in the same tab. Both are set or neither.
type HomeLinkSection struct {
	// Label is the link text, for example the operator's portal name.
	Label string `mapstructure:"label"`
	// URL is where the link goes. It must be an absolute http:// or https:// URL.
	URL string `mapstructure:"url"`
}

// HTTPExecutorSection configures HTTP-related executor knobs.
type HTTPExecutorSection struct {
	// UserAgent is the default User-Agent header for HTTP requests a task image
	// may make on the platform's behalf.
	UserAgent string `mapstructure:"user_agent"`
}

// ServerSection configures the HTTP, metrics, and agent gRPC listeners.
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

// Server roles (ADR 0049).
const (
	// RoleAll runs every component in one process (the default; Lite's only mode).
	RoleAll = "all"
	// RoleAPI runs the HTTP API + UI only (restricted network identity).
	RoleAPI = "api"
	// RoleScheduler runs the reconciler + dispatch + agent gRPC (privileged).
	RoleScheduler = "scheduler"
)

// EffectiveRole returns the configured role, defaulting empty to RoleAll so an
// unset role (Lite, and every pre-0049 deployment) keeps the monolith behavior.
func (s ServerSection) EffectiveRole() string {
	if s.Role == "" {
		return RoleAll
	}
	return s.Role
}

// ServesAPI reports whether this process runs the HTTP API + UI.
func (s ServerSection) ServesAPI() bool {
	r := s.EffectiveRole()
	return r == RoleAll || r == RoleAPI
}

// ServesScheduler reports whether this process runs the scheduler, dispatch, and
// the agent gRPC endpoint.
func (s ServerSection) ServesScheduler() bool {
	r := s.EffectiveRole()
	return r == RoleAll || r == RoleScheduler
}

// CORSSection configures cross-origin access.
type CORSSection struct {
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

// DatabaseSection configures the Postgres connection pool.
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
}

// RedisSection configures the Redis connection.
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

// TrustedIssuerSection configures one trusted external issuer (#1284). Its
// tokens are verified against its published JWKS and name an existing user,
// linked by (issuer:<name>, subject), in an allowed tenant; they never create
// users or grant roles.
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
}

// Enabled reports whether a trusted issuer is configured.
func (s TrustedIssuerSection) Enabled() bool { return s.Issuer != "" }

// AuthSection configures authentication.
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
}

// JWTSection configures JWT issuance and validation.
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

// OIDCSection configures the OIDC/SSO login flow (Authorization Code + PKCE).
// It is read only when auth.provider is "oidc", which is Pro-gated and fails
// boot closed unless Issuer, ClientID, and RedirectURL are all set.
//
// Verification is keyless: the ID token is validated against the issuer's
// public JWKS discovered from Issuer, so no secret is stored for the verify
// path. ClientSecret is used solely for the authorization-code exchange and is
// injected via LEOFLOW_AUTH_OIDC_CLIENT_SECRET (env, never persisted, never
// logged) — the same posture as the JWT secret.
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
	// exists; the new row is granted the roles from RoleMappings. OFF by default.
	//
	// OFF denies every first login. A login matches a user only by
	// (oidc_provider, oidc_subject) and CreateOIDCUser, reached only from this
	// path, is the sole statement that writes those columns, so no API, CLI or
	// migration can pre-create an OIDC identity (ADR 0057, amendment on D4).
	// cmd/dexaflow-server warns about this at boot.
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

// SchedulerSection configures the scheduler loop.
type SchedulerSection struct {
	LoopIntervalMS int             `mapstructure:"loop_interval_ms"`
	Enabled        bool            `mapstructure:"enabled"`
	Dispatch       DispatchSection `mapstructure:"dispatch"`
	Alerts         AlertsSection   `mapstructure:"alerts"`
}

// AlertsSection guards the destinations of native on-failure alerts (#424).
// An alert's URL and headers come from a tenant's connection, so on a shared
// engine a tenant could otherwise point one at the control plane's own network:
// loopback, a private service, or the cloud metadata endpoint.
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

// DispatchSection sizes the BufferedDispatcher (#127). BufferSize=0 keeps the
// scheduler tick synchronous with the inner dispatcher — the right shape for
// Lite (subprocess fork is microseconds). BufferSize>0 enables the worker
// pool — the right shape for Pro (Kubernetes API calls add real latency).
// The defaults are set per-edition by configsetup so the user does not have
// to think about this; an operator can still tune the knobs.
type DispatchSection struct {
	// BufferSize is the depth of the queued-dispatches channel. 0 disables the
	// pool (synchronous passthrough). A full channel returns ErrAtCapacity to
	// the scheduler, which leaves the TI scheduled for the next tick.
	BufferSize int `mapstructure:"buffer_size"`
	// Workers is the number of goroutines draining the queue. Ignored when
	// BufferSize <= 0; otherwise floored to 1.
	Workers int `mapstructure:"workers"`
}

// RetentionSection configures the leader-only retention janitor, which deletes
// metadata rows past a per-class age. Every class is off by default (0 days):
// nothing is ever deleted unless the operator configures a window. The pacing
// knobs keep each statement small and each cycle bounded.
type RetentionSection struct {
	// DagRunsDays deletes finished (success or failed) dag runs, with their task
	// instances, attempt history, state history and XCom index rows, once they
	// ended more than this many days ago. 0 (default) keeps every run.
	DagRunsDays int `mapstructure:"dag_runs_days"`
	// AuditLogDays deletes audit log entries older than this many days. 0
	// (default) keeps the whole audit log.
	AuditLogDays int `mapstructure:"audit_log_days"`
	// DryRun only counts the rows a cycle would delete, logs the counts and
	// exports them as gauges, and deletes nothing.
	DryRun bool `mapstructure:"dry_run"`
	// Interval is how often a cycle runs. Default 1h.
	Interval time.Duration `mapstructure:"interval"`
	// BatchSize caps the rows one DELETE statement removes. Default 1000.
	BatchSize int `mapstructure:"batch_size"`
	// BatchPause is the sleep between two batches, so the janitor never holds
	// the database busy. Default 100ms.
	BatchPause time.Duration `mapstructure:"batch_pause"`
	// MaxRowsPerCycle stops a cycle from starting another batch once this many
	// rows were deleted; the rest waits for the next cycle. Default 100000.
	MaxRowsPerCycle int `mapstructure:"max_rows_per_cycle"`
}

// maxRetentionBatchSize bounds retention.batch_size: a batch is meant to be
// small, and a value in the hundreds of thousands is the unbounded DELETE the
// janitor exists to avoid.
const maxRetentionBatchSize = 50000

// Enabled reports whether any retention class is configured.
func (r RetentionSection) Enabled() bool {
	return r.DagRunsDays > 0 || r.AuditLogDays > 0
}

// Validate rejects a negative window always, and pacing that would make the
// janitor unbounded or a busy loop when a class is on.
func (r RetentionSection) Validate() error {
	if r.DagRunsDays < 0 {
		return fmt.Errorf("retention.dag_runs_days must be >= 0 (got %d); 0 keeps every run", r.DagRunsDays)
	}
	if r.AuditLogDays < 0 {
		return fmt.Errorf("retention.audit_log_days must be >= 0 (got %d); 0 keeps the whole audit log", r.AuditLogDays)
	}
	if !r.Enabled() {
		return nil
	}
	if r.BatchSize < 1 || r.BatchSize > maxRetentionBatchSize {
		return fmt.Errorf("retention.batch_size must be between 1 and %d (got %d)", maxRetentionBatchSize, r.BatchSize)
	}
	if r.BatchPause < 0 {
		return fmt.Errorf("retention.batch_pause must be >= 0 (got %v)", r.BatchPause)
	}
	if r.Interval <= 0 {
		return fmt.Errorf("retention.interval must be > 0 (got %v)", r.Interval)
	}
	if r.MaxRowsPerCycle < 1 {
		return fmt.Errorf("retention.max_rows_per_cycle must be >= 1 (got %d)", r.MaxRowsPerCycle)
	}
	return nil
}

// ObservabilitySection configures logging, metrics, and tracing.
type ObservabilitySection struct {
	OTel      OTelSection `mapstructure:"otel"`
	LogLevel  string      `mapstructure:"log_level"`
	LogFormat string      `mapstructure:"log_format"`
}

// OTelSection configures OpenTelemetry export.
type OTelSection struct {
	Enabled  bool   `mapstructure:"enabled"`
	Endpoint string `mapstructure:"endpoint"`
}

// serverDefaults lists every leaf key with its default so that AutomaticEnv and
// Unmarshal resolve nested keys correctly.
var serverDefaults = map[string]any{
	// Empty defaults to RoleAll (EffectiveRole). This entry must exist so viper's
	// AutomaticEnv binds LEOFLOW_SERVER_ROLE — see the ui.auto_refresh note below.
	"server.role":                 "",
	"server.http_addr":            "0.0.0.0:8080",
	"server.metrics_addr":         "0.0.0.0:9090",
	"server.grpc_addr":            "0.0.0.0:9091",
	"server.grpc_tls_cert":        "",
	"server.grpc_tls_key":         "",
	"server.cors.allowed_origins": []string{"http://localhost:8080"},
	// Registered so viper's AutomaticEnv binds LEOFLOW_SERVER_TRUSTED_PROXIES. It
	// is a []string, but viper's default decode hook splits a single
	// comma-separated env var into a list, so the env-only Helm override path
	// works without a config file — this is what the chart renders (#725). Empty
	// (the default) trusts no proxy.
	"server.trusted_proxies":  []string{},
	"database.url":            "postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable",
	"database.max_open_conns": 25,
	"database.max_idle_conns": 5,
	// Pool tuning, all off by default (0) so an install that sets none of them
	// keeps one shared pool, no statement timeout and no lifetime jitter.
	// Registered so the DEXAFLOW_/LEOFLOW_DATABASE_* variables bind.
	"database.scheduler_max_conns":         0,
	"database.statement_timeout_ms":        0,
	"database.conn_max_lifetime_jitter_ms": 0,
	// Empty by default: no Redis configured selects the embedded edition (Lite —
	// XCom on Postgres, in-process log tailer, ADR 0026). Production sets this
	// explicitly via the Helm chart (external Redis).
	"redis.url": "",
	// Registered so AutomaticEnv binds LEOFLOW_REDIS_CA_FILE (the Helm chart sets
	// it when redis.caConfigMap is configured); without it, verified TLS to a
	// managed rediss:// endpoint silently falls back to system roots only (#725).
	"redis.ca_file":              "",
	"auth.provider":              "jwt",
	"auth.jwt.secret":            "",
	"auth.jwt.token_ttl_seconds": 3600,
	// Ceiling on a transparently-renewed user session's total lifetime (aresta #5).
	// 24h mirrors auth.max_attempt_credential_lifetime: a normal dev day renews
	// silently, and a session must re-authenticate at most once per day. The short
	// token_ttl_seconds is what bounds a stolen token; this only caps total renewed
	// age. A non-positive value disables the ceiling.
	"auth.jwt.max_lifetime_seconds":    86400,
	"auth.login_rate_limit_per_minute": 5,
	// Secret scope-by-declaration and token-liveness policies (ADR 0055). Both
	// ship SAFE by default: permissive delivers the whole tenant vault (today's
	// behavior) and observe logs a would-have-denied without denying. The go-live
	// flips (enforce) are separate operator decisions after an observe period.
	"auth.secret_scoping":       "permissive",
	"auth.secret_liveness_mode": "observe",
	// Agent-token transport (ADR 0055 Fix #3). Ships SAFE by default: envvar keeps
	// the plaintext LEOFLOW_AGENT_TOKEN env var (today's behavior, byte-identical).
	// The projected-SA-token exchange is opt-in ("exchange") and Pro/K8s-only; the
	// flip to it is a separate operator decision after real-cluster e2e.
	"auth.agent_token_transport": "envvar",
	// Hard ceiling on an attempt's total renewed credential lifetime (ADR 0055
	// Fix #4), the activeDeadlineSeconds floor of a task pod with no declared
	// execution_timeout, and the warm-pool per-attempt watchdog. Generous by
	// default so nothing regresses; the short
	// per-attempt TTL is what bounds a stolen token. Parsed as a duration by
	// viper's decode hook.
	"auth.max_attempt_credential_lifetime": "24h",
	// OIDC leaves. Every leaf is registered so viper's AutomaticEnv binds the
	// LEOFLOW_AUTH_OIDC_* env vars, including the slices: viper's default decoder
	// installs mapstructure's StringToSliceHookFunc(","), so a single
	// comma-separated env var becomes a list. TestLoadServerBindsTrustedProxies
	// locks that mechanism, and scopes, allowed_email_domains and
	// break_glass_emails all use it. This comment used to claim the opposite, and
	// a field report quoted it back at us as the explanation for a problem it did
	// not explain (#1144).
	//
	// The two maps (role_mappings, tenant_claims) are deliberately NOT registered
	// here and are tagged mapstructure:"-": their KEYS can contain dots (a Google
	// `hd` domain, a dotted IdP group), which viper's "." delimiter would split
	// into nested maps and fail to decode (#826). LoadServer decodes them straight
	// from the raw YAML instead.
	"auth.oidc.issuer":                "",
	"auth.oidc.client_id":             "",
	"auth.oidc.client_secret":         "",
	"auth.oidc.redirect_url":          "",
	"auth.oidc.scopes":                []string{"openid", "email", "profile"},
	"auth.oidc.groups_claim":          "groups",
	"auth.oidc.default_role":          "",
	"auth.oidc.tenant_claim":          "",
	"auth.oidc.allowed_email_domains": []string{},
	"auth.oidc.break_glass_emails":    []string{},
	"auth.oidc.jit_provisioning":      false,
	// Registered so viper binds LEOFLOW_AUTH_OIDC_AUTO_REDIRECT. Without an entry
	// here the chart renders the variable and the server ignores it: a setting
	// that looks configured and is not. TestDocumentedEnvVarsBind caught this
	// once before, and the rebase onto the session-cookie fix dropped it again.
	"auth.oidc.auto_redirect":      false,
	"auth.oidc.clock_skew_seconds": 60,
	"scheduler.loop_interval_ms":   1000,
	"scheduler.enabled":            true,
	// Default: synchronous dispatch (BufferSize=0). Safe and zero-overhead for
	// Lite. Pro deployments should set buffer_size>=1 + workers>=1 in their
	// values.yaml so K8s API latency does not stretch the tick (#127, ADR 0031).
	"scheduler.dispatch.buffer_size":        0,
	"scheduler.dispatch.workers":            0,
	"executor.http.user_agent":              "leoflow/0.1",
	"executor.task_namespace":               "leoflow",
	"executor.type":                         "kubernetes",
	"executor.agent_path":                   "leoflow-agent",
	"executor.subprocess_workdir":           "",
	"executor.agent_control_plane_addr":     "",
	"executor.agent_tls_ca_configmap":       "",
	"executor.task_service_account":         "",
	"executor.task_secret_name":             "",
	"executor.task_secret_mount_path":       "/etc/leoflow/secrets",
	"executor.collect_settled_run_pods":     false,
	"executor.defaults.staging_access_mode": "ReadWriteMany",

	// Alert egress guard: an alert's URL is tenant data (#424). The []string
	// binds from one comma-separated env var, like server.trusted_proxies.
	"scheduler.alerts.block_private_destinations": false,
	"scheduler.alerts.allowed_cidrs":              []string{},

	// Registered so AutomaticEnv binds LEOFLOW_EXECUTOR_DEFAULTS_STAGING_SIZE /
	// _STORAGE_CLASS (the env-only Helm override path, #743, same class as #725).
	// Empty leaves the L0 default unset, so a staging PVC inherits the cluster's
	// default StorageClass and no pinned size unless the operator configures one.
	"executor.defaults.staging_size":          "",
	"executor.defaults.staging_storage_class": "",
	// Registered so AutomaticEnv binds LEOFLOW_EXECUTOR_DEFAULTS_RESOURCES_CPU /
	// _MEMORY (the env-only Helm override path, #725). Empty leaves the L0 default
	// unset, so a task inherits no platform resource default unless the operator
	// configures one. Scalars (Kubernetes quantities, e.g. "250m"/"256Mi").
	"executor.defaults.resources_cpu":                  "",
	"executor.defaults.resources_memory":               "",
	"executor.defaults.run_tasks_as_non_root":          true,
	"executor.defaults.read_only_task_root_filesystem": false,
	// Warm worker pools (ADR 0058). Ships a byte-for-byte no-op: warm pools OFF =
	// dedicated pod-per-task, today's behavior. The D6/D9 caps carry their
	// documented values so an operator who flips warm_pools_enabled on inherits sane
	// bounds. Durations are written as strings and parsed by viper's decode hook,
	// matching auth.max_attempt_credential_lifetime.
	"execution.warm_pools_enabled":       false,
	"execution.max_attempts_per_worker":  50,
	"execution.max_worker_lifetime":      "1h",
	"execution.min_idle_workers":         0,
	"execution.worker_idle_ttl":          "5m",
	"execution.max_pool_size":            8,
	"execution.max_warm_pods_per_tenant": 100,
	"logs.dir":                           "/var/log/leoflow",
	"logs.backend":                       "disk",
	"logs.sink.bucket":                   "",
	"logs.sink.prefix":                   "",
	"logs.sink.region":                   "",
	"logs.sink.endpoint":                 "",
	"logs.sink.force_path_style":         false,
	"logs.sink.access_key_id":            "",
	"logs.sink.secret_access_key":        "",
	"logs.sink.credentials_file":         "",
	"logs.sink.layout":                   "single",
	"observability.otel.enabled":         false,
	"observability.otel.endpoint":        "localhost:4317",
	"observability.log_level":            "info",
	"observability.log_format":           "json",
	"ui.instance_name":                   "Dexaflow",
	"ui.edition":                         "",
	"ui.workspace":                       "",
	"ui.monaco_dir":                      "",
	"ui.home_link.label":                 "",
	"ui.home_link.url":                   "",
	"ui.theme":                           "",
	"ui.favicon_url":                     "",
	"ui.stylesheet_urls":                 []string{},
	"ui.etag_revalidation":               false,
	// Must appear here even though the zero value is meaningful (the handler
	// falls back to api.DefaultUIAutoRefreshIntervalSeconds when ≤ 0): viper's
	// AutomaticEnv only binds env vars for keys it has seen via SetDefault or
	// SetConfigFile. Without this line LEOFLOW_UI_AUTO_REFRESH_INTERVAL_SECONDS
	// was silently dropped, so `dexaflow lite` (which exports the env var to
	// poll every 1s) was actually running at the 30s production default.
	"ui.auto_refresh_interval_seconds":         0,
	"auth.dev_no_auth":                         false,
	"auth.service_token":                       "",
	"auth.trusted_issuer.name":                 "",
	"auth.trusted_issuer.issuer":               "",
	"auth.trusted_issuer.jwks_url":             "",
	"auth.trusted_issuer.audience":             "",
	"auth.trusted_issuer.tenant_claim":         "tenant_id",
	"auth.trusted_issuer.allowed_tenants":      []string{},
	"auth.trusted_issuer.max_lifetime_seconds": 0,
	"auth.trusted_issuer.allowed_origins":      []string{},
	"auth.external_signin_url":                 "",
	"auth.external_signout_url":                "",
	// Registered so LEOFLOW_AUTH_SESSION_COOKIE_INSECURE binds at all (viper's
	// AutomaticEnv only sees keys it has a default for), and false so the
	// hardened posture is what a config that never mentions it gets.
	"auth.session_cookie_insecure": false,
	"secret_key":                   "",
	"secrets.backend":              "",
	"secrets.backend_kwargs":       "",
	// Retention (the leader-only janitor). Every class ships off: nothing is
	// deleted until an operator sets a window. The pacing values apply once one is.
	"retention.dag_runs_days":      0,
	"retention.audit_log_days":     0,
	"retention.dry_run":            false,
	"retention.interval":           "1h",
	"retention.batch_size":         1000,
	"retention.batch_pause":        "100ms",
	"retention.max_rows_per_cycle": 100000,
	// Warm isolation mode (X3.2, ADR 0058). Registered so AutomaticEnv binds
	// DEXAFLOW_/LEOFLOW_EXECUTION_WARM_READ_ONLY_ROOT_FILESYSTEM; false keeps
	// today's writable warm root.
	"execution.warm_read_only_root_filesystem": false,
}

// LoadServer assembles the server configuration from defaults, the given file,
// LEOFLOW_* environment variables, and flags, in increasing precedence.
func LoadServer(configFile string, flags *pflag.FlagSet) (*ServerConfig, error) {
	v := viper.New()
	for key, val := range serverDefaults {
		v.SetDefault(key, val)
	}
	v.SetEnvPrefix("DEXAFLOW")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	if configFile != "" {
		v.SetConfigFile(configFile)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("reading config file %q: %w", configFile, err)
		}
	}
	if flags != nil {
		if err := v.BindPFlags(flags); err != nil {
			return nil, fmt.Errorf("binding flags: %w", err)
		}
	}
	if err := bindBothPrefixes(v, strings.NewReplacer(".", "_", "-", "_")); err != nil {
		return nil, err
	}

	var c ServerConfig
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("unmarshaling server config: %w", err)
	}
	// The OIDC role_mappings / tenant_claims maps are decoded straight from the
	// raw YAML, bypassing viper's "." key-delimiter flattening that would split a
	// dotted map key (a Google `hd` domain, a dotted IdP group) and fail (#826).
	if configFile != "" {
		if err := decodeDottedOIDCMaps(configFile, &c); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

// decodeDottedOIDCMaps reads the OIDC maps whose KEYS may contain dots directly
// from the raw YAML config, so a dotted key survives verbatim (#826). These
// fields are tagged mapstructure:"-", so viper never touches them; this is their
// only decode path. A file viper already read as YAML re-parses cleanly here.
func decodeDottedOIDCMaps(configFile string, c *ServerConfig) error {
	// The out-of-band decode is YAML (JSON is a YAML subset). A non-YAML config
	// (e.g. TOML) is read by viper but not here — skip rather than hard-fail its
	// boot; those maps then come back empty (OIDC tenant pinning fails closed).
	// Production config is YAML (Helm ConfigMap), so this is a narrow edge.
	switch strings.ToLower(filepath.Ext(configFile)) {
	case ".yaml", ".yml", ".json", "":
	default:
		slog.Warn("dotted OIDC map keys (tenant_claims/role_mappings) are only decoded from YAML/JSON config; skipping for this format",
			"config_file", configFile)
		return nil
	}
	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("reading config file %q for OIDC maps: %w", configFile, err)
	}
	var raw struct {
		Auth struct {
			OIDC struct {
				RoleMappings map[string]string `yaml:"role_mappings"`
				TenantClaims map[string]string `yaml:"tenant_claims"`
			} `yaml:"oidc"`
		} `yaml:"auth"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decoding OIDC maps from config file %q: %w", configFile, err)
	}
	c.Auth.OIDC.RoleMappings = raw.Auth.OIDC.RoleMappings
	c.Auth.OIDC.TenantClaims = raw.Auth.OIDC.TenantClaims
	return nil
}

// Auth providers (auth.provider allowlist). "jwt" is the default credential
// authenticator; "oidc" adds the SSO login flow on top of it (the JWT
// authenticator stays the request-path verifier in both modes).
const (
	// AuthProviderJWT is the default: username/password issues an HS256 token.
	AuthProviderJWT = "jwt"
	// AuthProviderOIDC enables the OIDC/SSO login flow. It is Pro-gated and
	// requires the auth.oidc.* configuration; boot fails closed otherwise.
	AuthProviderOIDC = "oidc"
)

// Secret policy allowlists (ADR 0055). auth.secret_scoping and
// auth.secret_liveness_mode are validated against these; an unknown value fails
// boot closed. Empty is valid — serverDefaults sets the safe default for each.
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

// Validate reports configuration errors that must abort startup.
func (c *ServerConfig) Validate() error {
	if err := c.validateRole(); err != nil {
		return err
	}
	if err := c.validateProvider(); err != nil {
		return err
	}
	if err := c.validateLogs(); err != nil {
		return err
	}
	if err := c.validateSecretPolicies(); err != nil {
		return err
	}
	if err := c.validateExecution(); err != nil {
		return err
	}
	if err := c.Retention.Validate(); err != nil {
		return err
	}
	if err := c.validatePlatformIntegration(); err != nil {
		return err
	}
	if _, err := egress.NewPolicy(c.Scheduler.Alerts.AllowedCIDRs); err != nil {
		return fmt.Errorf("scheduler.alerts.allowed_cidrs: %w", err)
	}
	// Both providers mint the app's own HS256 _token (oidc mints it after the IdP
	// verify), so the JWT secret is required for either.
	if (c.Auth.Provider == AuthProviderJWT || c.Auth.Provider == AuthProviderOIDC) && c.Auth.JWT.Secret == "" {
		return errors.New("auth.jwt.secret is required (set LEOFLOW_AUTH_JWT_SECRET)")
	}
	// auth.dev_no_auth disables authentication entirely; permit it only when the
	// HTTP API binds to loopback, so a misconfigured (or accidental) dev bypass can
	// never expose an unauthenticated API off-host. Fail closed otherwise.
	if c.Auth.DevNoAuth && !isLoopbackListenAddr(c.Server.HTTPAddr) {
		return fmt.Errorf("auth.dev_no_auth disables authentication and is only permitted on a loopback http_addr (got %q); never enable it in production", c.Server.HTTPAddr)
	}
	return nil
}

// isLoopbackListenAddr reports whether a listen address binds only to loopback,
// so a no-auth dev server is unreachable off-host.
func isLoopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// validateLogs rejects an unknown logs.backend and requires a bucket when an
// object backend is selected, so a misconfigured object sink fails closed at
// boot instead of losing every task log to a nonexistent bucket. Empty and
// "disk" are always valid — the on-disk default is unaffected.
func (c *ServerConfig) validateLogs() error {
	switch c.Logs.Backend {
	case "", "disk":
		return nil
	case "s3", "gcs":
		if c.Logs.Sink.Bucket == "" {
			return fmt.Errorf(`logs.sink.bucket is required when logs.backend is %q (set LEOFLOW_LOGS_SINK_BUCKET)`, c.Logs.Backend)
		}
		switch c.Logs.Sink.Layout {
		case "", "single", "segmented":
			return nil
		default:
			return fmt.Errorf(`unknown logs.sink.layout %q (want "single" or "segmented")`, c.Logs.Sink.Layout)
		}
	default:
		return fmt.Errorf(`unknown logs.backend %q (want "disk", "s3" or "gcs")`, c.Logs.Backend)
	}
}

// validateSecretPolicies rejects an unknown auth.secret_scoping or
// auth.secret_liveness_mode, failing closed at boot rather than letting main.go
// wire an unrecognized policy (ADR 0055). Empty is valid: serverDefaults sets
// the safe default (permissive / observe) for each.
func (c *ServerConfig) validateSecretPolicies() error {
	switch c.Auth.SecretScoping {
	case "", SecretScopingPermissive, SecretScopingEnforce, SecretScopingOff:
	default:
		return fmt.Errorf("invalid auth.secret_scoping %q: must be %q, %q or %q",
			c.Auth.SecretScoping, SecretScopingPermissive, SecretScopingEnforce, SecretScopingOff)
	}
	switch c.Auth.SecretLivenessMode {
	case "", SecretLivenessObserve, SecretLivenessEnforce:
	default:
		return fmt.Errorf("invalid auth.secret_liveness_mode %q: must be %q or %q",
			c.Auth.SecretLivenessMode, SecretLivenessObserve, SecretLivenessEnforce)
	}
	switch c.Auth.AgentTokenTransport {
	case "", AgentTokenTransportEnvVar, AgentTokenTransportExchange:
	default:
		return fmt.Errorf("invalid auth.agent_token_transport %q: must be %q or %q",
			c.Auth.AgentTokenTransport, AgentTokenTransportEnvVar, AgentTokenTransportExchange)
	}
	return nil
}

// validateExecution enforces the warm-pool boot gate (ADR 0058 N1a), fail-closed.
// The whole block is gated on WarmPoolsEnabled: with warm pools OFF (the default)
// none of these fields is validated, so an operator who never turns warm pools on
// is unaffected. With warm pools ON it rejects, rather than silently correcting:
//
//	(a) HIGH #1 — the security coupling. A warm pod reuses one credential across
//	    attempts; without token-exchange transport and liveness enforcement a
//	    superseded attempt's token would still resolve secrets. So warm pools
//	    require auth.agent_token_transport=exchange AND auth.secret_liveness_mode=
//	    enforce (ADR 0058 D2/HIGH-1). This half stays fail-closed: there is no safe
//	    degraded mode — the degraded mode IS the vulnerability.
//	(b) sanity — a zero/negative attempts cap, worker lifetime, or idle TTL would
//	    recycle instantly or never; each must be positive.
//
// There is deliberately NO max_worker_lifetime >= max_attempt_credential_lifetime
// ordering guard. That invariant was dropped (ADR 0058 D10 review): recycle is a
// graceful DRAIN, not a hard mid-attempt kill, so a worker lifetime shorter than
// the credential ceiling is harmless — the worker finishes its in-flight attempt
// (on its own renewed credential) before recycling. The guard defended a
// force-recycle that drain forbids, and with the shipped defaults (1h worker vs
// 24h ceiling) it also blocked a naive enable and pushed operators toward less-safe
// config. The real "credential lapses mid-attempt" bound is per-ATTEMPT
// (execution_timeout / the warm-worker watchdog <= the ceiling), enforced on the
// execution path, not here.
func (c *ServerConfig) validateExecution() error {
	if !c.Execution.WarmPoolsEnabled {
		return nil
	}
	if c.Auth.AgentTokenTransport != AgentTokenTransportExchange || c.Auth.SecretLivenessMode != SecretLivenessEnforce {
		return fmt.Errorf("execution.warm_pools_enabled requires auth.agent_token_transport=%q and auth.secret_liveness_mode=%q (a warm pod reuses one credential across attempts; without token-exchange + liveness enforcement a superseded attempt's token would still resolve secrets — ADR 0058 D2/HIGH-1)",
			AgentTokenTransportExchange, SecretLivenessEnforce)
	}
	if c.Execution.MaxAttemptsPerWorker < 1 {
		return fmt.Errorf("execution.max_attempts_per_worker must be >= 1 when execution.warm_pools_enabled (got %d): a zero cap would recycle a warm worker instantly (ADR 0058 D9)", c.Execution.MaxAttemptsPerWorker)
	}
	if c.Execution.MaxWorkerLifetime <= 0 {
		return fmt.Errorf("execution.max_worker_lifetime must be > 0 when execution.warm_pools_enabled (got %v): a non-positive lifetime would recycle a warm worker instantly (ADR 0058 D9)", c.Execution.MaxWorkerLifetime)
	}
	if c.Execution.WorkerIdleTTL <= 0 {
		return fmt.Errorf("execution.worker_idle_ttl must be > 0 when execution.warm_pools_enabled (got %v): a non-positive TTL would recycle an idle warm worker instantly (ADR 0058 D6)", c.Execution.WorkerIdleTTL)
	}
	if c.Execution.MaxPoolSize < 1 {
		return fmt.Errorf("execution.max_pool_size must be >= 1 when execution.warm_pools_enabled (got %d): a zero cap would forbid every warm worker (ADR 0058)", c.Execution.MaxPoolSize)
	}
	if c.Execution.MaxWarmPodsPerTenant < 1 {
		return fmt.Errorf("execution.max_warm_pods_per_tenant must be >= 1 when execution.warm_pools_enabled (got %d): a zero aggregate tenant cap would forbid every warm worker (M4)", c.Execution.MaxWarmPodsPerTenant)
	}
	return nil
}

// validateProvider rejects an unknown auth.provider, failing closed at boot
// instead of letting main.go build an authenticator regardless of what was
// configured. Empty is valid: serverDefaults sets auth.provider to "jwt", so an
// unset provider in an existing config keeps defaulting to JWT and is
// unaffected. "oidc" is valid only when its Pro-gated prerequisites are met
// (validateOIDC); anything else is a loud boot failure.
func (c *ServerConfig) validateProvider() error {
	switch c.Auth.Provider {
	case "", AuthProviderJWT:
		return nil
	case AuthProviderOIDC:
		return c.validateOIDC()
	default:
		return fmt.Errorf("invalid auth.provider %q: must be %q or %q (or empty = %q)",
			c.Auth.Provider, AuthProviderJWT, AuthProviderOIDC, AuthProviderJWT)
	}
}

// validateOIDC enforces the fail-closed prerequisites for auth.provider: oidc
// (D7): the Pro edition, and the fields the login flow cannot run without
// (issuer, client_id, redirect_url, and the tenant pin). The issuer must be
// https so discovery and JWKS are fetched over TLS (keyless verify, ADR 0035).
// A misconfigured OIDC deployment fails boot with an actionable message rather
// than starting a login flow that cannot complete.
//
// The tenant pin belongs in this set because it decides whether ANY login can
// succeed: Verify resolves a tenant on every login and fails closed when
// tenant_claim is unset or the claim value is not in tenant_claims
// (internal/oidc/verify.go). That rejection is correct, but it reaches the user
// as a generic 403 and its cause reaches only the audit log, which in an
// SSO-only deployment nobody can log in to read (#1143).
//
// Every missing key is reported in one error on purpose. Each boot failure on a
// Kubernetes deployment costs a values edit, an upgrade and a rollout to learn
// the next one, so reporting them one at a time turns first-time SSO setup into
// a chain of CrashLoopBackOffs.
func (c *ServerConfig) validateOIDC() error {
	if c.UI.Edition != "pro" {
		return errors.New("auth.provider: oidc requires the Pro edition (set ui.edition: pro)")
	}
	var missing []string
	if c.Auth.OIDC.Issuer == "" {
		missing = append(missing, "auth.oidc.issuer")
	}
	if c.Auth.OIDC.ClientID == "" {
		missing = append(missing, "auth.oidc.client_id")
	}
	if c.Auth.OIDC.RedirectURL == "" {
		missing = append(missing, "auth.oidc.redirect_url")
	}
	if c.Auth.OIDC.TenantClaim == "" {
		missing = append(missing, "auth.oidc.tenant_claim")
	}
	if len(c.Auth.OIDC.TenantClaims) == 0 {
		missing = append(missing, "auth.oidc.tenant_claims")
	}
	if len(missing) > 0 {
		return fmt.Errorf("auth.provider: oidc requires %s to be set%s", strings.Join(missing, ", "), tenantPinHint(c))
	}
	if err := validateOIDCNames(c.Auth.OIDC); err != nil {
		return err
	}
	if !strings.HasPrefix(c.Auth.OIDC.Issuer, "https://") {
		return fmt.Errorf("auth.oidc.issuer must be an https:// URL (got %q)", c.Auth.OIDC.Issuer)
	}
	return validateRedirectURL(c.Auth.OIDC.RedirectURL)
}

// validateOIDCNames rejects a name that is present but blank.
//
// "corp.example:" with nothing after it is valid YAML binding to the empty
// string, and the checks above only ask whether the map is non-empty. A blank
// tenant name resolves a login to a tenant that cannot exist. A blank role name
// is copied straight into the resolved role set by oidc.MapRoles and then fails
// the existence check. Both deny the login behind the same generic answer as
// every other failure, and the operator sees a key they did fill in.
//
// This fails boot where its neighbors only warn, and the difference is that it
// cannot be a transient. The tenant and role existence checks ask a live
// database, where "absent" and "could not ask" are the same answer during a
// blip, so a hard gate there turns a lagging replica into a restart loop. This
// is a string in a file: a blank name is never correct, no deployment can be
// working with one, and the answer is identical on every boot.
//
// Keys are checked too. A claim value or an IdP group that is blank can never be
// what an IdP sends, so the entry is unreachable rather than wrong, which is the
// same defect wearing the other hat.
func validateOIDCNames(o OIDCSection) error {
	for _, m := range []struct {
		key     string
		entries map[string]string
		keyWhat string
		valWhat string
	}{
		{"auth.oidc.tenant_claims", o.TenantClaims, "claim value", "tenant name"},
		{"auth.oidc.role_mappings", o.RoleMappings, "IdP group", "role name"},
	} {
		for k, v := range m.entries {
			if strings.TrimSpace(k) == "" {
				return fmt.Errorf("%s has an entry whose %s is blank; nothing an IdP sends can match it, so the entry is unreachable", m.key, m.keyWhat)
			}
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("%s maps %q to a blank %s; every login it governs is denied, because no %s resolves. Give it a value, or remove the entry",
					m.key, k, m.valWhat, m.valWhat)
			}
		}
	}
	// An ABSENT default_role is the documented strict posture. A blank one is a
	// typo that reads as set and grants nothing.
	if o.DefaultRole != "" && strings.TrimSpace(o.DefaultRole) == "" {
		return errors.New("auth.oidc.default_role is set to whitespace; leave it unset for strict default-deny, or name a role that exists")
	}
	return nil
}

// tenantPinHint explains the tenant pin when one of its two keys is missing, and
// returns "" otherwise. It is the whole remedy in one line, because this error is
// printed before the logger exists and is the only thing a crash-looping
// container leaves behind: what breaks without the pin, what to set, the single
// route that can carry the map (env vars cannot), and how to restore password
// login while the SSO configuration is being worked out.
func tenantPinHint(c *ServerConfig) string {
	if c.Auth.OIDC.TenantClaim != "" && len(c.Auth.OIDC.TenantClaims) > 0 {
		return ""
	}
	return ". The tenant pin decides whether any login can succeed: a claim value that is absent or not mapped is rejected with 403 (audited as tenant_not_allowed) and never falls back to the default tenant, so without it every SSO login fails. " +
		"The pin is two settings: auth.oidc.tenant_claim names the claim carrying the tenant (tid on Entra, hd on Google Workspace), and auth.oidc.tenant_claims maps each value of it you accept to a Dexaflow tenant. " +
		"auth.oidc.tenant_claims is a map, so it loads ONLY from the YAML config file named by LEOFLOW_CONFIG; no LEOFLOW_AUTH_OIDC_* environment variable can carry it. " +
		"To keep serving password logins while SSO is configured, set auth.provider: jwt"
}

// validatePlatformIntegration checks the settings an operator uses to serve
// Dexaflow from inside a larger platform: external sign-in and sign-out (#1288),
// the trusted issuer (#1284), the service API token (#1283), the home link
// (#1290) and branding (#1289).
func (c *ServerConfig) validatePlatformIntegration() error {
	if err := validateExternalAuthURL("auth.external_signin_url", c.Auth.ExternalSignInURL); err != nil {
		return err
	}
	if err := validateExternalAuthURL("auth.external_signout_url", c.Auth.ExternalSignOutURL); err != nil {
		return err
	}
	if err := validateTrustedIssuer(c.Auth.TrustedIssuer); err != nil {
		return err
	}
	if t := c.Auth.ServiceToken; t != "" && len(strings.TrimSpace(t)) < 32 {
		return errors.New("auth.service_token must be at least 32 characters (generate one with `openssl rand -base64 48`)")
	}
	if err := validateHomeLink(c.UI.HomeLink); err != nil {
		return err
	}
	return validateBranding(c.UI)
}

// validateExternalAuthURL checks one of the #1288 settings: empty, or an
// absolute http(s) URL with a host. A relative URL would send the browser back
// into Dexaflow, where the sign-in route redirects again: a loop.
func validateExternalAuthURL(key, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s must be a valid URL (got %q): %w", key, raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s must be an absolute http:// or https:// URL (got %q)", key, raw)
	}
	return nil
}

// issuerNamePattern is the shape of a trusted issuer's name: it becomes part of
// the provider key stored on every linked user.
var issuerNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// validateTrustedIssuer checks auth.trusted_issuer (#1284) when any key is set.
// Every missing or malformed key is reported in one error, so first-time setup
// is one edit rather than a chain of restarts.
func validateTrustedIssuer(s TrustedIssuerSection) error {
	if !s.Enabled() && s.Name == "" && s.JWKSURL == "" && s.Audience == "" && len(s.AllowedTenants) == 0 && len(s.AllowedOrigins) == 0 {
		return nil
	}
	checks := []struct {
		ok      bool
		problem string
	}{
		{issuerNamePattern.MatchString(s.Name), "auth.trusted_issuer.name (1-40 lowercase letters, digits or '-')"},
		{s.Issuer != "", "auth.trusted_issuer.issuer"},
		{jwksURLAllowed(s.JWKSURL), "auth.trusted_issuer.jwks_url (https, or http on a loopback host)"},
		{s.Audience != "", "auth.trusted_issuer.audience"},
		{s.TenantClaim != "", "auth.trusted_issuer.tenant_claim"},
		{len(s.AllowedTenants) > 0, `auth.trusted_issuer.allowed_tenants (tenant names, or "*" for all)`},
		{s.MaxLifetimeSeconds >= 0 && s.MaxLifetimeSeconds <= 600, "auth.trusted_issuer.max_lifetime_seconds (0 to 600)"},
		{originsValid(s.AllowedOrigins), "auth.trusted_issuer.allowed_origins (one or more scheme://host[:port], no path)"},
	}
	var problems []string
	for _, c := range checks {
		if !c.ok {
			problems = append(problems, c.problem)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("auth.trusted_issuer is incomplete or invalid; set: %s", strings.Join(problems, ", "))
	}
	return nil
}

// validateHomeLink checks ui.home_link (#1290): both fields or neither, and an
// absolute http(s) URL with a host, so a typo fails boot instead of rendering a
// dead link and no other scheme (javascript:, data:) can reach the page.
func validateHomeLink(l HomeLinkSection) error {
	if l == (HomeLinkSection{}) {
		return nil
	}
	if l.URL == "" {
		return errors.New("ui.home_link.url is required when ui.home_link.label is set")
	}
	if strings.TrimSpace(l.Label) == "" {
		return errors.New("ui.home_link.label is required when ui.home_link.url is set")
	}
	u, err := url.Parse(l.URL)
	if err != nil {
		return fmt.Errorf("ui.home_link.url must be a valid URL (got %q): %w", l.URL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("ui.home_link.url must start with http:// or https:// (got %q)", l.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("ui.home_link.url must include a host (got %q)", l.URL)
	}
	return nil
}

// themeKeys are the top-level keys of a theme the Airflow 3.2.1 UI reads.
var themeKeys = map[string]bool{"tokens": true, "globalCss": true, "icon": true, "icon_dark_mode": true}

// validateBranding checks the #1289 settings: ui.theme is a JSON object with
// only the keys the UI reads (an unknown key is almost always a typo the UI
// would ignore in silence), and every URL, the theme's icons included, is
// http(s) or root-relative.
func validateBranding(u UISection) error {
	if u.Theme != "" {
		var theme map[string]json.RawMessage
		if err := json.Unmarshal([]byte(u.Theme), &theme); err != nil {
			return fmt.Errorf("ui.theme must be a JSON object: %w", err)
		}
		for key, raw := range theme {
			if !themeKeys[key] {
				return fmt.Errorf("ui.theme has unknown key %q (the UI reads tokens, globalCss, icon, icon_dark_mode)", key)
			}
			if key != "icon" && key != "icon_dark_mode" {
				continue
			}
			var icon string
			if err := json.Unmarshal(raw, &icon); err != nil {
				return fmt.Errorf("ui.theme icon %q must be a string URL", key)
			}
			if err := checkAssetURL(icon); err != nil {
				return fmt.Errorf("ui.theme icon %q: %w", key, err)
			}
		}
	}
	if u.FaviconURL != "" {
		if err := checkAssetURL(u.FaviconURL); err != nil {
			return fmt.Errorf("ui.favicon_url: %w", err)
		}
	}
	for _, sheet := range u.StylesheetURLs {
		if err := checkAssetURL(sheet); err != nil {
			return fmt.Errorf("ui.stylesheet_urls: %w", err)
		}
	}
	return nil
}

// checkAssetURL accepts an absolute http(s) URL with a host or a root-relative
// path ("/brand/logo.svg"). It refuses every other scheme and the
// protocol-relative "//host" form, which would load from a host nobody named.
func checkAssetURL(raw string) error {
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q is not a valid URL: %w", raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q must be an http(s) URL or a root-relative path", raw)
	}
	return nil
}

// originsValid reports whether list is non-empty and every entry is a bare
// http(s) origin, the exact form a browser sends in the Origin header.
func originsValid(list []string) bool {
	if len(list) == 0 {
		return false
	}
	for _, o := range list {
		u, err := url.Parse(o)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return false
		}
	}
	return true
}

// jwksURLAllowed accepts an https URL with a host, or http on a loopback host
// for local development, like the OIDC redirect URL.
func jwksURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && isLoopbackHost(u.Hostname()))
}

// validateRedirectURL requires the OIDC callback URL to use https so the
// authorization code is never returned over plaintext, with an http exception
// for loopback hosts (localhost, 127.0.0.1, [::1]) to keep local dev workable.
func validateRedirectURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("auth.oidc.redirect_url must be a valid URL (got %q): %w", raw, err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("auth.oidc.redirect_url must be an https:// URL (got %q); http:// is only allowed for loopback hosts (localhost, 127.0.0.1, [::1])", raw)
	default:
		return fmt.Errorf("auth.oidc.redirect_url must be an https:// URL (got %q)", raw)
	}
}

// isLoopbackHost reports whether host is a loopback name or address. url.Hostname
// strips the brackets from an IPv6 literal, so [::1] arrives as "::1".
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// validateRole rejects an unknown server.role (ADR 0049). Empty is valid (defaults
// to "all"). A typo like "worker" is a loud boot failure, not a silent monolith.
func (c *ServerConfig) validateRole() error {
	switch c.Server.Role {
	case "", RoleAll, RoleAPI, RoleScheduler:
		return nil
	default:
		return fmt.Errorf("invalid server.role %q: must be one of %q, %q, %q (or empty = %q)",
			c.Server.Role, RoleAll, RoleAPI, RoleScheduler, RoleAll)
	}
}
