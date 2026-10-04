// Command leoflow-server runs the Dexaflow control plane: the HTTP API, auth,
// metrics, and (when enabled) the scheduler.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	dexaflow "github.com/dexadata/dexaflow"
	"github.com/dexadata/dexaflow/internal/agent"
	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/alerts"
	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/dispatch"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/envcompat"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/failurealert"
	"github.com/dexadata/dexaflow/internal/issuer"
	"github.com/dexadata/dexaflow/internal/kubeexchange"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/oidc"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/ui"
	"github.com/dexadata/dexaflow/internal/version"
	"github.com/dexadata/dexaflow/internal/workspace"
	"github.com/dexadata/dexaflow/internal/xcom"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// usage is printed for `--help`. leoflow-server takes no positional args; it is
// configured entirely via environment and an optional LEOFLOW_CONFIG file.
const usage = `dexaflow-server — the Dexaflow control plane (HTTP API, auth, metrics, scheduler).

Configured via environment variables and an optional config file (DEXAFLOW_CONFIG);
there are no positional arguments. See docs/configuration.md.

Flags:
  --version   print version and exit
  --help, -h  print this help and exit
`

func main() {
	// DEXAFLOW_* and the pre-rename LEOFLOW_* names are interchangeable; mirror
	// them before anything reads the environment.
	logEnvCompat(envcompat.MirrorProcess())

	// Answer `--version`/`--help` before loading any config, so an operator can
	// query a deployed binary without a runnable environment (#593). Without
	// this, `--help` falls through to a boot attempt that errors on missing
	// config.
	args := os.Args[1:]
	switch {
	case version.WantsVersion(args):
		fmt.Println(version.Get().String())
		return
	case version.WantsHelp(args):
		fmt.Print(usage)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dexaflow-server:", err)
		os.Exit(1)
	}
}

// openVerifiedPostgres opens the pool and fails fast when the schema is not
// current, with an actionable message — instead of booting and erroring at the
// first query against a missing/old table. The check is a read-only SELECT, so it
// works for the migration-less role:api (ADR 0049); the blessed Helm path applies
// migrations in a pre-install/upgrade Job before any role starts.
func openVerifiedPostgres(ctx context.Context, cfg config.DatabaseSection) (*storage.Postgres, error) {
	pg, err := storage.NewPostgres(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if serr := pg.CheckSchemaCurrent(ctx); serr != nil {
		pg.Close()
		return nil, serr
	}
	return pg, nil
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.LoadServer(os.Getenv("LEOFLOW_CONFIG"), nil)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if verr := validateStartup(cfg); verr != nil {
		return verr
	}

	tel, shutdownTel, err := observability.Setup(ctx, observability.Config{
		ServiceName:  "leoflow-server",
		LogLevel:     cfg.Observability.LogLevel,
		LogFormat:    cfg.Observability.LogFormat,
		OTelEnabled:  cfg.Observability.OTel.Enabled,
		OTelEndpoint: cfg.Observability.OTel.Endpoint,
	})
	if err != nil {
		return fmt.Errorf("observability setup: %w", err)
	}
	defer shutdownTel()
	// Point Go's package-level slog at the same handler. This does NOT replace
	// injection, and both exist deliberately: a component that needs a specific
	// logger takes one (logs.NewDurableSink, the gRPC interceptors), because a
	// library must honor the contract its owner configured rather than depend on
	// its caller having reassigned a global. But injection only covers the seams
	// that have actually been threaded, and internal/agentrpc alone still makes
	// two dozen bare slog calls — token-review rejections, secret-liveness
	// denials, the log stream's failed final flush — every one of which landed as
	// plain text on stderr, outside the configured format and level, where
	// nothing that collects the control plane's logs would see it. That is what
	// left the eviction-log failure modes unmeasurable (#918). One line fixes
	// every remaining site and takes nothing away from injection.
	slog.SetDefault(tel.Logger)
	warnStartup(cfg, tel.Logger)

	pg, err := openVerifiedPostgres(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer pg.Close()

	// Datastore for XCom + live-log tailing: Redis when configured (production,
	// ADR 0006), or the embedded Postgres/in-process backends when no Redis is
	// set (Lite — ADR 0026). The signal is the presence of a Redis URL.
	xcomBackend, logTailer, redisHealth, dsCleanup, err := selectDatastore(ctx, cfg, pg, tel.Logger, tel.Metrics)
	if err != nil {
		return err
	}
	defer dsCleanup()

	repo := storage.NewRepository(pg)
	if serr := configureSecrets(ctx, repo, cfg, tel.Logger); serr != nil {
		return serr
	}
	authn := auth.NewJWTAuthenticator(repo, cfg.Auth.JWT.Secret, time.Duration(cfg.Auth.JWT.TokenTTLSeconds)*time.Second)
	execStore := storage.NewExecutionStore(pg)
	xcomSvc := xcom.NewService(xcomBackend, storage.NewXComIndex(pg), xcom.DefaultTTL)
	xcomReader := storage.NewXComReader(pg, xcomBackend)

	if err := bootstrapAdmin(ctx, repo, tel.Logger); err != nil {
		return err
	}
	// The name check needs the database, so it cannot live in warnStartup with the
	// rest. It is bounded because it is still on the boot path ahead of the
	// listener: a hung lookup must not hold the probe endpoint down (the same
	// reason discovery is bounded), and its bound is counted in the startup-probe
	// budget.
	//
	// It runs AFTER bootstrapAdmin, and that order is the whole point. The break-glass
	// check asks whether a listed address has a local password login, and the
	// bootstrap admin is the only such account a fresh install has. Running first
	// meant a first install with bootstrap.password set and admin@leoflow.local in
	// break_glass_emails, which is the chart's own documented posture, warned that
	// its escape hatch does not open when it does. It cleared on the next restart,
	// which is worse than plainly wrong: wrong on the one boot an operator reads,
	// right afterwards, and that is how a whole family of warnings gets learned as
	// noise. Found by installing the chart, not by any test.
	warnOIDCNames(ctx, repo, cfg, tel.Logger)

	logSink, lerr := buildLogSink(ctx, cfg, tel.Logger)
	if lerr != nil {
		return lerr
	}
	// Secrets are served over the agent channel only when explicitly allowed
	// insecure (dev) until gRPC TLS lands (issue #58); otherwise the handlers
	// fail closed on a plaintext channel.
	allowInsecureSecrets := os.Getenv("LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS") == "true"
	// Edition-gated boot guards (#58, #281): a Pro deployment must not ship
	// secrets over a plaintext channel (the insecure escape hatch) and must have
	// TLS on the agent gRPC channel. Lite (edition=lite) and the unmarked default
	// (edition="") still tolerate the plaintext dev loop.
	if err := guardEditionSecurity(cfg.UI.Edition, allowInsecureSecrets, cfg.Server.GRPCTLSCert, cfg.Server.GRPCTLSKey); err != nil {
		return err
	}
	// Role gating (ADR 0049). "all" (the default, and Lite's only mode) serves both
	// sides, so both predicates are true and the wiring below is identical to the
	// pre-0049 monolith. The "scheduler" role runs the agent gRPC, the scheduler
	// loop, and the janitors; the "api" role runs the HTTP API + UI. Metrics run in
	// every role. The Helm two-Deployment rendering that lets an operator select a
	// role is a later phase; today the flag is process-level and covered by tests.
	servesAPI := cfg.Server.ServesAPI()
	servesScheduler := cfg.Server.ServesScheduler()

	// Start the scheduler-role components (agent gRPC, janitors, scheduler loop +
	// dispatch). In the api role this starts nothing and returns a nil health handle
	// + no-op stop; in "all"/"scheduler" it is the pre-0049 wiring unchanged.
	schedulerHealth, podDispatch, stopScheduler, serr := startSchedulerSide(ctx, cfg, pg, repo, execStore, authn, xcomSvc, logSink, logTailer, allowInsecureSecrets, servesScheduler, tel.Logger, tel.Metrics)
	if serr != nil {
		return serr
	}
	defer stopScheduler()

	agentAddr := cfg.Executor.AgentControlPlaneAddr
	if agentAddr == "" {
		agentAddr = cfg.Server.GRPCAddr
	}
	executorInfo := api.ExecutorInfo{
		PodDispatchEnabled:    executorDispatchEnabled(cfg, servesScheduler, podDispatch),
		TaskNamespace:         cfg.Executor.TaskNamespace,
		AgentControlPlaneAddr: agentAddr,
	}

	// Dependency health probes (Postgres, and Redis when configured). Built once
	// and shared read-only by the API's /readyz and the metrics-port /readyz.
	checks := healthChecks(pg, redisHealth)

	// In the split api role the scheduler runs in another process, so
	// startSchedulerSide returned a nil health handle. Reporting the scheduler as
	// healthy from nil would be a lie (finding F1) — instead read its liveness
	// from shared DB state: a live scheduler leader holds the leadership advisory
	// lock (ADR 0009). The "all" role keeps its in-process handle (real tick
	// health), unchanged.
	if servesAPI && !servesScheduler {
		schedulerHealth = scheduler.NewLeaderHealthReader(pg.Pool)
	}

	// The API side (HTTP + embedded UI) is built only in the api/"all" role. The
	// scheduler role serves no API, so none of the UI/handler machinery is even
	// constructed — apiSrv stays nil and serveHTTP omits it.
	apiSrv, aerr := startAPISide(ctx, cfg, tel, authn, pg, repo, xcomReader, logSink, logTailer, checks, executorInfo, schedulerHealth, servesAPI)
	if aerr != nil {
		return aerr
	}

	// The metrics listener also serves /healthz + /readyz in every role so a
	// scheduler-only pod (ADR 0049), which serves no API, still has a probe target
	// for the kubelet. Additive on the api/"all" role, whose probes still hit the
	// HTTP port.
	metricsSrv := &http.Server{Addr: cfg.Server.MetricsAddr, Handler: api.ObservabilityHandler(tel.Registry, checks, observabilityOptions(cfg)...), ReadHeaderTimeout: 10 * time.Second}

	tel.Logger.Info("leoflow-server started", "role", cfg.Server.EffectiveRole(), "http_addr", cfg.Server.HTTPAddr, "metrics_addr", cfg.Server.MetricsAddr, "serves_api", servesAPI, "serves_scheduler", servesScheduler)
	return serveHTTP(ctx, tel.Logger, servesAPI, apiSrv, metricsSrv)
}

// observabilityOptions maps the observability.metrics config onto the metrics
// listener.
func observabilityOptions(cfg *config.ServerConfig) []api.ObservabilityOption {
	if cfg.Observability.Metrics.DropLegacyNames {
		return []api.ObservabilityOption{api.WithoutLegacyMetricNames()}
	}
	return nil
}

// awaitShutdown blocks until a server errors or the context is canceled, then
// gracefully shuts the HTTP servers down.
func awaitShutdown(ctx context.Context, errCh <-chan error, logger *slog.Logger, servers ...*http.Server) error {
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
		// Shutdown deliberately uses a fresh context; the inherited ctx is already canceled.
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, srv := range servers {
			if serr := srv.Shutdown(shutCtx); serr != nil { //nolint:contextcheck // fresh shutdown context by design
				logger.Error("server shutdown", "addr", srv.Addr, "error", serr)
			}
		}
		return nil
	}
}

// showLiteBadge reports whether the served UI shows the silver LITE badge: the
// Lite edition, or the legacy dev auth bypass.
func showLiteBadge(cfg *config.ServerConfig) bool {
	return cfg.UI.Edition == "lite" || cfg.Auth.DevNoAuth
}

// showProBadge reports whether the served UI shows the gold PRO badge — the
// Pro edition, set explicitly by the Helm chart (values.yaml: ui.edition=pro).
// Demo and empty editions show no badge.
func showProBadge(cfg *config.ServerConfig) bool {
	return cfg.UI.Edition == "pro"
}

// liteEditorFS builds the workspace filesystem backing the Lite web editor when
// a workspace is configured (Lite only). A misconfigured workspace logs a
// warning and disables the editor rather than failing server boot.
func liteEditorFS(cfg *config.ServerConfig, logger *slog.Logger) api.WorkspaceFS {
	if cfg.UI.Workspace == "" {
		return nil
	}
	fs, err := workspace.New(cfg.UI.Workspace)
	if err != nil {
		logger.Warn("Lite editor disabled: invalid workspace", "workspace", cfg.UI.Workspace, "error", err)
		return nil
	}
	return fs
}

// configureSecretCipher wires the AES-256-GCM cipher for connection secrets
// (ADR 0019). Without a key the connection store stays plaintext-incapable:
// writes are refused, never silently stored in the clear.
func configureSecretCipher(repo *storage.Repository, secretKey string, logger *slog.Logger) error {
	keys, kerr := secrets.ParseKeys(secretKey)
	if kerr != nil {
		// The reason, not just the outcome. ParseKeys says WHICH entry of the
		// list is malformed, and an operator told only "no usable key set" after
		// setting one will go looking at the wrong thing. One bad entry disables
		// encryption even when the first key is perfect, so naming it matters
		// more here than it did with a single key.
		logger.Warn("no usable LEOFLOW_SECRET_KEY; connection management disabled (Variables still work)",
			"error", kerr)
		return nil //nolint:nilerr // a missing/unusable key is non-fatal: run without connection encryption
	}
	ciphers := make([]secrets.Cipher, 0, len(keys))
	for i, k := range keys {
		c, cerr := secrets.NewAESGCM(k)
		if cerr != nil {
			return fmt.Errorf("building secret cipher from key %d: %w", i+1, cerr)
		}
		ciphers = append(ciphers, c)
	}
	// The first key encrypts; the rest only decrypt, so a rotation reads what a
	// previous key wrote instead of orphaning it (#486). Nothing is ever written
	// under a later key, which is what makes the rotation real rather than
	// cosmetic.
	repo.SetCipher(secrets.WithFallback(ciphers[0], ciphers[1:]...))
	if len(ciphers) > 1 {
		logger.Info("secret key rotation: the first key encrypts, the rest only decrypt",
			"decrypt_only_keys", len(ciphers)-1)
	}
	// A 32-character all-hex key is what `openssl rand -hex 16` produces, which
	// this project's own docs recommended until they were corrected. ParseKey
	// takes those 32 characters as 32 raw bytes, so the cipher is AES-256 over
	// 128 bits of entropy and nothing else would ever mention it. Warn rather
	// than refuse: the shape is indistinguishable from a legitimate 32-character
	// passphrase, and breaking an operator who did nothing wrong is worse.
	//
	// Checked on EVERY key in the list, not just the first: an operator rotating
	// AWAY from a half-entropy key puts it in the tail, which is exactly the
	// case the warning's own advice ("re-encrypt existing connections") is for.
	for i, raw := range strings.Split(secretKey, ",") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || !secrets.LooksLikeHalfEntropyHexKey(trimmed) {
			continue
		}
		logger.Warn("a LEOFLOW_SECRET_KEY entry looks like `openssl rand -hex 16` output: "+
			"32 hex characters are consumed as 32 raw bytes, giving 128 bits of entropy where AES-256 expects 256. "+
			"Generate a replacement with `openssl rand -hex 32` (64 characters) and re-encrypt existing connections",
			"key_position", i+1, "key_length", len(trimmed))
	}
	logger.Info("connection secret encryption enabled (AES-256-GCM)")
	return nil
}

func bootstrapAdmin(ctx context.Context, repo *storage.Repository, logger *slog.Logger) error {
	email := os.Getenv("LEOFLOW_BOOTSTRAP_EMAIL")
	if email == "" {
		email = "admin@leoflow.local"
	}
	// Prefer a precomputed bcrypt hash (Dexaflow Lite never sends the plaintext to
	// the control plane); fall back to a plaintext bootstrap password.
	if hash := os.Getenv("LEOFLOW_BOOTSTRAP_PASSWORD_HASH"); hash != "" {
		created, err := repo.BootstrapAdminHash(ctx, "default", email, hash)
		if err != nil {
			return fmt.Errorf("bootstrap admin (hash): %w", err)
		}
		if created {
			logger.Info("bootstrapped admin user", "email", email)
		}
		return nil
	}
	pw := os.Getenv("LEOFLOW_BOOTSTRAP_PASSWORD")
	if pw == "" {
		return nil
	}
	created, err := repo.BootstrapAdmin(ctx, "default", email, pw)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if created {
		logger.Info("bootstrapped admin user", "email", email)
	}
	return nil
}

