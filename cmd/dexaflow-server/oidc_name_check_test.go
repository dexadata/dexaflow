package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeNameChecker struct {
	tenants map[string]bool
	roles   map[string]bool // "tenant/role"
	err     error
	// roleErr is returned by RoleExists alone, which is how the real repository
	// behaves when the tenant row is there but the roles query fails: a boot-time
	// check that reports the tenant half and swallows the role half is the silence
	// this whole check exists to remove.
	roleErr error
	// password maps "tenant/email" (email lowercased) to whether that address has
	// a usable local password login, and passwordErr fails that lookup alone.
	password    map[string]bool
	passwordErr error
	// calls counts every lookup, so a test can assert the query count and not just
	// the warning count. Deduplicating the message while still asking the database
	// once per claim value would satisfy the second and miss the first.
	calls *nameCheckCalls
}

// nameCheckCalls records what was asked of the database, per argument, so a test
// can distinguish "warned once" from "asked once".
type nameCheckCalls struct {
	tenants   []string
	roles     []string // "tenant/role"
	passwords []string // "tenant/email"
}

func (f fakeNameChecker) LocalPasswordUserExists(_ context.Context, tenant, email string) (bool, error) {
	if f.calls != nil {
		f.calls.passwords = append(f.calls.passwords, tenant+"/"+email)
	}
	if f.passwordErr != nil {
		return false, f.passwordErr
	}
	if f.err != nil {
		return false, f.err
	}
	// Lowercase but do NOT trim, mirroring CountLocalPasswordUser, which compares
	// lower(email) = lower($2) and trims nothing. A fake that normalized more than
	// the query does would hide a caller that forgets to trim, and the login path
	// does trim (authTokenHandler), so the two would disagree in production while
	// every test stayed green.
	return f.password[tenant+"/"+strings.ToLower(email)], nil
}

func (f fakeNameChecker) TenantExists(_ context.Context, name string) (bool, error) {
	if f.calls != nil {
		f.calls.tenants = append(f.calls.tenants, name)
	}
	if f.err != nil {
		return false, f.err
	}
	return f.tenants[name], nil
}

func (f fakeNameChecker) RoleExists(_ context.Context, tenant, role string) (bool, error) {
	if f.calls != nil {
		f.calls.roles = append(f.calls.roles, tenant+"/"+role)
	}
	if f.err != nil {
		return false, f.err
	}
	if f.roleErr != nil {
		return false, f.roleErr
	}
	// The real Repository.RoleExists resolves the tenant first and returns
	// domain.ErrNotFound when it is missing, rather than folding that into
	// (false, nil). Mirror it, so a caller that stopped checking the tenant first
	// would see the same thing here as in production.
	if !f.tenants[tenant] {
		return false, domain.ErrNotFound
	}
	return f.roles[tenant+"/"+role], nil
}

func oidcAuthFor(mut func(*config.AuthSection)) config.AuthSection {
	c := config.AuthSection{Provider: config.AuthProviderOIDC}
	mut(&c)
	return c
}

