//go:build integration

package storage_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// permissionKeys returns perms as sorted "action:resource" strings.
func permissionKeys(perms []auth.Permission) []string {
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		out = append(out, p.Action+":"+p.Resource)
	}
	slices.Sort(out)
	return out
}

// TestRolePermissionsIntegration proves the lookup a CI OIDC session makes
// on every request (ADR 0092): the grants of a role of one tenant, and
// nothing of a role of the same name in another tenant.
func TestRolePermissionsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)

	got, err := repo.RolePermissions(ctx, "default", "deployer")
	if err != nil {
		t.Fatalf("RolePermissions(default, deployer): %v", err)
	}
	if keys := permissionKeys(got); !slices.Equal(keys, []string{"deploy:dag", "read:dag"}) {
		t.Errorf("deployer grants %v, want [deploy:dag read:dag]", keys)
	}

	if _, err := repo.RolePermissions(ctx, "default", "wizard"); !errors.Is(err, auth.ErrRoleNotFound) {
		t.Errorf("unknown role: err = %v, want auth.ErrRoleNotFound", err)
	}
	if _, err := repo.RolePermissions(ctx, uniqueTenant("absent"), "deployer"); !errors.Is(err, auth.ErrRoleNotFound) {
		t.Errorf("unknown tenant: err = %v, want auth.ErrRoleNotFound", err)
	}
}

// TestRolePermissionsStaysInItsTenant: a custom role of tenant B is not a
// role of the default tenant, and a role of B that shares a built-in name
// answers B's grants, not the default tenant's.
func TestRolePermissionsStaysInItsTenant(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	db, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(db.Close)
	repo, pg := storage.NewRepository(db), db.Pool
	other := uniqueTenant("cirole")
	if _, err := repo.EnsureTenant(ctx, other, "CI role", 0, domain.TenantLimitsUpdate{}); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pg.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO roles (tenant_id, name, description, is_system)
	      SELECT id, 'ci-only', 'custom', false FROM tenants WHERE name = $1`, other)
	exec(`INSERT INTO role_permissions (role_id, permission_id)
	      SELECT r.id, p.id FROM roles r JOIN tenants t ON t.id = r.tenant_id, permissions p
	      WHERE t.name = $1 AND r.name = 'ci-only' AND p.action = 'read' AND p.resource = 'dag'`, other)
	// B's deployer loses deploy:dag; the default tenant's keeps it.
	exec(`DELETE FROM role_permissions rp USING roles r, tenants t, permissions p
	      WHERE rp.role_id = r.id AND r.tenant_id = t.id AND rp.permission_id = p.id
	        AND t.name = $1 AND r.name = 'deployer' AND p.action = 'deploy'`, other)

	if _, err := repo.RolePermissions(ctx, "default", "ci-only"); !errors.Is(err, auth.ErrRoleNotFound) {
		t.Errorf("tenant B's custom role read from the default tenant: err = %v, want auth.ErrRoleNotFound", err)
	}
	got, err := repo.RolePermissions(ctx, other, "ci-only")
	if err != nil || !slices.Equal(permissionKeys(got), []string{"read:dag"}) {
		t.Errorf("RolePermissions(B, ci-only) = %v, %v; want [read:dag]", permissionKeys(got), err)
	}
	got, err = repo.RolePermissions(ctx, other, "deployer")
	if err != nil || !slices.Equal(permissionKeys(got), []string{"read:dag"}) {
		t.Errorf("RolePermissions(B, deployer) = %v, %v; want B's own [read:dag]", permissionKeys(got), err)
	}
	got, err = repo.RolePermissions(ctx, "default", "deployer")
	if err != nil || !slices.Equal(permissionKeys(got), []string{"deploy:dag", "read:dag"}) {
		t.Errorf("RolePermissions(default, deployer) = %v, %v; want [deploy:dag read:dag]", permissionKeys(got), err)
	}
}
