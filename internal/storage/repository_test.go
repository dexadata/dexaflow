package storage

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestMapConflict: a Postgres unique-constraint violation (SQLSTATE 23505) maps to
// domain.ErrConflict (so the API returns 409 for a duplicate run); other errors,
// including a different SQLSTATE, pass through unchanged.
func TestMapConflict(t *testing.T) {
	unique := &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}
	if got := mapConflict(unique); !errors.Is(got, domain.ErrConflict) {
		t.Errorf("23505 must map to ErrConflict, got %v", got)
	}
	if got := mapConflict(fmt.Errorf("wrapped: %w", unique)); !errors.Is(got, domain.ErrConflict) {
		t.Errorf("wrapped 23505 must map to ErrConflict, got %v", got)
	}
	other := &pgconn.PgError{Code: "23503", Message: "foreign key violation"}
	if got := mapConflict(other); errors.Is(got, domain.ErrConflict) {
		t.Errorf("a non-unique SQLSTATE must NOT map to ErrConflict, got %v", got)
	}
	plain := errors.New("connection refused")
	if got := mapConflict(plain); errors.Is(got, domain.ErrConflict) {
		t.Errorf("a non-pg error must not map to ErrConflict, got %v", got)
	}
}

// TestIsTenantFKViolation: Postgres SQLSTATE 23503 referencing tenants is detected
// so stale tenant cache entries can be invalidated and write operations retried.
func TestIsTenantFKViolation(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "fk on constraint name",
			err:  &pgconn.PgError{Code: "23503", ConstraintName: "variables_tenant_id_fkey"},
			want: true,
		},
		{
			name: "fk on another table's detail",
			err:  &pgconn.PgError{Code: "23503", Detail: `Key (dag_id)=(x) is not present in table "dags".`},
			want: false,
		},
		{
			name: "fk on detail",
			err:  &pgconn.PgError{Code: "23503", Detail: `Key (tenant_id)=(...) is not present in table "tenants".`},
			want: true,
		},
		{
			name: "wrapped fk on tenant",
			err:  fmt.Errorf("upserting: %w", &pgconn.PgError{Code: "23503", ConstraintName: "connections_tenant_id_fkey"}),
			want: true,
		},
		{
			name: "unrelated fk",
			err:  &pgconn.PgError{Code: "23503", ConstraintName: "dag_runs_dag_id_fkey"},
			want: false,
		},
		{
			name: "unique violation on tenant",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "tenants_name_key"},
			want: false,
		},
		{
			name: "plain error",
			err:  errors.New("timeout"),
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTenantFKViolation(tc.err); got != tc.want {
				t.Errorf("isTenantFKViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
