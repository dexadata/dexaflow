//go:build integration

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// queryCounter is a pgx tracer that counts the statements sent to Postgres.
type queryCounter struct{ n atomic.Int64 }

func (q *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	q.n.Add(1)
	return ctx
}

func (q *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// countingRepo opens a Repository whose pool counts every query it sends.
func countingRepo(t *testing.T) (*Repository, *queryCounter, context.Context) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	counter := &queryCounter{}
	pc.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return NewRepository(&Postgres{Pool: pool, Queries: queries.New(pool)}), counter, ctx
}

// TestFindUserByIDIsOneQueryIntegration pins the per-request principal reload
// at one round trip: the user, its tenant, its roles and its permissions come
// back from a single statement, equal to what the three separate queries
// returned.
func TestFindUserByIDIsOneQueryIntegration(t *testing.T) {
	repo, counter, ctx := countingRepo(t)
	email := fmt.Sprintf("principal_%d@example.com", time.Now().UnixNano())
	created, err := repo.CreateUser(ctx, "default", email, "principal-secret-1", []string{"viewer", "operator"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	counter.n.Store(0)
	user, active, err := repo.FindUserByID(ctx, created.ID)
	if err != nil || !active {
		t.Fatalf("FindUserByID: active=%v err=%v", active, err)
	}
	if n := counter.n.Load(); n != 1 {
		t.Errorf("FindUserByID sent %d queries, want 1", n)
	}

	uid, _ := parseUUID(created.ID)
	wantRoles, err := repo.q.GetUserRoles(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	wantPerms, err := repo.q.GetUserPermissions(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	gotRoles := slices.Clone(user.Roles)
	slices.Sort(gotRoles)
	slices.Sort(wantRoles)
	if !slices.Equal(gotRoles, wantRoles) {
		t.Errorf("roles = %v, want %v", gotRoles, wantRoles)
	}
	if len(user.Permissions) != len(wantPerms) || len(wantPerms) == 0 {
		t.Fatalf("permissions = %d, want %d (non-zero)", len(user.Permissions), len(wantPerms))
	}
	for _, p := range wantPerms {
		if !slices.Contains(user.Permissions, auth.Permission{Action: p.Action, Resource: p.Resource}) {
			t.Errorf("permission %s:%s missing", p.Action, p.Resource)
		}
	}
	if user.TenantID != "default" || user.Email != email {
		t.Errorf("principal = %+v", user)
	}
}

// TestFindUserByIDUserWithoutRolesIntegration pins that a user with no role
// still loads, with empty roles and permissions rather than an error.
func TestFindUserByIDUserWithoutRolesIntegration(t *testing.T) {
	repo, _, ctx := countingRepo(t)
	email := fmt.Sprintf("noroles_%d@example.com", time.Now().UnixNano())
	created, err := repo.CreateUser(ctx, "default", email, "noroles-secret-1", nil)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	user, active, err := repo.FindUserByID(ctx, created.ID)
	if err != nil || !active {
		t.Fatalf("FindUserByID: active=%v err=%v", active, err)
	}
	if len(user.Roles) != 0 || len(user.Permissions) != 0 {
		t.Errorf("roles=%v permissions=%v, want none", user.Roles, user.Permissions)
	}
}

// TestTenantIDIsResolvedOnceIntegration pins the tenant cache: a tenant's id
// never changes, so its name is looked up once per process, while an unknown
// name is never cached (the tenant may be created later).
func TestTenantIDIsResolvedOnceIntegration(t *testing.T) {
	repo, counter, ctx := countingRepo(t)
	counter.n.Store(0)
	for range 3 {
		if _, err := repo.tenantID(ctx, "default"); err != nil {
			t.Fatal(err)
		}
	}
	if n := counter.n.Load(); n != 1 {
		t.Errorf("three lookups of one tenant sent %d queries, want 1", n)
	}

	counter.n.Store(0)
	for range 2 {
		if _, err := repo.tenantID(ctx, "no-such-tenant"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown tenant err = %v, want ErrNotFound", err)
		}
	}
	if n := counter.n.Load(); n != 2 {
		t.Errorf("two lookups of an unknown tenant sent %d queries, want 2 (not cached)", n)
	}
}
