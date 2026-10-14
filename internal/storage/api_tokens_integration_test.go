//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
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

// TestAPITokenLastUseIsThrottledInSQLIntegration: the five-minute throttle on
// last_used_at is decided by the UPDATE itself, so concurrent requests within
// five minutes write once, a later one writes again, and an older time never
// moves it back.
func TestAPITokenLastUseIsThrottledInSQLIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	userID, _ := apiTokenOwner(t, ctx, repo, "default", "deployer")
	_, tok := newStoredToken(t, ctx, repo, "default", userID, "touch")
	t0 := time.Now().UTC().Truncate(time.Second)

	var wg sync.WaitGroup
	var mu sync.Mutex
	writes := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			wrote, err := repo.TouchAPIToken(ctx, tok.ID, t0.Add(time.Duration(i)*10*time.Second))
			if err != nil {
				t.Errorf("TouchAPIToken: %v", err)
				return
			}
			if wrote {
				mu.Lock()
				writes++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if writes != 1 {
		t.Fatalf("20 concurrent uses within five minutes wrote %d times, want 1", writes)
	}
	first, err := repo.FindAPITokenByHash(ctx, hashOfID(t, ctx, tok.ID))
	if err != nil || first.LastUsedAt == nil {
		t.Fatalf("last use = %+v, %v", first, err)
	}
	if wrote, _ := repo.TouchAPIToken(ctx, tok.ID, first.LastUsedAt.Add(-time.Hour)); wrote {
		t.Error("an older use moved last_used_at back")
	}
	if wrote, _ := repo.TouchAPIToken(ctx, tok.ID, first.LastUsedAt.Add(4*time.Minute+59*time.Second)); wrote {
		t.Error("a use within five minutes wrote again")
	}
	if wrote, _ := repo.TouchAPIToken(ctx, tok.ID, first.LastUsedAt.Add(5*time.Minute+time.Second)); !wrote {
		t.Error("a use after five minutes was not recorded")
	}
}

