//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestCreateUserIntegration exercises the admin create-user query end to end:
// the new user is persisted, granted the requested role, and can authenticate
// with the supplied password (proving the reused bcrypt hashing round-trips).
func TestCreateUserIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("newuser_%d@example.com", time.Now().UnixNano())
	const password = "create-user-secret-1"

	got, err := repo.CreateUser(ctx, "default", email, password, []string{"admin"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if got.ID == "" || got.Email != email || len(got.Roles) != 1 || got.Roles[0] != "admin" || !got.IsActive {
		t.Fatalf("unexpected created user: %+v", got)
	}

	// The created user can log in with the plaintext password (hash round-trips),
	// and inherits the admin role's permissions.
	authn := auth.NewJWTAuthenticator(repo, "create-user-test-secret", time.Hour)
	if _, lerr := authn.IssueToken(ctx, auth.Credentials{Tenant: "default", Username: email, Password: password}); lerr != nil {
		t.Errorf("created user cannot log in: %v", lerr)
	}
}

// TestCreateUserDuplicateEmailIntegration proves the unique-constraint violation
// surfaces as domain.ErrConflict (the API maps it to 409), not a raw 500.
func TestCreateUserDuplicateEmailIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("dupe_%d@example.com", time.Now().UnixNano())

	if _, err := repo.CreateUser(ctx, "default", email, "pw-first-1234", []string{"admin"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := repo.CreateUser(ctx, "default", email, "pw-second-1234", []string{"admin"}); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate create err = %v, want ErrConflict", err)
	}
}

// TestCreateUserUnknownRoleIntegration proves an unknown role is rejected as a
// validation error and leaves no orphaned account behind (the role is resolved
// before the user is inserted).
func TestCreateUserUnknownRoleIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("norole_%d@example.com", time.Now().UnixNano())

	if _, err := repo.CreateUser(ctx, "default", email, "pw-12345678", []string{"wizard"}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unknown role err = %v, want ErrValidation", err)
	}
	// No account was created, so a subsequent create with no roles succeeds rather
	// than colliding.
	if _, err := repo.CreateUser(ctx, "default", email, "pw-12345678", nil); err != nil {
		t.Errorf("create after rejected role: %v", err)
	}
}

// TestListUsersIntegration proves the list surfaces a just-created account with
// its granted role aggregated, reports a total that counts it, and never exposes
// a password or hash (there is no such field to expose on the returned type).
func TestListUsersIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("listed_%d@example.com", time.Now().UnixNano())
	if _, err := repo.CreateUser(ctx, "default", email, "pw-12345678", []string{"admin"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	users, total, err := repo.ListUsers(ctx, "default", 100, 0)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if total < 1 {
		t.Fatalf("total = %d, want at least the created user", total)
	}
	var found *domain.User
	for i := range users {
		if users[i].Email == email {
			found = &users[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("created user %q not in list", email)
	}
	if found.ID == "" || !found.IsActive {
		t.Errorf("unexpected listed user: %+v", *found)
	}
	if len(found.Roles) != 1 || found.Roles[0] != "admin" {
		t.Errorf("roles = %v, want [admin]", found.Roles)
	}
}

// TestCreateUserAssignsMultipleRolesIntegration proves the create path grants
// every role in the slice, and the list reflects all of them. It seeds a second
// grantable role in the default tenant (removed on cleanup; its user_roles rows
// cascade) so the account can hold more than one.
func TestCreateUserAssignsMultipleRolesIntegration(t *testing.T) {
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
	repo := storage.NewRepository(pg)

	// A tenant-defined, non-built-in role planted just for this test. It must not
	// be one of the built-in ladder roles (viewer/editor/operator): those are
	// seeded by migration and marked is_system, so the destructive cleanup below
	// would delete real seeded data rather than the fixture.
	const extraRole = "grantable-test-role"
	if _, err := pg.Pool.Exec(ctx,
		`INSERT INTO roles (tenant_id, name, description, is_system)
		 SELECT id, $1, 'grantable test role', false FROM tenants WHERE name = 'default'
		 ON CONFLICT (tenant_id, name) DO NOTHING`, extraRole); err != nil {
		t.Fatalf("seeding role: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pg.Pool.Exec(context.Background(),
			`DELETE FROM roles WHERE name = $1 AND tenant_id = (SELECT id FROM tenants WHERE name = 'default')`, extraRole)
	})

	email := fmt.Sprintf("multirole_%d@example.com", time.Now().UnixNano())
	if _, err := repo.CreateUser(ctx, "default", email, "pw-12345678", []string{"admin", extraRole}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	users, _, err := repo.ListUsers(ctx, "default", 1000, 0)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	var found *domain.User
	for i := range users {
		if users[i].Email == email {
			found = &users[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("created user %q not in list", email)
	}
	got := map[string]bool{}
	for _, r := range found.Roles {
		got[r] = true
	}
	if len(found.Roles) != 2 || !got["admin"] || !got[extraRole] {
		t.Errorf("roles = %v, want both admin and %s", found.Roles, extraRole)
	}
}

// TestListUsersRoleAggregationIntegration proves a user with more than one role
// comes back with every role name, and one with none comes back with an empty
// (non-nil) set rather than a null.
func TestListUsersRoleAggregationIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	email := fmt.Sprintf("noroles_%d@example.com", time.Now().UnixNano())
	if _, err := repo.CreateUser(ctx, "default", email, "pw-12345678", nil); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	users, _, err := repo.ListUsers(ctx, "default", 100, 0)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for i := range users {
		if users[i].Email == email {
			if users[i].Roles == nil {
				t.Errorf("a role-less user must list an empty (non-nil) role set")
			}
			if len(users[i].Roles) != 0 {
				t.Errorf("roles = %v, want empty", users[i].Roles)
			}
			return
		}
	}
	t.Fatalf("role-less user %q not in list", email)
}
