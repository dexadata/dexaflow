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
