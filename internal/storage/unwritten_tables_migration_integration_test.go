//go:build integration

// Package storage_test: migration 051 drops the tables nothing writes (#1186).
//
// scheduler_loops and replicas (migration 004) and task_state_history
// (migration 003) were declared for observability and audit, and no code ever
// wrote a row to them. 051 drops them, and its down restores them exactly as
// 050 had them, so a rollback to the previous release finds the schema it
// expects.
package storage_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/migrations"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5" migrate scheme
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// unwrittenTablesVersion is the migration that drops the three tables.
const unwrittenTablesVersion = 51

// unwrittenTablesSchema is the shape migrations 003 and 004 gave the three
// tables, unchanged by every later migration up to 050: columns, constraints,
// indexes, sequences and the trim function.
var unwrittenTablesSchema = []string{
	"column replicas.hostname text NOT NULL",
	"column replicas.id uuid NOT NULL DEFAULT gen_random_uuid()",
	"column replicas.is_leader boolean NOT NULL DEFAULT false",
	"column replicas.last_heartbeat_at timestamp with time zone NOT NULL DEFAULT now()",
	"column replicas.metadata jsonb NULL DEFAULT '{}'::jsonb",
	"column replicas.process_id integer NOT NULL",
	"column replicas.role text NOT NULL",
	"column replicas.started_at timestamp with time zone NOT NULL DEFAULT now()",
	"column replicas.version text NOT NULL",
	"column scheduler_loops.dags_examined integer NOT NULL DEFAULT 0",
	"column scheduler_loops.duration_ms integer NOT NULL",
	"column scheduler_loops.ended_at timestamp with time zone NOT NULL",
	"column scheduler_loops.errors integer NOT NULL DEFAULT 0",
	"column scheduler_loops.id bigint NOT NULL DEFAULT nextval('scheduler_loops_id_seq'::regclass)",
	"column scheduler_loops.replica_id uuid NOT NULL",
	"column scheduler_loops.runs_created integer NOT NULL DEFAULT 0",
	"column scheduler_loops.started_at timestamp with time zone NOT NULL",
	"column scheduler_loops.tasks_scheduled integer NOT NULL DEFAULT 0",
	"column task_state_history.actor text NULL",
	"column task_state_history.from_state task_state NULL",
	"column task_state_history.id bigint NOT NULL DEFAULT nextval('task_state_history_id_seq'::regclass)",
	"column task_state_history.reason text NULL",
	"column task_state_history.task_instance_id uuid NOT NULL",
	"column task_state_history.to_state task_state NOT NULL",
	"column task_state_history.transitioned_at timestamp with time zone NOT NULL DEFAULT now()",
	"constraint replicas.replicas_pkey PRIMARY KEY (id)",
	"constraint scheduler_loops.scheduler_loops_pkey PRIMARY KEY (id)",
	"constraint scheduler_loops.scheduler_loops_replica_id_fkey FOREIGN KEY (replica_id) REFERENCES replicas(id) ON DELETE CASCADE",
	"constraint task_state_history.task_state_history_pkey PRIMARY KEY (id)",
	"constraint task_state_history.task_state_history_task_instance_id_fkey FOREIGN KEY (task_instance_id) REFERENCES task_instances(id) ON DELETE CASCADE",
	"function trim_scheduler_loops() returns void",
	"index replicas.idx_replicas_heartbeat CREATE INDEX idx_replicas_heartbeat ON public.replicas USING btree (last_heartbeat_at DESC)",
	"index replicas.idx_replicas_leader CREATE INDEX idx_replicas_leader ON public.replicas USING btree (is_leader) WHERE (is_leader = true)",
	"index replicas.replicas_pkey CREATE UNIQUE INDEX replicas_pkey ON public.replicas USING btree (id)",
	"index scheduler_loops.idx_scheduler_loops_replica CREATE INDEX idx_scheduler_loops_replica ON public.scheduler_loops USING btree (replica_id, started_at DESC)",
	"index scheduler_loops.scheduler_loops_pkey CREATE UNIQUE INDEX scheduler_loops_pkey ON public.scheduler_loops USING btree (id)",
	"index task_state_history.idx_tsh_ti CREATE INDEX idx_tsh_ti ON public.task_state_history USING btree (task_instance_id, transitioned_at DESC)",
	"index task_state_history.task_state_history_pkey CREATE UNIQUE INDEX task_state_history_pkey ON public.task_state_history USING btree (id)",
	"sequence scheduler_loops_id_seq owned by scheduler_loops.id",
	"sequence task_state_history_id_seq owned by task_state_history.id",
}