// TestNameWarningsCatchAConfigurationNothingCanSatisfy covers the last shape in
// this family: names that are syntactically fine, pass every config gate, and
// refer to rows that do not exist.
//
// The migrations create only "default": INSERT INTO tenants appears exactly
// once, in migration 001. Any other tenant exists only after an operator
// creates it through the service API (#1283). So a tenant_claims entry mapping
// a claim value to a name nobody created boots green and denies every login
// carrying it, behind the same generic 403 as everything else. The role ladder is seeded for "default"
// alone, so a typo in default_role does the same.
//
// Both are checkable at boot: Postgres is already connected and its schema
// verified before the API starts.
func TestNameWarningsCatchAConfigurationNothingCanSatisfy(t *testing.T) {
	ck := fakeNameChecker{
		tenants: map[string]bool{"default": true},
		roles:   map[string]bool{"default/viewer": true, "default/editor": true},
	}

	t.Run("a tenant that does not exist", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "acme"}
		}))
		if len(w) == 0 {
			t.Fatal("no warning: every login carrying that claim value is denied until someone creates the tenant")
		}
		if !strings.Contains(w[0].Msg, "acme") || !strings.Contains(w[0].Msg, "corp.example") {
			t.Errorf("the warning names neither the tenant nor the claim value it is mapped from: %s", w[0].Msg)
		}
	})

	t.Run("several claim values mapped to the same missing tenant warn once, naming all of them", func(t *testing.T) {
		calls := &nameCheckCalls{}
		tck := ck
		tck.calls = calls
		w := oidcNameWarnings(t.Context(), tck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "acme", "eu.corp.example": "acme"}
		}))
		if len(w) != 1 {
			t.Fatalf("got %d warnings for one missing tenant named by two claim values; the same missing row repeated is a warning operators stop reading: %v", len(w), w)
		}
		// Naming only the first claim value costs a second boot to discover the
		// second one, on a deployment where every affected login is already denied.
		for _, claim := range []string{"corp.example", "eu.corp.example"} {
			if !strings.Contains(w[0].Msg, claim) {
				t.Errorf("the warning does not name the claim value %q that maps to the missing tenant: %s", claim, w[0].Msg)
			}
		}
		if len(calls.tenants) != 1 {
			t.Errorf("asked the database %d times for the same tenant name (%v)", len(calls.tenants), calls.tenants)
		}
	})

	t.Run("a default_role that does not exist", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
			a.OIDC.DefaultRole = "viewr"
		}))
		if len(w) == 0 {
			t.Fatal("no warning: a typo in the role this project's own remedy tells operators to set denies every login")
		}
		if !strings.Contains(w[0].Msg, "viewr") {
			t.Errorf("the warning does not quote the name that is wrong: %s", w[0].Msg)
		}
	})

	t.Run("a role_mappings value that does not exist", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
			a.OIDC.RoleMappings = map[string]string{"data-eng": "edtior"}
		}))
		if len(w) == 0 {
			t.Fatal("no warning: a user in that group is denied, and only that group")
		}
		if !strings.Contains(w[0].Msg, "edtior") {
			t.Errorf("the warning does not quote the name that is wrong: %s", w[0].Msg)
		}
	})

	t.Run("an empty value is a name that does not exist, not an absent setting", func(t *testing.T) {
		// `corp.example:` with nothing after it is valid YAML and binds to "".
		// Nothing rejects it: validateOIDC checks only that tenant_claims is
		// non-empty as a map, and MapRoles copies an empty mapped value straight
		// into the resolved role set. Both deny every affected login, so both are
		// exactly what this check exists to name. Only default_role treats empty as
		// "not configured", and oidcRoleSourceWarnings already covers that.
		t.Run("tenant_claims", func(t *testing.T) {
			w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
				a.OIDC.TenantClaims = map[string]string{"corp.example": ""}
			}))
			if len(w) == 0 {
				t.Fatal("a claim value mapped to no tenant at all was passed over: every login carrying it is denied")
			}
		})
		t.Run("role_mappings", func(t *testing.T) {
			w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
				a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
				a.OIDC.RoleMappings = map[string]string{"data-eng": ""}
			}))
			if len(w) == 0 {
				t.Fatal("a group mapped to no role at all was passed over: MapRoles puts the empty name in the resolved set and the login is denied")
			}
		})
		t.Run("an unset default_role is not a missing name", func(t *testing.T) {
			w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
				a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
				a.OIDC.DefaultRole = ""
			}))
			if len(w) != 0 {
				t.Errorf("warned about default_role being unset, which is a supported strict default-deny: %s", w[0].Msg)
			}
		})
	})

	t.Run("everything exists: silent", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default", "b.example": "default"}
			a.OIDC.DefaultRole = "viewer"
			a.OIDC.RoleMappings = map[string]string{"data-eng": "editor"}
		}))
		if len(w) != 0 {
			t.Errorf("warned about a configuration whose every name resolves: %s", w[0].Msg)
		}
	})

	t.Run("not oidc: silent, and no query is made", func(t *testing.T) {
		exploding := fakeNameChecker{err: errors.New("must not be called")}
		if len(oidcNameWarnings(t.Context(), exploding, config.AuthSection{Provider: config.AuthProviderJWT})) != 0 {
			t.Error("warned on a jwt deployment")
		}
	})

	t.Run("a failed query says so instead of passing quietly", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), fakeNameChecker{err: errors.New("connection reset")}, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
		}))
		if len(w) == 0 {
			t.Fatal("a check that could not run reported nothing, which is indistinguishable from a check that found nothing")
		}
		if !strings.Contains(w[0].Msg, "could not") {
			t.Errorf("the warning does not say the check failed to run: %s", w[0].Msg)
		}
	})

	t.Run("a role lookup that fails says so too, not only a tenant one", func(t *testing.T) {
		rck := ck
		rck.roleErr = errors.New("connection reset")
		w := oidcNameWarnings(t.Context(), rck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
			a.OIDC.DefaultRole = "viewer"
		}))
		if len(w) == 0 {
			t.Fatal("the tenant resolved and the role lookup failed, and the check said nothing: half the lookups report a failure to run and half do not")
		}
		if !strings.Contains(w[0].Msg, "could not") || !strings.Contains(w[0].Msg, "viewer") {
			t.Errorf("the warning does not say which role lookup failed to run: %s", w[0].Msg)
		}
	})

	t.Run("the warning names the reason the denied login is audited under", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), ck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
			a.OIDC.DefaultRole = "viewr"
		}))
		if len(w) != 1 {
			t.Fatalf("want one warning, got %d", len(w))
		}
		// resolveUser checks the role before anything else, and an existing tenant
		// whose role is missing takes the !exists branch, which denies with
		// "unknown_role:"+role. role_check_failed is the OTHER branch: the lookup
		// itself erroring, which is what a missing TENANT produces. An operator who
		// greps the audit log for the reason the warning names must find the rows.
		if !strings.Contains(w[0].Msg, "unknown_role:viewr") {
			t.Errorf("the warning does not name the audit reason a login actually gets, so grepping the audit log for it finds nothing: %s", w[0].Msg)
		}
		if strings.Contains(w[0].Msg, "role_check_failed") {
			t.Errorf("the warning names role_check_failed, which is the reason for a lookup ERROR (a missing tenant), not for a missing role in a tenant that exists: %s", w[0].Msg)
		}
	})

	t.Run("several groups mapped to the same missing role warn once", func(t *testing.T) {
		calls := &nameCheckCalls{}
		gck := ck
		gck.calls = calls
		w := oidcNameWarnings(t.Context(), gck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
			a.OIDC.RoleMappings = map[string]string{"data-eng": "edtior", "ml": "edtior", "platform": "edtior"}
		}))
		if len(w) != 1 {
			t.Fatalf("got %d warnings for one missing role named by three groups; mapping several IdP groups to one Leoflow role is the normal shape, and the same warning three times is the noise this check claims to avoid: %v", len(w), w)
		}
		// The role name alone does not tell an operator which of thirty mappings to
		// edit. The groups that named it do.
		for _, group := range []string{"data-eng", "ml", "platform"} {
			if !strings.Contains(w[0].Msg, group) {
				t.Errorf("the warning does not name the group %q that maps to the missing role, so the operator cannot find the line to fix: %s", group, w[0].Msg)
			}
		}
		if len(calls.roles) != 1 {
			t.Errorf("asked the database %d times for the same role name (%v); on the boot path, ahead of the listener, each one is a round trip for an answer already held", len(calls.roles), calls.roles)
		}
	})

	t.Run("default_role and a mapping naming the same missing role are one lookup, two warnings", func(t *testing.T) {
		calls := &nameCheckCalls{}
		dck := ck
		dck.calls = calls
		w := oidcNameWarnings(t.Context(), dck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
			a.OIDC.DefaultRole = "viewr"
			a.OIDC.RoleMappings = map[string]string{"data-eng": "viewr"}
		}))
		// Two settings hold the typo, so both have to be named: an operator who
		// fixes only default_role still denies every login in data-eng.
		if len(w) != 2 {
			t.Fatalf("got %d warnings; both auth.oidc.default_role and auth.oidc.role_mappings name the missing role and both have to be edited: %v", len(w), w)
		}
		if len(calls.roles) != 1 {
			t.Errorf("asked the database %d times whether one role name exists (%v); the answer does not change between the two settings that named it", len(calls.roles), calls.roles)
		}
	})

	t.Run("roles are checked once per tenant, not once per claim value", func(t *testing.T) {
		calls := &nameCheckCalls{}
		cck := ck
		cck.calls = calls
		w := oidcNameWarnings(t.Context(), cck, oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"a.example": "default", "b.example": "default", "c.example": "default"}
			a.OIDC.DefaultRole = "viewr"
		}))
		if len(w) != 1 {
			t.Errorf("got %d warnings for one missing role mapped from three domains; a repeated warning is a warning operators stop reading", len(w))
		}
		// The warning count can be deduplicated after the fact; the query count
		// cannot. Both matter, and only one of them is on the boot path.
		if len(calls.tenants) != 1 || len(calls.roles) != 1 {
			t.Errorf("asked the database %d times for the tenant and %d times for the role behind three claim values mapped to one tenant; want 1 and 1", len(calls.tenants), len(calls.roles))
		}
	})
}

