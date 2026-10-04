//go:build integration

package storage_test

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/domain"
)

func uniqueTenant(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// TestEnsureTenantSeedsTheDefaultTenantsRolesAndPool covers #1283: a tenant
// created through the service API starts with the same built-in roles, the
// same permissions on each, and a default pool, as the tenant the migrations
// seed. Copying from "default" keeps them equal as later migrations change the
// ladder.
func TestEnsureTenantSeedsTheDefaultTenantsRolesAndPool(t *testing.T) {
	repo, _, ctx := openRepo(t)
	name := uniqueTenant("acme")

	created, err := repo.EnsureTenant(ctx, name, "Acme Corp", 0)

	if err != nil || !created {
		t.Fatalf("EnsureTenant = %v, %v; want created", created, err)
	}
	for _, role := range []string{"admin", "viewer", "editor", "operator"} {
		ok, rerr := repo.RoleExists(ctx, name, role)
		if rerr != nil || !ok {
			t.Errorf("role %s in %s: exists=%v err=%v", role, name, ok, rerr)
		}
	}
	got, err := repo.TenantRolePermissions(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	want, err := repo.TenantRolePermissions(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("role permissions of %s differ from default:\n got %v\nwant %v", name, got, want)
	}
	if ok, err := repo.TenantHasDefaultPool(ctx, name); err != nil || !ok {
		t.Errorf("default pool in %s: %v, %v", name, ok, err)
	}
}

// TestEnsureTenantIsIdempotent: the second call changes nothing and says so.
func TestEnsureTenantIsIdempotent(t *testing.T) {
	repo, _, ctx := openRepo(t)
	name := uniqueTenant("globex")
	if _, err := repo.EnsureTenant(ctx, name, "Globex", 0); err != nil {
		t.Fatal(err)
	}

	created, err := repo.EnsureTenant(ctx, name, "Globex", 0)

	if err != nil || created {
		t.Errorf("second EnsureTenant = %v, %v; want not created, no error", created, err)
	}
}

// TestEnsureTenantReconcilesADriftedLadder covers #1305: re-running ensure
// brings a tenant's built-in roles back to default's grants, adding the ones
// it misses and removing the ones default no longer has, while a custom role's
// grants are left alone.
func TestEnsureTenantReconcilesADriftedLadder(t *testing.T) {
	repo, _, pg, ctx := openInfra(t)
	name := uniqueTenant("drift")
	if _, err := repo.EnsureTenant(ctx, name, "Drift"); err != nil {
		t.Fatal(err)
	}
	drift := []string{
		// A grant a later migration added to default's viewer, missing here.
		`DELETE FROM role_permissions rp USING roles r, tenants t, permissions p
		 WHERE rp.role_id = r.id AND r.tenant_id = t.id AND t.name = $1 AND r.name = 'viewer'
		   AND rp.permission_id = p.id AND p.action = 'read' AND p.resource = 'dag'`,
		// A grant a later migration revoked from default's viewer, surviving here.
		`INSERT INTO role_permissions (role_id, permission_id)
		 SELECT r.id, p.id FROM roles r JOIN tenants t ON t.id = r.tenant_id
		 JOIN permissions p ON p.action = 'write' AND p.resource = 'dag'
		 WHERE t.name = $1 AND r.name = 'viewer'`,
		// A custom role the tenant made for itself, with a grant no built-in has.
		`INSERT INTO roles (tenant_id, name, description, is_system)
		 SELECT id, 'deployer', 'custom', false FROM tenants WHERE name = $1`,
		`INSERT INTO role_permissions (role_id, permission_id)
		 SELECT r.id, p.id FROM roles r JOIN tenants t ON t.id = r.tenant_id
		 JOIN permissions p ON p.action = 'write' AND p.resource = 'dag'
		 WHERE t.name = $1 AND r.name = 'deployer'`,
	}
	for _, stmt := range drift {
		if _, err := pg.Pool.Exec(ctx, stmt, name); err != nil {
			t.Fatalf("drifting %s: %v", name, err)
		}
	}

	if _, err := repo.EnsureTenant(ctx, name, "Drift"); err != nil {
		t.Fatal(err)
	}

	got, err := repo.TenantRolePermissions(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	want, err := repo.TenantRolePermissions(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("role permissions of %s after re-ensure differ from default:\n got %v\nwant %v", name, got, want)
	}
	var custom bool
	if err := pg.Pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM role_permissions rp JOIN roles r ON r.id = rp.role_id
		JOIN tenants t ON t.id = r.tenant_id JOIN permissions p ON p.id = rp.permission_id
		WHERE t.name = $1 AND r.name = 'deployer' AND p.action = 'write' AND p.resource = 'dag')`,
		name).Scan(&custom); err != nil {
		t.Fatal(err)
	}
	if !custom {
		t.Error("re-ensure removed a custom role's grant; only built-in roles are reconciled")
	}
}

// TestEnsureIssuerUserCreatesThenReconciles covers the user half of #1283: a
// passwordless user linked to the issuer is created with its roles, and a
// second call with other roles sets exactly those roles on the same user.
func TestEnsureIssuerUserCreatesThenReconciles(t *testing.T) {
	repo, _, ctx := openRepo(t)
	tenant := uniqueTenant("initech")
	if _, err := repo.EnsureTenant(ctx, tenant, "Initech", 0); err != nil {
		t.Fatal(err)
	}
	subject := uniqueTenant("sub")

	first, created, err := repo.EnsureIssuerUser(ctx, tenant, "peter@initech.com", "issuer:portal", subject, []string{"viewer"})
	if err != nil || !created || first.TenantID != tenant || len(first.Roles) != 1 || first.Roles[0] != "viewer" {
		t.Fatalf("first EnsureIssuerUser = %+v, %v, %v", first, created, err)
	}

	second, created, err := repo.EnsureIssuerUser(ctx, tenant, "peter@initech.com", "issuer:portal", subject, []string{"operator"})

	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second EnsureIssuerUser = %+v, %v, %v; want the same user, not created", second, created, err)
	}
	got, active, err := repo.FindUserByOIDCSubject(ctx, "issuer:portal", subject)
	if err != nil || !active || len(got.Roles) != 1 || got.Roles[0] != "operator" {
		t.Errorf("after reconcile = %+v active=%v err=%v; want exactly [operator]", got, active, err)
	}
}

// TestEnsureIssuerUserRefusesASubjectLinkedInAnotherTenant: one subject is one
// person in one tenant; moving it would silently change where they work.
func TestEnsureIssuerUserRefusesASubjectLinkedInAnotherTenant(t *testing.T) {
	repo, _, ctx := openRepo(t)
	a, b := uniqueTenant("a"), uniqueTenant("b")
	for _, n := range []string{a, b} {
		if _, err := repo.EnsureTenant(ctx, n, n, 0); err != nil {
			t.Fatal(err)
		}
	}
	subject := uniqueTenant("sub")
	if _, _, err := repo.EnsureIssuerUser(ctx, a, "x@a.com", "issuer:portal", subject, nil); err != nil {
		t.Fatal(err)
	}

	_, _, err := repo.EnsureIssuerUser(ctx, b, "x@a.com", "issuer:portal", subject, nil)

	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("err = %v, want domain.ErrConflict", err)
	}
}

// TestEnsureIssuerUserNeedsTheTenant: no tenant, no user.
func TestEnsureIssuerUserNeedsTheTenant(t *testing.T) {
	repo, _, ctx := openRepo(t)

	_, _, err := repo.EnsureIssuerUser(ctx, uniqueTenant("missing"), "x@y.com", "issuer:portal", uniqueTenant("sub"), nil)

	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("err = %v, want domain.ErrNotFound", err)
	}
}

// TestEnsureIssuerUserRefusesAnEmailAnotherUserHas: the tenant already has a
// password account with this email; linking a second account to it would
// break the one-email-per-tenant rule, so it is a conflict, not a silent merge.
func TestEnsureIssuerUserRefusesAnEmailAnotherUserHas(t *testing.T) {
	repo, _, ctx := openRepo(t)
	tenant := uniqueTenant("hooli")
	if _, err := repo.EnsureTenant(ctx, tenant, "Hooli", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateUser(ctx, tenant, "gavin@hooli.com", "a-long-password-123", nil); err != nil {
		t.Fatal(err)
	}

	_, _, err := repo.EnsureIssuerUser(ctx, tenant, "gavin@hooli.com", "issuer:portal", uniqueTenant("sub"), nil)

	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("err = %v, want domain.ErrConflict", err)
	}
}

// TestEnsureTenantSizesTheDefaultPool: defaultPoolSlots sizes a new tenant's
// default pool, re-sizes an existing one, and 0 leaves it alone (a new tenant
// then inherits the default tenant's size).
func TestEnsureTenantSizesTheDefaultPool(t *testing.T) {
	repo, _, ctx := openRepo(t)
	slots := func(tenant string) int {
		t.Helper()
		p, err := repo.GetPool(ctx, tenant, domain.DefaultPoolName)
		if err != nil {
			t.Fatalf("default pool of %s: %v", tenant, err)
		}
		return p.Slots
	}
	def := slots("default")

	inherited := uniqueTenant("pool-inherit")
	if _, err := repo.EnsureTenant(ctx, inherited, "", 0); err != nil {
		t.Fatal(err)
	}
	if got := slots(inherited); got != def {
		t.Errorf("unsized tenant pool = %d, want the default tenant's %d", got, def)
	}

	sized := uniqueTenant("pool-sized")
	if _, err := repo.EnsureTenant(ctx, sized, "", 8); err != nil {
		t.Fatal(err)
	}
	if got := slots(sized); got != 8 {
		t.Errorf("new tenant pool = %d, want 8", got)
	}
	if _, err := repo.EnsureTenant(ctx, sized, "", 16); err != nil {
		t.Fatal(err)
	}
	if got := slots(sized); got != 16 {
		t.Errorf("re-sized tenant pool = %d, want 16", got)
	}
	if _, err := repo.EnsureTenant(ctx, sized, "", 0); err != nil {
		t.Fatal(err)
	}
	if got := slots(sized); got != 16 {
		t.Errorf("an unsized ensure changed the pool to %d, want it left at 16", got)
	}
	if got := slots("default"); got != def {
		t.Errorf("sizing another tenant changed the default tenant's pool to %d", got)
	}
}

// TestEnsureTenantMarksALegacyDefaultPoolRow: a tenant whose default_pool row
// lost (or never had) is_default, as one created before #1283 could, gets the
// flag back from either ensure path, and a sized pool carries the seed
// description.
func TestEnsureTenantMarksALegacyDefaultPoolRow(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	unflag := func(tenant string) {
		t.Helper()
		if _, err := pg.Exec(ctx, `UPDATE pools SET is_default = false
			WHERE name = $1 AND tenant_id = (SELECT id FROM tenants WHERE name = $2)`, domain.DefaultPoolName, tenant); err != nil {
			t.Fatal(err)
		}
	}
	for _, slots := range []int{0, 8} {
		name := uniqueTenant(fmt.Sprintf("pool-legacy-%d", slots))
		if _, err := repo.EnsureTenant(ctx, name, "", 0); err != nil {
			t.Fatal(err)
		}
		unflag(name)
		if _, err := repo.EnsureTenant(ctx, name, "", slots); err != nil {
			t.Fatal(err)
		}
		p, err := repo.GetPool(ctx, name, domain.DefaultPoolName)
		if err != nil {
			t.Fatal(err)
		}
		if !p.IsDefault {
			t.Errorf("slots=%d: default_pool is_default = false after ensure, want true", slots)
		}
		if slots > 0 && (p.Slots != slots || p.Description != "Default pool") {
			t.Errorf("slots=%d: pool = %+v, want %d slots and the seed description", slots, p, slots)
		}
	}
}
