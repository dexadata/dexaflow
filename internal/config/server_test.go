package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadServerAppliesDefaults(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	checks := map[string]struct{ got, want any }{
		"http_addr":         {c.Server.HTTPAddr, "0.0.0.0:8080"},
		"metrics_addr":      {c.Server.MetricsAddr, "0.0.0.0:9090"},
		"grpc_addr":         {c.Server.GRPCAddr, "0.0.0.0:9091"},
		"logs.dir":          {c.Logs.Dir, "/var/log/leoflow"},
		"database.url":      {c.Database.URL, "postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable"},
		"max_open_conns":    {c.Database.MaxOpenConns, 25},
		"redis.url":         {c.Redis.URL, ""},
		"auth.provider":     {c.Auth.Provider, "jwt"},
		"token_ttl":         {c.Auth.JWT.TokenTTLSeconds, 3600},
		"loop_interval_ms":  {c.Scheduler.LoopIntervalMS, 1000},
		"scheduler.enabled": {c.Scheduler.Enabled, true},
		// Default is sync passthrough (#127): Pro deployments opt in via values.yaml.
		"scheduler.dispatch.buffer_size": {c.Scheduler.Dispatch.BufferSize, 0},
		"scheduler.dispatch.workers":     {c.Scheduler.Dispatch.Workers, 0},
		"otel.enabled":                   {c.Observability.OTel.Enabled, false},
		"log_level":                      {c.Observability.LogLevel, "info"},
		"log_format":                     {c.Observability.LogFormat, "json"},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", name, c.got, c.want)
		}
	}
}

func TestLoadServerExecutorHTTPDefaults(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.HTTP.UserAgent != "leoflow/0.1" {
		t.Errorf("user_agent = %q, want leoflow/0.1", c.Executor.HTTP.UserAgent)
	}
}

func TestLoadServerAgentControlPlaneAddr(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.AgentControlPlaneAddr != "" {
		t.Errorf("default agent_control_plane_addr = %q, want empty (falls back to grpc_addr)", c.Executor.AgentControlPlaneAddr)
	}
	t.Setenv("LEOFLOW_EXECUTOR_AGENT_CONTROL_PLANE_ADDR", "host.k3d.internal:9091")
	c, err = LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.AgentControlPlaneAddr != "host.k3d.internal:9091" {
		t.Errorf("agent_control_plane_addr = %q, want host.k3d.internal:9091", c.Executor.AgentControlPlaneAddr)
	}
}

func TestLoadServerExecutorTypeDefault(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.Type != "kubernetes" {
		t.Errorf("default executor.type = %q, want kubernetes", c.Executor.Type)
	}
	if c.Executor.AgentPath != "leoflow-agent" {
		t.Errorf("default executor.agent_path = %q, want leoflow-agent", c.Executor.AgentPath)
	}
	t.Setenv("LEOFLOW_EXECUTOR_TYPE", "subprocess")
	c, err = LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.Type != "subprocess" {
		t.Errorf("executor.type = %q, want subprocess", c.Executor.Type)
	}
}

func TestLoadServerEnvOverridesNestedKey(t *testing.T) {
	t.Setenv("LEOFLOW_SERVER_HTTP_ADDR", "127.0.0.1:9999")
	t.Setenv("LEOFLOW_AUTH_JWT_SECRET", "s3cr3t")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Server.HTTPAddr != "127.0.0.1:9999" {
		t.Errorf("HTTPAddr = %q, want 127.0.0.1:9999", c.Server.HTTPAddr)
	}
	if c.Auth.JWT.Secret != "s3cr3t" {
		t.Errorf("JWT.Secret = %q, want s3cr3t", c.Auth.JWT.Secret)
	}
}

func TestLoadServerFileOverridesDefaults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.yaml")
	body := "server:\n  http_addr: \"0.0.0.0:7000\"\nauth:\n  jwt:\n    secret: filesecret\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServer(p, nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Server.HTTPAddr != "0.0.0.0:7000" {
		t.Errorf("HTTPAddr = %q, want 0.0.0.0:7000", c.Server.HTTPAddr)
	}
	if c.Auth.JWT.Secret != "filesecret" {
		t.Errorf("JWT.Secret = %q, want filesecret", c.Auth.JWT.Secret)
	}
}

func TestServerConfigValidateRequiresJWTSecret(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil with empty JWT secret, want error")
	}
	c.Auth.JWT.Secret = "set"
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v with JWT secret set, want nil", err)
	}
}