// attemptTokenTTL is the short per-attempt agent-token lifetime minted at
// dispatch (ADR 0055 Fix #4), derived from the heartbeat interval so a live task
// refreshes its credential on every beat (see agentrpc.Server.Heartbeat) while a
// stolen or finished token lapses in minutes. It replaces the former flat 24h
// TTL at the dispatch site; the total renewed lifetime is separately capped by
// auth.max_attempt_credential_lifetime.
var attemptTokenTTL = agent.AttemptTokenTTL(agent.DefaultHeartbeatInterval)

// validateStartup runs every boot-time invariant check before any subsystem
// starts: the operator's configuration, then the resilience timing ladder the
// restart recovery depends on. A violation fails boot with the offending
// relation named, instead of surfacing as a false reap after the next restart.
func validateStartup(cfg *config.ServerConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return executor.ValidateResilienceLadder(resilienceLadder(cfg))
}

// warnStartup surfaces the boot-time settings validateStartup accepts but that
// remove a resilience backstop — today the disabled credential ceiling, which
// unbounds heartbeat renewal, drops the task pod deadline floor and switches
// off the warm-pool attempt watchdog. They are documented settings, not errors,
// so boot proceeds; the WARN is the only operator-visible signal that those
// guarantees are off. It runs after the logger exists, right after validation
// has passed. Each warning is logged with the config key and the offending
// value as attributes, so the JSON record an alert rule reads carries
// config_key/value fields instead of only a prose message (#924).
// Only a process that serves the scheduler warns: token renewal
// lives in the agent gRPC server and the pod deadline floor in the dispatcher,
// both scheduler-side, so an api-only replica in a split install would be
// warning about behavior it does not implement — and a WARN operators learn to
// ignore is worse than none.
func warnStartup(cfg *config.ServerConfig, logger *slog.Logger) {
	// Role resolution is API-side, so this one is gated on the API rather than the
	// scheduler: an api-only replica is the process that reconciles roles.
	if cfg.Server.ServesAPI() {
		// Both are API-side: role reconciliation and the code exchange both run in
		// the process that serves the callback.
		oidcWarnings := oidcRoleSourceWarnings(cfg.Auth)
		oidcWarnings = append(oidcWarnings, oidcClientSecretWarnings(cfg.Auth)...)
		oidcWarnings = append(oidcWarnings, oidcJITWarnings(cfg.Auth)...)
		oidcWarnings = append(oidcWarnings, oidcBreakGlassWarnings(cfg.Auth)...)
		// Not OIDC-specific, but API-side for the same reason: the process that
		// serves the sign-in page and the callback is the one that sets the cookie.
		oidcWarnings = append(oidcWarnings, sessionCookieWarnings(cfg.Auth)...)
		for _, w := range oidcWarnings {
			logger.Warn(w.Msg, "config_key", w.Key, "value", w.Value, "missing_config_key", w.MissingKey)
		}
	}
	if !cfg.Server.ServesScheduler() {
		return
	}
	for _, w := range executor.ResilienceLadderWarnings(resilienceLadder(cfg)) {
		logger.Warn(w.Msg, "config_key", w.Key, "value", w.Value)
	}
	for _, w := range platformDefaultWarnings(cfg.Executor.Defaults) {
		logger.Warn(w.Msg, "config_key", w.Key, "value", w.Value, "missing_config_key", w.MissingKey)
	}
}

// configWarning is one boot WARN about an operator setting that is accepted but
// quietly delivers less than the value is documented to deliver: the
// operator-readable sentence plus the fields a monitoring rule needs. Same shape
// and same reason as executor.LadderWarning — a pre-formatted string alone
// leaves the JSON record with nothing but msg, so the alert an operator would
// actually want can only be written as a substring match on prose that a later
// reword silently breaks. Msg stays self-contained so a plain-text log needs no
// field expansion to be read.
type configWarning struct {
	Msg   string
	Key   string
	Value string
	// MissingKey is the sibling key the operator has to set for the documented
	// behavior to hold.
	MissingKey string
}

// platformDefaultWarnings reports executor.defaults settings that pass
// validation but silently deliver less than the value promises. Today there is
// one: a PARTIAL resources pair.
//
// resources_cpu/resources_memory are documented — in the chart's values.yaml, in
// the rendered chart README, and in the configuration reference — as landing a
// task that declares no resources of its own in Guaranteed QoS. That holds only
// when BOTH are set, because Guaranteed requires requests == limits for cpu AND
// memory. With one set, platformDefaults below still builds a non-nil default,
// the empty dimension is dropped from the pod spec, and the task gets one
// dimension pinned and the other with no request or limit at all: Burstable,
// first throttled/evicted under node pressure, and invisible to the Cluster
// Autoscaler's scale-up math on the dimension left out. A half-configured
// default is better than none, so it is not a boot error — but it is otherwise
// invisible, and the operator who set one field believes they configured the
// documented behavior. Pure, like the ladder warnings: called once at boot,
// after the logger exists.
func platformDefaultWarnings(c config.PlatformDefaultsSection) []configWarning {
	const cpuKey, memKey = "executor.defaults.resources_cpu", "executor.defaults.resources_memory"
	if (c.ResourcesCPU == "") == (c.ResourcesMemory == "") {
		// Both set (the documented configuration) or neither (the shipped
		// default). Nothing to say.
		return nil
	}
	set, missing, value, dimension := cpuKey, memKey, c.ResourcesCPU, "memory"
	if c.ResourcesCPU == "" {
		set, missing, value, dimension = memKey, cpuKey, c.ResourcesMemory, "cpu"
	}
	return []configWarning{{
		Msg: fmt.Sprintf("%s is set (%q) with no %s: a task that declares no resources of its own gets that one quantity as its request AND limit and NO %s request or limit at all, so the pod is Burstable, not the Guaranteed QoS this default is documented to deliver — and nothing else supplies the missing %s. Set both quantities, or neither",
			set, value, missing, dimension, dimension),
		Key:        set,
		Value:      value,
		MissingKey: missing,
	}}
}

// defaultRoleKey is the config key that gives OIDC role resolution a floor. Two
// boot warnings point at it, so it is named once.
const defaultRoleKey = "auth.oidc.default_role"

// oidcRoleSourceWarnings reports an OIDC deployment whose role resolution has no
// floor: auth.oidc.default_role is empty, with or without auth.oidc.role_mappings.
// The two shapes get different messages because they are different mistakes.
//
// Role resolution is IdP-authoritative (ADR 0057 D5): every login computes its
// roles from role_mappings, applies default_role when that yields none, and hands
// the result to ReconcileUserRoles, which sets the user's DB grants to EXACTLY
// that set. With neither configured the set is always empty, and an empty
// reconcile is a full clear, so a pre-provisioned admin's first SSO login strips
// the grants they were provisioned with. The login still succeeds and the session
// is minted, which is why this cannot fail closed: nothing is wrong per request,
// the deployment is simply configured to grant nobody anything.
//
// It only became reachable when the tenant pin was made a boot requirement
// (#1143): before that, a deployment configured this loosely was usually also
// missing the pin, and the login was rejected before reconciliation ran. Removing
// that accidental shield is what makes the WARN necessary.
//
// With role_mappings set the same clear happens per login rather than always, and
// boot cannot tell a broken deployment from a strict one: on an IdP that emits
// the groups claim, granting nothing outside a mapped group is the intended
// posture, while on Google Workspace without Directory API group sync the claim
// never arrives and the mapping can never match. That is why this half names the
// Google shape and ends with a clause the operator can deliberately dismiss:
// default_role is not a free fix, it grants that role to every login the tenant
// pin admits, and a warning whose only remedy is to widen access is one a correct
// deployment learns to ignore.
func oidcRoleSourceWarnings(c config.AuthSection) []configWarning {
	const mappingsKey = "auth.oidc.role_mappings"
	if c.Provider != config.AuthProviderOIDC {
		return nil
	}
	if c.OIDC.DefaultRole != "" {
		return nil
	}
	if len(c.OIDC.RoleMappings) > 0 {
		return []configWarning{{
			Msg: "auth.oidc.role_mappings without " + defaultRoleKey +
				" makes the IdP group claim load-bearing: a login whose groups match no entry resolves to an EMPTY role set, " +
				"and roles are reconciled to exactly that set, so the user's existing grants are CLEARED rather than left alone. " +
				"Google Workspace emits no groups claim at all unless Directory API group sync is configured, " +
				"so every login on that IdP takes this path. Set " + defaultRoleKey +
				" to a read-only role such as viewer to give resolution a floor, or ignore this if your IdP does emit " +
				"auth.oidc.groups_claim and a login outside every mapped group is meant to hold no roles",
			Key:        defaultRoleKey,
			Value:      "",
			MissingKey: defaultRoleKey,
		}}
	}
	return []configWarning{{
		Msg: "auth.provider: oidc with neither " + mappingsKey + " nor " + defaultRoleKey +
			" grants no roles to anyone: every login resolves to an empty role set, and roles are reconciled to EXACTLY that set on each login, " +
			"so a pre-provisioned user's existing grants are CLEARED on their first single sign-on. " +
			"Map at least one IdP group to a role under " + mappingsKey + " (config file only: it is a map), or set " + defaultRoleKey +
			" to a read-only role such as viewer",
		Key:        defaultRoleKey,
		Value:      "",
		MissingKey: mappingsKey,
	}}
}

// oidcNameCheckTimeout bounds the boot-time existence lookups, all of them
// together rather than one each. It is a var so tests can shrink it; the value
// is what ships. The lookups are unique-index probes (tenants.name is UNIQUE,
// roles is UNIQUE (tenant_id, name)) against a database whose schema was just
// read, one per distinct tenant plus one per distinct role name, so 5s is
// already generous.
//
// It ADDS to the startup-probe budget rather than sitting inside it: this is a
// further sequential phase of boot, after the Postgres connect retry, not a
// share of it. The retry having succeeded proves the pool connects, which is
// exactly not what this bound guards against: a pool can be established and a
// query still never answer. scripts/check-startup-probe-budget.sh counts it, and
// raising it here without raising probes.startup.failureThreshold there fails
// that gate rather than turning a slow boot into a restart loop.
var oidcNameCheckTimeout = 5 * time.Second

// warnOIDCNames runs the database-backed name check and logs what it finds.
//
// Gated on the API role for the same reason the role warning is: the login path
// is API-side, so this is the process whose logins the missing names break. The
// lookups are bounded because this is on the boot path ahead of the listener,
// and a hung query must not hold the probe endpoint down.
func warnOIDCNames(ctx context.Context, ck oidcNameChecker, cfg *config.ServerConfig, logger *slog.Logger) {
	if !cfg.Server.ServesAPI() {
		return
	}
	nameCtx, cancel := context.WithTimeout(ctx, oidcNameCheckTimeout)
	defer cancel()
	for _, w := range oidcNameWarnings(nameCtx, ck, cfg.Auth) {
		logger.Warn(w.Msg, "config_key", w.Key, "value", w.Value, "missing_config_key", w.MissingKey)
	}
}

// oidcNameChecker is the pair of existence lookups the boot name check needs.
// It is an interface so the check is testable without a database, and narrow so
// it cannot grow into a second place that reads users.
type oidcNameChecker interface {
	TenantExists(ctx context.Context, name string) (bool, error)
	RoleExists(ctx context.Context, tenant, role string) (bool, error)
	LocalPasswordUserExists(ctx context.Context, tenant, email string) (bool, error)
}

// oidcNameWarnings reports OIDC settings that name rows which do not exist.
//
// Everything else in this file checks the shape of the configuration. These two
// are the shapes that are correct and still cannot work, because the name is
// fine and the row is missing:
//
//   - a tenant_claims VALUE naming a tenant that does not exist. The
//     migrations create only "default" (INSERT INTO tenants appears once, in
//     001); any other tenant exists only once an operator creates it, through
//     the service API (PUT /api/v2/service/tenants/{name}, #1283). Until then
//     every login carrying that claim value is denied.
//   - a default_role or role_mappings value naming a role that does not exist
//     for the resolved tenant. The ladder (viewer, editor, operator, admin) is
//     seeded for "default" alone. This one matters because "set default_role to
//     viewer" is the remedy this binary's own warnings recommend, so it is the
//     name most likely to be typed by hand.
//
// It runs after the schema check, so Postgres is connected and current. A lookup
// that fails is reported rather than skipped: a check that could not run and a
// check that found nothing must not produce the same silence.
//
// Both maps are walked per distinct VALUE rather than per key (keysByValue),
// because the value is the name being looked up: several domains commonly map to
// one tenant and several IdP groups to one role, and the same missing row asked
// for once per key is a repeated round trip on the boot path and a warning
// repeated until operators stop reading it. The keys that named it travel with
// it, so one warning still points at every line that has to be edited.
func oidcNameWarnings(ctx context.Context, ck oidcNameChecker, c config.AuthSection) []configWarning {
	if c.Provider != config.AuthProviderOIDC {
		return nil
	}
	const tenantsKey = "auth.oidc.tenant_claims"
	out := breakGlassAddressWarnings(ctx, ck, c.OIDC.BreakGlassEmails)
	// Walked per distinct TENANT, not per claim value: several domains commonly
	// map to the same tenant, and asking the same question once per domain is a
	// repeated round trip on the boot path and a warning repeated until operators
	// stop reading it. The claim values that named the tenant travel with it, so
	// one warning still points at every line that has to be edited.
	for tenant, claims := range keysByValue(c.OIDC.TenantClaims) {
		ok, err := ck.TenantExists(ctx, tenant)
		switch {
		case err != nil:
			out = append(out, configWarning{
				Msg: "could not check whether the tenant " + quoted(tenant) + " named by " + tenantsKey +
					" exists: " + err.Error() + ". A login resolving to a tenant that does not exist is denied, " +
					"so this is worth re-running by hand",
				Key:   tenantsKey,
				Value: tenant,
			})
			continue
		case !ok:
			out = append(out, configWarning{
				Msg: tenantsKey + " maps " + quotedList(claims) + " to the tenant " + quoted(tenant) +
					", which does not exist. Every login carrying those claim values is denied. The migrations create only " +
					quoted("default") + "; create " + quoted(tenant) + " through the service API " +
					"(PUT /api/v2/service/tenants/" + tenant + ", enabled by auth.service_token), or map them to " + quoted("default"),
				Key:        tenantsKey,
				Value:      tenant,
				MissingKey: tenantsKey,
			})
			continue
		}
		out = append(out, missingRoleWarnings(ctx, ck, tenant, c.OIDC)...)
	}
	return out
}

// breakGlassAddressWarnings reports a break-glass allowlist whose addresses
// cannot actually sign in.
//
// The empty-list warning says nobody can get in. A non-empty list looks safe and
// is not. The gate admits the address (internal/api/auth_handler.go) and then
// FindUserByLogin finds no row and answers the same "invalid credentials" a
// wrong password gets, so the hatch does not open and nothing says why. That is
// worse than the empty case, because the operator believes they have one.
//
// The only local password user that ever exists is the bootstrap admin, and only
// when a bootstrap password was set at first install. Every other local user
// comes from the admin-authenticated user API, which is exactly what cannot be
// reached during a lock-out, so writing your own address into this list and
// expecting it to work is the natural mistake.
//
// A row is not enough. A user created by OIDC provisioning has a NULL password
// (the users_has_auth check permits it because the OIDC subject is the other
// half), so the row can exist while no password can ever verify against it.
//
// The tenant is "default" and not a configured one: the login page sends no
// tenant, and authTokenHandler defaults an empty one to "default", so that is
// the only tenant a break-glass login can ever land in.
//
// One working address is enough to get back in, so this warns only when NONE of
// them does, and then names all of them.
func breakGlassAddressWarnings(ctx context.Context, ck oidcNameChecker, emails []string) []configWarning {
	const key = "auth.oidc.break_glass_emails"
	if len(emails) == 0 {
		return nil // the empty case is oidcBreakGlassWarnings, with its own message
	}
	var failures []string
	for _, raw := range emails {
		email := strings.ToLower(strings.TrimSpace(raw))
		if email == "" {
			continue
		}
		ok, err := ck.LocalPasswordUserExists(ctx, breakGlassTenant, email)
		if err != nil {
			return []configWarning{{
				Msg: "could not check whether the addresses in " + key + " have a local password login: " + err.Error() +
					". An address listed there with no password row is an escape hatch that does not open, so this is " +
					"worth re-running by hand",
				Key:   key,
				Value: email,
			}}
		}
		if ok {
			return nil
		}
		failures = append(failures, email)
	}
	if len(failures) == 0 {
		return nil
	}
	return []configWarning{{
		Msg: key + " lists " + quotedList(failures) + ", and none of them has a local password login in the " +
			quoted(breakGlassTenant) + " tenant, so none of them can sign in. The allowlist admits the address and " +
			"the credential store then answers the same " + quoted("invalid credentials") + " a wrong password gets. " +
			"A user provisioned through single sign-on does not count: it has no password for anything to verify " +
			"against. Create the local account BEFORE you need it, while an admin session still exists",
		Key:        key,
		Value:      strings.Join(failures, ","),
		MissingKey: key,
	}}
}

