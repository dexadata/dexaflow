package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// fakeConn is a white-box double that satisfies both queries.DBTX (for the
// non-transactional reads) and pgx.Tx (for the transactional writes). It lets
// CreateUser run its full path with no database: reads are answered from canned
// rows, the role-assignment Exec is forced to fail, and Commit/Rollback are
// recorded so the test can assert the whole thing rolled back.
type fakeConn struct {
	pgx.Tx // embedded so the fake satisfies the full pgx.Tx surface; only the
	//        methods overridden below are ever called.
	committed  bool
	rolledBack bool
}

func validUUID() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, Valid: true}
}

type fakeRow struct{ scan func(dest ...any) error }

func (r fakeRow) Scan(dest ...any) error { return r.scan(dest...) }

func (f *fakeConn) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "FROM tenants"):
		return fakeRow{func(d ...any) error {
			*(d[0].(*pgtype.UUID)) = validUUID()
			*(d[1].(*string)) = "default"
			return nil
		}}
	case strings.Contains(sql, "FROM roles"):
		return fakeRow{func(d ...any) error {
			*(d[0].(*pgtype.UUID)) = validUUID()
			return nil
		}}
	case strings.Contains(sql, "INSERT INTO users"):
		return fakeRow{func(d ...any) error {
			*(d[0].(*pgtype.UUID)) = validUUID()
			*(d[1].(*string)) = "alice@example.com"
			*(d[2].(*bool)) = true
			*(d[3].(*pgtype.Timestamptz)) = pgtype.Timestamptz{Time: time.Now(), Valid: true}
			return nil
		}}
	}
	return fakeRow{func(...any) error { return errors.New("unexpected QueryRow: " + sql) }}
}

func (f *fakeConn) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "user_roles") {
		return pgconn.CommandTag{}, errors.New("forced role-assignment failure")
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeConn) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (f *fakeConn) Commit(context.Context) error   { f.committed = true; return nil }
func (f *fakeConn) Rollback(context.Context) error { f.rolledBack = true; return nil }

type fakeBeginner struct{ tx pgx.Tx }

func (b fakeBeginner) Begin(context.Context) (pgx.Tx, error) { return b.tx, nil }

// TestCreateUserRollsBackWhenRoleAssignFails guards MEDIUM-1: the user insert and
// the role grant must be one atomic transaction. If the grant fails after the
// insert, the transaction must roll back (never commit) so no role-less account
// is left behind — otherwise the (tenant_id, email) UNIQUE would make every retry
// 409 forever with no recovery.
func TestCreateUserRollsBackWhenRoleAssignFails(t *testing.T) {
	fc := &fakeConn{}
	repo := &Repository{q: queries.New(fc), pool: fakeBeginner{tx: fc}}

	_, err := repo.CreateUser(context.Background(), "default", "alice@example.com", "pw-12345678", []string{"admin"})
	if err == nil {
		t.Fatal("expected an error when the role assignment fails")
	}
	if fc.committed {
		t.Error("transaction was committed despite the role-assignment failure")
	}
	if !fc.rolledBack {
		t.Error("transaction was not rolled back after the role-assignment failure")
	}
}
