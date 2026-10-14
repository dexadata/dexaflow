//go:build integration

package storage_test

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestEnsureTenantLeavesCustomRoleNamedLikeBuiltInAlone covers a tenant that
// created a custom role named "deployer" before migration 044 added the
// built-in of that name. 044 leaves such a role untouched, and so must every
// later PUT of the tenant: copying the built-in grants matches roles by name,
// and only the tenant's built-in roles may receive them.
func TestEnsureTenantLeavesCustomRoleNamedLikeBuiltInAlone(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	name := uniqueTenant("customdeployer")
	if _, err = repo.EnsureTenant(ctx, name, "Custom deployer", 0, domain.TenantLimitsUpdate{}); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, xerr := pg.Pool.Exec(ctx, sql, args...); xerr != nil {
			t.Fatalf("%s: %v", sql, xerr)
		}
	}
	// The tenant as it was before 044: no built-in deployer, and a custom
	// role of that name that may only read DAGs.
	exec(`DELETE FROM roles r USING tenants t WHERE r.tenant_id = t.id AND t.name = $1 AND r.name = 'deployer'`, name)
	exec(`INSERT INTO roles (tenant_id, name, description, is_system)
	      SELECT id, 'deployer', 'custom', false FROM tenants WHERE name = $1`, name)
	exec(`INSERT INTO role_permissions (role_id, permission_id)
	      SELECT r.id, p.id FROM roles r JOIN tenants t ON t.id = r.tenant_id, permissions p
	      WHERE t.name = $1 AND r.name = 'deployer' AND p.action = 'read' AND p.resource = 'dag'`, name)

	created, err := repo.EnsureTenant(ctx, name, "Custom deployer", 0, domain.TenantLimitsUpdate{})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatalf("EnsureTenant reported the existing tenant %s as created", name)
	}

	rows, err := pg.Pool.Query(ctx, `SELECT p.action || ':' || p.resource FROM role_permissions rp
	      JOIN roles r ON r.id = rp.role_id JOIN tenants t ON t.id = r.tenant_id
	      JOIN permissions p ON p.id = rp.permission_id
	      WHERE t.name = $1 AND r.name = 'deployer' AND NOT r.is_system`, name)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var grants []string
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			t.Fatal(err)
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(grants)
	if got := strings.Join(grants, " "); got != "read:dag" {
		t.Errorf("custom role deployer has grants %q after EnsureTenant, want only read:dag", got)
	}
}
