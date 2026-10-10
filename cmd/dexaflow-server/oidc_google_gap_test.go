package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

func oidcAuth(mut func(*config.AuthSection)) config.AuthSection {
	c := config.AuthSection{Provider: config.AuthProviderOIDC}
	mut(&c)
	return c
}

// TestRoleWarningCoversTheGoogleShape widens the guard to the configuration the
// SSO audit found unprotected.
//
// The warning fired only when role_mappings AND default_role were both empty. But
// Google emits no groups claim without Directory API setup, so an operator who
// maps IdP groups and leaves default_role empty has a non-empty role_mappings
// that can never match: every login resolves to zero roles, and roles are
// reconciled to EXACTLY that set, so a returning user's grants are cleared. The
// warning was silent for precisely that shape, which is the one a Google
// deployment lands in by following the obvious instinct.
func TestRoleWarningCoversTheGoogleShape(t *testing.T) {
	t.Run("mappings set, no default: the Google trap", func(t *testing.T) {
		w := oidcRoleSourceWarnings(oidcAuth(func(a *config.AuthSection) {
			a.OIDC.RoleMappings = map[string]string{"data-eng": "editor"}
		}))
		if len(w) == 0 {
			t.Fatal("no warning: an IdP that emits no groups resolves every login to zero roles and clears existing grants, silently")
		}
		if !strings.Contains(w[0].Msg, "default_role") {
			t.Errorf("the warning does not name the setting that fixes it: %s", w[0].Msg)
		}
		// role_mappings with no default_role is also the correct strict posture on
		// an IdP that does emit the claim: a login outside every mapped group is
		// meant to hold no roles. The warning cannot tell the two apart from
		// config alone, so it has to say which one it is guessing at. Without
		// that, the only way to silence it is to set default_role, which grants a
		// role to every login the tenant pin admits: a warning that instructs a
		// correctly configured operator to widen access is one they learn to
		// ignore, and then it is worse than none. The client-secret warning in
		// this same set already ends with its own "or ignore this if" clause.
		if !strings.Contains(w[0].Msg, "groups_claim") {
			t.Errorf("the warning reads as an instruction to set default_role without naming the deployment where the "+
				"current configuration is correct, so it cannot be dismissed on purpose: %s", w[0].Msg)
		}
	})

	t.Run("neither set: still warns", func(t *testing.T) {
		if len(oidcRoleSourceWarnings(oidcAuth(func(*config.AuthSection) {}))) == 0 {
			t.Error("the original case regressed")
		}
	})

	t.Run("default_role set: silent, because a login always resolves to something", func(t *testing.T) {
		w := oidcRoleSourceWarnings(oidcAuth(func(a *config.AuthSection) {
			a.OIDC.RoleMappings = map[string]string{"data-eng": "editor"}
			a.OIDC.DefaultRole = "viewer"
		}))
		if len(w) != 0 {
			t.Errorf("warned on a configuration that cannot resolve to zero roles: %s", w[0].Msg)
		}
	})

	t.Run("not oidc: silent", func(t *testing.T) {
		c := config.AuthSection{Provider: config.AuthProviderJWT}
		if len(oidcRoleSourceWarnings(c)) != 0 {
			t.Error("warned on a jwt deployment")
		}
	})
}

