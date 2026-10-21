//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/migrations"
)

// The installation id read with the version (ADR 0090 decisions 3 and 5): a
// release with database.installation_id set serves only the database that
// carries it. One pool walks the states a replaced database goes through,
// with the id read through the same probe as the version.
func TestInstallationIDFollowsTheDatabase(t *testing.T) {
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL must point at a migrated database for the installation id test")
	}
	ctx := context.Background()
	admin, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: raw})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(admin.Close)
	latest, err := migrations.Latest()
	if err != nil {
		t.Fatal(err)
	}

	const scratch = "leoflow_probe_installation"
	pg := connectAsInstallation(ctx, t, admin, raw, scratch, "id-a",
		"CREATE TABLE %[1]s.schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)",
		fmt.Sprintf("INSERT INTO %%[1]s.schema_migrations (version, dirty) VALUES (%d, false)", latest),
		"CREATE TABLE %[1]s.installation (singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton), id text NOT NULL, recorded_at timestamptz NOT NULL DEFAULT now())",
		"INSERT INTO %[1]s.installation (id) VALUES ('id-a')")
	defer pg.Close()

	var replacedCalls int
	pg.SetOnReplaced(func() { replacedCalls++ })
	var classes []string
	pg.SetSchemaObserver(func(class string) { classes = append(classes, class) })
	run := func(stmt string) {
		t.Helper()
		if _, err := admin.Pool.Exec(ctx, fmt.Sprintf(stmt, scratch)); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if err := pg.SchemaReady(ctx); err != nil {
		t.Fatalf("the database carrying this release's id must be ready: %v", err)
	}
	if w, err := pg.SchemaWaitState(ctx); err != nil || !w.Ready() {
		t.Fatalf("boot must start on this release's database: %+v %v", w, err)
	}

	cases := []struct {
		name, stmt, reason string
	}{
		{"another installation's database", "UPDATE %s.installation SET id = 'id-b'", "database replaced: another installation id"},
		{"a schema without an id", "DELETE FROM %s.installation", "database replaced: no installation id"},
		{"a schema from before the table", "DROP TABLE %s.installation", "database replaced: no installation id"},
		{"an emptied database", "DROP TABLE %s.schema_migrations", "database replaced: no schema"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run(c.stmt)
			before := replacedCalls
			err := pg.SchemaReady(ctx)
			if !errors.Is(err, domain.ErrDatabaseReplaced) {
				t.Fatalf("readiness = %v, want domain.ErrDatabaseReplaced", err)
			}
			if replacedCalls != before+1 {
				t.Errorf("readiness did not report the change to the process (calls %d -> %d)", before, replacedCalls)
			}
			if got := classes[len(classes)-1]; got != storage.SchemaReasonReplaced {
				t.Errorf("dexaflow_schema_ready reason = %q, want %q", got, storage.SchemaReasonReplaced)
			}
			w, err := pg.SchemaWaitState(ctx)
			if err != nil || w.Class != storage.SchemaReasonReplaced || w.Reason != c.reason {
				t.Errorf("boot verdict = %+v %v, want class %q reason %q", w, err, storage.SchemaReasonReplaced, c.reason)
			}
		})
	}
}

// Without database.installation_id nothing is compared: Lite, and a chart
// install whose migration step does not record the id, behave as before.
func TestInstallationIDUncheckedWhenNotConfigured(t *testing.T) {
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL must point at a migrated database for the installation id test")
	}
	ctx := context.Background()
	admin, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: raw})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(admin.Close)
	latest, err := migrations.Latest()
	if err != nil {
		t.Fatal(err)
	}
	pg := connectWithSearchPath(ctx, t, admin, raw, "leoflow_probe_installation_off",
		"CREATE TABLE %[1]s.schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)",
		fmt.Sprintf("INSERT INTO %%[1]s.schema_migrations (version, dirty) VALUES (%d, false)", latest))
	defer pg.Close()
	pg.SetOnReplaced(func() { t.Error("a release without an installation id reported a replaced database") })
	if err := pg.SchemaReady(ctx); err != nil {
		t.Fatalf("readiness without an installation id: %v", err)
	}
}
