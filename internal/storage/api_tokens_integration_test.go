//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/migrations"
)

// apiTokenOwner creates a user with roles in tenant and returns its id.
func apiTokenOwner(t *testing.T, ctx context.Context, repo *storage.Repository, tenant string, roles ...string) (string, string) {
	t.Helper()
	email := fmt.Sprintf("ci_%d@example.com", time.Now().UnixNano())
	u, err := repo.CreateUser(ctx, tenant, email, "ci-password-1234", roles)
	if err != nil {
		t.Fatalf("CreateUser in %s: %v", tenant, err)
	}
	return u.ID, email
}

// secondTenant creates a fresh tenant with the built-in roles, as the service
// API does.
func secondTenant(t *testing.T, ctx context.Context, repo *storage.Repository) string {
	t.Helper()
	name := fmt.Sprintf("tok-%d", time.Now().UnixNano())
	if _, err := repo.EnsureTenant(ctx, name, name, 0, domain.TenantLimitsUpdate{}); err != nil {
		t.Fatalf("EnsureTenant: %v", err)
	}
	return name
}

func newStoredToken(t *testing.T, ctx context.Context, repo *storage.Repository, tenant, userID, name string) (string, auth.APIToken) {
	t.Helper()
	plain, hash, err := auth.NewAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	scopes, _ := auth.APITokenScopes("deploy")
	tok, err := repo.CreateAPIToken(ctx, tenant, userID, name, hash, scopes, time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	return plain, tok
}

// TestAPITokenStoresOnlyTheHashIntegration: the row holds the SHA-256 of the
// token and nothing else of it, and the hash finds the token again with its
// tenant, owner and scopes.
func TestAPITokenStoresOnlyTheHashIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg := openPG(t)
	userID, _ := apiTokenOwner(t, ctx, repo, "default", "deployer")
	plain, tok := newStoredToken(t, ctx, repo, "default", userID, "ci-x")

	var stored string
	var leaked bool
	if err := pg.Pool.QueryRow(ctx, `SELECT token_hash, row_to_json(k)::text LIKE '%' || $2 || '%' FROM api_tokens k WHERE id = $1`,
		tok.ID, plain[len(auth.APITokenPrefix):]).Scan(&stored, &leaked); err != nil {
		t.Fatalf("reading row: %v", err)
	}
	if stored != auth.HashAPIToken(plain) || leaked {
		t.Errorf("stored %q (plaintext in row: %v), want only the hash", stored, leaked)
	}
	found, err := repo.FindAPITokenByHash(ctx, auth.HashAPIToken(plain))
	if err != nil {
		t.Fatalf("FindAPITokenByHash: %v", err)
	}
	if found.ID != tok.ID || found.TenantID != "default" || found.UserID != userID || found.Name != "ci-x" ||
		!slices.Equal(found.Scopes, []string{auth.ScopeDeploy}) || found.RevokedAt != nil {
		t.Errorf("found %+v, want the created token", found)
	}
	if _, err := repo.FindAPITokenByHash(ctx, auth.HashAPIToken(plain+"x")); !errors.Is(err, auth.ErrAPITokenNotFound) {
		t.Errorf("unknown hash = %v, want ErrAPITokenNotFound", err)
	}
}