// TestClientSecretWarningNamesTheExchange covers the second silent failure the SSO
// audit found: auth.oidc.client_secret is optional at boot, and every confidential
// client (Google, Entra, Okta web, a Keycloak client with client authentication
// on) rejects the code exchange without it. The rejection lands on the callback,
// which answers a generic 403, so an operator who forgot the secret sees a login
// that fails with nothing naming the secret.
//
// It stays a WARN rather than a boot failure because a public client (PKCE only,
// no secret) is a legitimate registration, and failing boot would break a
// deployment that works today.
func TestClientSecretWarningNamesTheExchange(t *testing.T) {
	t.Run("empty secret warns and names the key", func(t *testing.T) {
		w := oidcClientSecretWarnings(oidcAuth(func(a *config.AuthSection) {
			a.OIDC.Issuer = "https://accounts.google.com"
		}))
		if len(w) == 0 {
			t.Fatal("no warning: the code exchange fails with invalid_client and the operator sees only a 403")
		}
		if !strings.Contains(w[0].Msg, "client_secret") {
			t.Errorf("the warning does not name the key to set: %s", w[0].Msg)
		}
	})

	t.Run("secret set: silent", func(t *testing.T) {
		w := oidcClientSecretWarnings(oidcAuth(func(a *config.AuthSection) {
			a.OIDC.ClientSecret = "s3cret"
		}))
		if len(w) != 0 {
			t.Errorf("warned about a configured secret: %s", w[0].Msg)
		}
	})

	t.Run("not oidc: silent", func(t *testing.T) {
		if len(oidcClientSecretWarnings(config.AuthSection{Provider: config.AuthProviderJWT})) != 0 {
			t.Error("warned on a jwt deployment")
		}
	})
}

// TestWarnStartupEmitsEveryOIDCWarningOnAnAPIReplica pins the wiring: a warning
// that is computed and never logged is not a warning. The exchange runs API-side,
// so an api-only replica is exactly the process that must print it.
func TestWarnStartupEmitsEveryOIDCWarningOnAnAPIReplica(t *testing.T) {
	cfg := oidcWarnConfig()
	cfg.Server.Role = config.RoleAPI
	cfg.Auth.OIDC.RoleMappings = map[string]string{"platform-admins": "admin"}
	var buf bytes.Buffer
	warnStartup(cfg, slog.New(slog.NewTextHandler(&buf, nil)))
	// Keyed on the structured config_key, not on the prose: each of these
	// warnings names another one's key in its remedy sentence, so a substring
	// match over the buffer would report a warning that was never wired in.
	for _, want := range []string{"auth.oidc.default_role", "auth.oidc.client_secret", "auth.oidc.jit_provisioning"} {
		if warnLineFor(buf.String(), want) == "" {
			t.Errorf("warnStartup never logged a warning keyed %s:\n%s", want, buf.String())
		}
	}
}

// TestJITWarningNamesTheOnlyProvisioningPath covers the loose end the field
// report landed on: auth.oidc.jit_provisioning defaults to false, and the docs
// describe that as "a pre-provisioned user is required".
//
// No pre-provisioning path exists. A login is resolved ONLY by
// FindUserByOIDCSubject (internal/api/oidc_handler.go), so a row matches only if
// it carries oidc_provider and oidc_subject, and the sole statement that writes
// those two columns is CreateOIDCUser (internal/storage/queries/users.sql),
// reached only from jitProvision, which runs only when the flag is on. No API,
// no CLI and no migration writes them. With the flag off, every first login is
// denied as no_user_jit_off, and there is nothing an operator can do to create a
// user that would match.
//
// It is a WARN and not a boot failure because an operator who wrote the two
// columns with SQL by hand has a deployment that works, and boot must not break
// it.
func TestJITWarningNamesTheOnlyProvisioningPath(t *testing.T) {
	t.Run("jit off warns and names the key", func(t *testing.T) {
		w := oidcJITWarnings(oidcAuth(func(a *config.AuthSection) { a.OIDC.JITProvisioning = false }))
		if len(w) == 0 {
			t.Fatal("no warning: with jit off every first login is denied and no supported path creates a matching user")
		}
		if !strings.Contains(w[0].Msg, "jit_provisioning") {
			t.Errorf("the warning does not name the key: %s", w[0].Msg)
		}
	})

	t.Run("jit on: silent", func(t *testing.T) {
		if w := oidcJITWarnings(oidcAuth(func(a *config.AuthSection) { a.OIDC.JITProvisioning = true })); len(w) != 0 {
			t.Errorf("warned with provisioning enabled: %s", w[0].Msg)
		}
	})

	t.Run("not oidc: silent", func(t *testing.T) {
		if len(oidcJITWarnings(config.AuthSection{Provider: config.AuthProviderJWT})) != 0 {
			t.Error("warned on a jwt deployment")
		}
	})
}
