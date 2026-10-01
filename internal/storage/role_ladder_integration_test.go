//go:build integration

package storage_test

import (
	"context"
	"os"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestRoleLadderSeedIntegration pins the role-ladder migration: the default
// tenant carries the built-in viewer/editor/operator roles alongside admin,
// each granting exactly the permission set the ladder promises. The counts are
// the closure of the matrix (each higher role includes the lower one's grants),
// so a drift in either the migration or the shared permission vocabulary trips
// this test.
func TestRoleLadderSeedIntegration(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)

	// Each ladder role must exist as a built-in (is_system) role in the default
	// tenant, and grant the expected number of permissions.
	wantGrants := map[string]int{
		"viewer":   9,  // read on dag, dag_run, task_instance, task, xcom, pool, connection, variable, config
		"editor":   12, // viewer + write on dag, variable, connection
		"operator": 16, // editor + write on dag_run, task_instance, pool + execute on dag
	}

	for role, want := range wantGrants {
		var isSystem bool
		if err := pg.Pool.QueryRow(ctx,
			`SELECT is_system FROM roles r
			 JOIN tenants t ON t.id = r.tenant_id
			 WHERE t.name = 'default' AND r.name = $1`, role).Scan(&isSystem); err != nil {
			t.Fatalf("role %q not found in default tenant: %v", role, err)
		}
		if !isSystem {
			t.Errorf("role %q must be a built-in (is_system) role", role)
		}

		var count int
		if err := pg.Pool.QueryRow(ctx,
			`SELECT count(*) FROM role_permissions rp
			 JOIN roles r ON r.id = rp.role_id
			 JOIN tenants t ON t.id = r.tenant_id
			 WHERE t.name = 'default' AND r.name = $1`, role).Scan(&count); err != nil {
			t.Fatalf("counting grants for %q: %v", role, err)
		}
		if count != want {
			t.Errorf("role %q grants = %d, want %d", role, count, want)
		}
	}

	// The migration ships the roles unassigned — it only writes `roles` and
	// `role_permissions`, never `user_roles` — so no pre-existing account gains
	// access on upgrade. That is a property of the migration SQL (verified by
	// inspection and the permission-count assertions above), not a global runtime
	// invariant: application code (OIDC login reconciliation, admin role grants)
	// legitimately assigns these roles to users, so a shared-DB count of ladder
	// user_roles is NOT zero once other tests have run and must not be asserted.

	// A representative grant from each tier proves the matrix content, not just
	// its cardinality: viewer can read a variable, editor can write one, and
	// operator can write a pool.
	checks := []struct {
		role, action, resource string
	}{
		{"viewer", "read", "variable"},
		{"editor", "write", "variable"},
		{"operator", "write", "pool"},
	}
	for _, ch := range checks {
		var exists bool
		if err := pg.Pool.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM role_permissions rp
			   JOIN roles r ON r.id = rp.role_id
			   JOIN tenants t ON t.id = r.tenant_id
			   JOIN permissions p ON p.id = rp.permission_id
			   WHERE t.name = 'default' AND r.name = $1 AND p.action = $2 AND p.resource = $3
			 )`, ch.role, ch.action, ch.resource).Scan(&exists); err != nil {
			t.Fatalf("checking grant %s %s:%s: %v", ch.role, ch.action, ch.resource, err)
		}
		if !exists {
			t.Errorf("role %q must grant %s:%s", ch.role, ch.action, ch.resource)
		}
	}
}
