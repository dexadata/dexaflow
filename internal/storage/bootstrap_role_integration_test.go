//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// plantRolelessUser inserts a user into the tenant with no role, the state a
// crash between creating the admin and granting its role used to leave behind
// (#513).
func plantRolelessUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, email, hash string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO users (tenant_id, email, password_hash)
	      SELECT id, $2, $3 FROM tenants WHERE name = $1`, tenant, email, hash); err != nil {
		t.Fatalf("plant roleless user: %v", err)
	}
	// Registered after testPool's Close, so it runs first; user_roles rows go
	// with the user (ON DELETE CASCADE).
	t.Cleanup(func() {
		if _, err := pool.Exec(context.WithoutCancel(ctx), `DELETE FROM users WHERE email = $1`, email); err != nil {
			t.Logf("cleanup user: %v", err)
		}
	})
}

// adminRoleTenants returns the tenant name of every role named admin that the
// user holds, so a test can tell which tenant's admin role was granted.
func adminRoleTenants(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenant, email string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT rt.name FROM users u
	      JOIN tenants ut ON ut.id = u.tenant_id
	      JOIN user_roles ur ON ur.user_id = u.id
	      JOIN roles r ON r.id = ur.role_id
	      JOIN tenants rt ON rt.id = r.tenant_id
	      WHERE ut.name = $1 AND u.email = $2 AND r.name = 'admin'`, tenant, email)
	if err != nil {
		t.Fatalf("query admin roles: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func testPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestBootstrapAdminHashGrantsRoleToRolelessAdminIntegration covers #513: an
// admin that exists without the admin role (a crash between the create and the
// role grant) must get the role on the next bootstrap, which reconciles the
// existing user instead of creating it.
func TestBootstrapAdminHashGrantsRoleToRolelessAdminIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pool := testPool(t, ctx)
	email := fmt.Sprintf("roleless-admin-%d@leoflow.local", time.Now().UnixNano())
	hash, err := auth.HashPassword("roleless-pw-1")
	if err != nil {
		t.Fatal(err)
	}
	plantRolelessUser(t, ctx, pool, "default", email, hash)

	created, err := repo.BootstrapAdminHash(ctx, "default", email, hash)
	if err != nil {
		t.Fatalf("BootstrapAdminHash: %v", err)
	}
	if created {
		t.Error("bootstrap reported an existing user as newly created")
	}
	user, _, err := repo.FindUserByLogin(ctx, "default", email)
	if err != nil {
		t.Fatalf("load admin: %v", err)
	}
	if !slices.Contains(user.Roles, "admin") {
		t.Errorf("admin has roles %v after bootstrap, want admin", user.Roles)
	}
}

// TestBootstrapAdminHashGrantsTheTenantsOwnAdminRoleIntegration checks that the
// role granted to a roleless admin is the admin role of the admin's own tenant,
// never another tenant's role of the same name.
func TestBootstrapAdminHashGrantsTheTenantsOwnAdminRoleIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pool := testPool(t, ctx)
	tenant := uniqueTenant("bootstrap-role")
	if _, err := repo.EnsureTenant(ctx, tenant, "Bootstrap role", 0, domain.TenantLimitsUpdate{}); err != nil {
		t.Fatal(err)
	}
	email := fmt.Sprintf("tenant-admin-%d@leoflow.local", time.Now().UnixNano())
	hash, err := auth.HashPassword("tenant-pw-1")
	if err != nil {
		t.Fatal(err)
	}
	plantRolelessUser(t, ctx, pool, tenant, email, hash)

	if _, err := repo.BootstrapAdminHash(ctx, tenant, email, hash); err != nil {
		t.Fatalf("BootstrapAdminHash: %v", err)
	}
	got := adminRoleTenants(t, ctx, pool, tenant, email)
	if len(got) != 1 || got[0] != tenant {
		t.Errorf("admin role granted from tenants %v, want only %s", got, tenant)
	}
}

// TestBootstrapAdminHashFreshCreateGrantsRoleIntegration pins the fresh-create
// path: a new admin holds the admin role, and a second bootstrap keeps it.
func TestBootstrapAdminHashFreshCreateGrantsRoleIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("fresh-admin-%d@leoflow.local", time.Now().UnixNano())
	t.Cleanup(func() { deleteUserByEmail(t, email) })
	hash, err := auth.HashPassword("fresh-pw-1")
	if err != nil {
		t.Fatal(err)
	}
	for i, wantCreated := range []bool{true, false} {
		created, err := repo.BootstrapAdminHash(ctx, "default", email, hash)
		if err != nil {
			t.Fatalf("bootstrap %d: %v", i, err)
		}
		if created != wantCreated {
			t.Errorf("bootstrap %d: created=%v, want %v", i, created, wantCreated)
		}
		user, _, err := repo.FindUserByLogin(ctx, "default", email)
		if err != nil {
			t.Fatalf("bootstrap %d: load admin: %v", i, err)
		}
		if !slices.Contains(user.Roles, "admin") {
			t.Errorf("bootstrap %d: admin has roles %v, want admin", i, user.Roles)
		}
	}
}