// TestServerConfigValidateProviderAllowlist locks the auth.provider allowlist so
// an unimplemented or misspelled provider fails closed at boot rather than
// silently falling back to the JWT authenticator main.go always builds.
func TestServerConfigValidateProviderAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		secret   string
		wantErr  bool
	}{
		{"unset defaults to jwt", "", "set", false},
		{"jwt with secret", "jwt", "set", false},
		{"jwt without secret", "jwt", "", true},
		{"oidc without pro edition or fields", "oidc", "set", true},
		{"garbage unknown provider", "garbage", "set", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Auth.Provider = tc.provider
			c.Auth.JWT.Secret = tc.secret
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("provider %q secret %q: Validate() = nil, want error", tc.provider, tc.secret)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("provider %q secret %q: Validate() = %v, want nil", tc.provider, tc.secret, err)
			}
		})
	}
}

// TestServerConfigSecretPolicyDefaults locks the SAFE defaults (ADR 0055): a
// fresh config binds secret_scoping=permissive and secret_liveness_mode=observe,
// so a default deploy denies nothing.
func TestServerConfigSecretPolicyDefaults(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Auth.SecretScoping != "permissive" {
		t.Errorf("SecretScoping default = %q, want permissive", c.Auth.SecretScoping)
	}
	if c.Auth.SecretLivenessMode != "observe" {
		t.Errorf("SecretLivenessMode default = %q, want observe", c.Auth.SecretLivenessMode)
	}
}

// TestServerConfigSecretPolicyEnvBinding proves the two keys bind from their
// LEOFLOW_* env vars (they are registered in serverDefaults so AutomaticEnv sees
// them).
func TestServerConfigSecretPolicyEnvBinding(t *testing.T) {
	t.Setenv("LEOFLOW_AUTH_SECRET_SCOPING", "enforce")
	t.Setenv("LEOFLOW_AUTH_SECRET_LIVENESS_MODE", "enforce")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Auth.SecretScoping != "enforce" {
		t.Errorf("SecretScoping = %q, want enforce (from env)", c.Auth.SecretScoping)
	}
	if c.Auth.SecretLivenessMode != "enforce" {
		t.Errorf("SecretLivenessMode = %q, want enforce (from env)", c.Auth.SecretLivenessMode)
	}
}

// TestServerConfigValidateSecretPolicies locks the enum allowlist: valid values
// (and empty = default) pass; an unknown value fails closed at boot.
func TestServerConfigValidateSecretPolicies(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scoping  string
		liveness string
		wantErr  bool
	}{
		{"empty defaults", "", "", false},
		{"permissive+observe", "permissive", "observe", false},
		{"enforce+enforce", "enforce", "enforce", false},
		{"off+observe", "off", "observe", false},
		{"bad scoping", "loose", "observe", true},
		{"bad liveness", "permissive", "loud", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Auth.JWT.Secret = "set"
			c.Auth.SecretScoping = tc.scoping
			c.Auth.SecretLivenessMode = tc.liveness
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("scoping=%q liveness=%q: Validate() = nil, want error", tc.scoping, tc.liveness)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("scoping=%q liveness=%q: Validate() = %v, want nil", tc.scoping, tc.liveness, err)
			}
		})
	}
}

// validOIDCConfig returns a ServerConfig that passes the OIDC boot gate: Pro
// edition, a JWT secret (oidc still mints the app's own _token), and the three
// required oidc fields with an https issuer.
func validOIDCConfig() *ServerConfig {
	c := &ServerConfig{}
	c.Auth.Provider = "oidc"
	c.Auth.JWT.Secret = "set"
	c.UI.Edition = "pro"
	c.Auth.OIDC.Issuer = "https://idp.example.com"
	c.Auth.OIDC.ClientID = "client-123"
	c.Auth.OIDC.RedirectURL = "https://app.example.com/api/v2/auth/oidc/callback"
	// The tenant pin is part of a working configuration, not an extra. Verify
	// resolves a tenant on every login and fails closed when the claim is unset
	// or unmapped, so a config without these two rejects 100% of logins (#1143).
	// This helper used to omit them, which meant "valid" here described a
	// deployment nobody could log in to.
	c.Auth.OIDC.TenantClaim = "tid"
	c.Auth.OIDC.TenantClaims = map[string]string{"t-123": "default"}
	return c
}

// TestServerConfigValidateOIDCPassesWhenComplete locks that a fully-configured,
// Pro-edition OIDC deployment boots.
func TestServerConfigValidateOIDCPassesWhenComplete(t *testing.T) {
	if err := validOIDCConfig().Validate(); err != nil {
		t.Errorf("Validate() = %v for a complete Pro OIDC config, want nil", err)
	}
}

