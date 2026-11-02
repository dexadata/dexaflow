//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/migrations"
)

// roundTripFloor is the oldest version the down migrations are held to
// (#1616): every down from the head back to it must run, and must leave the
// schema exactly as the ups built it.
const roundTripFloor = 26

// irreversibleLeftovers are the changes a down migration documents as left in
// place: the schema line after the round trip, mapped to the line the floor
// had. Each comes from a down file that is a no-op on purpose
// (migrations/AGENTS.md, "Every up has a down").
var irreversibleLeftovers = map[string]string{
	// 047_removed_task_state.down.sql: PostgreSQL cannot drop an enum value.
	"enum task_state none,scheduled,queued,running,deferred,success,failed,skipped,upstream_failed,up_for_retry,up_for_reschedule,removed": "enum task_state none,scheduled,queued,running,deferred,success,failed,skipped,upstream_failed,up_for_retry,up_for_reschedule",
}

// TestMigrationsRoundTripIntegration migrates a scratch database up to
// roundTripFloor and snapshots its schema, goes up to the head, back down to
// the floor, compares the schema with the snapshot, and goes up again. A down
// file that fails, leaves a column, index, constraint or type behind, or drops
// one the floor had, fails here; so does an up that cannot be applied again
// after its down. Only TestAttemptEpochMigrationUpDownUp covered one migration
// this way before.
func TestMigrationsRoundTripIntegration(t *testing.T) {
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL must point at a Postgres server for integration tests")
	}
	ctx := context.Background()
	admin, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: raw})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(admin.Close)
	dbName := fmt.Sprintf("mig_roundtrip_%d", time.Now().UnixNano())
	if _, err := admin.Pool.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		skipOrFatal(t, "creating scratch database", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Pool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)"); err != nil {
			t.Errorf("dropping scratch database: %v", err)
		}
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("DATABASE_URL is not a URL: %v", err)
	}
	u.Path = "/" + dbName
	scratchURL := u.String()
	u.Scheme = "pgx5"
	src, err := iofs.New(migrations.Files, ".")
	if err != nil {
		t.Fatalf("loading migrations: %v", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, u.String())
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Migrate(roundTripFloor); err != nil {
		t.Fatalf("up to %d: %v", roundTripFloor, err)
	}
	before := schemaSnapshot(t, scratchURL)
	if err := m.Up(); err != nil {
		t.Fatalf("up to the head: %v", err)
	}
	head, _, err := m.Version()
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	for v := head; v > roundTripFloor; v-- {
		if err := m.Steps(-1); err != nil {
			t.Fatalf("down from %d: %v", v, err)
		}
	}
	after := schemaSnapshot(t, scratchURL)
	for i, l := range after {
		if was, ok := irreversibleLeftovers[l]; ok {
			after[i] = was
		}
	}
	if diff := diffLines(before, after); diff != "" {
		t.Errorf("the schema at %d after going up to %d and back differs from the one the ups built:\n%s", roundTripFloor, head, diff)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("up again after the round trip: %v", err)
	}
	if v, dirty, err := m.Version(); err != nil || dirty || v != head {
		t.Fatalf("after the second up: version %d dirty %v err %v, want %d clean", v, dirty, err, head)
	}
}

// schemaSnapshot lists the public schema as sorted lines: every column with
// its type, nullability and default, every index definition, every constraint
// definition, every enum label in order, and every function, trigger and
// view. migrate's own bookkeeping table is left out.
func schemaSnapshot(t *testing.T, dsn string) []string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, `
SELECT 'column ' || table_name || '.' || column_name || ' ' || data_type || ' ' ||
       COALESCE(udt_name, '') || ' null=' || is_nullable || ' default=' || COALESCE(column_default, '')
  FROM information_schema.columns WHERE table_schema = 'public' AND table_name <> 'schema_migrations'
UNION ALL
SELECT 'index ' || indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename <> 'schema_migrations'
UNION ALL
SELECT 'constraint ' || conrelid::regclass::text || ' ' || conname || ' ' || pg_get_constraintdef(oid)
  FROM pg_constraint WHERE connamespace = 'public'::regnamespace AND conrelid::regclass::text <> 'schema_migrations'
UNION ALL
SELECT 'enum ' || t.typname || ' ' || string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder)
  FROM pg_type t JOIN pg_enum e ON e.enumtypid = t.oid
 WHERE t.typnamespace = 'public'::regnamespace GROUP BY t.typname
UNION ALL
SELECT 'function ' || p.oid::regprocedure::text FROM pg_proc p WHERE p.pronamespace = 'public'::regnamespace
UNION ALL
SELECT 'trigger ' || tgrelid::regclass::text || ' ' || tgname FROM pg_trigger
 WHERE NOT tgisinternal AND tgrelid::regclass::text NOT LIKE 'pg_%'
UNION ALL
SELECT 'view ' || viewname || ' ' || definition FROM pg_views WHERE schemaname = 'public'
ORDER BY 1`)
	if err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("reading the schema: %v", err)
	}
	return lines
}

// diffLines reports the lines only one side has, "-" for before, "+" for after.
func diffLines(before, after []string) string {
	in := func(s []string) map[string]bool {
		m := make(map[string]bool, len(s))
		for _, l := range s {
			m[l] = true
		}
		return m
	}
	b, a := in(before), in(after)
	var out []string
	for _, l := range before {
		if !a[l] {
			out = append(out, "- "+l)
		}
	}
	for _, l := range after {
		if !b[l] {
			out = append(out, "+ "+l)
		}
	}
	return strings.Join(out, "\n")
}
