package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
)

// oidcWarnConfig is a Pro OIDC deployment that passes validateStartup: every key
// the boot gate requires is set, including the tenant pin. Each case below
// changes only the role configuration.
func oidcWarnConfig() *config.ServerConfig {
	cfg := &config.ServerConfig{}
	cfg.Auth.MaxAttemptCredentialLifetime = 24 * time.Hour // silence the ceiling WARN
	cfg.UI.Edition = "pro"
	cfg.Auth.Provider = "oidc"
	cfg.Auth.OIDC.Issuer = "https://idp.example.com"
	cfg.Auth.OIDC.ClientID = "leoflow"
	cfg.Auth.OIDC.RedirectURL = "https://leoflow.example.com/api/v2/auth/oidc/callback"
	cfg.Auth.OIDC.TenantClaim = "tid"
	cfg.Auth.OIDC.TenantClaims = map[string]string{"t-123": "default"}
	return cfg
}

// warnLineFor returns the log line whose structured config_key attribute is key,
// or "" when no warning named that key.
//
// Matching the attribute rather than the prose is what keeps these tests
// distinguishing anything: the boot warnings name each other's keys inside their
// remedy sentences (the jit_provisioning warning tells the operator to set
// default_role or role_mappings alongside it), so a substring match over the
// whole buffer reports a warning that never fired. The leading space is what
// separates config_key= from missing_config_key=.
func warnLineFor(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, " config_key="+key) {
			return line
		}
	}
	return ""
}

// TestWarnStartupFlagsOIDCWithNoRoleSource covers the failure the tenant-pin boot
// gate makes reachable.
//
// Role resolution is IdP-authoritative: resolveUser computes the login's roles
// from role_mappings (plus default_role) and hands the result to
// ReconcileUserRoles, which sets the DB rows to EXACTLY that set, so an empty set
// is a full clear (internal/storage/repository.go, locked by
// TestReconcileUserRolesEmptyIntegration). With neither role_mappings nor
// default_role configured, every OIDC login resolves to zero roles, and a
// pre-provisioned admin's first SSO login strips the grants they logged in with.
//
// Until the tenant pin was required at boot, that could not happen: the pin
// rejected the login before reconciliation ran. Requiring the pin removes that
// accidental shield, so the deployment shape it exposes needs a signal of its own.
// The login itself succeeds, so there is nothing to fail closed on; the WARN at
// boot is the only place an operator sees it coming.
func TestWarnStartupFlagsOIDCWithNoRoleSource(t *testing.T) {
	var buf bytes.Buffer
	warnStartup(oidcWarnConfig(), slog.New(slog.NewTextHandler(&buf, nil)))

	out := buf.String()
	line := warnLineFor(out, "auth.oidc.default_role")
	if line == "" {
		t.Fatalf("no role WARN logged for an OIDC deployment with no role source:\n%s", out)
	}
	for _, want := range []string{"auth.oidc.role_mappings", "auth.oidc.default_role"} {
		if !strings.Contains(line, want) {
			t.Errorf("the WARN does not name %q, so it cannot point at the remedy:\n%s", want, line)
		}
	}
	if !strings.Contains(line, "level=WARN") {
		t.Errorf("the role signal is not logged at WARN:\n%s", line)
	}
}

// TestWarnStartupSilentWhenARoleSourceExists keeps the WARN meaningful: a
// deployment that configured either source is correctly set up, and a warning an
// operator learns to ignore is worse than none.
func TestWarnStartupSilentWhenARoleSourceExists(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*config.ServerConfig)
	}{
		// role_mappings alone is deliberately NOT here: it still warns, because a
		// group claim that matches nothing resolves to zero roles and clears the
		// user. TestRoleWarningCoversTheGoogleShape owns that case.
		{"default_role set", func(c *config.ServerConfig) { c.Auth.OIDC.DefaultRole = "viewer" }},
		{"both set", func(c *config.ServerConfig) {
			c.Auth.OIDC.RoleMappings = map[string]string{"platform-admins": "admin"}
			c.Auth.OIDC.DefaultRole = "viewer"
		}},
		{"provider is jwt", func(c *config.ServerConfig) { c.Auth.Provider = "jwt" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := oidcWarnConfig()
			tc.apply(cfg)
			var buf bytes.Buffer
			warnStartup(cfg, slog.New(slog.NewTextHandler(&buf, nil)))
			// Match the structured key rather than the prose: the other boot
			// warnings name default_role in their remedy sentence, so a substring
			// match on the message would report a role warning that never fired.
			if strings.Contains(buf.String(), "config_key=auth.oidc.default_role") {
				t.Errorf("warned about role resolution for a configured deployment:\n%s", buf.String())
			}
		})
	}
}

// TestWarnStartupOIDCRoleWarningReachesAnAPIOnlyReplica pins the role gate. The
// login path is API-side (ADR 0049 splits the roles), so gating this WARN on the
// scheduler, as the resilience-ladder warnings are, would hide it from exactly
// the process that performs the reconciliation.
func TestWarnStartupOIDCRoleWarningReachesAnAPIOnlyReplica(t *testing.T) {
	cfg := oidcWarnConfig()
	cfg.Server.Role = config.RoleAPI
	var buf bytes.Buffer
	warnStartup(cfg, slog.New(slog.NewTextHandler(&buf, nil)))
	if warnLineFor(buf.String(), "auth.oidc.default_role") == "" {
		t.Errorf("api-only replica logged no role WARN, yet it is the process that reconciles roles:\n%s", buf.String())
	}
}
