package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/tenantctx"
	"github.com/dexadata/dexaflow/migrations"
)

// Row level security as a second tenant barrier (ADR 0104, #2180).
//
// The policies of migration 059 limit a connection bound to a tenant to that
// tenant's rows, and let a connection that is not bound through. With
// database.row_level_security on, the main pool binds each connection it hands
// out to the tenant the caller's context carries (tenantctx.With), and unbinds one
// handed to a caller without a tenant. Only a change costs a round trip: the
// binding a connection holds is remembered on the connection itself.

// boundTenantData is the key, in a connection's custom data, of the tenant
// name it is bound to ("" or absent for none).
const boundTenantData = "dexaflow.bound_tenant"

// nilTenant is what a tenant name that does not exist binds: the nil UUID
// matches no row, so a typo or a deleted tenant sees nothing.
const nilTenant = "00000000-0000-0000-0000-000000000000"

// bindTenant is the main pool's PrepareConn when row level security is on: it
// makes the connection's binding match the caller's context before the
// caller gets it. An error destroys the connection, so one whose binding is
// unknown never goes back to the pool.
func bindTenant(ctx context.Context, conn *pgx.Conn) (bool, error) {
	want := tenantctx.From(ctx)
	data := conn.PgConn().CustomData()
	if have, ok := data[boundTenantData].(string); ok && have == want {
		return true, nil
	}
	var err error
	if want == "" {
		_, err = conn.Exec(ctx, "SELECT set_config('dexaflow.tenant_id', '', false)")
	} else {
		_, err = conn.Exec(ctx,
			"SELECT set_config('dexaflow.tenant_id', COALESCE((SELECT id::text FROM tenants WHERE name = $1), $2), false)",
			want, nilTenant)
	}
	if err != nil {
		delete(data, boundTenantData)
		return false, fmt.Errorf("binding the connection to the request's tenant: %w", err)
	}
	data[boundTenantData] = want
	return true, nil
}

// enableTenantBinding installs bindTenant on a pool config.
func enableTenantBinding(pc *pgxpool.Config) {
	pc.PrepareConn = bindTenant
}

// ErrRowSecurityOff is returned by CheckRowSecurity when a tenant table does
// not have row level security on and forced.
var ErrRowSecurityOff = errors.New("row level security is not enforced")

// RowSecurityStatus reports, per tenant table, whether row level security is
// enabled and forced, and whether its tenant policy exists.
type RowSecurityStatus struct {
	Table   string
	Enabled bool
	Forced  bool
	Policy  bool
}

// On reports whether the table is protected: enabled, forced (so the table
// owner, which is usually the server's own role, is held to it too) and with
// its policy.
func (s RowSecurityStatus) On() bool { return s.Enabled && s.Forced && s.Policy }

// queryer is the read half of a pool, a connection or a transaction.
type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ReadRowSecurity reads the row level security state of the tenant tables.
func ReadRowSecurity(ctx context.Context, db queryer) ([]RowSecurityStatus, error) {
	rows, err := db.Query(ctx, `
SELECT c.relname, c.relrowsecurity, c.relforcerowsecurity,
       EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid AND p.polname = $2)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relkind = 'r' AND c.relname = ANY($1)
ORDER BY c.relname`, migrations.RowSecurityTables, migrations.RowSecurityPolicy)
	if err != nil {
		return nil, fmt.Errorf("reading row level security state: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (RowSecurityStatus, error) {
		var s RowSecurityStatus
		return s, r.Scan(&s.Table, &s.Enabled, &s.Forced, &s.Policy)
	})
	if err != nil {
		return nil, fmt.Errorf("reading row level security state: %w", err)
	}
	return out, nil
}

// CheckRowSecurity returns ErrRowSecurityOff, naming the tables, unless every
// tenant table has row level security on, forced and with its policy.
func (p *Postgres) CheckRowSecurity(ctx context.Context) error {
	var role string
	var bypass bool
	if err := p.probePool().QueryRow(ctx,
		"SELECT current_user, rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user").Scan(&role, &bypass); err != nil {
		return fmt.Errorf("reading the server's database role: %w", err)
	}
	if bypass {
		return fmt.Errorf("%w: the server connects as %q, a superuser or BYPASSRLS role, which no policy holds; connect as a role without either, or turn database.row_level_security off", ErrRowSecurityOff, role)
	}
	st, err := ReadRowSecurity(ctx, p.probePool())
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(st))
	var off []string
	for _, s := range st {
		seen[s.Table] = true
		if !s.On() {
			off = append(off, s.Table)
		}
	}
	for _, t := range migrations.RowSecurityTables {
		if !seen[t] {
			off = append(off, t)
		}
	}
	if len(off) > 0 {
		return fmt.Errorf("%w (%v): run `dexaflow-migrate rls on` against this database, or turn database.row_level_security off", ErrRowSecurityOff, off)
	}
	return nil
}