// breakGlassTenant is the tenant a break-glass login always lands in: the login
// page sends no tenant and authTokenHandler defaults an empty one to this.
const breakGlassTenant = "default"

// missingRoleWarnings reports the role names configured for one existing tenant
// that the tenant does not have.
//
// The tenant is known to exist, so a lookup that comes back false means the role
// row is absent and resolveUser takes its !exists branch, denying the login as
// unknown_role:<role>. role_check_failed is the OTHER branch, the lookup itself
// erroring, which is what a MISSING TENANT produces (Repository.RoleExists
// resolves the tenant first and returns domain.ErrNotFound). Naming the wrong one
// sends an operator to grep the audit log for a reason that is not there.
//
// Each distinct role name is looked up once. Mapping several IdP groups to one
// Dexaflow role is the normal shape, and asking the same question per group is
// both a repeated round trip on the boot path and, when the answer is "missing",
// the same warning printed once per group.
func missingRoleWarnings(ctx context.Context, ck oidcNameChecker, tenant string, o config.OIDCSection) []configWarning {
	const mappingsKey = "auth.oidc.role_mappings"
	var out []configWarning
	type lookup struct {
		ok  bool
		err error
	}
	looked := map[string]lookup{}
	exists := func(role string) lookup {
		if l, done := looked[role]; done {
			return l
		}
		ok, err := ck.RoleExists(ctx, tenant, role)
		looked[role] = lookup{ok: ok, err: err}
		return looked[role]
	}
	// named describes where the role came from, for a message that points at a
	// line the operator can edit rather than at a role name alone.
	check := func(key, role, named string) {
		l := exists(role)
		switch {
		case l.err != nil:
			out = append(out, configWarning{
				Msg: "could not check whether the role " + quoted(role) + " named by " + key + " exists in the tenant " +
					quoted(tenant) + ": " + l.err.Error() + ". A login resolving to a role that does not exist is denied",
				Key:   key,
				Value: role,
			})
		case !l.ok:
			out = append(out, configWarning{
				Msg: key + " " + named + "the role " + quoted(role) + ", which the tenant " + quoted(tenant) +
					" does not have. A login that resolves to it is denied (audited unknown_role:" + role +
					") behind the same generic 403 as every other failure. The roles seeded for a new deployment are " +
					"viewer, editor, operator and admin",
				Key:        key,
				Value:      role,
				MissingKey: key,
			})
		}
	}
	// An unset default_role is a supported strict default-deny, not a name that
	// is missing, and oidcRoleSourceWarnings already says so. An EMPTY value
	// anywhere else is a name that does not exist: MapRoles copies an empty
	// mapped value straight into the resolved role set, and the login is denied
	// on it like any other unknown role.
	if o.DefaultRole != "" {
		check(defaultRoleKey, o.DefaultRole, "names ")
	}
	for role, groups := range keysByValue(o.RoleMappings) {
		check(mappingsKey, role, "maps "+quotedList(groups)+" to ")
	}
	return out
}

// keysByValue inverts a configured name→name map into one entry per distinct
// VALUE, carrying the keys that named it: the tenant and the claim values mapped
// to it, the role and the IdP groups mapped to it.
//
// The value is what gets looked up, so this is what makes "once per distinct
// name" structural rather than a deduplication bolted onto a per-key walk. The
// keys travel with it so the warning can name every line the operator has to
// edit. Entries come out in the order their first key sorts, and the keys within
// an entry in sorted order, so two boots log the same lines and a diff between
// them means something.
//
// An empty value is kept, not dropped. `corp.example:` with nothing after it is
// valid YAML that binds to "", nothing rejects it (validateOIDC checks only that
// tenant_claims is non-empty as a map), and it denies every login it touches.
// That is a name that does not exist, which is what this check is for.
func keysByValue(m map[string]string) iter.Seq2[string, []string] {
	keys := map[string][]string{}
	order := make([]string, 0, len(m))
	for k, v := range sortedPairs(m) {
		if _, seen := keys[v]; !seen {
			order = append(order, v)
		}
		keys[v] = append(keys[v], k)
	}
	return func(yield func(string, []string) bool) {
		for _, v := range order {
			if !yield(v, keys[v]) {
				return
			}
		}
	}
}

// quotedList renders names for an operator-facing message, quoted for the same
// reason a single one is.
func quotedList(names []string) string {
	quotedNames := make([]string, 0, len(names))
	for _, n := range names {
		quotedNames = append(quotedNames, quoted(n))
	}
	return strings.Join(quotedNames, ", ")
}

// sessionCookieWarnings reports the session cookie running without Secure. It
// is a documented setting, not an error, so boot proceeds: a deployment served
// over plain http to a name that is not loopback has no other way to sign
// anybody in, because a browser refuses a Secure cookie from such an origin
// outright. The WARN is the only operator-visible signal that the session token
// is now carried in the clear, and it carries the key and the value as fields
// so an alert rule does not have to substring-match prose.
func sessionCookieWarnings(c config.AuthSection) []configWarning {
	const key = "auth.session_cookie_insecure"
	if !c.SessionCookieInsecure {
		return nil
	}
	return []configWarning{{
		Msg: key + " is on, so the browser session cookie and the OIDC state cookie are sent without Secure: " +
			"the session token travels over plain http and anything on the path can read it and replay it. " +
			"Set this only on a deployment that genuinely cannot be reached over https; a loopback deployment " +
			"(localhost, 127.0.0.1) does not need it, because browsers accept a Secure cookie there already",
		Key:   key,
		Value: "true",
	}}
}

// oidcBreakGlassWarnings reports an SSO deployment with no way back in.
//
// Under provider: oidc, newBreakGlass with an empty allowlist returns a gate that
// admits nobody, so every password login is rejected. That is correct, and it is
// also the state in which any of the other failures here leaves NOBODY able to
// reach the control plane, including the operator who has to fix it. An IdP
// outage does the same, and neither is in this deployment's control.
func oidcBreakGlassWarnings(c config.AuthSection) []configWarning {
	const key = "auth.oidc.break_glass_emails"
	if c.Provider != config.AuthProviderOIDC || len(c.OIDC.BreakGlassEmails) > 0 {
		return nil
	}
	return []configWarning{{
		Msg: "auth.provider: oidc with an empty " + key + " rejects every password login, so an IdP outage or a " +
			"wrong tenant pin leaves nobody able to reach the control plane, including whoever has to fix it. " +
			"List at least one local admin address there (Helm: auth.oidc.breakGlassEmails), or keep a documented " +
			"route back to auth.provider: jwt",
		Key:        key,
		Value:      "",
		MissingKey: key,
	}}
}

// quoted renders a configured name for an operator-facing message. The quotes
// are the point: a trailing space or an invisible character in a YAML value is
// otherwise impossible to see in a log line.
func quoted(s string) string { return strconv.Quote(s) }

// sortedPairs iterates a string map in key order, so a boot log reads the same
// on every restart. Go randomizes map order, which would reshuffle the warnings
// each time and defeat a diff between two boots.
func sortedPairs(m map[string]string) iter.Seq2[string, string] {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return func(yield func(string, string) bool) {
		for _, k := range keys {
			if !yield(k, m[k]) {
				return
			}
		}
	}
}

// oidcJITWarnings reports an OIDC deployment with just-in-time provisioning off.
//
// The flag reads as "require a pre-provisioned user", but no pre-provisioning
// path exists. A login resolves a user ONLY through FindUserByOIDCSubject, so a
// row matches only if it carries oidc_provider and oidc_subject, and the sole
// statement that writes those two columns is CreateOIDCUser, reached only from
// the JIT path. No API endpoint, CLI command or migration writes them. With the
// flag off, every first login is denied (audited no_user_jit_off) and the
// operator has no supported way to create a user that would ever match.
//
// It is a WARN and not a boot failure because an operator who wrote the two
// columns directly with SQL has a working deployment, and boot must not break
// it. Closing the gap properly is linking an existing user by verified email
// (ADR 0057 D3), which is a behavior change and not an RC fix.
func oidcJITWarnings(c config.AuthSection) []configWarning {
	const key = "auth.oidc.jit_provisioning"
	if c.Provider != config.AuthProviderOIDC || c.OIDC.JITProvisioning {
		return nil
	}
	return []configWarning{{
		Msg: "auth.provider: oidc with " + key + " disabled denies every first login (audited no_user_jit_off): " +
			"a login matches a user only by (oidc_provider, oidc_subject), and the only statement that writes those " +
			"columns is the just-in-time provisioning this flag disables. No API, CLI or migration can pre-provision " +
			"an OIDC identity, so with this off nobody can sign in for the first time. Set " + key + " to true, " +
			"together with " + defaultRoleKey + " or auth.oidc.role_mappings so a provisioned user gets the roles you intend",
		Key:        key,
		Value:      "false",
		MissingKey: key,
	}}
}

// oidcClientSecretWarnings reports an OIDC deployment with no client secret.
//
// The secret is used in exactly one place: the authorization-code exchange
// (internal/oidc/flow.go). A confidential client rejects an exchange without it
// with invalid_client, and every managed IdP an operator is likely to use
// registers a server-side application as confidential: Google Workspace and
// Entra always issue one, Okta and Keycloak do unless the client is explicitly
// registered as public. The rejection arrives at the callback, which answers a
// generic 403 by design (the browser must not learn why a login was refused), so
// the operator sees a login that fails with nothing anywhere naming the secret.
//
// It is a WARN and not a boot failure because a public client (PKCE only, no
// secret) is a legitimate registration, and failing boot on an empty secret
// would break a deployment that works today.
func oidcClientSecretWarnings(c config.AuthSection) []configWarning {
	const key = "auth.oidc.client_secret"
	if c.Provider != config.AuthProviderOIDC || c.OIDC.ClientSecret != "" {
		return nil
	}
	return []configWarning{{
		Msg: "auth.provider: oidc with no " + key + ": the authorization-code exchange is sent without a client secret, " +
			"which a confidential client rejects with invalid_client. Google Workspace and Entra always register a server-side " +
			"application as confidential; Okta and Keycloak do unless the client is explicitly public. The callback answers a " +
			"generic 403 on failure, so the login breaks with nothing naming this. Set it via the " +
			"LEOFLOW_AUTH_OIDC_CLIENT_SECRET environment variable (never in the config file), or ignore this if the IdP client " +
			"is registered as public",
		Key:        key,
		Value:      "",
		MissingKey: key,
	}}
}

// resilienceLadder assembles the effective timing knobs the control-plane
// restart recovery depends on, exactly as this binary wires them: the agent's
// heartbeat interval and derived token TTL, the reaper thresholds/graces, the
// reconciler sweep, the scheduler's longest infra re-place delay, and the
// operator's credential-lifetime ceiling — the one rung a deployment can move.
// validateStartup checks it before anything starts.
func resilienceLadder(cfg *config.ServerConfig) executor.ResilienceLadder {
	rc := executor.DefaultReaperConfig()
	return executor.ResilienceLadder{
		HeartbeatInterval:            agent.DefaultHeartbeatInterval,
		AgentLostThreshold:           rc.AgentLostThreshold,
		SettlingGrace:                rc.SettlingGrace,
		AttemptTokenTTL:              attemptTokenTTL,
		ReconcileInterval:            reconcileInterval,
		OrphanThreshold:              rc.OrphanThreshold,
		InfraReplaceMaxDelay:         scheduler.InfraReplaceMaxDelay(),
		MaxAttemptCredentialLifetime: cfg.Auth.MaxAttemptCredentialLifetime,
	}
}

// loginRateLimit returns the per-minute failed-login cap with a safe floor: a
// missing or nonsensical (<=0) config value falls back to the conservative
// default rather than 0, which would lock every user out.
func loginRateLimit(cfg *config.ServerConfig) int {
	if cfg.Auth.LoginRateLimitPerMinute > 0 {
		return cfg.Auth.LoginRateLimitPerMinute
	}
	return 5
}

// redispatchStore is the seam the warm reclaim observer uses to re-place an
// attempt (ADR 0058 N1d-c, H2). *storage.ExecutionStore satisfies it; a fake
// records the calls in tests.
type redispatchStore interface {
	RequeueForRedispatch(ctx context.Context, runID, taskID string, tryNumber int) error
}

// reclaimShouldRequeue reports whether a reclaimed warm assignment may be
// immediately re-placed for fast recovery (ADR 0058 N1d-c, H2). ONLY the reasons
// where the worker demonstrably will NOT run the attempt qualify, so re-placing
// cannot double-run:
//   - ReclaimWorkerGone: the worker's stream ended holding an unacked lease; a
//     disconnected worker can never run it.
//   - ReclaimRefused: the worker acked started=false — it explicitly declined.
//
// ReclaimLeaseExpired is deliberately NOT re-placed: a lease-expired worker may
// be alive-but-slow and, by agent order, will still ack-then-run the attempt, so
// fast-re-placing it could double-run. That case is recovered double-run-free by
// the 3-minute dispatch-lost reaper + the H3 warm-defer already merged.
func reclaimShouldRequeue(reason agentrpc.ReclaimReason) bool {
	switch reason {
	case agentrpc.ReclaimWorkerGone, agentrpc.ReclaimRefused:
		return true
	default: // ReclaimLeaseExpired and any future reason: recover via the reaper.
		return false
	}
}

// handleReclaim is the warm reclaim observer wired into the registry (ADR 0058
// N1d-c, H2). It logs every reclaim, then re-places the attempt only for the
// double-run-safe reasons (reclaimShouldRequeue). Re-placement is best-effort:
// an error is logged, never fatal, so a transient DB blip cannot crash the
// registry's emit path.
func handleReclaim(ctx context.Context, store redispatchStore, logger *slog.Logger, ev agentrpc.ReclaimEvent) {
	logger.Debug("warm worker assignment reclaimed", "assignment", ev.AssignmentID, "dag_version", ev.DagVersionID, "reason", ev.Reason)
	if !reclaimShouldRequeue(ev.Reason) {
		return
	}
	if err := store.RequeueForRedispatch(ctx, ev.RunID, ev.TaskID, ev.TryNumber); err != nil {
		logger.Error("warm reclaim re-placement failed", "run", ev.RunID, "task", ev.TaskID, "try", ev.TryNumber, "err", err)
	}
}

