//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/migrator"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/tenantctx"
)

// Row level security as a second tenant barrier (ADR 0104, #2180). Postgres
// exempts superusers and BYPASSRLS roles from every policy, so these tests
// connect as a role of their own, the way a production server does.

type rlsFixture struct {
	admin    *storage.Postgres // the test DSN's role, a superuser in CI
	adminURL string
	appURL   string // a role without superuser or BYPASSRLS
	a, b     string // two tenants with one DAG each
}

func newRLSFixture(t *testing.T) rlsFixture {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	admin, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: dsn})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(admin.Close)
	f := rlsFixture{admin: admin, adminURL: dsn, a: uniqueTenant("rls-a"), b: uniqueTenant("rls-b")}
	for _, tn := range []string{f.a, f.b} {
		if _, err := admin.Pool.Exec(ctx, `
WITH t AS (INSERT INTO tenants (name, display_name) VALUES ($1, $1) RETURNING id)
INSERT INTO dags (tenant_id, dag_id) SELECT id, 'rls_dag' FROM t`, tn); err != nil {
			t.Fatalf("seeding tenant %s: %v", tn, err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.Pool.Exec(context.Background(), `DELETE FROM dags WHERE tenant_id IN (SELECT id FROM tenants WHERE name = ANY($1))`, []string{f.a, f.b})
		_, _ = admin.Pool.Exec(context.Background(), `DELETE FROM tenants WHERE name = ANY($1)`, []string{f.a, f.b})
	})

	role := fmt.Sprintf("dexaflow_rls_%d", time.Now().UnixNano())
	for _, stmt := range []string{
		`CREATE ROLE ` + role + ` LOGIN PASSWORD 'rls' NOSUPERUSER NOBYPASSRLS`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ` + role,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + role,
	} {
		if _, err := admin.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = admin.Pool.Exec(c, `DROP OWNED BY `+role)
		_, _ = admin.Pool.Exec(c, `DROP ROLE `+role)
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "rls")
	f.appURL = u.String()
	return f
}

// rls runs `dexaflow-migrate rls <action>` against the test database.
func (f rlsFixture) rls(t *testing.T, action string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := migrator.Main(context.Background(), []string{"-database", f.adminURL, "rls", action}, &out, &errOut, "test"); code != 0 {
		t.Fatalf("rls %s exited %d: %s", action, code, errOut.String())
	}
	return out.String()
}

// appPool opens the main pool as the application role, with tenant binding
// on, and at most one connection, so every query reuses the same one and a
// binding left behind would show.
func (f rlsFixture) appPool(t *testing.T) *storage.Postgres {
	t.Helper()
	pg, err := storage.NewPostgres(context.Background(), config.DatabaseSection{URL: f.appURL, MaxOpenConns: 1, RowLevelSecurity: true})
	if err != nil {
		t.Fatalf("connect as the app role: %v", err)
	}
	t.Cleanup(pg.Close)
	return pg
}

func countDAGs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenants ...string) int {
	t.Helper()
	var n int
	// No tenant predicate on purpose: the query a future handler forgets to
	// scope. Only the fixture's tenants are counted, so other tests' rows do
	// not matter.
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM dags d JOIN tenants t ON t.id = d.tenant_id WHERE t.name = ANY($1)`, tenants).Scan(&n); err != nil {
		t.Fatalf("counting dags: %v", err)
	}
	return n
}