// TestBreakGlassWarningNamesTheLockOut covers the setting that decides whether any
// of the other failures is recoverable.
//
// With provider: oidc and an empty allowlist, newBreakGlass admits nobody: every
// password login is rejected. That is correct and it is also the state in which
// a wrong tenant pin, an IdP outage or any of the other denials leaves NOBODY
// able to reach the control plane, including the operator who has to fix it. The
// chart's own example calls it the lock-out escape hatch and nothing warned.
func TestBreakGlassWarningNamesTheLockOut(t *testing.T) {
	t.Run("empty under oidc: warns", func(t *testing.T) {
		w := oidcBreakGlassWarnings(oidcAuthFor(func(*config.AuthSection) {}))
		if len(w) == 0 {
			t.Fatal("no warning: an IdP problem locks every operator out with no way back in")
		}
		if !strings.Contains(w[0].Msg, "break_glass_emails") {
			t.Errorf("the warning does not name the setting: %s", w[0].Msg)
		}
	})

	t.Run("configured: silent", func(t *testing.T) {
		w := oidcBreakGlassWarnings(oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.BreakGlassEmails = []string{"admin@corp.example"}
		}))
		if len(w) != 0 {
			t.Errorf("warned on a deployment that has a way back in: %s", w[0].Msg)
		}
	})

	t.Run("not oidc: silent", func(t *testing.T) {
		if len(oidcBreakGlassWarnings(config.AuthSection{Provider: config.AuthProviderJWT})) != 0 {
			t.Error("warned on a jwt deployment, where the credential path is the primary login")
		}
	})
}

