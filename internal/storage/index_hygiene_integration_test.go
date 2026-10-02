//go:build integration

package storage_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

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