// startSchedulerSide starts the scheduler-role components (ADR 0049): the agent
// gRPC endpoint, the XCom/log janitors, and — when cfg.Scheduler.Enabled — the
// scheduler loop + dispatch pool. It returns the scheduler's health handle (nil
// when the loop is off; the API reports scheduler health as healthy on nil),
// whether pod dispatch is on, and a stop func to defer. When servesScheduler is
// false (the api role) it starts nothing and returns a nil handle + no-op stop, so
// the caller defers unconditionally. The stop func fires drain-then-gRPC-stop,
// matching the pre-0049 defer order (LIFO: drainDispatch before the bounded
// graceful stop, see stopGRPCWithin).
func startSchedulerSide(ctx context.Context, cfg *config.ServerConfig, pg *storage.Postgres, repo *storage.Repository, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, xcomSvc *xcom.Service, logSink logs.Sink, logTailer logs.Tailer, allowInsecureSecrets bool, servesScheduler bool, logger *slog.Logger, metrics *observability.Metrics) (health api.Heartbeater, podDispatch bool, stop func(), err error) {
	if !servesScheduler {
		return nil, false, func() {}, nil
	}
	// Warm-worker assignment registry (ADR 0058 N1b1-place). Built ONCE here, only
	// when warm pools are enabled, and shared between the gRPC handler (workers
	// register/ack on it via SetWarmPools) and the dispatcher's placer (Assign).
	// nil when warm pools are off => the handler stays inert (FailedPrecondition)
	// and the dispatcher's placer stays nil, so every path is byte-for-byte
	// today's dedicated pod-per-task; nothing reclaims and RequeueForRedispatch is
	// never called. The reclaim observer (ADR 0058 N1d-c, H2) re-places reclaimed
	// attempts through execStore for the double-run-safe reasons only.
	var warmReg *agentrpc.WorkerRegistry
	if cfg.Execution.WarmPoolsEnabled {
		warmReg = agentrpc.NewWorkerRegistry(func(ev agentrpc.ReclaimEvent) {
			handleReclaim(ctx, execStore, logger, ev)
		})
		logger.Warn("warm worker pools enabled (ADR 0058 N1b1-place); admitted tasks place on a free warm worker of their dag_version, else dispatch dedicated")
	}
	// Fail closed (#700): when the operator selected the exchange transport, the
	// Kubernetes client is a hard prerequisite. buildTokenExchange returns an
	// error here rather than leaving the exchange unwired, so the server refuses
	// to boot instead of advertising itself healthy while every task pod's
	// ExchangeToken bootstrap fails Unimplemented.
	xchg, xerr := buildTokenExchange(cfg, func() (kubernetes.Interface, error) {
		return buildK8sClient(cfg.Executor.KubeClient)
	})
	if xerr != nil {
		return nil, false, nil, xerr
	}
	// The bounded graceful stop reports how many handlers it left running, since
	// a log writer's final Put outlasts the post-force wait and an abandoned one
	// is otherwise silent. gRPC exposes no such number, so the counter rides on
	// the interceptor chain and the stop func reads it.
	inflight := agentrpc.NewInflightHandlers()
	grpcSrv, agentSrv, gerr := startAgentGRPC(ctx, cfg.Server.GRPCAddr, authn, execStore, repo, xcomSvc, logSink, logTailer, allowInsecureSecrets, cfg.Auth.SecretScoping, cfg.Auth.SecretLivenessMode, cfg.Auth.MaxAttemptCredentialLifetime, xchg, cfg.Server.GRPCTLSCert, cfg.Server.GRPCTLSKey, warmReg, inflight, logger)
	if gerr != nil {
		return nil, false, nil, gerr
	}
	// XCom-TTL and log-retention janitors are maintenance the scheduler owns; the
	// api role runs no background writers.
	startCleanup(ctx, storage.NewXComIndex(pg), logSink, cfg.Logs.Dir, logger)

	drain := func() {}
	if cfg.Scheduler.Enabled {
		sched, dispatchOn, dispatchCloser, serr := startScheduler(ctx, cfg, pg, repo, execStore, authn, warmReg, logSink, logger, metrics)
		if serr != nil {
			// Bounded, like every other stop of this server. At boot no stream is
			// open yet, so the unbounded form could not actually hang here — but a
			// second way to stop the same server is a way for the two to drift, and
			// the one that cannot exhaust the pod's grace is the one to keep.
			stopGRPCWithin(grpcSrv, grpcStopTimeout, logger, inflight.Count)
			return nil, false, nil, serr
		}
		// Warm-pool Hole B: gate the assignment stream to the scheduler LEADER. This
		// replica gates on ITS OWN leadership (sched.IsLeading), so behind one Service
		// exactly the leader — whose leader-only placer consults the SAME in-memory
		// registry workers register into here — serves AwaitAssignment; a follower
		// refuses so the worker reconnects toward the leader. Only with warm pools on;
		// off, the handler already refuses before the gate and nothing changes.
		if cfg.Execution.WarmPoolsEnabled {
			agentSrv.SetLeaderCheck(sched.IsLeading)
		}
		// On shutdown, drain the buffered dispatch pool (if any) so in-flight
		// dispatches settle (success or failed via the sink) instead of leaking
		// workers and leaving TIs stuck `queued` (#133). nil in Lite/passthrough.
		drain = func() { drainDispatch(dispatchCloser, logger) }
		health = sched
		podDispatch = dispatchOn
	}
	stop = func() {
		drain()
		stopGRPCWithin(grpcSrv, grpcStopTimeout, logger, inflight.Count)
	}
	return health, podDispatch, stop, nil
}

// grpcStopTimeout bounds the agent gRPC server's graceful stop on shutdown.
// Open log streams end at SIGTERM (agentrpc.Server.SetShutdown), so the
// graceful path normally finishes in milliseconds and the whole shutdown in well
// under a second; the bound is the safety net for a stream or unary call that
// does not end. The bounded WORST case is not inside the kubelet's default 30 s
// grace: the HTTP shutdown (10 s) and the dispatch drain (15 s, configurable)
// run before it, and stopGRPCWithin spends up to 2 × this timeout (graceful,
// then the wait for handlers after the forced stop), so a shutdown that hits
// every bound takes ~35 s plus the telemetry flush. That is the PROCESS bound,
// measured from SIGTERM; the pod's termination clock can be longer, because the
// chart's optional preStop sleep (deployment.preStopSleepSeconds, off by
// default, 5 in the HA profile) runs inside the same grace but BEFORE the signal
// — so the grace an operator needs is that sleep plus this bound. The default
// grace covers the normal shutdown; an installation running the object log sink
// at scale should raise terminationGracePeriodSeconds (the HA profile ships 60).
const grpcStopTimeout = 5 * time.Second

// grpcStopper is the subset of *grpc.Server the bounded stop needs; a fake
// stands in for it in tests.
type grpcStopper interface {
	GracefulStop()
	Stop()
}

// stopGRPCWithin stops the agent gRPC server gracefully, but for at most timeout;
// past it, Stop force-closes the transports. The unbounded GracefulStop waited
// on every in-flight RPC, and a task's StreamLogs stays open for the whole
// task, so with any task running the process exhausted the pod's termination
// grace and died by SIGKILL — the normal terminal state under load. Forcing
// cancels the remaining handlers' stream contexts; GracefulStop is still
// waiting for those handlers to return, and they return after their deferred
// log flushes, so the wait for it is kept (bounded again) and forced reports
// whether the fallback was taken.
//
// inflight (optional; nil omits the field) reports how many agent RPC handlers
// are still executing, and is logged on both bounded paths. A log writer's
// final Put is bounded at objectPutTimeout, which is longer than this timeout,
// so the process can legitimately exit with handlers still running: abandoning
// one is SAFE — a log object is written by a single atomic Put, so the stored
// object stays at its previous flush rather than being truncated — but without
// the count it is also silent, and nothing says how much log tail that cost.
func stopGRPCWithin(srv grpcStopper, timeout time.Duration, logger *slog.Logger, inflight func() int) (forced bool) {
	// inflightArgs renders the count as log attributes, or nothing when no
	// counter is wired, so an unwired caller logs a shorter line instead of a
	// misleading zero.
	inflightArgs := func() []any {
		if inflight == nil {
			return nil
		}
		return []any{"in_flight_handlers", inflight()}
	}
	done := make(chan struct{})
	go func() { srv.GracefulStop(); close(done) }()
	select {
	case <-done:
		return false
	case <-time.After(timeout):
	}
	logger.Warn("agent grpc graceful stop exceeded its bound; forcing stop so shutdown completes within the pod's grace",
		append([]any{"timeout", timeout}, inflightArgs()...)...)
	srv.Stop()
	select {
	case <-done:
	case <-time.After(timeout):
		logger.Error("agent grpc handlers did not finish after the forced stop; exiting without them (an abandoned log flush leaves the object at its previous flush, not truncated)",
			append([]any{"timeout", timeout}, inflightArgs()...)...)
	}
	return true
}

// buildAPIServer assembles the HTTP API + embedded UI server for the api/"all"
// role (ADR 0049). It is called only when the role serves the API, so the UI
// shell, editor FS, rate limiter, and full route set are never constructed in a
// scheduler-only process.
// startAPISide builds the HTTP API + embedded UI server, or nothing in a
// scheduler-only role (ADR 0049). It is factored out of run() so the OIDC-flow
// build and its error handling do not inflate run()'s complexity. Under
// provider: oidc it discovers the IdP and builds the login flow now, so a
// misconfigured issuer fails boot closed rather than at first login.
func startAPISide(ctx context.Context, cfg *config.ServerConfig, tel *observability.Telemetry, authn *auth.JWTAuthenticator, pg *storage.Postgres, repo *storage.Repository, xcomReader *storage.XComReader, logSink logs.Sink, logTailer logs.Tailer, checks map[string]api.HealthChecker, executorInfo api.ExecutorInfo, schedulerHealth api.Heartbeater, servesAPI bool) (*http.Server, error) {
	if !servesAPI {
		return nil, nil //nolint:nilnil // scheduler-only role serves no API; that is not an error
	}
	var oidcFlow *oidc.Flow
	if cfg.Auth.Provider == config.AuthProviderOIDC {
		flow, err := discoverOIDCFlow(ctx, cfg, tel.Logger)
		if err != nil {
			return nil, err
		}
		oidcFlow = flow
	}
	//nolint:contextcheck // buildAPIServer wires per-request handlers; each request carries its own context
	return buildAPIServer(cfg, tel, authn, pg, repo, xcomReader, logSink, logTailer, checks, executorInfo, schedulerHealth, oidcFlow, newTrustedIssuer(ctx, cfg)), nil
}

// oidcDiscoveryTimeout bounds the IdP discovery request on the boot path. It is
// a var so tests can shrink it; the value is what ships.
//
// go-oidc discovers over http.DefaultClient, which has no timeout, so an issuer
// that accepts the connection and never answers parks boot before the listener
// binds. 15s is generous for one well-known document over the public internet
// and far below the startup-probe budget that gates the pod
// (scripts/check-startup-probe-budget.sh reconciles the two).
var oidcDiscoveryTimeout = 15 * time.Second

// discoverOIDCFlow discovers the IdP and builds the Authorization Code + PKCE
// login flow. It is only called under provider: oidc (so it always returns a
// flow or an error, never nil,nil). The client secret comes from the environment
// (LEOFLOW_AUTH_OIDC_CLIENT_SECRET, bound by viper); it is never logged.
// Discovery failure is a boot failure — fail closed.

func discoverOIDCFlow(ctx context.Context, cfg *config.ServerConfig, logger *slog.Logger) (*oidc.Flow, error) {
	discCtx, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
	defer cancel()
	flow, err := oidc.NewFlow(discCtx, cfg.Auth.OIDC, cfg.Auth.JWT.Secret)
	if err != nil {
		// Keyed on the returned error rather than on discCtx.Err(): the deadline
		// belongs to this request only if the request is what hit it, and a
		// context that happens to expire while an unrelated error is on its way
		// back would otherwise be reported as a hung IdP. ctx.Err() == nil then
		// separates our own bound from a parent that was canceled or timed out,
		// which is a shutdown and not an IdP problem.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			// The prose stops at what a deadline actually proves. An accepted-and-
			// silent issuer, a name that never resolves, and a SYN dropped by a
			// NetworkPolicy all surface as the same context deadline, so claiming
			// the connection was accepted would point at the IdP for a failure
			// that is usually inside the cluster. The wrapped error carries the
			// URL and the transport-level detail that does separate them.
			return nil, fmt.Errorf("oidc setup: %s did not answer discovery within %s. "+
				"Nothing about the request completed, which narrows it to the failures that wait rather than refuse: "+
				"a NetworkPolicy or firewall that DROPs egress instead of REJECTing it, a name that never resolves, "+
				"an egress proxy or intercepting TLS middlebox holding the request open, or an issuer that accepts the "+
				"connection and never answers. A refused connection and an unknown host both fail fast instead. "+
				"Discovery runs before the HTTP listener binds, so without this bound the probe endpoint never comes up "+
				"and the pod restarts on a probe failure that names nothing: %w",
				cfg.Auth.OIDC.Issuer, oidcDiscoveryTimeout, err)
		}
		return nil, fmt.Errorf("oidc setup: %w", err)
	}
	logger.Info("oidc login enabled",
		"issuer", cfg.Auth.OIDC.Issuer,
		"jit_provisioning", cfg.Auth.OIDC.JITProvisioning,
		"break_glass_accounts", len(cfg.Auth.OIDC.BreakGlassEmails))
	return flow, nil
}

// newTrustedIssuer builds the #1284 verifier from auth.trusted_issuer, or nil
// when none is configured. It makes no network call: the JWKS is fetched on
// first use, so an issuer outage never blocks boot.
func newTrustedIssuer(ctx context.Context, cfg *config.ServerConfig) api.TrustedIssuer {
	s := cfg.Auth.TrustedIssuer
	if !s.Enabled() {
		return nil
	}
	return issuer.New(ctx, issuer.Config{
		Name:           s.Name,
		Issuer:         s.Issuer,
		JWKSURL:        s.JWKSURL,
		Audience:       s.Audience,
		TenantClaim:    s.TenantClaim,
		AllowedTenants: s.AllowedTenants,
		MaxLifetime:    time.Duration(s.MaxLifetimeSeconds) * time.Second,
	})
}

// newUIServer builds the embedded UI server from cfg and returns it with the
// Lite editor's workspace (nil when the editor is off).
func newUIServer(cfg *config.ServerConfig, logger *slog.Logger) (*ui.Server, api.WorkspaceFS) {
	// Show the LITE badge for the Lite edition (independent of the auth mode), and
	// also when the legacy dev auth bypass is on. The demo/production show neither.
	uiSrv := ui.New()
	uiSrv.SetLiteBanner(showLiteBadge(cfg))
	uiSrv.SetProBanner(showProBadge(cfg))
	uiSrv.SetInstanceName(cfg.UI.InstanceName)
	uiSrv.SetHomeLink(cfg.UI.HomeLink.Label, cfg.UI.HomeLink.URL)
	uiSrv.SetFavicon(cfg.UI.FaviconURL)
	uiSrv.SetStylesheets(cfg.UI.StylesheetURLs)

	editorFS := liteEditorFS(cfg, logger)
	uiSrv.SetEditorButton(editorFS != nil)
	return uiSrv, editorFS
}

// uiTheme returns ui.theme as raw JSON for /ui/config, or nil when unset.
// Validate has already checked it is a JSON object.
func uiTheme(cfg *config.ServerConfig) json.RawMessage {
	if cfg.UI.Theme == "" {
		return nil
	}
	return json.RawMessage(cfg.UI.Theme)
}