// TestServerConfigValidateOIDCRequiresPro locks D7: provider oidc without the
// Pro edition fails boot closed.
func TestServerConfigValidateOIDCRequiresPro(t *testing.T) {
	c := validOIDCConfig()
	c.UI.Edition = ""
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil for oidc without Pro edition, want error")
	}
	if !strings.Contains(err.Error(), "Pro edition") {
		t.Errorf("error = %q, want it to mention the Pro edition", err.Error())
	}
}

// TestServerConfigValidateOIDCRequiresFields locks D7: each missing required
// oidc field fails boot closed and names the offending key.
func TestServerConfigValidateOIDCRequiresFields(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*ServerConfig)
		want string
	}{
		{"missing issuer", func(c *ServerConfig) { c.Auth.OIDC.Issuer = "" }, "auth.oidc.issuer"},
		{"missing client_id", func(c *ServerConfig) { c.Auth.OIDC.ClientID = "" }, "auth.oidc.client_id"},
		{"missing redirect_url", func(c *ServerConfig) { c.Auth.OIDC.RedirectURL = "" }, "auth.oidc.redirect_url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validOIDCConfig()
			tc.mut(c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil with %s, want error", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q", err.Error(), tc.want)
			}
		})
	}
}

// TestServerConfigValidateOIDCRequiresHTTPSIssuer locks that a non-https issuer
// is rejected: discovery and JWKS must be fetched over TLS (keyless verify).
func TestServerConfigValidateOIDCRequiresHTTPSIssuer(t *testing.T) {
	c := validOIDCConfig()
	c.Auth.OIDC.Issuer = "http://idp.example.com"
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil for an http:// issuer, want error")
	}
}

// TestServerConfigValidateOIDCRedirectURLScheme locks that the callback URL the
// browser is redirected to (and where the IdP posts the code back) is https,
// except for loopback hosts which may use http for local dev. A plaintext
// redirect to a non-loopback host would expose the code in transit, so it fails
// boot closed.
func TestServerConfigValidateOIDCRedirectURLScheme(t *testing.T) {
	for _, tc := range []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"https non-loopback ok", "https://app.example.com/api/v2/auth/oidc/callback", false},
		{"http localhost ok", "http://localhost:8080/api/v2/auth/oidc/callback", false},
		{"http 127.0.0.1 ok", "http://127.0.0.1:8080/api/v2/auth/oidc/callback", false},
		{"http ipv6 loopback ok", "http://[::1]:8080/api/v2/auth/oidc/callback", false},
		{"https localhost ok", "https://localhost:8080/api/v2/auth/oidc/callback", false},
		{"http non-loopback rejected", "http://app.example.com/api/v2/auth/oidc/callback", true},
		{"non-url rejected", "not-a-url", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validOIDCConfig()
			c.Auth.OIDC.RedirectURL = tc.url
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil for redirect_url %q, want error", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v for redirect_url %q, want nil", err, tc.url)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), "auth.oidc.redirect_url") {
				t.Errorf("error = %q, want it to name auth.oidc.redirect_url", err.Error())
			}
		})
	}
}

// TestServerConfigValidateOIDCRequiresJWTSecret locks that oidc still needs the
// JWT secret, because the callback mints the app's own HS256 _token.
func TestServerConfigValidateOIDCRequiresJWTSecret(t *testing.T) {
	c := validOIDCConfig()
	c.Auth.JWT.Secret = ""
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil for oidc without a JWT secret, want error")
	}
}

func TestValidateRejectsDevNoAuthOnNonLoopback(t *testing.T) {
	base := func() *ServerConfig {
		c := &ServerConfig{}
		c.Auth.Provider = "jwt"
		c.Auth.JWT.Secret = "set" // satisfy the jwt-secret requirement
		c.Auth.DevNoAuth = true
		return c
	}
	// Exposed on all interfaces with auth disabled → must be rejected.
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "192.168.1.10:8080"} {
		c := base()
		c.Server.HTTPAddr = addr
		if err := c.Validate(); err == nil {
			t.Errorf("dev_no_auth on %q must be rejected", addr)
		}
	}
	// Loopback is allowed (the no-auth API is not reachable off-host).
	for _, addr := range []string{"127.0.0.1:8080", "localhost:8080"} {
		c := base()
		c.Server.HTTPAddr = addr
		if err := c.Validate(); err != nil {
			t.Errorf("dev_no_auth on loopback %q should be allowed, got %v", addr, err)
		}
	}
	// dev_no_auth off → any address is fine.
	c := &ServerConfig{}
	c.Server.HTTPAddr = "0.0.0.0:8080"
	if err := c.Validate(); err != nil {
		t.Errorf("non-dev config should validate, got %v", err)
	}
}

