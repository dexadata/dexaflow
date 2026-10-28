package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// registerDB is a queries.DBTX double for registerDagVersionTx. The DAG row
// lock answers no row for the first missingLocks calls, as when a DeleteDag
// commits between EnsureDag and the lock. Every EnsureDag is counted.
type registerDB struct {
	missingLocks int
	locks        int
	ensures      int
}

var errStopAtInsert = errors.New("stop at the version insert")

func (f *registerDB) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	if strings.Contains(sql, "INSERT INTO dags") {
		f.ensures++
	}
	return pgconn.CommandTag{}, nil
}

func (f *registerDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "FOR NO KEY UPDATE"):
		f.locks++
		if f.locks <= f.missingLocks {
			return fakeRow{func(...any) error { return pgx.ErrNoRows }}
		}
		return fakeRow{func(...any) error { return nil }}
	case strings.Contains(sql, "spec_hash = $2"):
		return fakeRow{func(...any) error { return pgx.ErrNoRows }}
	case strings.Contains(sql, "INSERT INTO dag_versions"):
		return fakeRow{func(...any) error { return errStopAtInsert }}
	}
	return fakeRow{func(...any) error { return errors.New("unexpected QueryRow: " + sql) }}
}

func (f *registerDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (f *registerDB) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("unexpected CopyFrom")
}

func registerRetrySpec() domain.DAGSpec {
	return domain.DAGSpec{SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "img:v1"}
}

// A DAG deleted between EnsureDag and the row lock is created again once and
// the register goes on (#1702).
func TestRegisterDagVersionTxRetriesALockLostToADelete(t *testing.T) {
	db := &registerDB{missingLocks: 1}
	_, err := registerDagVersionTx(context.Background(), queries.New(db), validUUID(), registration{spec: registerRetrySpec(), specHash: "h", specJSON: []byte("{}")})
	if !errors.Is(err, errStopAtInsert) {
		t.Fatalf("after one lost lock the register must reach the version insert, got %v", err)
	}
	if db.ensures != 2 || db.locks != 2 {
		t.Fatalf("ensures=%d locks=%d, want 2 and 2", db.ensures, db.locks)
	}
}

// A DAG that vanishes twice in a row is a conflict with a clear message, not
// "locking dag: no rows" (#1702).
func TestRegisterDagVersionTxConflictsWhenTheDagKeepsVanishing(t *testing.T) {
	db := &registerDB{missingLocks: 2}
	_, err := registerDagVersionTx(context.Background(), queries.New(db), validUUID(), registration{spec: registerRetrySpec(), specHash: "h", specJSON: []byte("{}")})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("want domain.ErrConflict, got %v", err)
	}
	if strings.Contains(err.Error(), "no rows") || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("message must say the DAG was deleted, got %q", err)
	}
	if db.ensures != 2 {
		t.Fatalf("ensures=%d, want exactly one retry (2)", db.ensures)
	}
}
