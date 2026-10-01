//go:build integration

package storage_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// TestCreateAndFindOIDCUserIntegration exercises the OIDC provisioning and
// resolution queries end to end: a JIT-created OIDC-only user (no password) is
// linked by (provider, subject), granted the requested role, and resolved back
// by that immutable pair with its roles and permissions loaded and active=true.
func TestCreateAndFindOIDCUserIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("oidc_%d@example.com", time.Now().UnixNano())
	subject := fmt.Sprintf("sub-%d", time.Now().UnixNano())

	created, err := repo.CreateOIDCUser(ctx, "default", email, "azure", subject, []string{"viewer"})
	if err != nil {
		t.Fatalf("CreateOIDCUser: %v", err)
	}
	if created.ID == "" || created.Email != email || len(created.Roles) != 1 || created.Roles[0] != "viewer" {
		t.Fatalf("unexpected created oidc user: %+v", created)
	}

	got, active, err := repo.FindUserByOIDCSubject(ctx, "azure", subject)
	if err != nil {
		t.Fatalf("FindUserByOIDCSubject: %v", err)
	}
	if !active {
		t.Error("resolved oidc user is not active")
	}
	if got.ID != created.ID || got.Email != email || got.TenantID != "default" {
		t.Errorf("resolved user = %+v, want id=%s email=%s tenant=default", got, created.ID, email)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "viewer" {
		t.Errorf("roles = %v, want [viewer]", got.Roles)
	}
	// viewer carries the read:dag permission (PR-A ladder), proving perms load.
	if !got.HasPermission("read", "dag") {
		t.Errorf("resolved viewer lacks read:dag; perms = %v", got.Permissions)
	}
}

// TestFindOIDCUserNotFoundIntegration proves an unknown (provider, subject)
// pair yields auth.ErrUserNotFound — the signal the login flow uses to consider
// JIT provisioning rather than a hard failure.
func TestFindOIDCUserNotFoundIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	if _, _, err := repo.FindUserByOIDCSubject(ctx, "azure", "nobody-here"); !errors.Is(err, auth.ErrUserNotFound) {
		t.Errorf("err = %v, want auth.ErrUserNotFound", err)
	}
}

// TestCreateOIDCUserUnknownRoleIntegration proves an unknown role (e.g. a
// misconfigured default_role or role_mapping) fails closed as ErrValidation and
// leaves no orphaned account — the pair can be provisioned cleanly afterwards.
func TestCreateOIDCUserUnknownRoleIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("oidcbad_%d@example.com", time.Now().UnixNano())
	subject := fmt.Sprintf("subbad-%d", time.Now().UnixNano())

	if _, err := repo.CreateOIDCUser(ctx, "default", email, "azure", subject, []string{"wizard"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unknown role err = %v, want ErrValidation", err)
	}
	// No account was created, so a subsequent clean provision succeeds.
	if _, err := repo.CreateOIDCUser(ctx, "default", email, "azure", subject, nil); err != nil {
		t.Errorf("create after rejected role: %v", err)
	}
}

// TestCreateOIDCUserDuplicateSubjectIntegration proves a second provision of the
// same (provider, subject) surfaces as domain.ErrConflict (the unique
// constraint), so a concurrent double-login cannot create two identities.
func TestCreateOIDCUserDuplicateSubjectIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	subject := fmt.Sprintf("subdup-%d", time.Now().UnixNano())

	if _, err := repo.CreateOIDCUser(ctx, "default", fmt.Sprintf("a_%d@example.com", time.Now().UnixNano()), "azure", subject, nil); err != nil {
		t.Fatalf("first provision: %v", err)
	}
	if _, err := repo.CreateOIDCUser(ctx, "default", fmt.Sprintf("b_%d@example.com", time.Now().UnixNano()), "azure", subject, nil); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate subject err = %v, want ErrConflict", err)
	}
}