func buildAPIServer(cfg *config.ServerConfig, tel *observability.Telemetry, authn *auth.JWTAuthenticator, pg *storage.Postgres, repo *storage.Repository, xcomReader *storage.XComReader, logSink logs.Sink, logTailer logs.Tailer, checks map[string]api.HealthChecker, executorInfo api.ExecutorInfo, schedulerHealth api.Heartbeater, oidcFlow *oidc.Flow, trustedIssuer api.TrustedIssuer) *http.Server {
	if cfg.Auth.DevNoAuth {
		tel.Logger.Warn("AUTHENTICATION DISABLED (auth.dev_no_auth): every request is treated as admin. Dev only — NEVER use in production")
	}
	uiSrv, editorFS := newUIServer(cfg, tel.Logger)
	// Gzip the SPA bundle once, off the startup path, so no browser pays it.
	go uiSrv.Precompress()

	handler := api.NewServer(api.Dependencies{
		Logger:                       tel.Logger,
		Authenticator:                authn,
		RateLimiter:                  auth.NewRateLimiter(loginRateLimit(cfg), time.Minute),
		Registry:                     tel.Registry,
		Metrics:                      tel.Metrics,
		Tracer:                       tel.Tracer,
		HealthChecks:                 checks,
		CORSOrigins:                  cfg.Server.CORS.AllowedOrigins,
		TrustedProxies:               cfg.Server.TrustedProxies,
		TokenTTLSecs:                 cfg.Auth.JWT.TokenTTLSeconds,
		TokenRenewer:                 authn,
		TokenMaxLifetimeSecs:         cfg.Auth.JWT.MaxLifetimeSeconds,
		InstanceName:                 cfg.UI.InstanceName,
		UIAutoRefreshIntervalSeconds: cfg.UI.AutoRefreshIntervalSeconds,
		UITheme:                      uiTheme(cfg),
		UIETagRevalidation:           cfg.UI.ETagRevalidation,
		DevNoAuth:                    cfg.Auth.DevNoAuth,
		Edition:                      cfg.UI.Edition,

		Dags:            repo,
		DagRuns:         repo,
		Tasks:           repo,
		Versions:        repo,
		Xcoms:           xcomReader,
		Logs:            storage.NewLogReader(pg, logSink, logTailer),
		Specs:           repo,
		LatestRuns:      repo,
		TaskSummary:     repo,
		DagVersions:     repo,
		DashboardStats:  repo,
		AuditLog:        repo,
		Variables:       repo,
		Users:           repo,
		UserAudit:       repo,
		Connections:     repo,
		Pools:           repo,
		Favorites:       repo,
		ImportErrors:    repo,
		Audit:           repo,
		ExecutorInfo:    executorInfo,
		SchedulerHealth: schedulerHealth,
		UI:              uiSrv,
		Workspace:       editorFS,
		MonacoDir:       cfg.UI.MonacoDir,
		ExamplesFS:      dexaflow.ExampleDAGs(),

		// OIDC/SSO login flow (nil in JWT mode → routes not registered). The repo
		// resolves/JIT-provisions identities and records auth-event audit.
		OIDCFlow:           oidcFlow,
		OIDCEnabled:        cfg.Auth.Provider == config.AuthProviderOIDC,
		OIDCSettings:       cfg.Auth.OIDC,
		ExternalSignInURL:  cfg.Auth.ExternalSignInURL,
		ExternalSignOutURL: cfg.Auth.ExternalSignOutURL,
		OIDCUsers:          repo,
		AuthAudit:          repo,
		JWTSecret:          cfg.Auth.JWT.Secret,

		// Trusted-issuer handoff (#1284): nil unless auth.trusted_issuer is set,
		// which leaves POST /api/v2/auth/session unregistered.
		TrustedIssuer:        trustedIssuer,
		TrustedIssuerUsers:   repo,
		TrustedIssuerOrigins: cfg.Auth.TrustedIssuer.AllowedOrigins,
		// Operator service API (#1283): off unless auth.service_token is set.
		ServiceToken:   cfg.Auth.ServiceToken,
		ServiceTenants: repo,
		// The service API links users only in tenants the issuer may sign in to.
		ServiceAllowedTenants: cfg.Auth.TrustedIssuer.AllowedTenants,

		SessionCookieInsecure: cfg.Auth.SessionCookieInsecure,
	})
	return &http.Server{Addr: cfg.Server.HTTPAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
}

// executorDispatchEnabled decides what /api/v2/monitor/executor reports for
// pod_dispatch_enabled. The scheduler owns the executor, so on the scheduler/all
// role the accurate runtime signal (whether a K8s client actually wired up) is
// right. In the split api role (ADR 0049) the scheduler runs in another process,
// so the runtime bool is always false here — reporting that would tell an
// operator dispatch is off when it isn't (F1). Fall back to the configured
// capability (executor.type), the config-derived ExecutorInfo the ADR calls for.
func executorDispatchEnabled(cfg *config.ServerConfig, servesScheduler, runtimePodDispatch bool) bool {
	if servesScheduler {
		return runtimePodDispatch
	}
	return cfg.Executor.Type == "kubernetes"
}

// serveHTTP starts the process's HTTP listeners — the api role's API+UI (when
// servesAPI) plus /metrics in every role, so a scheduler-only pod stays scrapable
// — and blocks until one errors or ctx is canceled, then shuts them down.
func serveHTTP(ctx context.Context, logger *slog.Logger, servesAPI bool, apiSrv, metricsSrv *http.Server) error {
	servers := []*http.Server{metricsSrv}
	if servesAPI {
		servers = append([]*http.Server{apiSrv}, servers...)
	}
	errCh := make(chan error, len(servers))
	for _, srv := range servers {
		go serve(srv, errCh)
	}
	return awaitShutdown(ctx, errCh, logger, servers...)
}

// startAgentGRPC starts the AgentService gRPC server and returns it for graceful
// shutdown. TLS is enabled when tlsCert/tlsKey are set (issue #58); otherwise the
// channel is plaintext (dev). The per-task bearer token in metadata authenticates
// each call regardless. inflight (required) is installed on the interceptor
// chain so the bounded stop can report the handlers it leaves running.
func startAgentGRPC(ctx context.Context, addr string, authn *auth.JWTAuthenticator, store *storage.ExecutionStore, secretsStore agentrpc.SecretsStore, xcomSvc agentrpc.XComService, logSink agentrpc.LogSink, logTailer agentrpc.LogPublisher, allowInsecureSecrets bool, secretScoping, secretLivenessMode string, maxAttemptLifetime time.Duration, exchange *tokenExchange, tlsCert, tlsKey string, warmPools *agentrpc.WorkerRegistry, inflight *agentrpc.InflightHandlers, logger *slog.Logger) (srv *grpc.Server, agentSrv *agentrpc.Server, err error) {
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listening for agent grpc on %s: %w", addr, err)
	}
	agentSrv = agentrpc.NewServer(authn, store, xcomSvc)
	// End open task log streams as soon as shutdown begins (ctx is the SIGTERM
	// context), so each stream's deferred log flush runs while the process is
	// alive and the bounded graceful stop below is not held open by running tasks.
	agentSrv.SetShutdown(ctx)
	agentSrv.SetLogSink(logSink)
	agentSrv.SetLogPublisher(logTailer)
	agentSrv.SetSecrets(secretsStore, allowInsecureSecrets)
	// Refresh a live attempt's bearer on every heartbeat (ADR 0055 Fix #4) with the
	// same short per-attempt TTL used at dispatch, so a long task keeps a working
	// credential while a stolen/finished token lapses in minutes. The renewal total
	// is capped by auth.max_attempt_credential_lifetime.
	agentSrv.SetTokenRenewal(authn, attemptTokenTTL, maxAttemptLifetime)
	// Scope-by-declaration policy (ADR 0055 D9). Default permissive: the whole
	// tenant vault, warning on a narrow declaration but never denying.
	agentSrv.SetSecretScoping(secretScoping)
	// Bind secret delivery to task-instance liveness (ADR 0055 E2). The
	// ExecutionStore is the read-only liveness predicate; the mode defaults to
	// observe (log a would-have-denied, still deliver). Secret-path only.
	agentSrv.SetLivenessGate(store, secretLivenessMode)
	// Projected-SA-token exchange (ADR 0055 Fix #3). Wired only when the operator
	// selected the exchange transport on a Kubernetes executor; nil (the default)
	// leaves ExchangeToken Unimplemented, so the env-var transport is unchanged.
	if exchange != nil {
		agentSrv.SetTokenExchange(exchange.reviewer, exchange.resolver, authn, attemptTokenTTL, allowInsecureSecrets)
		logger.Warn("agent token exchange enabled (projected SA token → TokenReview → task-scoped JWT); real-cluster e2e owed before flipping this on in production")
	}
	// The secrets store is the Repository, which also records the warn-phase
	// secret-scope audit event (ADR 0045, ADR 0055); wire it as the auditor so a
	// narrowly-declared task's full-vault delivery is surfaced. Optional: without
	// it the WARN log still fires.
	if auditor, ok := secretsStore.(agentrpc.SecretScopeAuditor); ok {
		agentSrv.SetSecretScopeAuditor(auditor)
	}
	// The Repository also records the secret-path liveness would-have-denied /
	// denial audit event (ADR 0055 E2); wire it so the observe phase is
	// observable. Optional: without it the WARN log still fires.
	if auditor, ok := secretsStore.(agentrpc.SecretLivenessAuditor); ok {
		agentSrv.SetSecretLivenessAuditor(auditor)
	}
	// Warm worker assignment transport (ADR 0058 N1b1-place). The registry is built
	// once by startSchedulerSide and shared: the dispatcher's placer Assign()s onto
	// the SAME instance whose workers register/ack here. nil (warm pools off, the
	// default) leaves AwaitAssignment inert (FailedPrecondition) and no running path
	// changes.
	if warmPools != nil {
		agentSrv.SetWarmPools(warmPools)
	}

	// Recover panics in any agent RPC handler so a single malformed request from a
	// worker pod cannot crash the control plane (it returns Internal instead).
	// The in-flight counter sits INSIDE recovery so recovery stays outermost (it
	// must cover every later interceptor), and so a panicking handler is counted
	// out by the counter's own defer before recovery translates the panic.
	opts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(agentrpc.RecoveryUnaryInterceptor(logger), inflight.UnaryInterceptor()),
		grpc.ChainStreamInterceptor(agentrpc.RecoveryStreamInterceptor(logger), inflight.StreamInterceptor()),
		// Tolerate a warm worker's keepalive pings on an otherwise idle assignment
		// stream so gRPC does not GOAWAY it as abusive (ADR 0058 N1b). This only
		// RELAXES the server's default client-ping enforcement (5m, streams-only),
		// so it cannot change StreamLogs behavior — an active StreamLogs already
		// satisfies both the old and new policy. Server-initiated keepalive
		// (ServerParameters: MaxConnectionIdle / Time pings) is deliberately NOT
		// added here: it would start pinging existing idle StreamLogs connections,
		// a behavior change with no benefit until a real warm worker connects.
		// Deferred to N1b1-place as a documented TODO.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}
	secure := tlsCert != "" && tlsKey != ""
	if secure {
		creds, cerr := credentials.NewServerTLSFromFile(tlsCert, tlsKey)
		if cerr != nil {
			return nil, nil, fmt.Errorf("loading agent grpc TLS cert: %w", cerr)
		}
		opts = append(opts, grpc.Creds(creds))
	}
	srv = grpc.NewServer(opts...)
	agentv1.RegisterAgentServiceServer(srv, agentSrv)
	go func() {
		if serr := srv.Serve(lis); serr != nil && !errors.Is(serr, grpc.ErrServerStopped) {
			logger.Error("agent grpc server", "error", serr)
		}
	}()
	logger.Info("agent grpc server started", "grpc_addr", addr, "tls", secure)
	return srv, agentSrv, nil
}

// tokenExchange bundles the Kubernetes-backed dependencies of the agent
// token-exchange transport (ADR 0055 Fix #3): the TokenReview client and the
// pod→task-instance resolver. nil means the exchange is not wired (the default
// env-var transport), in which case ExchangeToken reports Unimplemented.
type tokenExchange struct {
	reviewer agentrpc.TokenReviewer
	resolver agentrpc.PodTaskResolver
}

// buildTokenExchange constructs the exchange dependencies when the operator
// selected the exchange transport on a Kubernetes executor; it returns (nil,
// nil) otherwise (env-var default, or the subprocess executor, which has no
// pod/SA/TokenReview and needs no client). newK8sClient is the seam that
// supplies the Kubernetes client (buildK8sClient in production); it is consulted
// only when the exchange transport is actually selected.
//
// When the exchange transport IS selected on a Kubernetes executor and no client
// can be built, this FAILS CLOSED and returns an error so the server refuses to
// boot. An unwired exchange makes ExchangeToken report Unimplemented, so every
// task pod fails at bootstrap while the control plane reports itself healthy —
// a fail-open on a security-critical transport the operator explicitly opted
// into. The transport flip is a deliberate opt-in, which is an argument for
// honoring it strictly, not for silently ignoring it.
func buildTokenExchange(cfg *config.ServerConfig, newK8sClient func() (kubernetes.Interface, error)) (*tokenExchange, error) {
	if cfg.Auth.AgentTokenTransport != config.AgentTokenTransportExchange || cfg.Executor.Type != "kubernetes" {
		return nil, nil //nolint:nilnil // env-var transport or non-Kubernetes executor needs no exchange; a nil exchange is the correct wiring, not an error
	}
	cs, err := newK8sClient()
	if err != nil {
		return nil, fmt.Errorf("agent token transport %q requires a Kubernetes client, but none is available (no in-cluster config or kubeconfig): %w", config.AgentTokenTransportExchange, err)
	}
	return &tokenExchange{
		reviewer: kubeexchange.NewTokenReviewer(cs, executor.DefaultAgentTokenAudience),
		resolver: kubeexchange.NewPodResolver(cs, cfg.Executor.TaskNamespace),
	}, nil
}

// loadK8sRESTConfig loads the in-cluster config or, failing that, the local
// kubeconfig. It returns an error when neither is available, in which case pod
// dispatch is disabled and tasks have no executor to run them.
func loadK8sRESTConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("no in-cluster config or kubeconfig: %w", err)
		}
	}
	// Bound every API call. Workers dispatch with a detached context (they
	// already accepted responsibility for the task), so without a client-side
	// deadline an apiserver that accepts the connection and never answers hangs
	// a worker for good, and with it the shutdown drain (#463).
	cfg.Timeout = k8sClientTimeout
	return cfg, nil
}

// buildK8sClient builds one Kubernetes client with the dispatch limits. The agent
// token exchange uses it, so its TokenReviews and pod reads keep their own token
// bucket, as before.
func buildK8sClient(limits config.KubeClientSection) (kubernetes.Interface, error) {
	base, err := loadK8sRESTConfig()
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(k8sClientConfig(base, limits.QPS, limits.Burst))
	if err != nil {
		return nil, fmt.Errorf("building kubernetes client: %w", err)
	}
	return cs, nil
}

// k8sClients holds the pod-path Kubernetes clients. dispatch creates task pods;
// maintenance serves the pod informer, reconciler, reapers, staging GC and warm
// pool reconciler. They are the same client unless maintenance limits are set.
type k8sClients struct {
	dispatch    kubernetes.Interface
	maintenance kubernetes.Interface
}

// buildK8sClients builds the pod-path clients from the in-cluster config or the
// local kubeconfig.
func buildK8sClients(limits config.KubeClientSection) (k8sClients, error) {
	base, err := loadK8sRESTConfig()
	if err != nil {
		return k8sClients{}, err
	}
	return newK8sClients(base, limits)
}

// newK8sClients builds the dispatch client and, when MaintenanceQPS is set, a
// separate maintenance client. client-go gives every clientset its own token
// bucket, so the split keeps a maintenance burst (a reconciler sweep, a GC pass)
// from spending the budget pod creation depends on. Unset, both roles share one
// client and one budget, exactly as before the split.
func newK8sClients(base *rest.Config, limits config.KubeClientSection) (k8sClients, error) {
	dispatchCS, err := kubernetes.NewForConfig(k8sClientConfig(base, limits.QPS, limits.Burst))
	if err != nil {
		return k8sClients{}, fmt.Errorf("building kubernetes dispatch client: %w", err)
	}
	if limits.MaintenanceQPS <= 0 {
		return k8sClients{dispatch: dispatchCS, maintenance: dispatchCS}, nil
	}
	maintCS, err := kubernetes.NewForConfig(k8sClientConfig(base, limits.MaintenanceQPS, limits.MaintenanceBurst))
	if err != nil {
		return k8sClients{}, fmt.Errorf("building kubernetes maintenance client: %w", err)
	}
	return k8sClients{dispatch: dispatchCS, maintenance: maintCS}, nil
}

// k8sClientConfig copies base with the given client-side limits. A non-positive
// QPS or burst falls back to client-go's default, never to an unthrottled client
// or one that can never send.
func k8sClientConfig(base *rest.Config, qps float64, burst int) *rest.Config {
	cfg := rest.CopyConfig(base)
	cfg.QPS = rest.DefaultQPS
	if qps > 0 {
		cfg.QPS = float32(qps)
	}
	cfg.Burst = rest.DefaultBurst
	if burst > 0 {
		cfg.Burst = burst
	}
	return cfg
}

// k8sClientTimeout caps a single Kubernetes API call. Generous enough for a pod
// create against a loaded apiserver, short enough that a hung call is cut well
// inside the dispatch drain timeout.
const k8sClientTimeout = 10 * time.Second

// reconcileInterval is how often the pod reconciler sweeps for failed pods.
const reconcileInterval = 30 * time.Second

// cleanupInterval is how often expired XCom index rows and old logs are purged.
const cleanupInterval = time.Hour

// logRetention is how long task logs are kept before garbage collection.
const logRetention = 30 * 24 * time.Hour

// lowDiskWarnBytes is the free-space threshold below which the janitor warns: the
// embedded datastore (managed Postgres + logs) lives on this filesystem, and a
// full disk makes Postgres fail writes with a cryptic error.
const lowDiskWarnBytes = 1 << 30 // 1 GiB