// TestAPITokenExpiredNameIsReusableIntegration: an expired token that was
// never revoked does not hold its name: creating a token with that name
// revokes the expired one in the same transaction and succeeds.
func TestAPITokenExpiredNameIsReusableIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg := openPG(t)
	userID, _ := apiTokenOwner(t, ctx, repo, "default", "deployer")
	_, old := newStoredToken(t, ctx, repo, "default", userID, "nightly")
	if _, err := pg.Pool.Exec(ctx, `UPDATE api_tokens SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	_, fresh := newStoredToken(t, ctx, repo, "default", userID, "nightly")
	if fresh.ID == old.ID {
		t.Fatal("the expired token was returned again")
	}
	var revoked *time.Time
	if err := pg.Pool.QueryRow(ctx, `SELECT revoked_at FROM api_tokens WHERE id = $1`, old.ID).Scan(&revoked); err != nil || revoked == nil {
		t.Errorf("expired token revoked_at = %v (%v), want it revoked by the new create", revoked, err)
	}
	// A live token of the same name still blocks the name.
	_, h, _ := auth.NewAPIToken()
	scopes, _ := auth.APITokenScopes("deploy")
	if _, err := repo.CreateAPIToken(ctx, "default", userID, "nightly", h, scopes, time.Now().Add(time.Hour)); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("name of a live token = %v, want ErrConflict", err)
	}
}

// expireToken moves a stored token's expiry an hour into the past.
func expireToken(t *testing.T, ctx context.Context, pg *storage.Postgres, id string) {
	t.Helper()
	if _, err := pg.Pool.Exec(ctx, `UPDATE api_tokens SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

// retireAudit is one api_token.retire_expired row of the audit log.
type retireAudit struct {
	tenantID, userID, resourceType, resourceID string
	meta                                       map[string]string
}

// retireAudits returns the api_token.retire_expired rows that name token id.
func retireAudits(t *testing.T, ctx context.Context, pg *storage.Postgres, id string) []retireAudit {
	t.Helper()
	rows, err := pg.Pool.Query(ctx, `SELECT tenant_id::text, COALESCE(user_id::text, ''), COALESCE(resource_type, ''), COALESCE(resource_id, ''), metadata
		FROM audit_log WHERE action = 'api_token.retire_expired' AND metadata->>'token_id' = $1`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []retireAudit
	for rows.Next() {
		var a retireAudit
		if err := rows.Scan(&a.tenantID, &a.userID, &a.resourceType, &a.resourceID, &a.meta); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestAPITokenRetireIsAuditedIntegration (#1768): the create that retires an
// expired token of the same name records it in the audit log, in the
// creator's tenant, naming the old token, when it expired, and the token that
// took its name, so the timeline does not read it as a manual revoke.
func TestAPITokenRetireIsAuditedIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg := openPG(t)
	tenant := secondTenant(t, ctx, repo)
	userID, email := apiTokenOwner(t, ctx, repo, tenant, "deployer")
	_, old := newStoredToken(t, ctx, repo, tenant, userID, "nightly")
	expireToken(t, ctx, pg, old.ID)
	var expiredAt time.Time
	if err := pg.Pool.QueryRow(ctx, `SELECT expires_at FROM api_tokens WHERE id = $1`, old.ID).Scan(&expiredAt); err != nil {
		t.Fatal(err)
	}
	_, fresh := newStoredToken(t, ctx, repo, tenant, userID, "nightly")

	got := retireAudits(t, ctx, pg, old.ID)
	if len(got) != 1 {
		t.Fatalf("retire audit rows for the expired token = %d, want 1", len(got))
	}
	var tid string
	if err := pg.Pool.QueryRow(ctx, `SELECT id::text FROM tenants WHERE name = $1`, tenant).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	a := got[0]
	if a.tenantID != tid || a.userID != userID || a.resourceType != "auth" || a.resourceID != email {
		t.Errorf("audit row = tenant %s user %s resource %s/%s, want tenant %s user %s resource auth/%s",
			a.tenantID, a.userID, a.resourceType, a.resourceID, tid, userID, email)
	}
	want := map[string]string{
		"outcome": "success", "email": email, "token_id": old.ID, "token_name": "nightly",
		"token_owner_id": userID, "expires_at": expiredAt.UTC().Format(time.RFC3339),
		"replaced_by_token_id": fresh.ID,
	}
	for k, v := range want {
		if a.meta[k] != v {
			t.Errorf("audit metadata %s = %q, want %q", k, a.meta[k], v)
		}
	}
	if n := len(retireAudits(t, ctx, pg, fresh.ID)); n != 0 {
		t.Errorf("a create that retired nothing wrote %d retire rows for the new token", n)
	}
}

// TestAPITokenRetireRollsBackWithTheCreateIntegration (#1768): the retire and
// its audit row belong to the create's transaction. A deferred constraint
// trigger makes the commit fail after both are written, and the expired token
// is left unrevoked with no retire row, so neither was written outside the
// transaction.
func TestAPITokenRetireRollsBackWithTheCreateIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg := openPG(t)
	userID, _ := apiTokenOwner(t, ctx, repo, "default", "deployer")
	name := fmt.Sprintf("rollback-%d", time.Now().UnixNano())
	_, old := newStoredToken(t, ctx, repo, "default", userID, name)
	expireToken(t, ctx, pg, old.ID)
	failCommitOnTokenName(t, ctx, pg, name)

	_, h, _ := auth.NewAPIToken()
	scopes, _ := auth.APITokenScopes("read")
	if _, err := repo.CreateAPIToken(ctx, "default", userID, name, h, scopes, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("create succeeded although its commit was made to fail")
	}
	var revoked *time.Time
	if err := pg.Pool.QueryRow(ctx, `SELECT revoked_at FROM api_tokens WHERE id = $1`, old.ID).Scan(&revoked); err != nil || revoked != nil {
		t.Errorf("expired token revoked_at = %v (%v) after a failed create, want NULL", revoked, err)
	}
	if n := len(retireAudits(t, ctx, pg, old.ID)); n != 0 {
		t.Errorf("a failed create left %d retire audit rows", n)
	}
}

// failCommitOnTokenName installs, for the test's lifetime, a deferred
// constraint trigger that fails the commit of any transaction inserting an
// API token named name. Only that name is affected, so other tests are not.
func failCommitOnTokenName(t *testing.T, ctx context.Context, pg *storage.Postgres, name string) {
	t.Helper()
	fn := fmt.Sprintf("test_fail_token_commit_%d", time.Now().UnixNano())
	stmts := []string{
		`CREATE FUNCTION ` + fn + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'commit refused by test'; END $$`,
		`CREATE CONSTRAINT TRIGGER ` + fn + ` AFTER INSERT ON api_tokens DEFERRABLE INITIALLY DEFERRED
			FOR EACH ROW WHEN (NEW.name = '` + name + `') EXECUTE FUNCTION ` + fn + `()`,
	}
	for _, stmt := range stmts {
		if _, err := pg.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("installing the commit-failing trigger: %v", err)
		}
	}
	t.Cleanup(func() {
		if _, err := pg.Pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS `+fn+` ON api_tokens`); err != nil {
			t.Errorf("dropping trigger %s: %v", fn, err)
		}
		if _, err := pg.Pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+fn+`()`); err != nil {
			t.Errorf("dropping function %s: %v", fn, err)
		}
	})
}

