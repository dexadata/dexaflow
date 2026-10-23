package config

import (
	"github.com/dexadata/dexaflow/internal/domain"
)

// serverDefaults lists every leaf key with its default so that AutomaticEnv and
// Unmarshal resolve nested keys correctly.
var serverDefaults = map[string]any{
	// Empty defaults to RoleAll (EffectiveRole). This entry must exist so viper's
	// AutomaticEnv binds LEOFLOW_SERVER_ROLE, see the ui.auto_refresh note below.
	"server.role":                 "",
	"server.http_addr":            "0.0.0.0:8080",
	"server.metrics_addr":         "0.0.0.0:9090",
	"server.grpc_addr":            "0.0.0.0:9091",
	"server.grpc_tls_cert":        "",
	"server.grpc_tls_key":         "",
	"server.http_tls_cert":        "", // empty: the API listener stays on plain HTTP (#638)
	"server.http_tls_key":         "",
	"server.cors.allowed_origins": []string{"http://localhost:8080"},
	// Registered so viper's AutomaticEnv binds LEOFLOW_SERVER_TRUSTED_PROXIES. It
	// is a []string, but viper's default decode hook splits a single
	// comma-separated env var into a list, so the env-only Helm override path
	// works without a config file, this is what the chart renders (#725). Empty
	// (the default) trusts no proxy.
	"server.trusted_proxies": []string{},
	// Off by default: pools stay writable through the tenant-facing API under
	// write:pool. Registered so AutomaticEnv binds the Helm-rendered env var.
	"server.pools_read_only":  false,
	"database.url":            "postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable",
	"database.max_open_conns": 25,
	"database.max_idle_conns": 5,
	// Pool tuning, all off by default (0) so an install that sets none of them
	// keeps one shared pool, no statement timeout and no lifetime jitter.
	// Registered so the DEXAFLOW_/LEOFLOW_DATABASE_* variables bind.
	"database.scheduler_max_conns":         0,
	"database.statement_timeout_ms":        0,
	"database.conn_max_lifetime_jitter_ms": 0,
	// Empty by default: no installation id is compared (Lite, and installs
	// whose migration step does not record one). The chart sets it.
	"database.installation_id": "",
	// Off by default: migrations run in the chart's hook Job (ADR 0090
	// decision 4 makes self-migration opt-in).
	"database.migrate_on_boot": false,
	// Empty by default: no Redis configured selects the embedded edition (Lite,
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
	"auth.oidc.auto_redirect":             false,
	"auth.oidc.clock_skew_seconds":        60,
	"scheduler.loop_interval_ms":          1000,
	"scheduler.enabled":                   true,
	"scheduler.pool_starvation_threshold": "60s",
	// Off by default (ADR 0062 feature gate): promotion keeps its one-step-per-
	// tick cadence until an operator opts in.
	"scheduler.eager_promotion": false,
	// Default: synchronous dispatch (BufferSize=0). Safe and zero-overhead for
	// Lite. Pro deployments should set buffer_size>=1 + workers>=1 in their
	// values.yaml so K8s API latency does not stretch the tick (#127, ADR 0031).
	"scheduler.dispatch.buffer_size": 0,
	"scheduler.dispatch.workers":     0,
	// The tick dispatch budget is on by default (#1596): with synchronous
	// dispatch and client QPS 5, one wide fan-out otherwise stalls every run
	// of every tenant, cron creation included.
	"scheduler.dispatch.tick_budget":             "5s",
	"scheduler.dispatch.max_per_tenant_per_tick": 0,
	"executor.http.user_agent":                   "leoflow/0.1",
	"executor.task_namespace":                    "leoflow",
	"executor.type":                              "kubernetes",
	"executor.agent_path":                        "leoflow-agent",
	"executor.subprocess_workdir":                "",
	"executor.agent_control_plane_addr":          "",
	"executor.agent_tls_ca_configmap":            "",
	"executor.task_service_account":              "",
	"executor.task_secret_name":                  "",
	"executor.task_secret_mount_path":            "/etc/leoflow/secrets",
	"executor.collect_settled_run_pods":          false,
	"executor.defaults.staging_access_mode":      "ReadWriteMany",
	// Execution allowlist (issue #629): allow every field and any priority
	// class, so an install that does not set it is unchanged. Each list binds
	// from one comma-separated env var.
	"executor.defaults.execution_allowlist.fields":           []string{domain.ExecutionPolicyAllFields},
	"executor.defaults.execution_allowlist.priority_classes": []string{},
	"executor.defaults.execution_allowlist.service_accounts": []string{},
	"executor.defaults.execution_allowlist.images":           []string{},
	// client-go's own defaults on one shared client, so an unconfigured install
	// keeps its effective apiserver budget.
	"executor.kube_client.qps":               5.0,
	"executor.kube_client.burst":             10,
	"executor.kube_client.maintenance_qps":   0.0,
	"executor.kube_client.maintenance_burst": 0,

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
	"executor.defaults.resources_cpu":    "",
	"executor.defaults.resources_memory": "",
	// Registered so AutomaticEnv binds LEOFLOW_EXECUTOR_DEFAULTS_LIMITS_CPU /
	// _MEMORY (#537). Empty sets no default task limit.
	"executor.defaults.limits_cpu":    "",
	"executor.defaults.limits_memory": "",
	// Registered so AutomaticEnv binds LEOFLOW_EXECUTOR_UNIT_CPU / _MEMORY. Empty
	// (both) means no resource unit (ADR 0066).
	"executor.unit.cpu":                                "",
	"executor.unit.memory":                             "",
	"executor.unit.enforce":                            domain.UnitEnforceRefuse,
	"executor.unit.max_size":                           domain.DefaultUnitMaxSize,
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
	"logs.persistence_enabled":           true,
	"logs.tail.publish":                  LogTailPublishAlways,
	"logs.tail.replay_budget_bytes":      int64(0), // gate (ADR 0062): no budget, as before (#1621)
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
	"ui.auto_refresh_interval_seconds":                0,
	"auth.dev_no_auth":                                false,
	"auth.service_token":                              "",
	"auth.trusted_issuer.name":                        "",
	"auth.trusted_issuer.issuer":                      "",
	"auth.trusted_issuer.jwks_url":                    "",
	"auth.trusted_issuer.audience":                    "",
	"auth.trusted_issuer.tenant_claim":                "tenant_id",
	"auth.trusted_issuer.allowed_tenants":             []string{},
	"auth.trusted_issuer.max_lifetime_seconds":        0,
	"auth.trusted_issuer.allowed_origins":             []string{},
	"auth.trusted_issuer.bearer_audiences":            []string{},
	"auth.trusted_issuer.bearer_max_lifetime_seconds": 0,
	"auth.trusted_issuer.host_pattern":                "",
	"auth.trusted_issuer.deploy_audience":             "",
	"auth.ci_oidc.issuers":                            "",
	"auth.ci_oidc.session_ttl_seconds":                3600,
	"auth.api_tokens.disabled":                        false,
	"auth.external_signin_url":                        "",
	"auth.external_signout_url":                       "",
	// Registered so LEOFLOW_AUTH_SESSION_COOKIE_INSECURE binds at all (viper's
	// AutomaticEnv only sees keys it has a default for), and false so the
	// hardened posture is what a config that never mentions it gets.
	"auth.session_cookie_insecure": false,
	// _token is the name every earlier release used (#1405).
	"auth.session_cookie_name":     DefaultSessionCookieName,
	"secret_key":                   "",
	"secret_key_reencrypt_on_boot": true,
	"secret_key_migration_lock":    false,
	// Registered so DEXAFLOW_/LEOFLOW_VARIABLE_ENCRYPTION binds; off keeps the
	// plaintext Variables every earlier release wrote and reads (#507).
	"variable_encryption": VariableEncryptionOff,
	// Airflow's default (#1705): new DAGs start paused.
	"dags_are_paused_at_creation": true,
	"secrets.backend":             "",
	"secrets.backend_kwargs":      "",
	// Gate (ADR 0062): false sends every API body uncompressed, as before.
	"server.gzip_responses": false,
	// Gate (ADR 0062): 0 leaves list limits uncapped, as before.
	"server.max_page_limit": 0,
	// Gate (ADR 0062): 0 leaves request bodies unlimited, as before (#1613).
	"server.max_request_body_bytes": int64(0),
	"server.max_upload_body_bytes":  int64(0),
	// Gates (ADR 0062): 0 keeps the listeners without read or idle timeout.
	"server.read_timeout": "0s",
	"server.idle_timeout": "0s",
	// The /readyz budget every earlier release hardcoded (#1050).
	"server.probe_budget": "2s",
	// Trace sampling gates (ADR 0062): the defaults trace every request.
	"observability.otel.sample_ratio":     1.0,
	"observability.otel.skip_probe_spans": false,
	// Gate (ADR 0062): false keeps the leoflow_ twin of every metric family.
	"observability.metrics.drop_legacy_names": false,
	// Warm isolation mode (X3.2, ADR 0058). Registered so AutomaticEnv binds
	// DEXAFLOW_/LEOFLOW_EXECUTION_WARM_READ_ONLY_ROOT_FILESYSTEM; false keeps
	// today's writable warm root.
	"execution.warm_read_only_root_filesystem":  false,
	"execution.warm_require_attempt_home_sweep": false,
	"execution.source_mode.enabled":             false,
	"execution.source_mode.image":               "",
	// Event-driven warm-pool refill (ADR 0058). Registered so AutomaticEnv binds
	// DEXAFLOW_/LEOFLOW_EXECUTION_WARM_POOL_EVENT_REFILL; false keeps polling.
	"execution.warm_pool_event_refill": false,
	// Cluster deploy defaults (#1704). Registered so AutomaticEnv binds
	// DEXAFLOW_/LEOFLOW_DEPLOY_DEFAULTS_*; empty advertises nothing.
	"deploy.defaults.registry":   "",
	"deploy.defaults.platforms":  []string{},
	"deploy.defaults.base_image": "",
	// Off: no runtime image is advertised for source mode (#1772).
	"deploy.defaults.source_mode": false,
	// #1890: the remote builder a deploy uses when nothing local names one,
	// and the switches that advertise the task service account and the warm
	// pools. Unset and off advertise nothing.
	"deploy.defaults.remote_builder.cloud_build.project":         "",
	"deploy.defaults.remote_builder.cloud_build.region":          "",
	"deploy.defaults.remote_builder.cloud_build.worker_pool":     "",
	"deploy.defaults.remote_builder.cloud_build.service_account": "",
	"deploy.defaults.remote_builder.buildkit.namespace":          "",
	"deploy.defaults.remote_builder.buildkit.pod_selector":       "",
	"deploy.defaults.task_service_account":                       false,
	"deploy.defaults.warm_pools":                                 false,
	// Digest-pinned images at register (#1703). off keeps every register as
	// it was.
	"deploy.require_digest": string(domain.DigestRequireOff),
	// Retention (the leader-only janitor). Every class ships off: nothing is
	// deleted until an operator sets a window. The pacing values apply once one is.
	"retention.dag_runs_days":      0,
	"retention.audit_log_days":     0,
	"retention.dry_run":            false,
	"retention.interval":           "1h",
	"retention.batch_size":         1000,
	"retention.batch_pause":        "100ms",
	"retention.max_rows_per_cycle": 100000,
}