// TestWarnOIDCNamesReachesTheProcessThatServesLogins pins the wiring. A check
// that runs and logs nowhere is not a check, and the scheduler role never serves
// a login, so gating it there would hide it from the only process the missing
// names break.
func TestWarnOIDCNamesReachesTheProcessThatServesLogins(t *testing.T) {
	ck := fakeNameChecker{tenants: map[string]bool{"default": true}}
	cfg := &config.ServerConfig{}
	cfg.Auth.Provider = config.AuthProviderOIDC
	cfg.Auth.OIDC.TenantClaims = map[string]string{"corp.example": "acme"}

	for _, tc := range []struct {
		name string
		role string
		want bool
	}{
		{"api-only replica logs it", config.RoleAPI, true},
		// The default, and the only mode Lite has: a gate written against RoleAPI
		// alone passes the two cases below and silences every single-process
		// deployment, which is most of them.
		{"the single-process default logs it", config.RoleAll, true},
		{"an unset role logs it", "", true},
		{"scheduler-only replica stays quiet", config.RoleScheduler, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.Server.Role = tc.role
			var buf bytes.Buffer
			warnOIDCNames(t.Context(), ck, cfg, slog.New(slog.NewTextHandler(&buf, nil)))
			if got := strings.Contains(buf.String(), "acme"); got != tc.want {
				t.Errorf("logged=%v, want %v:\n%s", got, tc.want, buf.String())
			}
		})
	}
}

