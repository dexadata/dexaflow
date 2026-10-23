package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
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

// HTTPTLSEnabled reports whether the API listener serves HTTPS, which needs
// both server.http_tls_cert and server.http_tls_key.
func (s ServerSection) HTTPTLSEnabled() bool {
	return s.HTTPTLSCert != "" && s.HTTPTLSKey != ""
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
}

// RedisSection configures the Redis connection.
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
}

// ObservabilitySection configures logging, metrics, and tracing.
type ObservabilitySection struct {
	OTel      OTelSection    `mapstructure:"otel"`
	LogLevel  string         `mapstructure:"log_level"`
	LogFormat string         `mapstructure:"log_format"`
	Metrics   MetricsSection `mapstructure:"metrics"`
}

// MetricsSection configures the Prometheus scrape.
type MetricsSection struct {
	// DropLegacyNames stops publishing every dexaflow_* family a second time
	// under its pre-rename leoflow_* name. Off by default (ADR 0062 gate), so
	// dashboards and alerts written against the old names keep working.
	DropLegacyNames bool `mapstructure:"drop_legacy_names"`
}

// OTelSection configures OpenTelemetry export.
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
	if err := refuseAdvertisedSourceModeImage(v); err != nil {
		return nil, err
	}
	readInsecureSecretsAsBefore(v)

	var c ServerConfig
	if err := v.Unmarshal(&c); err != nil {
		return nil, fmt.Errorf("unmarshaling server config: %w", err)
	}
	normalizeRequireDigest(v, &c)
	if err := decodeCIOIDCIssuers(v, configFile, &c); err != nil {
		return nil, err
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
	// (e.g. TOML) is read by viper but not here, skip rather than hard-fail its
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

// validateNonNegative refuses settings where a negative value would silently
// read as "off" instead of failing boot.
func (c *ServerConfig) validateNonNegative() error {
	if c.Server.MaxPageLimit < 0 {
		return fmt.Errorf("server.max_page_limit must not be negative (got %d); 0 leaves list pages uncapped", c.Server.MaxPageLimit)
	}
	if c.Scheduler.PoolStarvationThreshold < 0 {
		return fmt.Errorf("scheduler.pool_starvation_threshold must not be negative (got %s); 0 disables it", c.Scheduler.PoolStarvationThreshold)
	}
	if c.Scheduler.Dispatch.TickBudget < 0 {
		return fmt.Errorf("scheduler.dispatch.tick_budget must not be negative (got %s); 0 disables it", c.Scheduler.Dispatch.TickBudget)
	}
	if c.Scheduler.Dispatch.MaxPerTenantPerTick < 0 {
		return fmt.Errorf("scheduler.dispatch.max_per_tenant_per_tick must not be negative (got %d); 0 means no cap", c.Scheduler.Dispatch.MaxPerTenantPerTick)
	}
	return nil
}

// Validate reports configuration errors that must abort startup.
func (c *ServerConfig) Validate() error {
	if err := c.validateListeners(); err != nil {
		return err
	}
	if err := c.validateProvider(); err != nil {
		return err
	}
	if err := c.validateLogs(); err != nil {
		return err
	}
	if err := c.validateNonNegative(); err != nil {
		return err
	}
	for _, check := range []func() error{
		c.validateSecretPolicies,
		c.validateExecution,
		c.Retention.Validate,
		c.validateExecutorPolicies,
		c.validatePlatformIntegration,
		c.validateDeployDefaults,
		c.validateRequireDigest,
		c.validateCIOIDC,
		c.validateMigrateOnBoot,
		c.validateBootstrap,
	} {
		if err := check(); err != nil {
			return err
		}
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

// validateMigrateOnBoot refuses self-migration on the api role (ADR 0090
// decision 4): that role runs on a restricted identity (ADR 0049) and never
// migrates, whatever its DSN can do. In split mode the chart renders the
// setting on the scheduler only.
func (c *ServerConfig) validateMigrateOnBoot() error {
	if c.Database.MigrateOnBoot && c.Server.EffectiveRole() == RoleAPI {
		return errors.New("database.migrate_on_boot is set on the api role, which never migrates: set it on the all or scheduler role only (the chart's migrations.onBoot does)")
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

// validateListeners checks the server section: the role and the API
// listener's TLS pair.
func (c *ServerConfig) validateListeners() error {
	if err := c.validateRole(); err != nil {
		return err
	}
	return c.validateHTTPTLS()
}

// validateHTTPTLS refuses half of the API listener's TLS pair (#638): a cert
// without its key, or the reverse, would otherwise boot on plain HTTP while the
// operator believes the hop is encrypted.
func (c *ServerConfig) validateHTTPTLS() error {
	if (c.Server.HTTPTLSCert == "") != (c.Server.HTTPTLSKey == "") {
		return errors.New("server.http_tls_cert and server.http_tls_key must be set together " +
			"(DEXAFLOW_SERVER_HTTP_TLS_CERT and DEXAFLOW_SERVER_HTTP_TLS_KEY): only one is set, " +
			"so the API listener would silently stay on plain HTTP")
	}
	return nil
}

// validateRole rejects an unknown server.role (ADR 0049). Empty is valid (defaults
// to "all"). A typo like "worker" is a loud boot failure, not a silent monolith.
func (c *ServerConfig) validateRole() error {
	// The listener timeouts are validated with the role: both shape how this
	// process serves HTTP. A negative duration is a typo, not "off".
	if c.Server.ReadTimeout < 0 || c.Server.IdleTimeout < 0 {
		return fmt.Errorf("server.read_timeout (%v) and server.idle_timeout (%v) must not be negative; 0 disables them",
			c.Server.ReadTimeout, c.Server.IdleTimeout)
	}
	switch c.Server.Role {
	case "", RoleAll, RoleAPI, RoleScheduler:
		return nil
	default:
		return fmt.Errorf("invalid server.role %q: must be one of %q, %q, %q (or empty = %q)",
			c.Server.Role, RoleAll, RoleAPI, RoleScheduler, RoleAll)
	}
}