// TestLoadServerReadsUIAutoRefreshIntervalFromEnv pins the bug that broke #247:
// `dexaflow lite` exports LEOFLOW_UI_AUTO_REFRESH_INTERVAL_SECONDS=1 so the SPA
// polls fast in the dev loop, but the server returned 30 (the handler fallback)
// because `ui.auto_refresh_interval_seconds` was missing from serverDefaults —
// without an entry there, viper's AutomaticEnv never bound the env key, so the
// value silently fell back to the zero default. Users had to reload the page
// to see state changes (observed locally 2026-06-01).
func TestLoadServerReadsUIAutoRefreshIntervalFromEnv(t *testing.T) {
	t.Setenv("LEOFLOW_UI_AUTO_REFRESH_INTERVAL_SECONDS", "1")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.UI.AutoRefreshIntervalSeconds != 1 {
		t.Errorf("UI.AutoRefreshIntervalSeconds = %d, want 1 (env var must override default)", c.UI.AutoRefreshIntervalSeconds)
	}
}

// TestLoadServerLogsBackendDefaultsToDisk locks the off-by-default guarantee:
// with nothing configured the log backend is "disk", so the on-disk path (Lite
// and every install that does not opt in) is unchanged.
func TestLoadServerLogsBackendDefaultsToDisk(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Logs.Backend != "disk" {
		t.Errorf("Logs.Backend = %q, want \"disk\" (object storage must be opt-in)", c.Logs.Backend)
	}
	if c.Logs.Sink.Bucket != "" {
		t.Errorf("Logs.Sink.Bucket = %q, want empty by default", c.Logs.Sink.Bucket)
	}
}

// TestLoadServerReadsLogsSinkS3FromEnv checks the S3 surface binds from
// LEOFLOW_LOGS_SINK_* env vars, the path the Helm chart uses.
func TestLoadServerReadsLogsSinkS3FromEnv(t *testing.T) {
	t.Setenv("LEOFLOW_LOGS_BACKEND", "s3")
	t.Setenv("LEOFLOW_LOGS_SINK_BUCKET", "my-bucket")
	t.Setenv("LEOFLOW_LOGS_SINK_PREFIX", "acme")
	t.Setenv("LEOFLOW_LOGS_SINK_ENDPOINT", "http://minio.internal:9000")
	t.Setenv("LEOFLOW_LOGS_SINK_FORCE_PATH_STYLE", "true")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Logs.Backend != "s3" {
		t.Errorf("Logs.Backend = %q, want \"s3\"", c.Logs.Backend)
	}
	if c.Logs.Sink.Bucket != "my-bucket" {
		t.Errorf("Logs.Sink.Bucket = %q, want \"my-bucket\"", c.Logs.Sink.Bucket)
	}
	if c.Logs.Sink.Prefix != "acme" {
		t.Errorf("Logs.Sink.Prefix = %q, want \"acme\"", c.Logs.Sink.Prefix)
	}
	if c.Logs.Sink.Endpoint != "http://minio.internal:9000" {
		t.Errorf("Logs.Sink.Endpoint = %q, want the MinIO endpoint", c.Logs.Sink.Endpoint)
	}
	if !c.Logs.Sink.ForcePathStyle {
		t.Error("Logs.Sink.ForcePathStyle = false, want true from env")
	}
}

// TestLoadServerReadsLogsSinkGCSFromEnv checks the GCS surface binds from the same
// LEOFLOW_LOGS_SINK_* env vars — a bucket (and optional keyless-escape-hatch
// credentials file), with no S3-only region/endpoint required.
func TestLoadServerReadsLogsSinkGCSFromEnv(t *testing.T) {
	t.Setenv("LEOFLOW_LOGS_BACKEND", "gcs")
	t.Setenv("LEOFLOW_LOGS_SINK_BUCKET", "gcs-logs")
	t.Setenv("LEOFLOW_LOGS_SINK_PREFIX", "team-a")
	t.Setenv("LEOFLOW_LOGS_SINK_CREDENTIALS_FILE", "/var/run/secrets/gcs/key.json")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Logs.Backend != "gcs" {
		t.Errorf("Logs.Backend = %q, want \"gcs\"", c.Logs.Backend)
	}
	if c.Logs.Sink.Bucket != "gcs-logs" {
		t.Errorf("Logs.Sink.Bucket = %q, want \"gcs-logs\"", c.Logs.Sink.Bucket)
	}
	if c.Logs.Sink.CredentialsFile != "/var/run/secrets/gcs/key.json" {
		t.Errorf("Logs.Sink.CredentialsFile = %q, want the mounted key path", c.Logs.Sink.CredentialsFile)
	}
}