// startCleanup runs a periodic janitor that purges expired XCom index rows,
// prunes old log files, and warns on low disk for the datastore dir. The
// buildLogSink selects the durable task-log sink from configuration. The default
// (logs.backend "disk" or unset) is the on-disk sink — unchanged behavior for
// Lite and every deployment that does not opt in. "s3" ships each attempt to an
// S3-compatible bucket (AWS S3, MinIO, Ceph RGW) via aws-sdk-go-v2; "gcs" ships
// to Google Cloud Storage via its native SDK. Both default to keyless auth
// (ADR 0035): IRSA/instance-profile for S3, GKE Workload Identity for GCS. ctx
// bounds the store operations for the sink's lifetime. The config layer has
// already validated that an object backend carries a bucket
// (config.validateLogs), so a nil sink here is a real build error.
func buildLogSink(ctx context.Context, cfg *config.ServerConfig, logger *slog.Logger) (logs.Sink, error) {
	switch cfg.Logs.Backend {
	case "", "disk":
		return logs.NewDurableSink(ctx, cfg.Logs.Backend, cfg.Logs.Dir, nil, "", logger)
	case "s3":
		store, err := logs.NewS3Store(ctx, logs.S3Config{
			Bucket:          cfg.Logs.Sink.Bucket,
			Region:          cfg.Logs.Sink.Region,
			Endpoint:        cfg.Logs.Sink.Endpoint,
			ForcePathStyle:  cfg.Logs.Sink.ForcePathStyle,
			AccessKeyID:     cfg.Logs.Sink.AccessKeyID,
			SecretAccessKey: cfg.Logs.Sink.SecretAccessKey,
		})
		if err != nil {
			return nil, fmt.Errorf("building s3 log store: %w", err)
		}
		logger.Info("task logs: s3 object-store backend enabled",
			"bucket", cfg.Logs.Sink.Bucket, "endpoint", cfg.Logs.Sink.Endpoint, "prefix", cfg.Logs.Sink.Prefix)
		return logs.NewDurableSink(ctx, "s3", "", store, cfg.Logs.Sink.Prefix, logger)
	case "gcs":
		store, err := logs.NewGCSStore(ctx, logs.GCSConfig{
			Bucket:          cfg.Logs.Sink.Bucket,
			CredentialsFile: cfg.Logs.Sink.CredentialsFile,
		})
		if err != nil {
			return nil, fmt.Errorf("building gcs log store: %w", err)
		}
		logger.Info("task logs: gcs object-store backend enabled",
			"bucket", cfg.Logs.Sink.Bucket, "prefix", cfg.Logs.Sink.Prefix)
		return logs.NewDurableSink(ctx, "gcs", "", store, cfg.Logs.Sink.Prefix, logger)
	default:
		return nil, fmt.Errorf("unknown logs.backend %q", cfg.Logs.Backend)
	}
}

// operations are idempotent, so it is safe on every replica.
func startCleanup(ctx context.Context, idx *storage.XComIndex, sink logs.Sink, dataDir string, logger *slog.Logger) {
	// Only a self-managing sink prunes: the disk sink deletes old files, while the
	// object-store sink leaves retention to bucket lifecycle policy and does not
	// implement logs.Pruner, so the janitor skips it.
	pruner, canPrune := sink.(logs.Pruner)
	go func() {
		t := time.NewTicker(cleanupInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				safeCycle("cleanup", logger, func() {
					if err := idx.PurgeExpired(ctx); err != nil {
						logger.Error("purging expired xcom index", "error", err)
					}
					if canPrune {
						if err := pruner.Prune(time.Now(), logRetention); err != nil {
							logger.Error("pruning old logs", "error", err)
						}
					}
					if free, derr := dirFreeBytes(dataDir); derr == nil && lowDisk(free, lowDiskWarnBytes) {
						logger.Warn("low disk space for the datastore", "dir", dataDir,
							"free_mb", free/(1<<20), "threshold_mb", uint64(lowDiskWarnBytes)/(1<<20))
					}
				})
			}
		}
	}()
}

// lowDisk reports whether free is below the threshold (both in bytes).
func lowDisk(free, threshold uint64) bool { return free < threshold }

// dirFreeBytes returns the bytes available on the filesystem holding dir.
func dirFreeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	return st.Bavail * uint64(st.Bsize), nil //nolint:gosec // G115: a filesystem block size is never negative
}

// safeCycle runs one iteration of a periodic background loop, recovering any
// panic so a single bad cycle logs and is retried next tick instead of crashing
// the goroutine (and, since an unrecovered panic in any goroutine aborts the
// program, the whole control plane). It changes no behavior on the happy path.
func safeCycle(name string, logger *slog.Logger, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("recovered panic in background cycle", "cycle", name, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	fn()
}

// xcomSweepInterval is how often the embedded Postgres XCom store is swept of
// expired rows. Redis expires keys natively; Postgres needs a sweep (ADR 0026).
const xcomSweepInterval = 10 * time.Minute

// selectDatastore chooses the XCom backend and live-log tailer for the run. With
// a configured Redis URL it uses the production Redis backends (ADR 0006); with
// no Redis configured it uses the embedded backends — XCom on Postgres and an
// in-process tailer — so Lite needs no Redis (ADR 0026). It returns a health
// checker for Redis (nil when embedded) and a cleanup to defer.
func selectDatastore(ctx context.Context, cfg *config.ServerConfig, pg *storage.Postgres, logger *slog.Logger, metrics *observability.Metrics) (xcom.Backend, logs.Tailer, api.HealthChecker, func(), error) {
	if cfg.Redis.URL == "" {
		logger.Info("embedded datastore: XCom on Postgres, in-process log tailer (no Redis)")
		backend := xcom.NewPostgresBackend(pg.Pool)
		startXComSweep(ctx, backend, logger)
		return backend, logs.NewMemoryTailer(), nil, func() {}, nil
	}
	rd, err := storage.NewRedis(ctx, cfg.Redis)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("redis: %w", err)
	}
	// Attach the per-reason error counter, dial-latency histogram, and pool
	// gauges (#312 sibling — same shape as the #311 step-down observability).
	// 5s scrape interval is comfortably under the default 15s Prometheus
	// scrape, so a P99 spike never reaches the dashboard from missing data.
	// Lite already returned above (no Redis), so metrics is always non-nil
	// here on the Pro path.
	stopObs := storage.AttachRedisObservability(ctx, rd, metrics, 5*time.Second)
	cleanup := func() {
		stopObs()
		if cerr := rd.Close(); cerr != nil {
			logger.Error("closing redis", "error", cerr)
		}
	}
	return xcom.NewRedisBackend(rd.Client), logs.NewRedisTailer(rd.Client), rd, cleanup, nil
}

// healthChecks builds the health checks read by /readyz and
// /api/v2/monitor/health, including Redis only when it is the active datastore
// (redisHealth is nil in the embedded edition).
//
// The "postgres" entry must keep satisfying api.SchemaChecker. Both endpoints
// discover the schema assertion with a runtime type assertion on the VALUE in
// this map, and an assertion that stops matching is silent — they would fall
// back to Ping-only and report ready over an empty database with every test
// still green, which is #1023. A compile-time `var _ api.SchemaChecker =
// (*storage.Postgres)(nil)` does not cover that: it pins the TYPE, so wrapping
// the value in any decorator that implements only Ping (metrics, a circuit
// breaker, a tenant router) still compiles. The guard is on the value, in
// TestHealthChecksPostgresCarriesTheSchemaAssertion.
func healthChecks(pg *storage.Postgres, redisHealth api.HealthChecker) map[string]api.HealthChecker {
	checks := map[string]api.HealthChecker{"postgres": pg}
	if redisHealth != nil {
		checks["redis"] = redisHealth
	}
	return checks
}

// startXComSweep periodically deletes expired rows from the embedded Postgres
// XCom store (the durable equivalent of Redis's native key expiry, ADR 0026).
func startXComSweep(ctx context.Context, backend *xcom.PostgresBackend, logger *slog.Logger) {
	go func() {
		t := time.NewTicker(xcomSweepInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				safeCycle("xcom-sweep", logger, func() {
					if n, err := backend.DeleteExpired(ctx); err != nil {
						logger.Error("sweeping expired xcom", "error", err)
					} else if n > 0 {
						logger.Debug("swept expired xcom", "rows", n)
					}
				})
			}
		}
	}()
}

// startGatedTicker runs fn every interval on a background goroutine, but only
// while leading() reports this instance holds leadership. It stops when ctx is
// done. The pod reconciler and staging GC mutate cluster state, so at
// replicaCount>1 they must run on the leader alone (ADR 0009/0031); a nil gate
// means "always run" (single-replica / no election).
func startGatedTicker(ctx context.Context, name string, interval time.Duration, leading func() bool, logger *slog.Logger, fn func()) {
	t := time.NewTicker(interval)
	go func() {
		defer t.Stop()
		runGatedTicker(ctx, name, t.C, leading, logger, fn)
	}()
}

// runGatedTicker is the loop of startGatedTicker with the tick source injected,
// so the leadership gate is testable without a real timer. Each cycle is
// panic-isolated by safeCycle. A tick that arrives while not leading is dropped,
// not queued.
func runGatedTicker(ctx context.Context, name string, ticks <-chan time.Time, leading func() bool, logger *slog.Logger, fn func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			if leading != nil && !leading() {
				continue
			}
			safeCycle(name, logger, fn)
		}
	}
}

// podInformerSyncTimeout bounds the informer cache warm-up on the boot path. The
// warm cache is an optimization (every consumer falls back to a live read), so
// waiting longer than this buys nothing a running control plane cannot get on its
// own, while waiting unbounded costs the HTTP and metrics listeners entirely
// (#1083). A var so a test can shorten it; nothing changes it at runtime.
var podInformerSyncTimeout = 10 * time.Second

// buildPodInformer constructs and starts the shared pod informer for the
// scheduler read-path (PR-10), returning nil when this process must NOT watch
// pods: the api-only role (ADR 0049 — the sibling scheduler process owns the
// read-path) or a non-Kubernetes executor (no pods to watch). A nil return leaves
// the reapers and reconciler on their live read paths — today's behavior. The
// informer is always-on for the process lifetime, warming the cache before
// leadership; a not-yet-synced cache is safe because its readings only ever DEFER
// a reap (#461), never authorize one.
func buildPodInformer(ctx context.Context, cfg *config.ServerConfig, cs kubernetes.Interface, logger *slog.Logger) *executor.PodInformer {
	if !cfg.Server.ServesScheduler() || cfg.Executor.Type != "kubernetes" {
		return nil
	}
	pi := executor.NewPodInformer(cs, cfg.Executor.TaskNamespace)
	pi.Start(ctx)
	// Bound the warm-up. This wait is on the boot path, ahead of startAPISide and
	// therefore ahead of the HTTP and metrics listeners, and cache.WaitForCacheSync
	// returns only on sync or on its context being canceled. Handed the process
	// context, canceled at shutdown, a cache that cannot sync held boot forever:
	// gRPC served, /readyz never bound, the liveness probe on that same port killed
	// the container about every 70s, and each cycle was recorded as
	// `Completed exit=0` because SIGTERM is handled cleanly. A dependency failure
	// that reports itself as a success (#1083). The usual cause is a ServiceAccount
	// without list/watch on pods in the task namespace, which the reflector retries
	// forever.
	//
	// Not syncing is already survivable by design: CachedPodActive gates on
	// HasSynced and consumers keep their live read paths, and HasSynced stays live
	// so a cache that warms later (an RBAC fix) is picked up. That fallback was
	// correct and unreachable; the deadline is what makes it reachable.
	syncCtx, cancel := context.WithTimeout(ctx, podInformerSyncTimeout)
	defer cancel()
	switch {
	case pi.WaitForCacheSync(syncCtx):
		logger.Info("pod informer cache synced; reaper/reconciler read-path warm", "namespace", cfg.Executor.TaskNamespace)
	case errors.Is(syncCtx.Err(), context.DeadlineExceeded):
		// Say the cause, not just the symptom: without this line the operator has a
		// readiness failure and a klog line from a vendored library to correlate.
		logger.Warn("pod informer cache did not sync within the boot budget; continuing with live reads",
			"namespace", cfg.Executor.TaskNamespace,
			"budget", podInformerSyncTimeout,
			"likely_cause", "the ServiceAccount cannot list/watch pods in this namespace, or the Kubernetes API is unreachable",
			"effect", "reapers and the reconciler read pods live; no reap is authorized from a cold cache")
	default:
		logger.Warn("pod informer cache did not sync before shutdown; reapers use live reads until it warms")
	}
	return pi
}

// startMaintenance runs the leader's periodic recovery work — the pod
// reconciler's sweep and the execution reapers — as ONE ordered cycle on one
// leader-gated ticker, every reconcileInterval: reconcile, then reap. The
// reapers used to run from the scheduler's 1s tick while the reconciler swept on
// its own 30s timer; two independent clocks gave no ordering, so a reaper could
// judge a task pod "gone" at 1s that the reconciler would have recovered as
// succeeded at its next sweep. In one cycle the reap always sees post-reconcile
// state, and the reaper's settling gate reads the reconciler's sweep record to
// know a sweep has completed under this leadership. The cost is detection
// latency: a stuck run/TI is now noticed up to one interval (30s) later than at
// the 1s tick — negligible against 60s–5m thresholds, and reaping is a backstop,
// never the primary path. Each phase runs under its own one-interval budget
// (maintenancePhaseTimeout). Lite/subprocess never calls this: no pods, no
// reaping.
func startMaintenance(ctx context.Context, cs kubernetes.Interface, namespace string, reporter executor.OutcomeReporter, reaper *executor.Reaper, leading func() bool, logger *slog.Logger, snapshotter executor.PodSnapshotter) {
	rec := executor.NewReconciler(cs, namespace, reporter)
	// Read task pods from the shared informer cache instead of a live LIST every
	// tick when the informer is wired (PR-10); nil keeps the live LIST.
	if snapshotter != nil {
		rec.SetPodSnapshotter(snapshotter)
	}
	reaper.SetLastSweepCompleted(rec.LastSweepCompletedAt)
	startGatedTicker(ctx, "maintenance", reconcileInterval, leading, logger, func() {
		maintenanceCycle(ctx, maintenancePhaseTimeout, rec.Reconcile, reaper.ReapOnce, logger)
	})
}

// maintenancePhaseTimeout bounds each phase of a maintenance cycle — the
// reconciler's sweep and the reaper pass — separately, at one interval. The
// reapers used to run inside the scheduler tick under its step timeout; sharing
// one goroutine with the reconciler must not lose that bound: a sweep hung on a
// slow apiserver would otherwise starve the reap indefinitely, and a reap that
// issues one live LIST per running task instance (plus lock-blocked UPDATEs)
// would starve the very sweep it depends on. Each phase gets its own FRESH
// budget, so a reconcile that spends all of its own still hands the reap a live
// context — the reaper's destructive gate reads ctx.Err() and a dead context
// would silently disable reaping rather than fail loudly. A cycle can therefore
// take up to two intervals; the ticker coalesces the ticks it overruns, and the
// settling gate's "two cycles inside the grace" still holds by construction.
const maintenancePhaseTimeout = reconcileInterval

// maintenanceCycle is one pass of the maintenance loop: reconcile, then reap,
// in that order, always, each under its own phaseTimeout. A reconcile error
// (the LIST failed) is logged and the reapers still run — they are independent
// backstops with their own fail-closed liveness reads, and whether a leader that
// has never completed a sweep may reap is the reaper's settling gate's decision,
// not this loop's.
func maintenanceCycle(ctx context.Context, phaseTimeout time.Duration, reconcile, reap func(context.Context) error, logger *slog.Logger) {
	runMaintenancePhase(ctx, "pod reconcile", phaseTimeout, reconcile, logger)
	runMaintenancePhase(ctx, "execution reaper", phaseTimeout, reap, logger)
}

// runMaintenancePhase runs one phase under its own deadline. Overrunning the
// budget is a load signal (a large namespace, a slow apiserver), not a bug, so
// it logs at WARN with the budget; any other error logs at ERROR. Nothing is
// logged once the parent context is done — that cancel fan-out at shutdown is
// expected, not a finding.
func runMaintenancePhase(ctx context.Context, name string, timeout time.Duration, fn func(context.Context) error, logger *slog.Logger) {
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := fn(pctx)
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(pctx.Err(), context.DeadlineExceeded):
		logger.Warn(name+" exceeded its maintenance budget", "budget", timeout, "error", err)
	case err != nil:
		logger.Error(name, "error", err)
	}
}