// TestAPITokenCapHoldsUnderConcurrencyIntegration: concurrent creates cannot
// pass the live-token cap together.
func TestAPITokenCapHoldsUnderConcurrencyIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg := openPG(t)
	userID, _ := apiTokenOwner(t, ctx, repo, "default", "deployer")
	scopes, _ := auth.APITokenScopes("read")
	exp := time.Now().Add(time.Hour)
	for i := 0; i < storage.MaxLiveAPITokensPerUser-2; i++ {
		_, h, _ := auth.NewAPIToken()
		if _, err := repo.CreateAPIToken(ctx, "default", userID, fmt.Sprintf("pre%d", i), h, scopes, exp); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, refused := 0, 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, h, _ := auth.NewAPIToken()
			_, err := repo.CreateAPIToken(ctx, "default", userID, fmt.Sprintf("race%d", i), h, scopes, exp)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, domain.ErrValidation):
				refused++
			default:
				t.Errorf("create %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	var live int
	if err := pg.Pool.QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now()`, userID).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if created != 2 || refused != 8 || live != storage.MaxLiveAPITokensPerUser {
		t.Errorf("created %d refused %d live %d, want 2, 8 and %d", created, refused, live, storage.MaxLiveAPITokensPerUser)
	}
}

// TestRevokeIsOnceAndNamesTheOwnerIntegration: a revoke returns the token's
// owner, and revoking a revoked token is not found, so it is not done (or
// audited) twice.
func TestRevokeIsOnceAndNamesTheOwnerIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	userID, _ := apiTokenOwner(t, ctx, repo, "default", "deployer")
	_, tok := newStoredToken(t, ctx, repo, "default", userID, "once")
	got, err := repo.RevokeAPIToken(ctx, "default", tok.ID, "")
	if err != nil || got.UserID != userID || got.Name != "once" {
		t.Fatalf("revoke = %+v, %v; want the token with its owner", got, err)
	}
	if _, err := repo.RevokeAPIToken(ctx, "default", tok.ID, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second revoke = %v, want ErrNotFound", err)
	}
}

// TestAPITokenCreateSerializesOnTheOwnerIntegration pins why the cap holds:
// a create waits while another transaction holds the owner's row, as a
// concurrent create does between its count and its insert.
func TestAPITokenCreateSerializesOnTheOwnerIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg := openPG(t)
	userID, _ := apiTokenOwner(t, ctx, repo, "default", "deployer")
	holder, err := pg.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT id FROM users WHERE id = $1 FOR NO KEY UPDATE`, userID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, h, _ := auth.NewAPIToken()
		scopes, _ := auth.APITokenScopes("read")
		_, cerr := repo.CreateAPIToken(ctx, "default", userID, "serial", h, scopes, time.Now().Add(time.Hour))
		done <- cerr
	}()
	select {
	case cerr := <-done:
		t.Fatalf("create finished (%v) while another transaction held the owner", cerr)
	case <-time.After(300 * time.Millisecond):
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case cerr := <-done:
		if cerr != nil {
			t.Errorf("create after the holder committed: %v", cerr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("create still blocked after the holder committed")
	}
}