// TestWarnOIDCNamesDoesNotHangBootOnASlowDatabase locks the bound. This runs
// before the HTTP listener binds, so a query that never returns means the probe
// endpoint never comes up and the kubelet restarts the pod on a probe failure
// that names nothing, which is the failure mode the OIDC discovery bound exists
// to prevent one step earlier.
func TestWarnOIDCNamesDoesNotHangBootOnASlowDatabase(t *testing.T) {
	restore := oidcNameCheckTimeout
	oidcNameCheckTimeout = 100 * time.Millisecond
	t.Cleanup(func() { oidcNameCheckTimeout = restore })

	cfg := &config.ServerConfig{}
	cfg.Auth.Provider = config.AuthProviderOIDC
	cfg.Auth.OIDC.TenantClaims = map[string]string{"corp.example": "default"}

	done := make(chan struct{})
	go func() {
		warnOIDCNames(t.Context(), blockingNameChecker{}, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("boot is unbounded against a database that accepts the query and never answers, so the listener never binds")
	}
}

// blockingNameChecker answers only when the context is done, which is what a
// query against a wedged database looks like from here.
type blockingNameChecker struct{}

func (blockingNameChecker) TenantExists(ctx context.Context, _ string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func (blockingNameChecker) LocalPasswordUserExists(ctx context.Context, _, _ string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func (blockingNameChecker) RoleExists(ctx context.Context, _, _ string) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

// TestBreakGlassWarningCatchesAnAddressThatCannotSignIn covers the worse half of
// the lock-out problem.
//
// The empty-allowlist warning says nobody can get in. A non-empty allowlist was
// treated as safe, and it is not: the gate admits the address, and then
// FindUserByLogin finds no row and answers the same "invalid credentials" a
// wrong password gets. The only local password user that ever exists is the
// bootstrap admin, and only when a bootstrap password was set at first install;
// every other local user comes from the admin-authenticated user API, which is
// exactly what cannot be reached during a lock-out.
//
// So an operator who writes their own work address into break_glass_emails gets
// silence from the empty check and a hatch that does not open. That is worse
// than the empty case, because they believe they have one.
//
// A row is not enough either. A user created by OIDC provisioning has a NULL
// password, so the row exists while no password can ever verify against it.
func TestBreakGlassWarningCatchesAnAddressThatCannotSignIn(t *testing.T) {
	ck := fakeNameChecker{
		tenants:  map[string]bool{"default": true},
		roles:    map[string]bool{"default/viewer": true},
		password: map[string]bool{"default/admin@leoflow.local": true},
	}
	withBreakGlass := func(emails ...string) config.AuthSection {
		return oidcAuthFor(func(a *config.AuthSection) {
			a.OIDC.TenantClaims = map[string]string{"corp.example": "default"}
			a.OIDC.BreakGlassEmails = emails
		})
	}
	find := func(w []configWarning, key string) *configWarning {
		for i := range w {
			if w[i].Key == key {
				return &w[i]
			}
		}
		return nil
	}
	const key = "auth.oidc.break_glass_emails"

	t.Run("no listed address has a local password", func(t *testing.T) {
		w := find(oidcNameWarnings(t.Context(), ck, withBreakGlass("ops@corp.example")), key)
		if w == nil {
			t.Fatal("no warning: the allowlist admits an address the credential store cannot authenticate, so the escape hatch does not open")
		}
		if !strings.Contains(w.Msg, "ops@corp.example") {
			t.Errorf("the warning does not quote the address that cannot sign in: %s", w.Msg)
		}
	})

	t.Run("one address works: silent, because one way in is enough", func(t *testing.T) {
		if w := find(oidcNameWarnings(t.Context(), ck, withBreakGlass("ops@corp.example", "admin@leoflow.local")), key); w != nil {
			t.Errorf("warned although one listed address can sign in: %s", w.Msg)
		}
	})

	t.Run("an OIDC-provisioned row is not a password login", func(t *testing.T) {
		oidcOnly := ck
		oidcOnly.password = map[string]bool{} // the row exists, the password does not
		if w := find(oidcNameWarnings(t.Context(), oidcOnly, withBreakGlass("admin@leoflow.local")), key); w == nil {
			t.Fatal("a row with no password counted as a way in; nothing can ever verify against it")
		}
	})

	t.Run("case and spacing match the way the login normalizes them", func(t *testing.T) {
		if w := find(oidcNameWarnings(t.Context(), ck, withBreakGlass("  Admin@Leoflow.Local  ")), key); w != nil {
			t.Errorf("warned about an address the login path would accept: %s", w.Msg)
		}
	})

	t.Run("a failed lookup says so instead of passing quietly", func(t *testing.T) {
		broken := ck
		broken.passwordErr = errors.New("connection reset")
		w := find(oidcNameWarnings(t.Context(), broken, withBreakGlass("ops@corp.example")), key)
		if w == nil || !strings.Contains(w.Msg, "could not") {
			t.Fatalf("a check that could not run reported nothing distinguishable from a check that found nothing: %+v", w)
		}
	})

	t.Run("empty allowlist keeps its own warning and does not gain this one", func(t *testing.T) {
		w := oidcNameWarnings(t.Context(), ck, withBreakGlass())
		if got := find(w, key); got != nil && strings.Contains(got.Msg, "cannot sign in") {
			t.Errorf("an empty list was reported as addresses that cannot sign in: %s", got.Msg)
		}
	})
}

// TestNameCheckRunsAfterTheBootstrapAdminExists pins an ordering that a k3d
// install exposed and every unit test missed.
//
// The break-glass check asks whether a listed address has a local password
// login. bootstrapAdmin creates admin@leoflow.local, and it ran AFTER the check.
// So on a first install with bootstrap.password set and that address in
// break_glass_emails, which is the chart's own documented posture, the very
// first boot warned that the escape hatch does not open, and it did open. The
// warning cleared on the next restart, which is worse than being simply wrong:
// it is wrong on exactly the boot an operator reads, and right afterwards, so it
// teaches them the whole family of warnings is noise.
//
// This test pins the order rather than the message, because the bug was not in
// the message. run() is not callable from a test, so it reads the source: crude,
// and it fails when someone moves either call, which is the only property that
// matters here.
func TestNameCheckRunsAfterTheBootstrapAdminExists(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	body := string(src)
	check := strings.Index(body, "\twarnOIDCNames(ctx, repo, cfg, tel.Logger)")
	bootstrap := strings.Index(body, "\tif err := bootstrapAdmin(ctx, repo, tel.Logger); err != nil {")
	if check < 0 || bootstrap < 0 {
		t.Fatal("one of the two calls was renamed; this test guards the order between them and can no longer see it")
	}
	if check < bootstrap {
		t.Error("the name check runs before bootstrapAdmin, so a first install warns that its own break-glass account cannot sign in, on the one boot an operator reads")
	}
}