// TestReconcileUserRolesDemotionIntegration proves the IdP-authoritative
// reconcile: an OIDC user first granted [operator] whose IdP groups later map to
// [viewer] ends with EXACTLY [viewer] in the DB, losing operator's extra
// permissions. Because the resolve path (FindUserByOIDCSubject) reads the same
// user_roles rows the per-request authz reload reads, the demotion takes effect
// on the next request.
func TestReconcileUserRolesDemotionIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("oidc_demote_%d@example.com", time.Now().UnixNano())
	subject := fmt.Sprintf("subdemote-%d", time.Now().UnixNano())

	created, err := repo.CreateOIDCUser(ctx, "default", email, "azure", subject, []string{"operator"})
	if err != nil {
		t.Fatalf("CreateOIDCUser: %v", err)
	}

	if err := repo.ReconcileUserRoles(ctx, created.ID, []string{"viewer"}); err != nil {
		t.Fatalf("ReconcileUserRoles([viewer]): %v", err)
	}

	got, active, err := repo.FindUserByOIDCSubject(ctx, "azure", subject)
	if err != nil || !active {
		t.Fatalf("FindUserByOIDCSubject: user=%v active=%v err=%v", got, active, err)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "viewer" {
		t.Errorf("roles after demotion = %v, want [viewer]", got.Roles)
	}
	if !got.HasPermission("read", "dag") {
		t.Error("demoted viewer lost read:dag")
	}
	if got.HasPermission("write", "dag_run") {
		t.Error("demoted user still holds operator's write:dag_run — demotion did not take effect")
	}
}

// TestReconcileUserRolesEmptyIntegration proves reconciling to the empty set
// clears all grants (default-deny), the shape produced when a login maps to no
// role and no default_role is configured.
func TestReconcileUserRolesEmptyIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("oidc_empty_%d@example.com", time.Now().UnixNano())
	subject := fmt.Sprintf("subempty-%d", time.Now().UnixNano())

	created, err := repo.CreateOIDCUser(ctx, "default", email, "azure", subject, []string{"viewer"})
	if err != nil {
		t.Fatalf("CreateOIDCUser: %v", err)
	}
	if err := repo.ReconcileUserRoles(ctx, created.ID, nil); err != nil {
		t.Fatalf("ReconcileUserRoles(nil): %v", err)
	}
	got, _, err := repo.FindUserByOIDCSubject(ctx, "azure", subject)
	if err != nil {
		t.Fatalf("FindUserByOIDCSubject: %v", err)
	}
	if len(got.Roles) != 0 {
		t.Errorf("roles after empty reconcile = %v, want none", got.Roles)
	}
}

// TestReconcileUserRolesIdempotentIntegration proves re-login with the same
// groups yields the same rows: reconciling to [viewer] twice leaves exactly one
// grant (no duplicate rows, no error).
func TestReconcileUserRolesIdempotentIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("oidc_idem_%d@example.com", time.Now().UnixNano())
	subject := fmt.Sprintf("subidem-%d", time.Now().UnixNano())

	created, err := repo.CreateOIDCUser(ctx, "default", email, "azure", subject, nil)
	if err != nil {
		t.Fatalf("CreateOIDCUser: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := repo.ReconcileUserRoles(ctx, created.ID, []string{"viewer"}); err != nil {
			t.Fatalf("ReconcileUserRoles pass %d: %v", i, err)
		}
	}
	got, _, err := repo.FindUserByOIDCSubject(ctx, "azure", subject)
	if err != nil {
		t.Fatalf("FindUserByOIDCSubject: %v", err)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "viewer" {
		t.Errorf("roles after idempotent reconcile = %v, want [viewer]", got.Roles)
	}
}

// TestReconcileUserRolesUnknownRoleFailsClosedIntegration proves an unknown role
// name fails closed as ErrValidation and leaves the prior grants intact — the
// login path turns this into a rejected, audited login rather than silently
// wiping the user's roles.
func TestReconcileUserRolesUnknownRoleFailsClosedIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("oidc_unknown_%d@example.com", time.Now().UnixNano())
	subject := fmt.Sprintf("subunknown-%d", time.Now().UnixNano())

	created, err := repo.CreateOIDCUser(ctx, "default", email, "azure", subject, []string{"operator"})
	if err != nil {
		t.Fatalf("CreateOIDCUser: %v", err)
	}
	if err := repo.ReconcileUserRoles(ctx, created.ID, []string{"wizard"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("ReconcileUserRoles([wizard]) = %v, want ErrValidation", err)
	}
	// The failed reconcile must not have wiped the existing grant.
	got, _, err := repo.FindUserByOIDCSubject(ctx, "azure", subject)
	if err != nil {
		t.Fatalf("FindUserByOIDCSubject: %v", err)
	}
	if len(got.Roles) != 1 || got.Roles[0] != "operator" {
		t.Errorf("roles after failed reconcile = %v, want unchanged [operator]", got.Roles)
	}
}

