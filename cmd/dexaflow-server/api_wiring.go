package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"k8s.io/client-go/kubernetes"

	dexaflow "github.com/dexadata/dexaflow"
	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/issuer"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/oidc"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/ui"
)

// loginRateLimit returns the per-minute failed-login cap with a safe floor: a
// missing or nonsensical (<=0) config value falls back to the conservative
// default rather than 0, which would lock every user out.
func loginRateLimit(cfg *config.ServerConfig) int {
	if cfg.Auth.LoginRateLimitPerMinute > 0 {
		return cfg.Auth.LoginRateLimitPerMinute
	}
	return 5
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
	if oidcFlow != nil {
		startCLIDeviceSweep(ctx, repo, tel.Logger)
	}
	//nolint:contextcheck // buildAPIServer wires per-request handlers; each request carries its own context
	srv, err := buildAPIServer(cfg, tel, authn, pg, repo, xcomReader, logSink, logTailer, checks, executorInfo, schedulerHealth, oidcFlow, newTrustedIssuer(ctx, cfg))
	if err != nil {
		return nil, err
	}
	// Optional HTTPS on the API listener (#638); plain HTTP when unset.
	if err := configureAPITLS(srv, cfg.Server, tel.Metrics); err != nil {
		return nil, err
	}
	tel.Logger.Info("api listener", "http_addr", cfg.Server.HTTPAddr, "tls", srv.TLSConfig != nil)
	return srv, nil
}

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
	return issuer.New(ctx, trustedIssuerConfig(s))
}

// trustedIssuerConfig maps auth.trusted_issuer onto the verifier's config.
func trustedIssuerConfig(s config.TrustedIssuerSection) issuer.Config {
	return issuer.Config{
		Name:           s.Name,
		Issuer:         s.Issuer,
		JWKSURL:        s.JWKSURL,
		Audience:       s.Audience,
		TenantClaim:    s.TenantClaim,
		AllowedTenants: s.AllowedTenants,
		MaxLifetime:    time.Duration(s.MaxLifetimeSeconds) * time.Second,

		BearerAudiences:   s.BearerAudiences,
		BearerMaxLifetime: time.Duration(s.BearerMaxLifetimeSeconds) * time.Second,
		DeployAudience:    s.DeployAudience,
	}
}