// TestAPITokenAuthenticatesEndToEndIntegration drives the authenticator over
// the repository: the token authenticates as its scoped owner, records its
// use, and stops working once revoked or once its owner is deactivated.
func TestAPITokenAuthenticatesEndToEndIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg := openPG(t)
	userID, _ := apiTokenOwner(t, ctx, repo, "default", "deployer")
	plain, tok := newStoredToken(t, ctx, repo, "default", userID, "ci-e2e")
	authn := auth.NewAPITokenAuthenticator(auth.NewJWTAuthenticator(repo, "s", time.Hour), repo, repo)

	user, err := authn.Authenticate(ctx, plain)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if user.ID != userID || !user.Scoped || !user.HasPermission("deploy", "dag") || user.HasPermission("write", "dag") {
		t.Errorf("principal %+v, want the scoped deployer", user)
	}
	var lastUsed *time.Time
	if err := pg.Pool.QueryRow(ctx, `SELECT last_used_at FROM api_tokens WHERE id = $1`, tok.ID).Scan(&lastUsed); err != nil || lastUsed == nil {
		t.Errorf("last_used_at = %v (%v), want it recorded", lastUsed, err)
	}

	if _, err := repo.RevokeAPIToken(ctx, "default", tok.ID, userID); err != nil {
		t.Fatalf("RevokeAPIToken: %v", err)
	}
	if _, err := authn.Authenticate(ctx, plain); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("revoked token = %v, want ErrInvalidToken", err)
	}

	plain2, _ := newStoredToken(t, ctx, repo, "default", userID, "ci-e2e")
	if _, err := pg.Pool.Exec(ctx, `UPDATE users SET is_active = false WHERE id = $1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := authn.Authenticate(ctx, plain2); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("token of a deactivated owner = %v, want ErrInvalidToken", err)
	}
}

// TestAPITokenNameAndCapIntegration: one live token per owner and name, the
// name is free again once revoked, and an owner holds at most
// MaxLiveAPITokensPerUser live tokens.
func TestAPITokenNameAndCapIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	userID, _ := apiTokenOwner(t, ctx, repo, "default", "deployer")
	_, first := newStoredToken(t, ctx, repo, "default", userID, "dup")
	_, hash, _ := auth.NewAPIToken()
	scopes, _ := auth.APITokenScopes("read")
	exp := time.Now().Add(time.Hour)
	if _, err := repo.CreateAPIToken(ctx, "default", userID, "dup", hash, scopes, exp); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("second live token named dup = %v, want ErrConflict", err)
	}
	if _, err := repo.RevokeAPIToken(ctx, "default", first.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateAPIToken(ctx, "default", userID, "dup", hash, scopes, exp); err != nil {
		t.Errorf("name of a revoked token not reusable: %v", err)
	}

	for i := 1; i < storage.MaxLiveAPITokensPerUser; i++ {
		_, h, _ := auth.NewAPIToken()
		if _, err := repo.CreateAPIToken(ctx, "default", userID, fmt.Sprintf("t%d", i), h, scopes, exp); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	_, h, _ := auth.NewAPIToken()
	if _, err := repo.CreateAPIToken(ctx, "default", userID, "one-too-many", h, scopes, exp); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("token over the cap = %v, want ErrValidation", err)
	}
}

// TestAPITokensStayInTheirTenantIntegration: a token is created only for a
// user of the caller's tenant, and listing and revoking never reach another
// tenant's tokens; an owner filter keeps another user's tokens out.
func TestAPITokensStayInTheirTenantIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	other := secondTenant(t, ctx, repo)
	userA, emailA := apiTokenOwner(t, ctx, repo, "default", "deployer")
	userA2, _ := apiTokenOwner(t, ctx, repo, "default", "viewer")
	userB, _ := apiTokenOwner(t, ctx, repo, other, "deployer")
	_, tokA := newStoredToken(t, ctx, repo, "default", userA, "a")
	_, tokA2 := newStoredToken(t, ctx, repo, "default", userA2, "a2")
	_, tokB := newStoredToken(t, ctx, repo, other, userB, "b")

	_, hash, _ := auth.NewAPIToken()
	scopes, _ := auth.APITokenScopes("deploy")
	if _, err := repo.CreateAPIToken(ctx, other, userA, "cross", hash, scopes, time.Now().Add(time.Hour)); !errors.Is(err, domain.ErrValidation) {
		t.Errorf("token for a user of another tenant = %v, want ErrValidation", err)
	}

	own, total, err := repo.ListAPITokens(ctx, "default", userA, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(own) != 1 || own[0].ID != tokA.ID || own[0].UserEmail != emailA {
		t.Errorf("own list = %+v (total %d), want only token a", own, total)
	}
	all, _, err := repo.ListAPITokens(ctx, other, "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != tokB.ID {
		t.Errorf("tenant %s list = %+v, want only token b", other, all)
	}

	if _, err := repo.RevokeAPIToken(ctx, other, tokA.ID, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("revoking default's token from %s = %v, want ErrNotFound", other, err)
	}
	if _, err := repo.RevokeAPIToken(ctx, "default", tokA2.ID, userA); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("revoking another owner's token = %v, want ErrNotFound", err)
	}
	if found, _ := repo.FindAPITokenByHash(ctx, hashOfID(t, ctx, tokA.ID)); found == nil || found.RevokedAt != nil {
		t.Error("a refused revoke revoked the token")
	}
	if _, err := repo.RevokeAPIToken(ctx, "default", "not-a-uuid", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("revoking a malformed id = %v, want ErrNotFound", err)
	}
}

// hashOfID reads a token's stored hash, for tests that only hold its id.
func hashOfID(t *testing.T, ctx context.Context, id string) string {
	t.Helper()
	var h string
	if err := openPG(t).Pool.QueryRow(ctx, `SELECT token_hash FROM api_tokens WHERE id = $1`, id).Scan(&h); err != nil {
		t.Fatal(err)
	}
	return h
}

// TestDeployerRoleIntegration pins the built-in deployer role: in default and
// in a tenant created afterwards, it grants read:dag and deploy:dag and
// nothing else.
func TestDeployerRoleIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	other := secondTenant(t, ctx, repo)
	for _, tenant := range []string{"default", other} {
		grants := deployerGrants(t, ctx, tenant)
		if !slices.Equal(grants, []string{"deploy:dag", "read:dag"}) {
			t.Errorf("%s deployer grants = %v, want deploy:dag and read:dag", tenant, grants)
		}
	}
}

// TestDeployerMigrationReachesEveryTenantIntegration applies the up migration
// again over a tenant that has no roles and one with a custom deployer role:
// the first gets the built-in role, the custom role is left as it is, and
// nothing changes on default (the migration is idempotent).
func TestDeployerMigrationReachesEveryTenantIntegration(t *testing.T) {
	_, _, ctx := openRepo(t)
	pg := openPG(t)
	bare := insertTenant(t, ctx, pg.Pool, fmt.Sprintf("bare-%d", time.Now().UnixNano()))
	custom := insertTenant(t, ctx, pg.Pool, fmt.Sprintf("custom-%d", time.Now().UnixNano()))
	if _, err := pg.Pool.Exec(ctx, `INSERT INTO roles (tenant_id, name, is_system) VALUES ($1, 'deployer', false)`, custom); err != nil {
		t.Fatal(err)
	}
	up, err := migrations.Files.ReadFile("044_deployer_role_and_api_tokens.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Pool.Exec(ctx, string(up)); err != nil {
		t.Fatalf("re-applying 044: %v", err)
	}
	var bareName, customName string
	if err := pg.Pool.QueryRow(ctx, `SELECT name FROM tenants WHERE id = $1`, bare).Scan(&bareName); err != nil {
		t.Fatal(err)
	}
	if err := pg.Pool.QueryRow(ctx, `SELECT name FROM tenants WHERE id = $1`, custom).Scan(&customName); err != nil {
		t.Fatal(err)
	}
	if got := deployerGrants(t, ctx, bareName); !slices.Equal(got, []string{"deploy:dag", "read:dag"}) {
		t.Errorf("tenant without roles: deployer grants = %v", got)
	}
	if got := deployerGrants(t, ctx, customName); len(got) != 0 {
		t.Errorf("custom deployer role was granted %v, want it untouched", got)
	}
	if got := deployerGrants(t, ctx, "default"); !slices.Equal(got, []string{"deploy:dag", "read:dag"}) {
		t.Errorf("default deployer grants after re-apply = %v", got)
	}
}

func deployerGrants(t *testing.T, ctx context.Context, tenant string) []string {
	t.Helper()
	rows, err := openPG(t).Pool.Query(ctx, `
		SELECT p.action || ':' || p.resource
		FROM roles r
		JOIN tenants tn ON tn.id = r.tenant_id
		JOIN role_permissions rp ON rp.role_id = r.id
		JOIN permissions p ON p.id = rp.permission_id
		WHERE tn.name = $1 AND r.name = 'deployer'
		ORDER BY 1`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// openPG connects to the integration database directly, for checks the
// repository does not expose.
func openPG(t *testing.T) *storage.Postgres {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	pg, err := storage.NewPostgres(context.Background(), config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	return pg
}
