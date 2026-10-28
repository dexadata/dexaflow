//go:build integration

package storage

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/migrations"
)

// TestSchemaWaitStateRereadsAMigrationThatFinished: the boot gate reads the
// version, then checks the migrator's lock. A migrator that finishes the
// latest migration between the two has cleared dirty and released the lock,
// so the first read is stale; taking it for a migration that stopped half
// applied stopped a boot the migration had just unblocked.
func TestSchemaWaitStateRereadsAMigrationThatFinished(t *testing.T) {
	// Arrange
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL must point at a Postgres for integration tests")
	}
	ctx := context.Background()
	latest, err := migrations.Latest()
	if err != nil {
		t.Fatal(err)
	}
	admin, err := NewPostgres(ctx, config.DatabaseSection{URL: raw})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("schema_wait_race_%d", time.Now().UnixNano())
	for _, stmt := range []string{
		"CREATE SCHEMA " + schema,
		"CREATE TABLE " + schema + ".schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)",
		fmt.Sprintf("INSERT INTO %s.schema_migrations VALUES (%d, true)", schema, latest),
	} {
		if _, err := admin.Pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("seeding %s: %v", schema, err)
		}
	}
	t.Cleanup(func() { _, _ = admin.Pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE") })
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	pg, err := NewPostgres(ctx, config.DatabaseSection{URL: u.String()})
	if err != nil {
		t.Fatalf("connect with search_path %s: %v", schema, err)
	}
	t.Cleanup(pg.Close)
	pg.beforeLockCheck = func() {
		if _, err := admin.Pool.Exec(ctx, "UPDATE "+schema+".schema_migrations SET dirty = false"); err != nil {
			t.Errorf("finishing the migration: %v", err)
		}
	}

	// Act
	w, err := pg.SchemaWaitState(ctx)

	// Assert
	if err != nil || !w.Ready() {
		t.Errorf("SchemaWaitState = %+v, %v; want ready: the migration finished before the lock check", w, err)
	}
}