// startWarmPoolReconciler runs the leader-gated warm-pool reconciler (ADR 0058
// N1b2b + N1d-b, model A2): each tick it keeps EffectiveMinIdle IDLE warm workers
// ready per active dag_version, growing the pool past min_idle under load up to
// MaxPoolSize and deleting only idle/terminal workers (never a busy one). It reads
// the busy set (busy) once per tick to classify workers. Like the pod reconciler
// it mutates cluster state, so it runs on the leader alone via the gated ticker.
// It is only called when warm pools are enabled.
func startWarmPoolReconciler(ctx context.Context, targets executor.WarmTargetSource, pods executor.WarmPodClient, busy executor.BusyWarmWorkerSource, maxWarmPodsPerTenant int, leading func() bool, rec executor.DecisionRecorder, logger *slog.Logger) {
	rc := executor.NewWarmPoolReconciler(targets, pods, busy, maxWarmPodsPerTenant, logger, rec)
	startGatedTicker(ctx, "warm-pool-reconcile", reconcileInterval, leading, logger, func() {
		if err := rc.Reconcile(ctx); err != nil {
			logger.Error("warm pool reconcile", "error", err)
		}
	})
}

// warmPodSpecFunc returns the per-target warm-pod spec builder the warm-pod client
// calls on each create. It mints a fresh, UNIQUE bootstrap credential per pod
// (each worker registers as a distinct member of its dag_version pool) and stamps
// the connection + hardening fields.
//
// The bootstrap credential authorizes ONLY Register + AwaitAssignment (the warm
// control channel) and resolves NO secrets or task — a warm-worker-scoped token
// (ADR 0058 D2). It reaches the pod by the operator's auth.agent_token_transport:
//
//   - exchange (the transport the N1a boot guard forces on when warm pools are
//     enabled): the pod projects a ServiceAccount token and NO plaintext token; the
//     warm agent exchanges it once per connect, and the control plane — recognizing
//     the warm-worker LABEL — mints the worker-scoped JWT. This is the default and
//     the whole point of this change: the bootstrap credential is never a plaintext
//     env var on the pod.
//   - env-var (dev / explicit fallback): a worker-scoped JWT is minted here and
//     ridden in as a plaintext env var. Even this fallback is control-channel-only,
//     so a leaked warm bootstrap token can never read the vault.
//
// The per-attempt token — the one that resolves secrets — arrives in-band over
// AwaitAssignment and is exchange/liveness-gated upstream (N1b1/N1b2a).
func warmPodSpecFunc(cfg *config.ServerConfig, authn *auth.JWTAuthenticator, controlAddr string) executor.WarmPodSpecFunc {
	defaults := platformDefaults(cfg.Executor.Defaults)
	useExchange := cfg.Auth.AgentTokenTransport == config.AgentTokenTransportExchange
	return func(t executor.WarmTarget) (executor.WarmPodSpec, error) {
		spec := executor.WarmPodSpec{
			DagVersionID:        t.DagVersionID,
			Image:               t.Image,
			TenantID:            t.TenantID,
			ControlPlaneAddr:    controlAddr,
			AgentTLSCAConfigMap: cfg.Executor.AgentTLSCAConfigMap,
			ServiceAccount:      cfg.Executor.TaskServiceAccount,
			PodSecurity:         defaults.PodSecurity,
			// Self-lifecycle caps (ADR 0058 D9/D10/D6/H3). The attempt watchdog is
			// anchored to the credential ceiling: an attempt can never validly outlive
			// its per-attempt credential, so max_attempt_credential_lifetime is the
			// natural hard upper bound and needs no separate operator knob.
			MaxAttemptsPerWorker:     cfg.Execution.MaxAttemptsPerWorker,
			MaxWorkerLifetimeSeconds: int64(cfg.Execution.MaxWorkerLifetime.Seconds()),
			WorkerIdleTTLSeconds:     int64(cfg.Execution.WorkerIdleTTL.Seconds()),
			AttemptWatchdogSeconds:   int64(cfg.Auth.MaxAttemptCredentialLifetime.Seconds()),
			// X3.2: a read-only root plus a per-attempt HOME, so nothing one attempt
			// writes to the image survives into the next attempt on this worker.
			ReadOnlyRootFilesystem: cfg.Execution.WarmReadOnlyRootFilesystem,
			// X4: sized like the dedicated pod of a task without resources of its own.
			Resources: warmPodResources(cfg),
		}
		if useExchange {
			// Exchange transport: project an SA token, no plaintext bootstrap token.
			// BuildWarmPod (identity nil) projects the token and stamps the warm-worker
			// LABEL the exchange resolver keys on; the control plane mints the
			// worker-scoped JWT. Audience/expiration mirror the task-pod projected token
			// (executor floors the expiration).
			spec.AgentTokenTransport = config.AgentTokenTransportExchange
			spec.AgentTokenAudience = executor.DefaultAgentTokenAudience
			return spec, nil
		}
		// Env-var fallback: mint a WORKER-scoped (control-channel-only) bootstrap JWT
		// and ride it in as a plaintext env var. The unique worker id is the token
		// Subject; the scope claim makes AwaitAssignment accept it and every secret/
		// task RPC reject it.
		token, err := authn.IssueAgentToken(auth.AgentIdentity{
			Scope:        auth.ScopeWarmWorker,
			WorkerID:     "warm-" + t.DagVersionID + "-" + uuid.NewString(),
			DagVersionID: t.DagVersionID,
			TenantID:     t.TenantID,
		}, warmBootstrapTokenTTL(cfg))
		if err != nil {
			return executor.WarmPodSpec{}, fmt.Errorf("minting warm bootstrap token: %w", err)
		}
		spec.BootstrapToken = token
		return spec, nil
	}
}

// warmPodResources resolves the requests and limits warm worker pods are created
// with (X4): execution.warm_pod_resources_* where set, else the
// executor.defaults.resources_* a task without resources gets on a dedicated
// pod, each dimension on its own and each value as both request and limit, the
// same convention as platformDefaults. Nil when neither is configured, which
// leaves warm pods unsized as before. The dispatcher is handed the same value so
// it keeps a task declaring other resources off warm workers.
func warmPodResources(cfg *config.ServerConfig) *domain.Resources {
	cpu := cmp.Or(cfg.Execution.WarmPodResourcesCPU, cfg.Executor.Defaults.ResourcesCPU)
	memory := cmp.Or(cfg.Execution.WarmPodResourcesMemory, cfg.Executor.Defaults.ResourcesMemory)
	if cpu == "" && memory == "" {
		return nil
	}
	return &domain.Resources{
		Requests: &domain.ResourceQuantity{CPU: cpu, Memory: memory},
		Limits:   &domain.ResourceQuantity{CPU: cpu, Memory: memory},
	}
}

// warmBootstrapTokenTTL sizes the bootstrap credential to outlive the worker's
// whole life (the stream bearer is validated once at open), anchored to the worker
// lifetime cap so it cannot lapse while the worker is still valid.
func warmBootstrapTokenTTL(cfg *config.ServerConfig) time.Duration {
	if cfg.Execution.MaxWorkerLifetime > 0 {
		return cfg.Execution.MaxWorkerLifetime + time.Hour
	}
	return 2 * time.Hour
}

// stagingGCInterval is how often the per-run staging-volume GC sweeps; stagingTTL
// is how long a FAILED run's volume is kept after its terminal time before the
// PVC is deleted (ADR 0022 — long enough for a clear+re-run to re-attach the
// data). A SUCCEEDED run's volume is freed immediately, regardless of the TTL.
const (
	stagingGCInterval = time.Minute // frequent so a succeeded run's volume is freed ~at DAG end
	// stagingTTL must stay well above the reconciler's finished-pod grace. A pod
	// preserved by a reaper teardown (#928) still holds kubernetes.io/pvc-protection
	// on the run's staging claim — upstream only skips terminated pods on the newer
	// unused-since path, not on the deletion path — so if this TTL ever dropped near
	// that grace, the GC would mark a claim deleted while a pod it cannot see is
	// still keeping the finalizer on. Today the margin is 24h against 10 minutes.
	stagingTTL = 24 * time.Hour
)

// startStagingGC periodically reclaims per-run staging PVCs from the
// metadatabase-tracked lifecycle: succeeded runs immediately, failed runs after
// the TTL, orphaned volumes (run gone) on sight (ADR 0022).
func startStagingGC(ctx context.Context, cs kubernetes.Interface, namespace string, store executor.StagingStore, leading func() bool, logger *slog.Logger) {
	exec := executor.NewKubernetesExecutor(cs, namespace)
	exec.SetStagingStore(store)
	startGatedTicker(ctx, "staging-gc", stagingGCInterval, leading, logger, func() {
		if err := exec.GCStagingClaims(ctx, stagingTTL); err != nil {
			logger.Error("staging gc", "error", err)
		}
	})
}

func startScheduler(ctx context.Context, cfg *config.ServerConfig, pg *storage.Postgres, repo *storage.Repository, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, warmPools *agentrpc.WorkerRegistry, logSink logs.Sink, logger *slog.Logger, metrics *observability.Metrics) (*scheduler.Scheduler, bool, io.Closer, error) {
	leaderPool, err := storage.NewLeaderPool(ctx, cfg.Database)
	if err != nil {
		return nil, false, nil, fmt.Errorf("leader pool: %w", err)
	}
	store := storage.NewSchedulerStore(pg)
	sched := scheduler.NewScheduler(store, logger,
		time.Duration(cfg.Scheduler.LoopIntervalMS)*time.Millisecond)
	sched.SetRecorder(metrics)
	// Named pools are Pro-only (ADR 0053): enable the cross-DAG slot gate only on
	// the Pro edition. Left off on Lite/non-Pro, the tick never queries pool
	// budgets and planning is byte-identical to the max_active_tasks-only path.
	if cfg.UI.Edition == "pro" {
		sched.EnablePools()
	}
	// Native on-failure alerting (#424): the scheduler fires Slack/webhook rules
	// declared in dexaflow.yaml when a run finalizes failed, resolving each rule's
	// managed connection to its endpoint URL. Best-effort, off the tick path.
	sched.SetAlerter(failurealert.New(
		alerts.NewNotifier(&http.Client{Timeout: alertHTTPTimeout}),
		connEndpointResolver{repo},
		metrics,
		logger,
	))
	podDispatch, dispatchCloser := setupDispatch(ctx, cfg, sched, execStore, authn, store, warmPools, logSink, logger, metrics)
	leader := scheduler.NewLeader(leaderPool)
	go func() {
		defer leaderPool.Close()
		campaignAndRun(ctx, leader, sched, logger)
	}()
	return sched, podDispatch, dispatchCloser, nil
}

// drainDispatch closes the buffered dispatch pool on shutdown so in-flight
// dispatches drain instead of leaking workers / leaving TIs stuck `queued`
// (#133). Nil closer (Lite/passthrough) is a no-op; a close error is logged.
func drainDispatch(closer io.Closer, logger *slog.Logger) {
	if closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		logger.Error("draining dispatch pool on shutdown", "error", err)
	}
}

// alertHTTPTimeout bounds each on-failure alert POST so a slow or hung channel
// endpoint cannot pile up detached alert goroutines (#424).
const alertHTTPTimeout = 10 * time.Second

// connEndpointResolver adapts the connection store to failurealert.EndpointResolver:
// an alert channel's endpoint URL is the connection's decrypted secret (#424).
type connEndpointResolver struct{ repo *storage.Repository }

// ResolveAlertEndpoint returns the connection's endpoint — its secret URL and any
// headers from the connection extra — for the tenant, or an error when it is
// missing/undecryptable.
func (c connEndpointResolver) ResolveAlertEndpoint(ctx context.Context, tenantID, connID string) (failurealert.Endpoint, error) {
	url, headers, err := c.repo.AlertEndpoint(ctx, tenantID, connID)
	if err != nil {
		return failurealert.Endpoint{}, err
	}
	return failurealert.Endpoint{URL: url, Headers: headers}, nil
}

// leaderCheckInterval is how often we both poll for leadership (as a follower)
// and revalidate it (as a leader).
const leaderCheckInterval = 5 * time.Second

// maxLeaderCheckFailures is how many consecutive leadership-check errors are
// tolerated before stepping down, so a single transient error (e.g. the leader
// connection being recycled) does not cause needless leadership churn.
const maxLeaderCheckFailures = 3

// leadershipChecker reports whether this instance still holds leadership.
type leadershipChecker interface {
	HoldsLock(ctx context.Context) (bool, error)
}

// campaignAndRun acquires scheduler leadership (polling every leaderCheckInterval)
// and runs the loop only while leader, so a single replica schedules at a time
// (ADR 0009). If leadership is lost mid-run (the advisory lock dropped because the
// connection blipped, was idle-reaped, or recycled), it steps down and
// re-campaigns rather than scheduling alongside the new leader.
func campaignAndRun(ctx context.Context, leader *scheduler.Leader, sched *scheduler.Scheduler, logger *slog.Logger) {
	ticker := time.NewTicker(leaderCheckInterval)
	defer ticker.Stop()
	// stepDownAt is the wall-clock the previous runAsLeader returned at (i.e.
	// when we transitioned out of leadership). Zero means "no prior step-down
	// in this process" — the first acquisition at boot doesn't record a
	// re-acquire latency (no churn happened yet).
	var stepDownAt time.Time
	for {
		acquired, err := leader.TryAcquire(ctx)
		switch {
		case err != nil:
			logger.Error("acquiring leadership", "error", err)
		case acquired:
			// Record the time we spent stepped down (#311 observability). A
			// growing P99 here surfaces leader churn that affects scheduling
			// latency — operators alert on the histogram, not log content.
			sched.RecordReacquireSince(stepDownAt)
			sched.ClearSteppingDown()
			runAsLeader(ctx, leader, sched, logger)
			if ctx.Err() != nil {
				return // shutting down, not a leadership loss
			}
			stepDownAt = time.Now()
			logger.Warn("stepped down as scheduler leader; re-campaigning")
		default:
			logger.Info("scheduler follower; retrying for leadership")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runAsLeader runs the scheduling loop while leadership is held. A watchdog
// revalidates the advisory lock and cancels the loop the moment leadership is
// lost, so a stale leader (whose lock-holding session died) steps down instead
// of double-scheduling against the new leader. It signals leadership to the
// scheduler so the heartbeat/health reflects whether this instance is the
// active scheduler.
func runAsLeader(ctx context.Context, leader *scheduler.Leader, sched *scheduler.Scheduler, logger *slog.Logger) {
	logger.Info("became scheduler leader")
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sched.SetLeading(true)
	defer sched.SetLeading(false)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The watch passes the step-down reason to the scheduler BEFORE
		// canceling the run-context, so the reapers/Step that are about to
		// return ctx-canceled log at WARN ("expected during step-down") not
		// ERROR (#311). A nil scheduler is tolerated by tests.
		watchLeadership(runCtx, leader, leaderCheckInterval, cancel, logger,
			func(reason string) { sched.MarkSteppingDown(reason) })
	}()

	if runErr := sched.Run(runCtx); runErr != nil && !errors.Is(runErr, context.Canceled) {
		logger.Error("scheduler stopped", "error", runErr)
	}
	cancel()
	<-done
	releaseLeader(leader, logger) //nolint:contextcheck // release uses a fresh bounded context
}

// watchLeadership cancels the run when leadership is lost: a definitive "not
// held" steps down immediately, while transient check errors are tolerated up to
// maxLeaderCheckFailures (a connection blip should not churn leadership). It
// returns when the run context is canceled. The onStepDown callback fires
// once, BEFORE cancel(), with the step-down reason so the scheduler can
// classify the cancel fanout (#311) and increment the per-reason counter.
func watchLeadership(ctx context.Context, chk leadershipChecker, interval time.Duration, cancel context.CancelFunc, logger *slog.Logger, onStepDown func(reason string)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failures := 0
	stepDown := func(reason string) {
		if onStepDown != nil {
			onStepDown(reason)
		}
		cancel()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			held, err := chk.HoldsLock(ctx)
			switch {
			case err != nil:
				if ctx.Err() != nil {
					return // canceled mid-check (shutdown/stepdown)
				}
				failures++
				logger.Warn("leadership check failed", "error", err, "consecutive", failures)
				if failures >= maxLeaderCheckFailures {
					logger.Warn("too many failed leadership checks; stepping down",
						"reason", "check_timeout", "consecutive_failures", failures)
					stepDown("check_timeout")
					return
				}
			case !held:
				logger.Warn("lost scheduler advisory lock; stepping down",
					"reason", "lock_released")
				stepDown("lock_released")
				return
			default:
				failures = 0
			}
		}
	}
}

func releaseLeader(leader *scheduler.Leader, logger *slog.Logger) {
	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := leader.Release(rctx); err != nil { //nolint:contextcheck // fresh context to release after shutdown
		logger.Error("releasing leadership", "error", err)
	}
}

func serve(s *http.Server, errCh chan<- error) {
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("serving %s: %w", s.Addr, err)
	}
}

