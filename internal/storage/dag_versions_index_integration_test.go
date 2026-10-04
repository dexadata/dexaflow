//go:build integration

package storage_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/migrations"
	"github.com/jackc/pgx/v5/pgconn"
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

// TestDagVersionsIndexBuildsFailOverInvalidIndex replays each migration that
// builds a dag_versions index CONCURRENTLY against the state an interrupted
// build leaves behind: an INVALID index with the final name. The retry must
// fail with duplicate_table instead of reporting success and keeping the
// invalid index.
func TestDagVersionsIndexBuildsFailOverInvalidIndex(t *testing.T) {
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
	const schema = "dexaflow_probe_dag_versions_index"
	cases := []struct{ file, index string }{
		{"032_dag_versions_dag_hash_index.up.sql", "idx_dag_versions_dag_hash"},
		{"033_drop_dag_versions_hash_index.down.sql", "idx_dag_versions_hash"},
		{"034_drop_dag_versions_spec_gin_index.down.sql", "idx_dag_versions_spec"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE; CREATE SCHEMA "+schema); err != nil {
				t.Skipf("creating a scratch schema needs CREATE on the database: %v", err)
			}
			t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
			cfg := pool.Config().Copy()
			cfg.ConnConfig.RuntimeParams["search_path"] = schema
			scratch, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer scratch.Close()

			// A unique build over duplicate keys fails the way an interrupted
			// one does, leaving an INVALID index with the final name.
			if _, err := scratch.Exec(ctx, "CREATE TABLE dag_versions (k int, dag_id text, spec_hash text, spec jsonb); INSERT INTO dag_versions (k) VALUES (1), (1)"); err != nil {
				t.Fatal(err)
			}
			if _, err := scratch.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY "+tc.index+" ON dag_versions (k)"); err == nil {
				t.Fatal("precondition: the unique build over duplicate keys must fail")
			}
			body, err := fs.ReadFile(migrations.Files, tc.file)
			if err != nil {
				t.Fatal(err)
			}
			_, err = scratch.Exec(ctx, string(body))
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42P07" {
				t.Errorf("retrying %s over an INVALID %s: err = %v, want duplicate_table (42P07)", tc.file, tc.index, err)
			}
		})
	}
}
