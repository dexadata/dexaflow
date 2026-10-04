//go:build integration

package storage_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// tableIndex is one index of a table as the catalog describes it.
type tableIndex struct {
	name    string
	unique  bool
	partial bool
	columns []string // key columns in order; an expression column is ""
	def     string
}

func openSchemaPool(t *testing.T) (*pgxpool.Pool, context.Context) {
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
	return pool, ctx
}

func indexesOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) []tableIndex {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT c.relname, i.indisunique, i.indpred IS NOT NULL, pg_get_indexdef(i.indexrelid),
       ARRAY(SELECT coalesce(a.attname, '')
             FROM unnest(i.indkey[0:i.indnkeyatts - 1]) WITH ORDINALITY k(attnum, ord)
             LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
             ORDER BY k.ord)
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
WHERE i.indrelid = $1::regclass`, table)
	if err != nil {
		t.Fatalf("list indexes of %s: %v", table, err)
	}
	defer rows.Close()
	var out []tableIndex
	for rows.Next() {
		var ix tableIndex
		if err := rows.Scan(&ix.name, &ix.unique, &ix.partial, &ix.def, &ix.columns); err != nil {
			t.Fatal(err)
		}
		out = append(out, ix)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestDagRunsVersionForeignKeyIsIndexed guards DAG deletion: deleting a DAG
// cascades to its versions, and every deleted version makes the
// dag_runs.dag_version_id foreign key check look for runs still pointing at
// it. Without an index leading with dag_version_id that check scans dag_runs
// once per version (4.8 s for a DAG with 51 versions at 1M runs).
func TestDagRunsVersionForeignKeyIsIndexed(t *testing.T) {
	pool, ctx := openSchemaPool(t)
	for _, ix := range indexesOf(t, ctx, pool, "dag_runs") {
		if !ix.partial && len(ix.columns) > 0 && ix.columns[0] == "dag_version_id" {
			return
		}
	}
	t.Error("dag_runs has no index leading with dag_version_id")
}

// TestTaskInstanceIndexesAreNotRedundant guards write cost on the hottest
// table: a plain index whose key columns are a leading prefix of another
// full index's key columns serves no lookup the longer one cannot, and every
// insert and non-HOT update pays to maintain it.
func TestTaskInstanceIndexesAreNotRedundant(t *testing.T) {
	pool, ctx := openSchemaPool(t)
	indexes := indexesOf(t, ctx, pool, "task_instances")
	for _, short := range indexes {
		if short.unique || short.partial || slices.Contains(short.columns, "") {
			continue
		}
		for _, long := range indexes {
			if long.name == short.name || long.partial || len(long.columns) < len(short.columns) {
				continue
			}
			if slices.Equal(long.columns[:len(short.columns)], short.columns) {
				t.Errorf("%s (%s) is a prefix of %s (%s)", short.name, strings.Join(short.columns, ", "),
					long.name, strings.Join(long.columns, ", "))
			}
		}
	}
}

// TestHeartbeatColumnIsNotIndexed guards RecordTaskHeartbeat, the most
// frequent write in the system (every running task, every few seconds). An
// update can be HOT, rewriting no index entry, only when no index references
// a column it changes. The agent-lost reaper that reads last_heartbeat_at
// finds its rows through the partial idx_ti_state instead.
func TestHeartbeatColumnIsNotIndexed(t *testing.T) {
	pool, ctx := openSchemaPool(t)
	for _, ix := range indexesOf(t, ctx, pool, "task_instances") {
		if strings.Contains(ix.def, "last_heartbeat_at") {
			t.Errorf("%s references last_heartbeat_at, so heartbeat updates cannot be HOT: %s", ix.name, ix.def)
		}
	}
}

// TestTaskInstancesLeavesRoomForHOTUpdates guards the other half of HOT: the
// new row version must fit on the same heap page, so task_instances keeps 15%
// of each page free for the state and heartbeat updates every row receives.
func TestTaskInstancesLeavesRoomForHOTUpdates(t *testing.T) {
	pool, ctx := openSchemaPool(t)
	var opts []string
	if err := pool.QueryRow(ctx, `SELECT coalesce(reloptions, '{}') FROM pg_class WHERE oid = 'task_instances'::regclass`).Scan(&opts); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(opts, "fillfactor=85") {
		t.Errorf("task_instances reloptions = %v, want fillfactor=85", opts)
	}
}

// TestNoInvalidIndexesAfterMigrating guards the migrated schema itself: an
// interrupted CREATE INDEX CONCURRENTLY leaves an INVALID index that the
// planner ignores but every write still maintains.
func TestNoInvalidIndexesAfterMigrating(t *testing.T) {
	pool, ctx := openSchemaPool(t)
	rows, err := pool.Query(ctx, `
SELECT c.relname
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
WHERE NOT i.indisvalid AND c.relnamespace = current_schema()::regnamespace`)
	if err != nil {
		t.Fatal(err)
	}
	invalid, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(invalid) > 0 {
		t.Errorf("invalid indexes after migrating: %v", invalid)
	}
}

// TestConcurrentIndexMigrationsFailOverInvalidIndex replays each migration
// that builds an index CONCURRENTLY against the state an interrupted build
// leaves behind: an INVALID index with the final name. The retry must fail
// with duplicate_table instead of reporting success and keeping the invalid
// index.
func TestConcurrentIndexMigrationsFailOverInvalidIndex(t *testing.T) {
	cases := []struct {
		file, index, table, columns string
	}{
		{"027_dag_runs_version_index.up.sql", "idx_dag_runs_version", "dag_runs", "dag_version_id bigint"},
		{"028_drop_ti_run_index.down.sql", "idx_ti_run", "task_instances", "dag_run_id bigint"},
		{"029_drop_ti_task_index.down.sql", "idx_ti_task", "task_instances", "dag_run_id bigint, task_id text"},
		{"030_drop_ti_running_heartbeat_index.down.sql", "idx_ti_running_heartbeat", "task_instances", "last_heartbeat_at timestamptz, state text"},
	}
	pool, ctx := openSchemaPool(t)
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			err := retryOverInvalidIndex(t, ctx, pool, tc.file, tc.index, tc.table, tc.columns)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42P07" {
				t.Errorf("retrying %s over an INVALID %s: err = %v, want duplicate_table (42P07)", tc.file, tc.index, err)
			}
		})
	}
}

// retryOverInvalidIndex builds table in a scratch schema, leaves an INVALID
// index named index on it (a unique build over duplicate keys fails the same
// way an interrupted one does), then applies the migration file there and
// returns its error.
func retryOverInvalidIndex(t *testing.T, ctx context.Context, pool *pgxpool.Pool, file, index, table, columns string) error {
	t.Helper()
	const schema = "dexaflow_probe_invalid_index"
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

	if _, err := scratch.Exec(ctx, "CREATE TABLE "+table+" (k int, "+columns+"); INSERT INTO "+table+" (k) VALUES (1), (1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := scratch.Exec(ctx, "CREATE UNIQUE INDEX CONCURRENTLY "+index+" ON "+table+" (k)"); err == nil {
		t.Fatal("precondition: the unique build over duplicate keys must fail")
	}
	var valid bool
	if err := scratch.QueryRow(ctx, "SELECT indisvalid FROM pg_index WHERE indexrelid = $1::regclass", index).Scan(&valid); err != nil || valid {
		t.Fatalf("precondition: want an INVALID %s, got valid=%v err=%v", index, valid, err)
	}
	body, err := fs.ReadFile(migrations.Files, file)
	if err != nil {
		t.Fatal(err)
	}
	_, err = scratch.Exec(ctx, string(body))
	return err
}