// TestValidateLogsBackend locks the log-backend validation: "s3" and "gcs" each
// require a bucket, an unknown backend fails closed, and disk/empty stay valid.
func TestValidateLogsBackend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend string
		bucket  string
		wantErr bool
	}{
		{"empty defaults to disk", "", "", false},
		{"disk", "disk", "", false},
		{"s3 with bucket", "s3", "b", false},
		{"s3 without bucket", "s3", "", true},
		{"gcs with bucket", "gcs", "b", false},
		{"gcs without bucket", "gcs", "", true},
		{"unknown backend", "gopher", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Auth.Provider = AuthProviderJWT
			c.Auth.JWT.Secret = "set"
			c.Server.HTTPAddr = "0.0.0.0:8080"
			c.Logs.Backend = tc.backend
			c.Logs.Sink.Bucket = tc.bucket
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("backend %q bucket %q: Validate() = nil, want error", tc.backend, tc.bucket)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("backend %q bucket %q: Validate() = %v, want nil", tc.backend, tc.bucket, err)
			}
		})
	}
}

// TestLoadServerDottedOIDCMapKeys locks the fix for #826: a MAP KEY containing
// dots (Google Workspace `hd` = a domain; a dotted IdP group name) must survive
// config decoding. viper's key delimiter is ".", so without the empty-map
// defaults registered in serverDefaults the key `example.com` is flattened into
// nested maps and fails to decode into map[string]string. This is a regression
// lock: if someone removes those "unused" defaults, this test goes red before the
// bug reaches a user (it manifests only as a rejected Google/Okta login).
func TestLoadServerDottedOIDCMapKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
auth:
  jwt:
    secret: "test-secret"
  oidc:
    tenant_claim: hd
    tenant_claims:
      "example.com": acme
      "sub.example.co.uk": acme
    role_mappings:
      "app.admins": admin
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := LoadServer(path, nil)
	if err != nil {
		t.Fatalf("LoadServer with dotted OIDC map keys errored (#826 regression): %v", err)
	}
	if got := c.Auth.OIDC.TenantClaims["example.com"]; got != "acme" {
		t.Errorf("tenant_claims[example.com] = %q, want acme (dotted key was split)", got)
	}
	if got := c.Auth.OIDC.TenantClaims["sub.example.co.uk"]; got != "acme" {
		t.Errorf("tenant_claims[sub.example.co.uk] = %q, want acme (multi-dot key was split)", got)
	}
	if got := c.Auth.OIDC.RoleMappings["app.admins"]; got != "admin" {
		t.Errorf("role_mappings[app.admins] = %q, want admin (dotted group was split)", got)
	}
}

// TestLoadServerReadsUIHomeLinkFromEnv locks that both home-link keys bind
// from the environment. Viper's AutomaticEnv only binds keys it has a default
// for, so a missing default would drop LEOFLOW_UI_HOME_LINK_* silently, the
// failure TestLoadServerReadsUIAutoRefreshIntervalFromEnv already caught once.
func TestLoadServerReadsUIHomeLinkFromEnv(t *testing.T) {
	t.Setenv("LEOFLOW_UI_HOME_LINK_LABEL", "Back to portal")
	t.Setenv("LEOFLOW_UI_HOME_LINK_URL", "https://portal.example.com/team")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.UI.HomeLink.Label != "Back to portal" || c.UI.HomeLink.URL != "https://portal.example.com/team" {
		t.Errorf("UI.HomeLink = %+v, want the values from the environment", c.UI.HomeLink)
	}
}

// TestLoadServerHomeLinkIsOffByDefault locks the default: no link unless the
// operator sets one.
func TestLoadServerHomeLinkIsOffByDefault(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.UI.HomeLink != (HomeLinkSection{}) {
		t.Errorf("UI.HomeLink = %+v, want empty by default", c.UI.HomeLink)
	}
}