// TestRoleExistsIntegration proves the role existence check the OIDC login path
// uses to fail closed on a misconfigured default_role: a seeded role is found, a
// nonexistent one is not, and neither is an error.
func TestRoleExistsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	if ok, err := repo.RoleExists(ctx, "default", "viewer"); err != nil || !ok {
		t.Errorf("RoleExists(viewer) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := repo.RoleExists(ctx, "default", "wizard"); err != nil || ok {
		t.Errorf("RoleExists(wizard) = %v, %v; want false, nil", ok, err)
	}
}

// TestTenantExistsIntegration proves the tenant existence check the boot-time
// OIDC name warning uses, against the real schema: the tenant the first
// migration creates is found, a name nothing creates is not, and neither is an
// error. The second half is the whole point of the warning: an absent tenant has
// to be distinguishable from a lookup that could not run, or a degraded database
// and a typo produce the same boot log.
//
// That "default" is the ONLY tenant a fresh schema has is pinned separately, in
// the migrations package, where the claim actually lives.
func TestTenantExistsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	if ok, err := repo.TenantExists(ctx, "default"); err != nil || !ok {
		t.Errorf("TenantExists(default) = %v, %v; want true, nil", ok, err)
	}
	if ok, err := repo.TenantExists(ctx, "acme"); err != nil || ok {
		t.Errorf("TenantExists(acme) = %v, %v; want false, nil (absent, not a failure)", ok, err)
	}
}

// TestRoleExistsOnAMissingTenantIsAnError pins the branch that decides which
// reason a denied login is audited under, and therefore what the boot warnings
// are allowed to say.
//
// RoleExists resolves the tenant before the role, so a tenant that does not
// exist comes back as an ERROR, not (false, nil). resolveUser turns that into
// role_check_failed, and a role that is merely missing from a tenant that does
// exist into unknown_role:<role>. Folding the missing tenant into (false, nil)
// would silently swap the two, and every operator sent to grep the audit log by
// a boot warning would find nothing.
func TestRoleExistsOnAMissingTenantIsAnError(t *testing.T) {
	repo, _, ctx := openRepo(t)
	ok, err := repo.RoleExists(ctx, "acme", "viewer")
	if ok {
		t.Errorf("RoleExists on a tenant that does not exist = true")
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("RoleExists on a tenant that does not exist returned err = %v; want domain.ErrNotFound, which is what makes the denial audit as role_check_failed rather than unknown_role", err)
	}
}

// TestLocalPasswordUserExistsIntegration pins the query the break-glass boot
// check asks, against a real database, because its whole value is the
// distinction SQL makes and a fake cannot: a row that exists but has no password.
//
// A user created through OIDC provisioning has a NULL password_hash (the
// users_has_auth constraint permits it because the OIDC subject is the other
// half), so an address listed in break_glass_emails can have a row and still be
// an escape hatch that does not open.
func TestLocalPasswordUserExistsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	stamp := time.Now().UnixNano()
	local := fmt.Sprintf("bg-local-%d@example.com", stamp)
	ssoOnly := fmt.Sprintf("bg-sso-%d@example.com", stamp)

	if _, err := repo.CreateUser(ctx, "default", local, "s3cret-password", []string{"viewer"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := repo.CreateOIDCUser(ctx, "default", ssoOnly, "azure", fmt.Sprintf("sub-%d", stamp), []string{"viewer"}); err != nil {
		t.Fatalf("CreateOIDCUser: %v", err)
	}

	for _, tc := range []struct {
		name, tenant, email string
		want                bool
	}{
		{"a local password user is a way in", "default", local, true},
		{"an OIDC-provisioned row is not: it has no password to verify", "default", ssoOnly, false},
		{"an address with no row at all", "default", fmt.Sprintf("bg-absent-%d@example.com", stamp), false},
		{"case is not significant, as it is not on the login path", "default", strings.ToUpper(local), true},
		{"a tenant that does not exist is absent, not an error", "acme", local, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.LocalPasswordUserExists(ctx, tc.tenant, tc.email)
			if err != nil {
				t.Fatalf("LocalPasswordUserExists: %v", err)
			}
			if got != tc.want {
				t.Errorf("= %v, want %v", got, tc.want)
			}
		})
	}
}
