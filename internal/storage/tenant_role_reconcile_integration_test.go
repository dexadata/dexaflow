//go:build integration

package storage_test

import (
	"fmt"
	"io/fs"
	"os"
	"sort"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/migrations"
)

const reconcileMigration = "037_reconcile_tenant_system_roles.up.sql"

// TestReconcileMigrationAlignsEveryTenantsBuiltInRoles covers #1305: after a
// migration changed the built-in roles of "default" only, the reconcile
// migration brings every other tenant's built-in roles back in line. It adds
// the grants and roles the tenant is missing, removes the grants default no
// longer has, and leaves the tenant's custom roles exactly as they were.
//
// The shared integration database is already migrated, so the test builds the
// drift by hand and then runs the migration body again; it is idempotent.
func TestReconcileMigrationAlignsEveryTenantsBuiltInRoles(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	name := uniqueTenant("drift")
	if _, err = repo.EnsureTenant(ctx, name, "Drift", 0, domain.TenantLimitsUpdate{}); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, xerr := pg.Pool.Exec(ctx, sql, args...); xerr != nil {
			t.Fatalf("%s: %v", sql, xerr)
		}
	}
	// A grant default has that the tenant lost (a later "add" migration).
	exec(`DELETE FROM role_permissions rp USING roles r, tenants t, permissions p
	      WHERE rp.role_id = r.id AND r.tenant_id = t.id AND rp.permission_id = p.id
	        AND t.name = $1 AND r.name = 'operator' AND p.action = 'execute' AND p.resource = 'dag'`, name)
	// A grant default no longer has that the tenant kept (a later "revoke").
	exec(`INSERT INTO role_permissions (role_id, permission_id)
	      SELECT r.id, p.id FROM roles r JOIN tenants t ON t.id = r.tenant_id, permissions p
	      WHERE t.name = $1 AND r.name = 'viewer' AND p.action = 'admin' AND p.resource = 'tenant'`, name)
	// A built-in role the tenant never received (a later "new role" migration).
	exec(`DELETE FROM roles r USING tenants t WHERE r.tenant_id = t.id AND t.name = $1 AND r.name = 'editor'`, name)
	// A custom role with a grant no built-in role has: the tenant's own.
	exec(`INSERT INTO roles (tenant_id, name, description, is_system)
	      SELECT id, 'auditor', 'custom', false FROM tenants WHERE name = $1`, name)
	exec(`INSERT INTO role_permissions (role_id, permission_id)
	      SELECT r.id, p.id FROM roles r JOIN tenants t ON t.id = r.tenant_id, permissions p
	      WHERE t.name = $1 AND r.name = 'auditor' AND p.action = 'admin' AND p.resource = 'tenant'`, name)

	body, err := fs.ReadFile(migrations.Files, reconcileMigration)
	if err != nil {
		t.Fatalf("reading %s: %v", reconcileMigration, err)
	}
	exec(string(body))

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
		t.Errorf("built-in grants of %s differ from default after reconcile:\n got %v\nwant %v", name, got, want)
	}
	var custom int
	if err := pg.Pool.QueryRow(ctx, `SELECT count(*) FROM role_permissions rp
	      JOIN roles r ON r.id = rp.role_id JOIN tenants t ON t.id = r.tenant_id
	      WHERE t.name = $1 AND r.name = 'auditor' AND NOT r.is_system`, name).Scan(&custom); err != nil {
		t.Fatal(err)
	}
	if custom != 1 {
		t.Errorf("custom role auditor has %d grants after reconcile, want its 1 untouched", custom)
	}
}