// issuerBearer returns the trusted issuer as the verifier of request bearers
// when auth.trusted_issuer.bearer_audiences (#1468) or deploy_audience
// (#1773) is set, or nil, which leaves the bearer mode off.
func issuerBearer(ti api.TrustedIssuer) api.TrustedIssuerBearer {
	v, ok := ti.(*issuer.Verifier)
	if !ok || !v.BearerEnabled() {
		return nil
	}
	return v
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

func buildAPIServer(cfg *config.ServerConfig, tel *observability.Telemetry, authn *auth.JWTAuthenticator, pg *storage.Postgres, repo *storage.Repository, xcomReader *storage.XComReader, logSink logs.Sink, logTailer logs.Tailer, checks map[string]api.HealthChecker, executorInfo api.ExecutorInfo, schedulerHealth api.Heartbeater, oidcFlow *oidc.Flow, trustedIssuer api.TrustedIssuer) (*http.Server, error) {
	if cfg.Auth.DevNoAuth {
		tel.Logger.Warn("AUTHENTICATION DISABLED (auth.dev_no_auth): every request is treated as admin. Dev only: NEVER use in production")
	}
	uiSrv, editorFS := newUIServer(cfg, tel.Logger)
	// Gzip the SPA bundle once, off the startup path, so no browser pays it.
	go uiSrv.Precompress()

	// CI OIDC (ADR 0092): unchanged deps unless auth.ci_oidc lists an issuer.
	handler, err := api.NewServer(withCIOIDC(withAPITokens(api.Dependencies{
		Observability: api.ObservabilityDeps{
			Logger:          tel.Logger,
			Registry:        tel.Registry,
			Metrics:         tel.Metrics,
			Tracer:          tel.Tracer,
			HealthChecks:    checks,
			ExecutorInfo:    executorInfo,
			SchedulerHealth: schedulerHealth,
			ProbeBudget:     cfg.Server.ProbeBudget,
		},
		Server: api.ServerDeps{
			CORSOrigins:         cfg.Server.CORS.AllowedOrigins,
			TrustedProxies:      cfg.Server.TrustedProxies,
			GzipResponses:       cfg.Server.GzipResponses,
			MaxPageLimit:        cfg.Server.MaxPageLimit,
			MaxRequestBodyBytes: cfg.Server.MaxRequestBodyBytes,
			MaxUploadBodyBytes:  cfg.Server.MaxUploadBodyBytes,
			Edition:             cfg.UI.Edition,
			PoolsReadOnly:       cfg.Server.PoolsReadOnly,
		},
		Auth: api.AuthDeps{
			// withAPITokens puts the API tokens (ADR 0074) in front of it unless
			// auth.api_tokens.disabled is true.
			Authenticator:        authn,
			RateLimiter:          auth.NewRateLimiter(loginRateLimit(cfg), time.Minute),
			TokenTTLSecs:         cfg.Auth.JWT.TokenTTLSeconds,
			TokenRenewer:         authn,
			TokenMaxLifetimeSecs: cfg.Auth.JWT.MaxLifetimeSeconds,
			DevNoAuth:            cfg.Auth.DevNoAuth,

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
			// The device sign-in of the CLI (ADR 0081) keeps its pending
			// sign-ins in the database, shared by every replica.
			CLIDevices: repo,

			SessionCookieInsecure: cfg.Auth.SessionCookieInsecure,
			SessionCookieName:     cfg.Auth.SessionCookieName,
		},
		Issuers: api.IssuerDeps{
			// Trusted-issuer handoff (#1284): nil unless auth.trusted_issuer is set,
			// which leaves POST /api/v2/auth/session unregistered.
			TrustedIssuer:        trustedIssuer,
			TrustedIssuerUsers:   repo,
			TrustedIssuerOrigins: cfg.Auth.TrustedIssuer.AllowedOrigins,
			// Host binding (#1463): empty unless auth.trusted_issuer.host_pattern is set.
			TrustedIssuerHostPattern: cfg.Auth.TrustedIssuer.HostPattern,
			// Trusted-issuer bearer (#1468): nil unless bearer audiences are set.
			TrustedIssuerBearer: issuerBearer(trustedIssuer),
			// Deploys through the issuer only (#1773): off unless
			// auth.trusted_issuer.deploy_audience is set.
			DeployViaIssuer: cfg.Auth.TrustedIssuer.DeployAudience != "",
			// Operator service API (#1283): off unless auth.service_token is set.
			ServiceToken:   cfg.Auth.ServiceToken,
			ServiceTenants: repo,
			// The service API links users only in tenants the issuer may sign in to.
			ServiceAllowedTenants: cfg.Auth.TrustedIssuer.AllowedTenants,
		},
		UI: api.UIDeps{
			InstanceName:               cfg.UI.InstanceName,
			AutoRefreshIntervalSeconds: cfg.UI.AutoRefreshIntervalSeconds,
			Theme:                      uiTheme(cfg),
			ETagRevalidation:           cfg.UI.ETagRevalidation,
			SPA:                        uiSrv,
			Workspace:                  editorFS,
			MonacoDir:                  cfg.UI.MonacoDir,
			ExamplesFS:                 dexaflow.ExampleDAGs(),
		},
		Execution: api.ExecutionDeps{
			ResourceUnit:      resourceUnit(cfg),
			UnitMisfits:       unitMisfits(tel.Metrics),
			SourceModeImage:   cfg.Execution.SourceMode.RuntimeImage(),
			ExecutionPolicy:   executionPolicy(cfg.Executor.Defaults),
			DeployDefaults:    cfg.AdvertisedDeployDefaults(),
			RequireDigest:     cfg.Deploy.RequireDigest,
			UnpinnedImages:    unpinnedImages(tel.Metrics),
			WarmPolicy:        warmPolicy(cfg),
			WarmPools:         cfg.Execution,
			WarmWorkersAbsent: warmWorkersAbsent(cfg, executorInfo.PodDispatchEnabled),
			TaskPods: taskPodSettings(cfg, executorInfo.PodDispatchEnabled, func() (kubernetes.Interface, error) {
				return buildK8sClient(cfg.Executor.KubeClient)
			}, tel.Logger),
		},
		Stores: api.StoreDeps{
			Dags:           repo,
			DagRuns:        repo,
			Tasks:          repo,
			Versions:       repo,
			Xcoms:          xcomReader,
			Logs:           storage.NewLogReader(pg, logSink, logTailer),
			EphemeralLogs:  cfg.Logs.Backend == "disk" && !cfg.Logs.PersistenceEnabled,
			Specs:          repo,
			DagSources:     repo,
			LatestRuns:     repo,
			TaskSummary:    repo,
			DagVersions:    repo,
			DashboardStats: repo,
			AuditLog:       repo,
			Variables:      repo,
			Users:          repo,
			UserAudit:      repo,
			Connections:    repo,
			Pools:          repo,
			Favorites:      repo,
			ImportErrors:   repo,
			Audit:          repo,
		},
	}, cfg, repo), cfg, repo, tel.Metrics))
	if err != nil {
		return nil, err
	}
	return newHTTPServer(cfg.Server.HTTPAddr, handler, cfg), nil
}

// newHTTPServer builds an HTTP listener with the configured timeouts. The
// header timeout is always on. ReadTimeout and IdleTimeout come from
// server.read_timeout and server.idle_timeout (0, the default, leaves them
// off). WriteTimeout is deliberately never set: a write deadline would cut
// live log tails and long downloads mid-stream.
//
// net/http uses ReadTimeout as the idle timeout when IdleTimeout is 0, so with
// only a read timeout set an idle 0 is passed on as negative (no idle timeout):
// idle keep-alive connections stay open, as before, until idle_timeout is set.
// The header timeout is 10s, or read_timeout when that is shorter.
func newHTTPServer(addr string, handler http.Handler, cfg *config.ServerConfig) *http.Server {
	idle := cfg.Server.IdleTimeout
	if idle == 0 && cfg.Server.ReadTimeout > 0 {
		idle = -1
	}
	header := 10 * time.Second
	if rt := cfg.Server.ReadTimeout; rt > 0 && rt < header {
		header = rt
	}
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: header,
		ReadTimeout:       cfg.Server.ReadTimeout,
		IdleTimeout:       idle,
	}
}

// serveHTTP starts the process's HTTP listeners, the api role's API+UI (when
// servesAPI) plus /metrics in every role, so a scheduler-only pod stays scrapable,
// and blocks until one errors or ctx is canceled, then shuts them down.
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