// setupDispatch wires the pod-path executor selected by executor.type onto the
// scheduler and returns whether pod dispatch is active. "subprocess" runs the
// agent on the host (dev only); "kubernetes" (default) launches task pods.
func setupDispatch(ctx context.Context, cfg *config.ServerConfig, sched *scheduler.Scheduler, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, store *storage.SchedulerStore, warmPools *agentrpc.WorkerRegistry, logSink logs.Sink, logger *slog.Logger, metrics *observability.Metrics) (bool, io.Closer) {
	if cfg.Executor.Type == "subprocess" {
		return setupSubprocessDispatch(cfg, sched, execStore, authn, warmPools, logger, store, metrics) //nolint:contextcheck // buffered worker deliberately detaches from caller ctx
	}
	return setupK8sDispatch(ctx, cfg, sched, execStore, authn, store, warmPools, logSink, logger, metrics) //nolint:contextcheck // buffered worker deliberately detaches from caller ctx
}

// setWarmPlacer attaches the warm-worker placement seam to the dispatcher, but
// only when the registry is actually present (ADR 0058 N1b1-place). The nil check
// is on the concrete *agentrpc.WorkerRegistry BEFORE it is boxed into the
// dispatch.WarmPlacer interface: passing a typed-nil pointer would make the
// interface non-nil, so the dispatcher would try to place onto a nil registry
// instead of taking the dedicated path. Warm pools off => reg nil => no placer.
func setWarmPlacer(d *dispatch.Dispatcher, reg *agentrpc.WorkerRegistry) {
	if reg != nil {
		d.SetWarmPlacer(reg)
	}
}

// resolveAgentControlAddr returns the address task agents dial back, defaulting
// to the server's own gRPC address.
func resolveAgentControlAddr(cfg *config.ServerConfig) string {
	if cfg.Executor.AgentControlPlaneAddr != "" {
		return cfg.Executor.AgentControlPlaneAddr
	}
	return cfg.Server.GRPCAddr
}

// setupSubprocessDispatch wires the dev-only subprocess executor (ADR 0023): it
// runs the agent on the host with no isolation, so it is gated to dev use.
func setupSubprocessDispatch(cfg *config.ServerConfig, sched *scheduler.Scheduler, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, warmPools *agentrpc.WorkerRegistry, logger *slog.Logger, sink dispatch.FailureSink, metrics *observability.Metrics) (bool, io.Closer) {
	subExec := executor.NewSubprocessExecutor(cfg.Executor.AgentPath, logger)
	subExec.SetWorkDir(cfg.Executor.SubprocessWorkDir)
	dispatcher := dispatch.NewDispatcher(subExec, execStore, authn, resolveAgentControlAddr(cfg), attemptTokenTTL)
	dispatcher.SetPlatformDefaults(platformDefaults(cfg.Executor.Defaults))
	setWarmPlacer(dispatcher, warmPools)
	disp, closer := wrapBuffered(dispatcher, sink, logger, metrics, cfg.Scheduler.Dispatch)
	sched.SetDispatcher(disp)
	logger.Warn("subprocess dispatch enabled (dev only; user code runs unsandboxed)")
	return true, closer
}

// setupK8sDispatch wires the production pod-per-task executor; it is a no-op
// (tasks have no executor and are failed as undispatchable) when no Kubernetes
// client is available.
func setupK8sDispatch(ctx context.Context, cfg *config.ServerConfig, sched *scheduler.Scheduler, execStore *storage.ExecutionStore, authn *auth.JWTAuthenticator, store *storage.SchedulerStore, warmPools *agentrpc.WorkerRegistry, logSink logs.Sink, logger *slog.Logger, metrics *observability.Metrics) (bool, io.Closer) {
	clients, perr := buildK8sClients(cfg.Executor.KubeClient)
	if perr != nil {
		logger.Warn("pod dispatch disabled; tasks have no executor and will fail as undispatchable", "error", perr)
		return false, nil
	}
	cs, mcs := clients.dispatch, clients.maintenance
	controlAddr := resolveAgentControlAddr(cfg)
	podExec := executor.NewKubernetesExecutor(cs, cfg.Executor.TaskNamespace)
	podExec.SetStagingStore(store) // record per-run staging volumes in the metadatabase (ADR 0022)
	dispatcher := dispatch.NewDispatcher(podExec, execStore, authn, controlAddr, attemptTokenTTL)
	dispatcher.SetAgentTLSCAConfigMap(cfg.Executor.AgentTLSCAConfigMap)
	dispatcher.SetTaskSecret(cfg.Executor.TaskSecretName, cfg.Executor.TaskSecretMountPath)
	dispatcher.SetDefaultTaskServiceAccount(cfg.Executor.TaskServiceAccount)
	// What warm pods are sized with (X4), so a task declaring other resources
	// takes a dedicated pod. Read only when a warm placer is wired.
	dispatcher.SetWarmPodResources(warmPodResources(cfg))
	// Agent-token transport (ADR 0055 Fix #3). Under the exchange transport the
	// executor projects a ServiceAccount token instead of the plaintext one; the
	// default (envvar) leaves the pod spec unchanged. The audience is the shared
	// const the control-plane TokenReviewer validates against; expiration floors in
	// BuildPod.
	dispatcher.SetAgentTokenTransport(cfg.Auth.AgentTokenTransport, executor.DefaultAgentTokenAudience, 0)
	dispatcher.SetPlatformDefaults(platformDefaults(cfg.Executor.Defaults))
	// Deadline floor for task pods that declare no execution timeout: the agent's
	// reports retry for as long as the control plane is unreachable, so a pod
	// with no deadline of its own would outlive a total outage indefinitely. The
	// credential ceiling is the natural bound — past it the pod cannot renew its
	// bearer — and it is the same knob the warm worker's attempt watchdog uses.
	dispatcher.SetAttemptLifetimeCeiling(cfg.Auth.MaxAttemptCredentialLifetime)
	// External secrets backend (ADR 0060, Pro pod path): deliver the operator config
	// to task pods as LEOFLOW_SECRETS_* env. Empty backend → podEnv skips it (chain
	// stays vault-only). The D6 registration relaxation is wired separately in run()
	// where the Repository is in scope.
	dispatcher.SetSecretsBackend(cfg.Secrets.Backend, secretsKwargsJSON(cfg.Secrets))
	// Warm placement seam (ADR 0058 N1b1-place): the dispatcher Assign()s onto the
	// SAME registry the gRPC handler serves. nil when warm pools are off.
	setWarmPlacer(dispatcher, warmPools)
	disp, closer := wrapBuffered(dispatcher, store, logger, metrics, cfg.Scheduler.Dispatch) //nolint:contextcheck // buffered worker deliberately detaches from caller ctx
	sched.SetDispatcher(disp)
	// Shared informer read-path (PR-10): one long-lived watch replaces the
	// reapers' per-running-TI LIST storm and the reconciler's 30s LIST. Consulted
	// only to DEFER a reap; the live TaskPodPresence kill path is unchanged (#461).
	// A typed-nil pointer would defeat the reconciler's / reaper's nil check, so
	// the interfaces are only populated when the informer actually built.
	podInformer := buildPodInformer(ctx, cfg, mcs, logger)
	var (
		snapshotter executor.PodSnapshotter
		cache       executor.PodPresenceCache
	)
	if podInformer != nil {
		snapshotter = podInformer
		cache = podInformer
	}
	// Warm failover seam (ADR 0058 N1d-a2): one warm-pod client, built ONLY when
	// warm pools are enabled, is shared as the live-warm-pod lister for the reaper
	// (the warm-worker-lost recovery + the dispatch-lost H3 defer) AND as the
	// reconciler's create/delete/list client. With the flag off it stays a nil
	// interface, so both warm reaper paths are inert and no warm pod is ever
	// created — dispatch is byte-for-byte today's dedicated pod-per-task.
	var warmLister executor.WarmPodLister
	var warmPods *executor.KubernetesWarmPods
	if cfg.Execution.WarmPoolsEnabled {
		warmPods = executor.NewKubernetesWarmPods(mcs, cfg.Executor.TaskNamespace, warmPodSpecFunc(cfg, authn, controlAddr))
		warmLister = warmPods
	}
	// The execution reaper (#120/#128/#202/#527) fails stuck runs and TIs; it
	// tears down a reaped task's pod and gates the dispatch-lost decision on real
	// pod liveness (#474, #461), so it is wired only on the pod path. Lite/
	// subprocess starts no maintenance loop and does no reaping.
	// The reaper's live pod reads and deletes go through the maintenance client,
	// which is podExec's own client unless maintenance limits are set.
	var reapPods executor.PodManager = podExec
	if mcs != cs {
		reapPods = executor.NewKubernetesExecutor(mcs, cfg.Executor.TaskNamespace)
	}
	reaper := executor.NewReaper(store, reapPods, cache, warmLister, metrics, logger, executor.DefaultReaperConfig(), sched.SteppingDown)
	// Give the reaper an append-aware marker sink so a reaped attempt's log ends
	// with a "killed: agent_lost" marker instead of a silent truncation (#861).
	// Both DiskSink and ObjectSink implement MarkerSink (append preserves the
	// agent's streamed content on either backend); the assertion holds for every
	// sink NewDurableSink returns.
	if ms, ok := logSink.(logs.MarkerSink); ok {
		reaper.SetLogSink(ms)
	}
	// Leader-settling gate: no reaper fires until this instance has led for the
	// settling grace, the pod informer has synced, and the reconciler has
	// completed a sweep under this leadership (wired in startMaintenance). A
	// control-plane restart makes heartbeats look stale and finished task pods
	// look lost; the gate holds every reaper until the true outcomes have been
	// recovered, with a liveness valve so a broken sweep cannot disable reaping.
	reaper.SetLeaderSince(sched.LeaderSince)
	if podInformer != nil {
		reaper.SetInformerSynced(podInformer.HasSynced)
	}
	// Close the reaper's destructive gate the moment this instance stops leading:
	// together with the step-down flag and the run-context cancel, this keeps a
	// draining or stepping-down leader from marking TIs failed or deleting pods
	// on its way out — the successor redoes the reap under its own settling gate.
	reaper.SetLeading(sched.IsLeading)
	startMaintenance(ctx, mcs, cfg.Executor.TaskNamespace, execStore, reaper, sched.IsLeading, logger, snapshotter)
	startStagingGC(ctx, mcs, cfg.Executor.TaskNamespace, store, sched.IsLeading, logger)
	// Warm-pool reconciler (ADR 0058 N1b2b, model A2): keeps min_idle warm workers
	// ready per active dag_version. Started ONLY when warm pools are enabled — with
	// them off it is never constructed, so no warm pod is ever created and dispatch
	// stays byte-for-byte today's dedicated pod-per-task.
	if cfg.Execution.WarmPoolsEnabled {
		store.SetWarmExecution(cfg.Execution)
		// The busy set (running attempts durably bound to a warm worker) makes the
		// reconciler busy-aware (ADR 0058 N1d-b): it scales the IDLE buffer and never
		// deletes a worker with an in-flight attempt. store implements it.
		startWarmPoolReconciler(ctx, store, warmPods, store, cfg.Execution.MaxWarmPodsPerTenant, sched.IsLeading, metrics, logger)
		logger.Warn("warm pool reconciler enabled (ADR 0058 N1b2b); maintaining min_idle warm workers per active dag_version", "namespace", cfg.Executor.TaskNamespace)
	}
	logger.Info("pod dispatch enabled", "namespace", cfg.Executor.TaskNamespace, "agent_control_plane_addr", controlAddr,
		"kube_qps", cfg.Executor.KubeClient.QPS, "kube_burst", cfg.Executor.KubeClient.Burst,
		"separate_maintenance_client", mcs != cs)
	return true, closer
}

// wrapBuffered returns the dispatcher to plug into the scheduler. When
// BufferSize > 0 the inner dispatcher is fronted by the worker pool (#127);
// when BufferSize == 0 the inner dispatcher is used directly (Lite). The
// caller passes a FailureSink (typically the SchedulerStore) so worker-side
// dispatch failures fail the TI with a clear reason instead of leaving it
// stuck `queued`.
// The io.Closer is non-nil only in buffered mode; the caller defers Close() on
// shutdown so in-flight dispatches drain (workers finish or fail via the sink)
// instead of leaking goroutines and leaving TIs stuck `queued` (#133).
func wrapBuffered(inner dispatch.Inner, sink dispatch.FailureSink, logger *slog.Logger, metrics *observability.Metrics, cfg config.DispatchSection) (scheduler.Dispatcher, io.Closer) {
	if cfg.BufferSize <= 0 {
		// Passthrough: keep the inner dispatcher exposed verbatim so the
		// scheduler sees the same surface it always did in Lite. No pool to close.
		return inner, nil
	}
	bd := dispatch.NewBuffered(inner, sink, logger, metrics, dispatch.BufferConfig{
		BufferSize: cfg.BufferSize,
		Workers:    cfg.Workers,
	})
	logger.Info("buffered dispatch enabled (async pool)",
		"buffer_size", cfg.BufferSize, "workers", cfg.Workers)
	return bd, bd
}

// platformDefaults maps the executor.defaults config (L0 task defaults, ADR
// 0023) into the dispatcher's PlatformDefaults. Resources are set only when a
// quantity is configured, so an unset section leaves req.Resources untouched;
// when set, both requests and limits are populated from the same quantity.
//
// That reaches Guaranteed QoS only when BOTH cpu and memory are configured.
// A partial pair yields Burstable — the empty dimension is dropped from the pod
// spec entirely — which is why platformDefaultWarnings emits a boot WARN for it
// rather than letting the gap stay invisible.
func platformDefaults(c config.PlatformDefaultsSection) dispatch.PlatformDefaults {
	d := dispatch.PlatformDefaults{
		StagingSize:         c.StagingSize,
		StagingStorageClass: c.StagingStorageClass,
		StagingAccessMode:   c.StagingAccessMode,
		PodSecurity: executor.PodSecurity{
			RunAsNonRoot:           c.RunTasksAsNonRoot,
			ReadOnlyRootFilesystem: c.ReadOnlyTaskRootFilesystem,
		},
	}
	if c.ResourcesCPU != "" || c.ResourcesMemory != "" {
		// Set requests AND limits to the same quantity so a task that relies on the
		// platform default reaches Guaranteed QoS (requests == limits, cpu AND
		// memory — a partial pair only reaches Burstable, and warns at boot).
		// Requests alone yields Burstable and is first to be throttled/evicted
		// under node pressure — the opposite of what pinning a per-cluster default
		// is for.
		// A DAG/task that declares its own resources still overrides this wholesale
		// (see internal/dispatch), so this only shapes the no-declaration case.
		d.Resources = &domain.Resources{
			Requests: &domain.ResourceQuantity{CPU: c.ResourcesCPU, Memory: c.ResourcesMemory},
			Limits:   &domain.ResourceQuantity{CPU: c.ResourcesCPU, Memory: c.ResourcesMemory},
		}
	}
	return d
}

// logEnvCompat reports the result of mirroring the environment: every
// conflicting pair (the DEXAFLOW_* value won) and, in one line, how many
// variables are set only under their LEOFLOW_* names, which keep working.
func logEnvCompat(r envcompat.Report) {
	for _, note := range r.ConflictNotes() {
		slog.Warn("environment: " + note)
	}
	if n := len(r.Legacy); n > 0 {
		slog.Info("environment: variables set under their LEOFLOW_* names are also read as DEXAFLOW_*", "count", n)
	}
}