// unwrittenTablesSchemaQuery describes every column, constraint, index,
// sequence and function that belongs to the three tables, one line each.
const unwrittenTablesSchemaQuery = `
WITH t(name) AS (VALUES ('replicas'), ('scheduler_loops'), ('task_state_history'))
SELECT format('column %s.%s %s %s%s', c.table_name, c.column_name,
              format_type(a.atttypid, a.atttypmod),
              CASE WHEN c.is_nullable = 'NO' THEN 'NOT NULL' ELSE 'NULL' END,
              COALESCE(' DEFAULT ' || c.column_default, ''))
FROM information_schema.columns c
JOIN t ON t.name = c.table_name
JOIN pg_attribute a ON a.attrelid = to_regclass('public.' || c.table_name) AND a.attname = c.column_name
WHERE c.table_schema = 'public'
UNION ALL
SELECT format('constraint %s.%s %s', con.conrelid::regclass, con.conname, pg_get_constraintdef(con.oid))
FROM pg_constraint con JOIN t ON con.conrelid = to_regclass('public.' || t.name)
UNION ALL
SELECT format('index %s.%s %s', i.tablename, i.indexname, i.indexdef)
FROM pg_indexes i JOIN t ON t.name = i.tablename
WHERE i.schemaname = 'public'
UNION ALL
SELECT format('sequence %s owned by %s.%s', s.relname, d.refobjid::regclass, a.attname)
FROM pg_class s
JOIN pg_depend d ON d.objid = s.oid AND d.deptype = 'a' AND d.classid = 'pg_class'::regclass
JOIN pg_attribute a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
WHERE s.relkind = 'S' AND s.relname IN ('scheduler_loops_id_seq', 'task_state_history_id_seq')
UNION ALL
SELECT format('function %s() returns %s', p.proname, format_type(p.prorettype, NULL))
FROM pg_proc p
WHERE p.proname = 'trim_scheduler_loops'`

// TestUnwrittenTablesMigrationUpDownUp applies every migration to a scratch
// database, checks that 051 left none of the three tables (or their indexes,
// sequences, keys and trim function), rolls 051 back and checks they are back
// exactly as 050 had them, then applies it again.
func TestUnwrittenTablesMigrationUpDownUp(t *testing.T) {
	ctx := context.Background()
	m, pg := migratedScratchDatabase(ctx, t, "unwritten_tables_mig")

	if got := unwrittenTablesObjects(ctx, t, pg); len(got) != 0 {
		t.Errorf("after up, the dropped tables left objects behind:\n%v", got)
	}

	if err := m.Migrate(unwrittenTablesVersion - 1); err != nil {
		t.Fatalf("down to v%d: %v", unwrittenTablesVersion-1, err)
	}
	if got := unwrittenTablesObjects(ctx, t, pg); !slices.Equal(got, unwrittenTablesSchema) {
		t.Errorf("after down, the restored tables differ from 050's schema:\n got: %q\nwant: %q", got, unwrittenTablesSchema)
	}

	if err := m.Up(); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if got := unwrittenTablesObjects(ctx, t, pg); len(got) != 0 {
		t.Errorf("after up again, the dropped tables left objects behind:\n%v", got)
	}
}

func unwrittenTablesObjects(ctx context.Context, t *testing.T, pg *storage.Postgres) []string {
	t.Helper()
	rows, err := pg.Pool.Query(ctx, unwrittenTablesSchemaQuery)
	if err != nil {
		t.Fatalf("describing the three tables: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scanning: %v", err)
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading: %v", err)
	}
	slices.Sort(out)
	return out
}

// migratedScratchDatabase creates a database named after prefix, applies every
// migration to it and returns the migrator and a connection. Both, and the
// database, are cleaned up when the test ends.
func migratedScratchDatabase(ctx context.Context, t *testing.T, prefix string) (*migrate.Migrate, *storage.Postgres) {
	t.Helper()
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL must point at a Postgres server for integration tests")
	}
	admin, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: raw})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(admin.Close)
	dbName := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	if _, cerr := admin.Pool.Exec(ctx, "CREATE DATABASE "+dbName); cerr != nil {
		skipOrFatal(t, "creating scratch database", cerr)
	}
	t.Cleanup(func() {
		// ctx is the test's background context, never canceled.
		if _, derr := admin.Pool.Exec(ctx, "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)"); derr != nil {
			t.Errorf("dropping scratch database: %v", derr)
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
	t.Cleanup(func() { _, _ = m.Close() })
	if err = m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: scratchURL})
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	t.Cleanup(pg.Close)
	return m, pg
}