// TestValidateUIHomeLink covers the boot checks: the link needs both a label
// and an absolute http(s) URL, so a typo fails boot instead of rendering a
// dead or script-bearing link.
func TestValidateUIHomeLink(t *testing.T) {
	cases := []struct {
		name    string
		link    HomeLinkSection
		wantErr string
	}{
		{"unset", HomeLinkSection{}, ""},
		{"https", HomeLinkSection{Label: "Portal", URL: "https://portal.example.com"}, ""},
		{"http", HomeLinkSection{Label: "Portal", URL: "http://portal.internal:8080/x"}, ""},
		{"label without url", HomeLinkSection{Label: "Portal"}, "ui.home_link.url"},
		{"url without label", HomeLinkSection{URL: "https://portal.example.com"}, "ui.home_link.label"},
		{"blank label", HomeLinkSection{Label: "  ", URL: "https://portal.example.com"}, "ui.home_link.label"},
		{"javascript scheme", HomeLinkSection{Label: "Portal", URL: "javascript:alert(1)"}, "http:// or https://"},
		{"relative", HomeLinkSection{Label: "Portal", URL: "/portal"}, "http:// or https://"},
		{"no host", HomeLinkSection{Label: "Portal", URL: "https:///path"}, "host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Auth.JWT.Secret = "set"
			c.UI.HomeLink = tc.link
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Validate() = %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

// TestLoadServerReadsUIBrandingFromEnv locks that the branding keys (#1289)
// bind from the environment, the list one comma-split like trusted_proxies.
func TestLoadServerReadsUIBrandingFromEnv(t *testing.T) {
	t.Setenv("LEOFLOW_UI_THEME", `{"tokens":{"colors":{"brand":{"500":{"value":"#3b82f6"}}}}}`)
	t.Setenv("LEOFLOW_UI_FAVICON_URL", "https://cdn.example.com/favicon.png")
	t.Setenv("LEOFLOW_UI_STYLESHEET_URLS", "https://fonts.example.com/a.css,https://cdn.example.com/b.css")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if !strings.Contains(c.UI.Theme, `"brand"`) {
		t.Errorf("UI.Theme = %q, want the JSON from the environment", c.UI.Theme)
	}
	if c.UI.FaviconURL != "https://cdn.example.com/favicon.png" {
		t.Errorf("UI.FaviconURL = %q", c.UI.FaviconURL)
	}
	want := []string{"https://fonts.example.com/a.css", "https://cdn.example.com/b.css"}
	if len(c.UI.StylesheetURLs) != 2 || c.UI.StylesheetURLs[0] != want[0] || c.UI.StylesheetURLs[1] != want[1] {
		t.Errorf("UI.StylesheetURLs = %v, want %v", c.UI.StylesheetURLs, want)
	}
}

// TestLoadServerBrandingIsOffByDefault locks that a default install keeps the
// stock look: no theme, favicon or extra stylesheet.
func TestLoadServerBrandingIsOffByDefault(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.UI.Theme != "" || c.UI.FaviconURL != "" || len(c.UI.StylesheetURLs) != 0 {
		t.Errorf("branding defaults = theme %q favicon %q stylesheets %v, want all empty",
			c.UI.Theme, c.UI.FaviconURL, c.UI.StylesheetURLs)
	}
}

// TestValidateUIBranding covers the boot checks: the theme is a JSON object
// with only the keys the Airflow 3.2.1 UI reads, and every URL is http(s) or
// root-relative, so a typo fails boot instead of shipping a broken look and
// no javascript: or data: URL reaches the page.
func TestValidateUIBranding(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*UISection)
		wantErr string
	}{
		{"unset", func(*UISection) {}, ""},
		{"full theme", func(u *UISection) {
			u.Theme = `{"tokens":{"colors":{}},"globalCss":{"body":{"fontFamily":"Outfit"}},"icon":"https://x.example/i.svg","icon_dark_mode":"/static/i-dark.svg"}`
		}, ""},
		{"theme not json", func(u *UISection) { u.Theme = `{tokens:` }, "ui.theme"},
		{"theme not an object", func(u *UISection) { u.Theme = `["tokens"]` }, "ui.theme"},
		{"theme unknown key", func(u *UISection) { u.Theme = `{"tokenz":{}}` }, "tokenz"},
		{"theme icon javascript", func(u *UISection) { u.Theme = `{"icon":"javascript:alert(1)"}` }, "ui.theme icon"},
		{"theme icon not a string", func(u *UISection) { u.Theme = `{"icon":3}` }, "ui.theme icon"},
		{"favicon https", func(u *UISection) { u.FaviconURL = "https://cdn.example.com/f.png" }, ""},
		{"favicon root-relative", func(u *UISection) { u.FaviconURL = "/brand/f.png" }, ""},
		{"favicon data", func(u *UISection) { u.FaviconURL = "data:image/png;base64,AAAA" }, "ui.favicon_url"},
		{"favicon protocol-relative", func(u *UISection) { u.FaviconURL = "//evil.example/f.png" }, "ui.favicon_url"},
		{"stylesheet javascript", func(u *UISection) { u.StylesheetURLs = []string{"https://ok.example/a.css", "javascript:x"} }, "ui.stylesheet_urls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Auth.JWT.Secret = "set"
			tc.mutate(&c.UI)
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Validate() = %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

// TestLoadServerReadsExternalAuthURLsFromEnv locks that both #1288 keys bind
// from the environment and default to empty (Leoflow's own pages).
func TestLoadServerReadsExternalAuthURLsFromEnv(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Auth.ExternalSignInURL != "" || c.Auth.ExternalSignOutURL != "" {
		t.Fatalf("defaults = %q / %q, want empty", c.Auth.ExternalSignInURL, c.Auth.ExternalSignOutURL)
	}

	t.Setenv("LEOFLOW_AUTH_EXTERNAL_SIGNIN_URL", "https://portal.example.com/engine")
	t.Setenv("LEOFLOW_AUTH_EXTERNAL_SIGNOUT_URL", "https://portal.example.com/signout")
	c, err = LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Auth.ExternalSignInURL != "https://portal.example.com/engine" || c.Auth.ExternalSignOutURL != "https://portal.example.com/signout" {
		t.Errorf("Auth external URLs = %q / %q, want the values from the environment", c.Auth.ExternalSignInURL, c.Auth.ExternalSignOutURL)
	}
}

// TestValidateExternalAuthURLs covers the boot checks: absolute http(s) URLs
// with a host, so neither setting can become a script URL or a relative
// redirect back into Leoflow that loops.
func TestValidateExternalAuthURLs(t *testing.T) {
	cases := []struct {
		name            string
		signIn, signOut string
		wantErr         string
	}{
		{"unset", "", "", ""},
		{"both https", "https://portal.example.com/engine", "https://portal.example.com/signout", ""},
		{"loopback http", "http://localhost:3000/engine", "", ""},
		{"relative sign-in", "/api/v2/auth/login", "", "auth.external_signin_url"},
		{"javascript sign-out", "", "javascript:alert(1)", "auth.external_signout_url"},
		{"no host", "https:///engine", "", "auth.external_signin_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Auth.JWT.Secret = "set"
			c.Auth.ExternalSignInURL, c.Auth.ExternalSignOutURL = tc.signIn, tc.signOut
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("Validate() = %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

// TestLoadServerReadsTrustedIssuerFromEnv locks that every #1284 key binds
// from the environment, the tenant list comma-split, and that the section is
// empty by default (no handoff endpoint).
func TestLoadServerReadsTrustedIssuerFromEnv(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Auth.TrustedIssuer.Issuer != "" || c.Auth.TrustedIssuer.TenantClaim != "tenant_id" {
		t.Fatalf("defaults = %+v, want no issuer and tenant_claim tenant_id", c.Auth.TrustedIssuer)
	}

	t.Setenv("LEOFLOW_AUTH_TRUSTED_ISSUER_NAME", "portal")
	t.Setenv("LEOFLOW_AUTH_TRUSTED_ISSUER_ISSUER", "https://portal.example.com")
	t.Setenv("LEOFLOW_AUTH_TRUSTED_ISSUER_JWKS_URL", "https://portal.example.com/jwks")
	t.Setenv("LEOFLOW_AUTH_TRUSTED_ISSUER_AUDIENCE", "leoflow-engine")
	t.Setenv("LEOFLOW_AUTH_TRUSTED_ISSUER_TENANT_CLAIM", "org")
	t.Setenv("LEOFLOW_AUTH_TRUSTED_ISSUER_ALLOWED_TENANTS", "acme,globex")
	t.Setenv("LEOFLOW_AUTH_TRUSTED_ISSUER_MAX_LIFETIME_SECONDS", "300")
	t.Setenv("LEOFLOW_AUTH_TRUSTED_ISSUER_ALLOWED_ORIGINS", "https://portal.example.com,http://localhost:3000")
	c, err = LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	want := TrustedIssuerSection{
		Name: "portal", Issuer: "https://portal.example.com", JWKSURL: "https://portal.example.com/jwks",
		Audience: "leoflow-engine", TenantClaim: "org", AllowedTenants: []string{"acme", "globex"}, MaxLifetimeSeconds: 300,
	}
	got := c.Auth.TrustedIssuer
	if got.Name != want.Name || got.Issuer != want.Issuer || got.JWKSURL != want.JWKSURL || got.Audience != want.Audience ||
		got.TenantClaim != want.TenantClaim || strings.Join(got.AllowedTenants, ",") != "acme,globex" || got.MaxLifetimeSeconds != 300 ||
		strings.Join(got.AllowedOrigins, ",") != "https://portal.example.com,http://localhost:3000" {
		t.Errorf("TrustedIssuer = %+v, want %+v", got, want)
	}
}

// TestValidateTrustedIssuer covers the boot checks. A half-configured issuer
// fails boot with every missing key named at once, so first-time setup is one
// edit, not a chain of restarts.
func TestValidateTrustedIssuer(t *testing.T) {
	full := TrustedIssuerSection{
		Name: "portal", Issuer: "https://portal.example.com", JWKSURL: "https://portal.example.com/jwks",
		Audience: "leoflow-engine", TenantClaim: "tenant_id", AllowedTenants: []string{"*"},
		AllowedOrigins: []string{"https://portal.example.com"},
	}
	cases := []struct {
		name    string
		mutate  func(*TrustedIssuerSection)
		wantErr []string
	}{
		{"unset", func(s *TrustedIssuerSection) { *s = TrustedIssuerSection{TenantClaim: "tenant_id"} }, nil},
		{"complete", func(*TrustedIssuerSection) {}, nil},
		{"loopback http jwks", func(s *TrustedIssuerSection) { s.JWKSURL = "http://localhost:9000/jwks" }, nil},
		{"only an issuer", func(s *TrustedIssuerSection) {
			*s = TrustedIssuerSection{Issuer: "https://portal.example.com", TenantClaim: "tenant_id"}
		}, []string{"auth.trusted_issuer.name", "auth.trusted_issuer.jwks_url", "auth.trusted_issuer.audience", "auth.trusted_issuer.allowed_tenants", "auth.trusted_issuer.allowed_origins"}},
		{"no origins", func(s *TrustedIssuerSection) { s.AllowedOrigins = nil }, []string{"auth.trusted_issuer.allowed_origins"}},
		{"origin with a path", func(s *TrustedIssuerSection) { s.AllowedOrigins = []string{"https://portal.example.com/engine"} }, []string{"auth.trusted_issuer.allowed_origins"}},
		{"wildcard origin", func(s *TrustedIssuerSection) { s.AllowedOrigins = []string{"*"} }, []string{"auth.trusted_issuer.allowed_origins"}},
		{"bad name", func(s *TrustedIssuerSection) { s.Name = "Portal One" }, []string{"auth.trusted_issuer.name"}},
		{"plain http jwks", func(s *TrustedIssuerSection) { s.JWKSURL = "http://portal.example.com/jwks" }, []string{"auth.trusted_issuer.jwks_url"}},
		{"lifetime too long", func(s *TrustedIssuerSection) { s.MaxLifetimeSeconds = 7200 }, []string{"auth.trusted_issuer.max_lifetime_seconds"}},
		{"lifetime above the cap", func(s *TrustedIssuerSection) { s.MaxLifetimeSeconds = 601 }, []string{"auth.trusted_issuer.max_lifetime_seconds"}},
		{"lifetime at the cap", func(s *TrustedIssuerSection) { s.MaxLifetimeSeconds = 600 }, nil},
		{"empty tenant claim", func(s *TrustedIssuerSection) { s.TenantClaim = "" }, []string{"auth.trusted_issuer.tenant_claim"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Auth.JWT.Secret = "set"
			c.Auth.TrustedIssuer = full
			tc.mutate(&c.Auth.TrustedIssuer)
			err := c.Validate()
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error naming %v", tc.wantErr)
			}
			for _, key := range tc.wantErr {
				if !strings.Contains(err.Error(), key) {
					t.Errorf("Validate() = %v, want it to name %q", err, key)
				}
			}
		})
	}
}

// TestLoadServerReadsServiceTokenFromEnv locks the #1283 key: off by default,
// bound from the environment like the JWT secret.
func TestLoadServerReadsServiceTokenFromEnv(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil || c.Auth.ServiceToken != "" {
		t.Fatalf("default service token = %q (%v), want empty", c.Auth.ServiceToken, err)
	}
	t.Setenv("LEOFLOW_AUTH_SERVICE_TOKEN", "a-service-token-of-at-least-32-chars")
	c, err = LoadServer("", nil)
	if err != nil || c.Auth.ServiceToken != "a-service-token-of-at-least-32-chars" {
		t.Errorf("service token = %q (%v), want the value from the environment", c.Auth.ServiceToken, err)
	}
}

// TestValidateServiceToken refuses a short service token: it is a bearer
// credential that can create tenants and users.
func TestValidateServiceToken(t *testing.T) {
	cases := map[string]struct {
		token   string
		wantErr bool
	}{
		"unset":  {"", false},
		"long":   {strings.Repeat("x", 32), false},
		"short":  {"too-short", true},
		"spaces": {strings.Repeat(" ", 40), true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Auth.JWT.Secret = "set"
			c.Auth.ServiceToken = tc.token
			err := c.Validate()
			if (err != nil) != tc.wantErr || (err != nil && !strings.Contains(err.Error(), "auth.service_token")) {
				t.Errorf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
