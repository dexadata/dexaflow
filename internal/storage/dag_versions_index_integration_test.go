//go:build integration

package storage_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// dagVersionIndexDefs returns the definition of every index on dag_versions,
// keyed by index name.
func dagVersionIndexDefs(t *testing.T) map[string]string {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	rows, err := pool.Query(ctx, `SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'dag_versions'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	defs := map[string]string{}
	for rows.Next() {
		var name, def string
		if err := rows.Scan(&name, &def); err != nil {
			t.Fatal(err)
		}
		defs[name] = def
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return defs
}

// TestDagVersionHashLookupIsIndexedPerDag guards GetDagVersionByHash, which
// registration calls for every bundle push with WHERE dag_id = $1 AND
// spec_hash = $2: its index must lead with both columns, and the old
// spec_hash-only index, which matched identical specs across every DAG, must
// be gone.
func TestDagVersionHashLookupIsIndexedPerDag(t *testing.T) {
	defs := dagVersionIndexDefs(t)
	found := false
	for name, def := range defs {
		if strings.HasSuffix(def, "USING btree (dag_id, spec_hash)") {
			found = true
		}
		if strings.HasSuffix(def, "USING btree (spec_hash)") {
			t.Errorf("%s indexes spec_hash alone: %s", name, def)
		}
	}
	if !found {
		t.Errorf("no btree index on dag_versions (dag_id, spec_hash); have %v", defs)
	}
}

// TestDagVersionSpecHasNoGinIndex guards version registration cost: no query
// filters dag_versions.spec by JSONB containment or key existence, so a GIN
// index on the whole spec document is pure write and storage overhead on
// every version insert.
func TestDagVersionSpecHasNoGinIndex(t *testing.T) {
	for name, def := range dagVersionIndexDefs(t) {
		if strings.Contains(def, "USING gin") {
			t.Errorf("%s is a GIN index no query uses: %s", name, def)
		}
	}
}
