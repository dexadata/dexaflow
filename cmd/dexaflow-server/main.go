// Command dexaflow-server runs the Dexaflow control plane: the HTTP API, auth,
// metrics, and (when enabled) the scheduler.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/envcompat"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/version"
	"github.com/dexadata/dexaflow/internal/workspace"
	"github.com/dexadata/dexaflow/internal/xcom"
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
		fmt.Print(versionOutput())
		return
	case version.WantsHelp(args):
		fmt.Print(usage)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dexaflow-server:", err)
		os.Exit(exitCode(err))
	}
}

// openVerifiedPostgres opens the pool and waits, serving only the probes and
// metrics, until the schema is one this binary can serve (ADR 0090 decision
// 1), instead of exiting into a crash loop on an absent or behind schema. The
// check is a read-only SELECT, so it works for the migration-less role:api
// (ADR 0049); the blessed Helm path applies migrations in a
// pre-install/upgrade Job before any role starts.
func openVerifiedPostgres(ctx context.Context, cfg *config.ServerConfig, tel *observability.Telemetry) (*storage.Postgres, error) {
	pg, err := storage.NewPostgres(ctx, cfg.Database)
	if err != nil {
		return nil, err
	}
	// A pod that was serving keeps the metric current through its readiness
	// probe, so an emptied database shows on dexaflow_schema_ready too.
	pg.SetSchemaObserver(tel.Metrics.RecordSchemaState)
	if werr := awaitSchema(ctx, cfg, tel, pg); werr != nil {
		pg.Close()
		return nil, werr
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
		ServiceName:    "leoflow-server",
		LogLevel:       cfg.Observability.LogLevel,
		LogFormat:      cfg.Observability.LogFormat,
		OTelEnabled:    cfg.Observability.OTel.Enabled,
		OTelEndpoint:   cfg.Observability.OTel.Endpoint,
		SampleRatio:    cfg.Observability.OTel.SampleRatio,
		SkipProbeSpans: cfg.Observability.OTel.SkipProbeSpans,
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

	// Lite only: the key-migration lock is held before anything reads or writes
	// a stored secret, and the server stops if it is lost (ADR 0065 section 3).
	pg, ctx, releaseKeyLock, err := openPostgresHoldingKeyLock(ctx, cfg, tel)
	if err != nil {
		return stoppedWhileWaiting(err, tel.Logger)
	}
	defer pg.Close()
	defer releaseKeyLock()
	// A readiness probe that finds the database replaced under this process
	// stops it (ADR 0090 decision 5).
	ctx, releaseReplaced := stopOnReplaced(ctx, pg)
	defer releaseReplaced()

	// Datastore for XCom + live-log tailing: Redis when configured (production,
	// ADR 0006), or the embedded Postgres/in-process backends when no Redis is
	// set (Lite — ADR 0026). The signal is the presence of a Redis URL.
	xcomBackend, logTailer, redisHealth, dsCleanup, err := selectDatastore(ctx, cfg, pg, tel.Logger, tel.Metrics)
	if err != nil {
		return err
	}
	defer dsCleanup()

	repo, err := newServerRepository(ctx, cfg, pg, xcomBackend, tel.Logger)
	if err != nil {
		return err
	}
	authn := auth.NewJWTAuthenticator(repo, cfg.Auth.JWT.Secret, time.Duration(cfg.Auth.JWT.TokenTTLSeconds)*time.Second)
	execStore := storage.NewExecutionStore(pg)
	xcomSvc := xcom.NewService(xcomBackend, storage.NewXComIndex(pg), xcom.DefaultTTL)
	xcomReader := storage.NewXComReader(pg, xcomBackend)

	if err := bootstrapAdmin(ctx, repo, cfg.Bootstrap, tel.Logger); err != nil {
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
	// meant a first install with bootstrap.password set and admin@dexaflow.local in
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
	allowInsecureSecrets := cfg.Agent.AllowInsecureSecrets
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
	metricsSrv := newHTTPServer(cfg.Server.MetricsAddr, api.ObservabilityHandler(tel.Registry, checks, observabilityOptions(cfg)...), cfg)

	tel.Logger.Info("leoflow-server started", "role", cfg.Server.EffectiveRole(), "http_addr", cfg.Server.HTTPAddr, "metrics_addr", cfg.Server.MetricsAddr, "serves_api", servesAPI, "serves_scheduler", servesScheduler)
	return serveExit(ctx, serveHTTP(ctx, tel.Logger, servesAPI, apiSrv, metricsSrv))
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

// secretCipher builds the AES-256-GCM cipher for connection secrets (ADR
// 0019), or nil without a usable key. Without a key the connection store stays
// plaintext-incapable: writes are refused, never silently stored in the clear.
func secretCipher(secretKey string, logger *slog.Logger) (secrets.Cipher, error) {
	keys, kerr := secrets.ParseKeys(secretKey)
	if kerr != nil {
		// The reason, not just the outcome. ParseKeys says WHICH entry of the
		// list is malformed, and an operator told only "no usable key set" after
		// setting one will go looking at the wrong thing. One bad entry disables
		// encryption even when the first key is perfect, so naming it matters
		// more here than it did with a single key.
		logger.Warn("no usable LEOFLOW_SECRET_KEY; connection management disabled (Variables still work)",
			"error", kerr)
		return nil, nil //nolint:nilerr,nilnil // a missing/unusable key is non-fatal: run without connection encryption
	}
	ciphers := make([]secrets.Cipher, 0, len(keys))
	for i, k := range keys {
		c, cerr := secrets.NewAESGCM(k)
		if cerr != nil {
			return nil, fmt.Errorf("building secret cipher from key %d: %w", i+1, cerr)
		}
		ciphers = append(ciphers, c)
	}
	// The first key encrypts; the rest only decrypt, so a rotation reads what a
	// previous key wrote instead of orphaning it (#486). Nothing is ever written
	// under a later key, which is what makes the rotation real rather than
	// cosmetic.
	cipher := secrets.WithFallback(ciphers[0], ciphers[1:]...)
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
	return cipher, nil
}

// discoverOIDCFlow discovers the IdP and builds the Authorization Code + PKCE
// login flow. It is only called under provider: oidc (so it always returns a
// flow or an error, never nil,nil). The client secret comes from the environment
// (LEOFLOW_AUTH_OIDC_CLIENT_SECRET, bound by viper); it is never logged.
// Discovery failure is a boot failure: fail closed.

// startCleanup runs a periodic janitor that purges expired XCom index rows,
// prunes old log files, and warns on low disk for the datastore dir. The
// buildLogSink selects the durable task-log sink from configuration. The default
// (logs.backend "disk" or unset) is the on-disk sink, unchanged behavior for
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
			"bucket", cfg.Logs.Sink.Bucket, "endpoint", cfg.Logs.Sink.Endpoint, "prefix", cfg.Logs.Sink.Prefix, "layout", cfg.Logs.Sink.Layout)
		return logs.NewDurableSink(ctx, "s3", "", store, cfg.Logs.Sink.Prefix, logger, logs.WithObjectLayout(cfg.Logs.Sink.Layout))
	case "gcs":
		store, err := logs.NewGCSStore(ctx, logs.GCSConfig{
			Bucket:          cfg.Logs.Sink.Bucket,
			CredentialsFile: cfg.Logs.Sink.CredentialsFile,
		})
		if err != nil {
			return nil, fmt.Errorf("building gcs log store: %w", err)
		}
		logger.Info("task logs: gcs object-store backend enabled",
			"bucket", cfg.Logs.Sink.Bucket, "prefix", cfg.Logs.Sink.Prefix, "layout", cfg.Logs.Sink.Layout)
		return logs.NewDurableSink(ctx, "gcs", "", store, cfg.Logs.Sink.Prefix, logger, logs.WithObjectLayout(cfg.Logs.Sink.Layout))
	default:
		return nil, fmt.Errorf("unknown logs.backend %q", cfg.Logs.Backend)
	}
}

