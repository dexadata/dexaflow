//go:build integration

// Package storage_test: migration 057 makes the keys of new runs and task
// instances time-ordered UUIDv7 (#525, ADR 0098).
package storage_test

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/google/uuid"
)

// uuidv7IDsVersion is the migration that switches the two defaults.
const uuidv7IDsVersion = 57

// uuidv7IDsDefaultsQuery lists the id default of the two hot tables and
// whether the generator function exists.
const uuidv7IDsDefaultsQuery = `
SELECT format('%s.id %s', table_name, column_default)
FROM information_schema.columns
WHERE table_schema = 'public' AND table_name IN ('dag_runs', 'task_instances') AND column_name = 'id'
UNION ALL
SELECT format('function %s() returns %s', p.proname, format_type(p.prorettype, NULL))
FROM pg_proc p
WHERE p.proname = 'dexaflow_uuidv7'`

// TestUUIDv7IDsMigrationUpDownUp applies every migration to a scratch
// database, checks that the two tables draw their ids from dexaflow_uuidv7(),
// that it returns version 7 UUIDs carrying the current time in order, rolls
// 057 back to the UUIDv4 defaults with no function left, and applies it again.
func TestUUIDv7IDsMigrationUpDownUp(t *testing.T) {
	ctx := context.Background()
	m, pg := migratedScratchDatabase(ctx, t, "uuidv7_ids_mig")

	up := []string{
		"dag_runs.id dexaflow_uuidv7()",
		"function dexaflow_uuidv7() returns uuid",
		"task_instances.id dexaflow_uuidv7()",
	}
	if got := uuidv7IDsDefaults(ctx, t, pg); !slices.Equal(got, up) {
		t.Errorf("after up:\n got: %q\nwant: %q", got, up)
	}
	checkUUIDv7(ctx, t, pg)

	if err := m.Migrate(uuidv7IDsVersion - 1); err != nil {
		t.Fatalf("down to v%d: %v", uuidv7IDsVersion-1, err)
	}
	down := []string{
		"dag_runs.id gen_random_uuid()",
		"task_instances.id gen_random_uuid()",
	}
	if got := uuidv7IDsDefaults(ctx, t, pg); !slices.Equal(got, down) {
		t.Errorf("after down:\n got: %q\nwant: %q", got, down)
	}

	if err := m.Up(); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if got := uuidv7IDsDefaults(ctx, t, pg); !slices.Equal(got, up) {
		t.Errorf("after up again:\n got: %q\nwant: %q", got, up)
	}
}

// checkUUIDv7 draws ids a few milliseconds apart and checks each is an RFC
// 9562 version 7 UUID whose time is now, and that they sort in the order they
// were made.
func checkUUIDv7(ctx context.Context, t *testing.T, pg *storage.Postgres) {
	t.Helper()
	var ids []uuid.UUID
	for range 5 {
		before := time.Now().Add(-time.Second)
		var id uuid.UUID
		if err := pg.Pool.QueryRow(ctx, "SELECT dexaflow_uuidv7()").Scan(&id); err != nil {
			t.Fatalf("dexaflow_uuidv7(): %v", err)
		}
		if id.Version() != 7 || id.Variant() != uuid.RFC4122 {
			t.Fatalf("%s: version %d variant %s, want 7 and RFC 4122", id, id.Version(), id.Variant())
		}
		sec, nsec := id.Time().UnixTime()
		if at := time.Unix(sec, nsec); at.Before(before) || at.After(time.Now().Add(time.Second)) {
			t.Errorf("%s carries %s, want about now", id, at)
		}
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond)
	}
	if !slices.IsSortedFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }) {
		t.Errorf("ids made 2 ms apart are not in order: %v", ids)
	}
}

func uuidv7IDsDefaults(ctx context.Context, t *testing.T, pg *storage.Postgres) []string {
	t.Helper()
	rows, err := pg.Pool.Query(ctx, uuidv7IDsDefaultsQuery)
	if err != nil {
		t.Fatalf("describing the defaults: %v", err)
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
