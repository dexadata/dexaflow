package api

import (
	"errors"
	"github.com/dexadata/dexaflow/internal/domain"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dexadata/dexaflow/internal/auth"
)

// TestOIDCReturningUserReconcilesToMappedRoles proves the IdP is authoritative
// for a returning user: a user previously granted [operator] whose IdP groups now
// map to [viewer] has the DB roles reconciled to exactly [viewer] on login, and
// the minted token carries the same set. The demotion therefore takes effect for
// the per-request authz reload, not just the token.
func TestOIDCReturningUserReconcilesToMappedRoles(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.RoleMappings = map[string]string{"data-eng": "viewer"} // group now maps to viewer
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Email: "alice@corp.example", Roles: []string{"operator"}}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	rec := driveCallback(t, srv, f, cfg, nil) // groups: [data-eng]

	assertLoginSucceeded(t, rec, "callback")
	roles, ok := store.lastReconcile("user-1")
	if !ok {
		t.Fatal("returning user was not reconciled")
	}
	if len(roles) != 1 || roles[0] != "viewer" {
		t.Errorf("reconciled roles = %v, want [viewer] (IdP-authoritative demotion)", roles)
	}
	if ck := sessionCookie(rec); ck == nil {
		t.Fatal("no session cookie")
	} else if tr := tokenRoles(t, ck.Value); len(tr) != 1 || tr[0] != "viewer" {
		t.Errorf("token roles = %v, want [viewer]", tr)
	}
}

// TestOIDCReturningUserEmptyMappingWithDefaultRole proves a returning user whose
// groups map to no role is reconciled to [default_role] when one is set.
func TestOIDCReturningUserEmptyMappingWithDefaultRole(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.DefaultRole = "viewer"
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Email: "alice@corp.example", Roles: []string{"operator"}}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	rec := driveCallback(t, srv, f, cfg, func(c jwt.MapClaims) { c["groups"] = []string{"unmapped-group"} })

	assertLoginSucceeded(t, rec, "callback")
	roles, ok := store.lastReconcile("user-1")
	if !ok || len(roles) != 1 || roles[0] != "viewer" {
		t.Errorf("reconciled roles = %v (found=%v), want [viewer] via default_role", roles, ok)
	}
}

// TestOIDCReturningUserEmptyMappingNoDefaultRole proves a returning user whose
// groups map to no role and with no default_role is reconciled to the empty set
// (default-deny) — a prior grant is stripped.
func TestOIDCReturningUserEmptyMappingNoDefaultRole(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.DefaultRole = ""
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Email: "alice@corp.example", Roles: []string{"operator"}}, true)
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	rec := driveCallback(t, srv, f, cfg, func(c jwt.MapClaims) { c["groups"] = []string{"unmapped-group"} })

	assertLoginSucceeded(t, rec, "callback")
	roles, ok := store.lastReconcile("user-1")
	if !ok {
		t.Fatal("returning user was not reconciled")
	}
	if len(roles) != 0 {
		t.Errorf("reconciled roles = %v, want [] (default-deny)", roles)
	}
}

// TestOIDCJITUserAlsoReconciles proves reconciliation runs for the JIT path too,
// with the same mapped set the account was created with — so both paths converge
// on identical DB state.
func TestOIDCJITUserAlsoReconciles(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.JITProvisioning = true
	store := newFakeOIDCStore()
	srv := oidcServer(t, f, cfg, store, &fakeAuthAudit{}, nil)

	rec := driveCallback(t, srv, f, cfg, nil) // groups [data-eng] → editor

	assertLoginSucceeded(t, rec, "callback")
	if len(store.created) != 1 {
		t.Fatalf("JIT created %d users, want 1", len(store.created))
	}
	roles, ok := store.lastReconcile("jit-subject-123")
	if !ok {
		t.Fatal("JIT-created user was not reconciled")
	}
	if len(roles) != 1 || roles[0] != "editor" {
		t.Errorf("reconciled roles = %v, want [editor]", roles)
	}
}

// TestOIDCReconcileFailureFailsClosed proves a reconcile error rejects the login:
// no session cookie is minted and the failure is audited, so a role write that
// cannot be applied never yields a token whose roles the DB does not back.
func TestOIDCReconcileFailureFailsClosed(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	store := newFakeOIDCStore()
	store.seed(cfg.Issuer, "subject-123", &auth.User{ID: "user-1", TenantID: "default", Email: "alice@corp.example", Roles: []string{"editor"}}, true)
	store.reconcileErr = errors.New("db unavailable")
	audit := &fakeAuthAudit{}
	srv := oidcServer(t, f, cfg, store, audit, nil)

	rec := driveCallback(t, srv, f, cfg, nil)

	assertLoginDenied(t, rec, "reconcile failure")
	if !audit.has(auditOIDCLoginFailure, "denied") {
		t.Error("the reconcile failure was not audited")
	}
}

// TestUnknownTenantIsAuditedAsItself separates two failures that shared one
// reason.
//
// RoleExists resolves the tenant before the role, so a tenant that does not
// exist comes back as an error, exactly like a database that is down. Both were
// audited role_check_failed and logged "oidc: checking role", so the audit row an
// operator reads could not tell a one-character typo in tenant_claims from an
// outage, and the boot warning that now names the typo precisely had no
// counterpart at login time.
func TestUnknownTenantIsAuditedAsItself(t *testing.T) {
	f := newFakeIDP(t)
	cfg := baseOIDCConfig(f)
	cfg.DefaultRole = "viewer"

	t.Run("a tenant that does not exist", func(t *testing.T) {
		store := newFakeOIDCStore()
		store.roleErr = domain.ErrNotFound
		audit := &fakeAuthAudit{}
		rec := driveCallback(t, oidcServer(t, f, cfg, store, audit, nil), f, cfg, nil)

		assertLoginDenied(t, rec, "a login resolving to a tenant that does not exist")
		if !audit.hasReason(auditOIDCLoginFailure, "unknown_tenant") {
			t.Error("audited as something other than unknown_tenant, so the row cannot be told apart from a database outage")
		}
	})

	t.Run("a database that is down keeps the reason that means that", func(t *testing.T) {
		store := newFakeOIDCStore()
		store.roleErr = errors.New("connection reset by peer")
		audit := &fakeAuthAudit{}
		rec := driveCallback(t, oidcServer(t, f, cfg, store, audit, nil), f, cfg, nil)

		assertLoginDenied(t, rec, "a login during a database outage")
		if !audit.hasReason(auditOIDCLoginFailure, "role_check_failed") {
			t.Error("an outage lost the reason that distinguishes it from a configuration mistake")
		}
	})
}