func TestRowSecurityHidesOtherTenantsFromABoundConnection(t *testing.T) {
	f := newRLSFixture(t)
	if out := f.rls(t, "on"); !strings.Contains(out, "row level security: on") {
		t.Fatalf("rls on printed %q", out)
	}
	t.Cleanup(func() { f.rls(t, "off") })
	pg := f.appPool(t)
	bg := context.Background()
	both := []string{f.a, f.b}

	if n := countDAGs(t, tenantctx.With(bg, f.a), pg.Pool, both...); n != 1 {
		t.Fatalf("bound to %s, an unscoped query saw %d DAGs, want only its own 1", f.a, n)
	}
	// Same connection (MaxOpenConns 1): the unbound caller must not inherit
	// the last binding, and a second tenant must replace it.
	if n := countDAGs(t, bg, pg.Pool, both...); n != 2 {
		t.Fatalf("unbound, saw %d DAGs, want both tenants' 2", n)
	}
	if n := countDAGs(t, tenantctx.With(bg, f.b), pg.Pool, both...); n != 1 {
		t.Fatalf("bound to %s, saw %d DAGs, want 1", f.b, n)
	}
	if n := countDAGs(t, tenantctx.With(bg, "no-such-tenant"), pg.Pool, both...); n != 0 {
		t.Fatalf("bound to a tenant that does not exist, saw %d DAGs, want none", n)
	}
	// Transactions acquire their connection the same way.
	tx, err := pg.Pool.Begin(tenantctx.With(bg, f.a))
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(bg, `SELECT count(*) FROM dags d JOIN tenants t ON t.id = d.tenant_id WHERE t.name = ANY($1)`, both).Scan(&n); err != nil || n != 1 {
		t.Fatalf("in a bound transaction: n=%d err=%v, want 1", n, err)
	}
	_ = tx.Rollback(bg)
}

func TestRowSecurityRefusesAWriteIntoAnotherTenant(t *testing.T) {
	f := newRLSFixture(t)
	f.rls(t, "on")
	t.Cleanup(func() { f.rls(t, "off") })
	pg := f.appPool(t)
	ctx := tenantctx.With(context.Background(), f.a)
	_, err := pg.Pool.Exec(ctx, `INSERT INTO dags (tenant_id, dag_id) SELECT id, 'smuggled' FROM tenants WHERE name = $1`, f.b)
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Fatalf("a write into %s from a connection bound to %s: err = %v, want a row level security refusal", f.b, f.a, err)
	}
	tag, err := pg.Pool.Exec(ctx, `UPDATE dags SET description = 'x' WHERE tenant_id = (SELECT id FROM tenants WHERE name = $1)`, f.b)
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("an update of %s's rows: affected=%d err=%v, want none", f.b, tag.RowsAffected(), err)
	}
}

func TestRowSecurityOffChangesNothing(t *testing.T) {
	f := newRLSFixture(t)
	if out := f.rls(t, "status"); !strings.Contains(out, "row level security: off") {
		t.Fatalf("a migrated database must start with row level security off, status printed %q", out)
	}
	pg := f.appPool(t)
	if n := countDAGs(t, tenantctx.With(context.Background(), f.a), pg.Pool, f.a, f.b); n != 2 {
		t.Fatalf("with the tables not enforcing, a bound connection saw %d DAGs, want 2", n)
	}
}

func TestCheckRowSecurity(t *testing.T) {
	f := newRLSFixture(t)
	pg := f.appPool(t)
	ctx := context.Background()
	if err := pg.CheckRowSecurity(ctx); !errors.Is(err, storage.ErrRowSecurityOff) || !strings.Contains(err.Error(), "dags") {
		t.Fatalf("tables off: err = %v, want ErrRowSecurityOff naming the tables", err)
	}
	f.rls(t, "on")
	t.Cleanup(func() { f.rls(t, "off") })
	if err := pg.CheckRowSecurity(ctx); err != nil {
		t.Fatalf("tables on, app role: %v", err)
	}
	// The test DSN's own role is a superuser in CI, which no policy holds.
	var super bool
	if err := f.admin.Pool.QueryRow(ctx, `SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&super); err != nil {
		t.Fatal(err)
	}
	if super {
		if err := f.admin.CheckRowSecurity(ctx); !errors.Is(err, storage.ErrRowSecurityOff) || !strings.Contains(err.Error(), "BYPASSRLS") {
			t.Fatalf("superuser: err = %v, want a refusal naming BYPASSRLS", err)
		}
	}
}