// schedulerDatabase is the database section the scheduler side opens its
// pool from. The dedicated pool (database.scheduler_max_conns) is only for a
// process that runs the scheduler loop; with scheduler.enabled=false the
// janitors stay on the main pool and no extra connections are opened.
func schedulerDatabase(cfg *config.ServerConfig) config.DatabaseSection {
	db := cfg.Database
	if !cfg.Scheduler.Enabled {
		db.SchedulerMaxConns = 0
	}
	return db
}

// selectDatastore chooses the XCom backend and live-log tailer for the run. With
// a configured Redis URL it uses the production Redis backends (ADR 0006); with
// no Redis configured it uses the embedded backends, XCom on Postgres and an
// in-process tailer, so Lite needs no Redis (ADR 0026). It returns a health
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
	// gauges (#312 sibling: same shape as the #311 step-down observability).
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
// this map, and an assertion that stops matching is silent: they would fall
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

// serve runs one listener until it is shut down. A server with a TLSConfig
// (the API listener with server.http_tls_cert/key, #638) serves HTTPS; its
// certificate comes from TLSConfig.GetCertificate, so no file names are passed.
func serve(s *http.Server, errCh chan<- error) {
	listen := s.ListenAndServe
	if s.TLSConfig != nil {
		listen = func() error { return s.ListenAndServeTLS("", "") }
	}
	if err := listen(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("serving %s: %w", s.Addr, err)
	}
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

// newServerRepository builds the control plane's repository with every
// dependency it reads given at construction (#1949), then runs the boot sweeps
// over the stored secrets.
func newServerRepository(ctx context.Context, cfg *config.ServerConfig, pg *storage.Postgres, xcomBackend storage.XComValueDeleter, logger *slog.Logger) (*storage.Repository, error) {
	secretOpts, varMode, err := secretOptions(cfg, logger)
	if err != nil {
		return nil, err
	}
	// A clear deletes the stored XCom of the attempts it clears, not just their
	// index rows, since agents fetch values by key (#1131).
	repo, err := storage.NewServerRepository(pg, append(secretOpts,
		storage.WithXComBackend(xcomBackend), registrationOption(cfg))...)
	if err != nil {
		return nil, err
	}
	convergeSecrets(ctx, repo, cfg, varMode, logger)
	return repo, nil
}

// registrationOption is the setting DAG registration reads: whether a DAG
// registered for the first time starts paused (dags_are_paused_at_creation,
// #1705). A DAG's own is_paused_upon_creation still wins over it.
func registrationOption(cfg *config.ServerConfig) storage.Option {
	return storage.WithDagsArePausedAtCreation(cfg.DagsArePausedAtCreation)
}
