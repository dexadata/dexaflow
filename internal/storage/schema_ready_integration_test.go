//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/migrations"
)

// TestSchemaReadyAgainstLiveDatabase exercises the readiness schema assertion
// (#1023) against a real Postgres, which is the only place the query itself can
// be wrong: the pure gate is unit-tested, but "does SELECT version, dirty FROM
// schema_migrations actually report an absent table as absent, on this server,
// through this pool" is not something a fake can answer. It is the same
// distinction the issue is about — a Ping proving the connection while saying
// nothing about the schema.
//
// Each case connects with search_path pointed at a scratch schema, so a missing
// or stale schema_migrations is produced WITHOUT touching the migrated one the
// rest of the suite shares.
//
// PRIVILEGES: building those scratch schemas needs CREATE on the database, which
// the read-only role:api identity (ADR 0049) deliberately does not have. CI runs
// this with the DSN in .github/workflows/ci.yaml — the `leoflow` role the
// postgres image bootstraps, which owns the database — so a green there is real
// evidence. Against a least-privilege DSN the test SKIPS with the reason rather
// than failing, so nobody reads a permission error as a schema regression.
func TestSchemaReadyAgainstLiveDatabase(t *testing.T) {
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL must point at a migrated database for the schema readiness test")
	}
	ctx := context.Background()

	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: raw})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pg.Close()

	t.Run("migrated database → ready", func(t *testing.T) {
		if err := pg.SchemaReady(ctx); err != nil {
			t.Errorf("a migrated database must be ready, got: %v", err)
		}
	})

	t.Run("schema_migrations absent → not ready", func(t *testing.T) {
		// The #1023 cluster state: the connection is healthy, the database is empty.
		other := connectWithSearchPath(ctx, t, pg, raw, "leoflow_probe_empty")
		defer other.Close()
		if err := other.Ping(ctx); err != nil {
			t.Fatalf("precondition: the connection itself must be healthy, got: %v", err)
		}
		if err := other.SchemaReady(ctx); err == nil {
			t.Error("a database with no schema_migrations reported ready")
		}
	})

	t.Run("version behind the binary → not ready", func(t *testing.T) {
		// A restore from a backup older than this binary, or a lagging replica.
		other := connectWithSearchPath(ctx, t, pg, raw, "leoflow_probe_behind",
			"CREATE TABLE %[1]s.schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)",
			"INSERT INTO %[1]s.schema_migrations (version, dirty) VALUES (1, false)")
		defer other.Close()
		if err := other.SchemaReady(ctx); err == nil {
			t.Error("a database at v1 reported ready against a binary that requires the embedded latest")
		}
	})

	t.Run("boot and readiness agree on an empty schema", func(t *testing.T) {
		// Outside the in-flight-migration case the two verdicts must not drift:
		// whatever boot refuses to start against, readiness must refuse to be
		// ready against.
		other := connectWithSearchPath(ctx, t, pg, raw, "leoflow_probe_agree")
		defer other.Close()
		bootErr := other.CheckSchemaCurrent(ctx)
		readyErr := other.SchemaReady(ctx)
		if bootErr == nil || readyErr == nil {
			t.Fatalf("both must reject an empty schema: boot=%v readiness=%v", bootErr, readyErr)
		}
		if bootErr.Error() != readyErr.Error() {
			t.Errorf("boot and readiness disagree:\n boot: %v\n ready: %v", bootErr, readyErr)
		}
	})

	t.Run("a migration in flight above this binary: boot refuses, readiness stays ready", func(t *testing.T) {
		// The state that a `pre-upgrade` migration Job puts the database in while
		// the OLD pods are still live and in the Service's endpoints:
		// golang-migrate has committed dirty=true at a version this binary does
		// not need. Treating that as not-ready flips every old replica at once —
		// they all read this one row — and empties the Service mid-upgrade.
		//
		// v9999 stands in for "ahead of whatever this binary embeds", so the case
		// does not have to track migrations.Latest().
		other := connectWithSearchPath(ctx, t, pg, raw, "leoflow_probe_dirty_ahead",
			"CREATE TABLE %[1]s.schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)",
			"INSERT INTO %[1]s.schema_migrations (version, dirty) VALUES (9999, true)")
		defer other.Close()

		if err := other.SchemaReady(ctx); err != nil {
			t.Errorf("readiness must survive a forward migration in flight, got: %v", err)
		}
		if err := other.CheckSchemaCurrent(ctx); err == nil {
			t.Error("boot must still refuse to START against a half-applied schema")
		}
	})

	t.Run("a migration in flight at or below this binary is still not ready", func(t *testing.T) {
		// The other arm: the half-applied migration is one this binary depends
		// on, so the exemption must not apply.
		other := connectWithSearchPath(ctx, t, pg, raw, "leoflow_probe_dirty_behind",
			"CREATE TABLE %[1]s.schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)",
			"INSERT INTO %[1]s.schema_migrations (version, dirty) VALUES (1, true)")
		defer other.Close()
		if err := other.SchemaReady(ctx); err == nil {
			t.Error("a dirty schema behind this binary reported ready")
		}
	})

	t.Run("a migration in flight AT this binary's own version is not ready", func(t *testing.T) {
		// The boundary of the exemption, seeded at the exact version this binary
		// embeds. Widening dirtyAhead from "strictly above" to "dirty at all"
		// passes every other case in this file and fails only here.
		latest, err := migrations.Latest()
		if err != nil {
			t.Fatalf("reading the embedded schema version: %v", err)
		}
		other := connectWithSearchPath(ctx, t, pg, raw, "leoflow_probe_dirty_at_latest",
			"CREATE TABLE %[1]s.schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)",
			fmt.Sprintf("INSERT INTO %%[1]s.schema_migrations (version, dirty) VALUES (%d, true)", latest))
		defer other.Close()
		if err := other.SchemaReady(ctx); err == nil {
			t.Error("a half-applied migration to this binary's own required version reported ready")
		}
	})

	t.Run("every not-ready verdict is the schema sentinel, not a transient error", func(t *testing.T) {
		// What the health endpoints branch on to say "schema not current" rather
		// than "unavailable". Asserted here as well as in the unit tests because
		// only here does the error come back through the real query path.
		other := connectWithSearchPath(ctx, t, pg, raw, "leoflow_probe_sentinel")
		defer other.Close()
		if err := other.SchemaReady(ctx); !errors.Is(err, domain.ErrSchemaNotCurrent) {
			t.Errorf("want domain.ErrSchemaNotCurrent, got %v", err)
		}
	})
}

// connectWithSearchPath opens a second pool whose search_path is a scratch schema
// it (re)creates, optionally seeding it, and registers the drop. Unqualified
// lookups then resolve inside that schema, so schema_migrations can be absent or
// stale for this connection while the shared migrated schema is untouched. Each
// seed statement is a format string whose %[1]s is the scratch schema name (the
// seeding connection has its own search_path, so the DDL must qualify itself).
func connectWithSearchPath(ctx context.Context, t *testing.T, admin *storage.Postgres, raw, schema string, seed ...string) *storage.Postgres {
	t.Helper()
	if _, err := admin.Pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		skipOrFatal(t, "dropping stale scratch schema", err)
	}
	if _, err := admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		skipOrFatal(t, "creating scratch schema", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Errorf("dropping scratch schema %s: %v", schema, err)
		}
	})
	for _, stmt := range seed {
		if _, err := admin.Pool.Exec(ctx, fmt.Sprintf(stmt, schema)); err != nil {
			t.Fatalf("seeding scratch schema: %v", err)
		}
	}

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("DATABASE_URL is not a URL: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: u.String()})
	if err != nil {
		t.Fatalf("connecting with search_path=%s: %v", schema, err)
	}
	return pg
}

// TestSchemaReadyFollowsTheDatabaseUnderOnePool walks ONE pool through the
// states a real upgrade puts the database in, in order, without reconnecting.
//
// Two things only this shape can show. First, that the verdict tracks the
// database rather than anything cached at connect time — the whole premise of
// #1023 is a database that changes UNDER a running pod, so a probe that answered
// from a memo would reproduce the bug. Second, that the "a migration is in
// flight" log line is latched per EPISODE: readiness runs every periodSeconds
// and a migration can take minutes, so logging it per probe would bury the log,
// while never resetting would silence every later upgrade for the life of the
// process.
func TestSchemaReadyFollowsTheDatabaseUnderOnePool(t *testing.T) {
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL must point at a migrated database for the schema readiness test")
	}
	ctx := context.Background()

	admin, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: raw})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// t.Cleanup, not defer: connectWithSearchPath registers its DROP SCHEMA as a
	// cleanup too, and cleanups run after the deferred calls. Closing the admin
	// pool with defer would pull it out from under that drop. Registered first,
	// so LIFO ordering runs it last.
	t.Cleanup(admin.Close)

	latest, err := migrations.Latest()
	if err != nil {
		t.Fatalf("reading the embedded schema version: %v", err)
	}

	const scratch = "leoflow_probe_transitions"
	pg := connectWithSearchPath(ctx, t, admin, raw, scratch,
		"CREATE TABLE %[1]s.schema_migrations (version bigint NOT NULL, dirty boolean NOT NULL)",
		"INSERT INTO %[1]s.schema_migrations (version, dirty) VALUES (1, false)")
	defer pg.Close()

	setRow := func(version uint, dirty bool) {
		t.Helper()
		if _, err := admin.Pool.Exec(ctx,
			fmt.Sprintf("UPDATE %s.schema_migrations SET version = $1, dirty = $2", scratch), int64(version), dirty); err != nil {
			t.Fatalf("moving the schema row to (v%d, dirty=%v): %v", version, dirty, err)
		}
	}

	// SchemaReady logs through the default logger; capture it for the duration.
	var logged bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(restore) })
	inFlightLines := func() int {
		return strings.Count(logged.String(), "a migration is in flight")
	}

	// Behind: not ready, and nothing to report.
	if err := pg.SchemaReady(ctx); err == nil {
		t.Fatal("a schema at v1 reported ready")
	}
	if n := inFlightLines(); n != 0 {
		t.Errorf("in-flight logged %d times before any migration started", n)
	}

	// A forward migration starts, past what this binary needs: stay ready, say so once.
	setRow(latest+1, true)
	if err := pg.SchemaReady(ctx); err != nil {
		t.Fatalf("readiness must survive a forward migration in flight, got: %v", err)
	}
	if n := inFlightLines(); n != 1 {
		t.Fatalf("in-flight logged %d times on the first probe of the episode, want 1", n)
	}

	// The kubelet keeps probing for as long as the migration runs. Still ready,
	// and still one line — not one per period.
	for range 5 {
		if err := pg.SchemaReady(ctx); err != nil {
			t.Fatalf("readiness flipped mid-migration: %v", err)
		}
	}
	if n := inFlightLines(); n != 1 {
		t.Errorf("in-flight logged %d times across 6 probes of one episode, want 1", n)
	}

	// The migration lands. The pod stays ready, and the latch clears.
	setRow(latest+1, false)
	if err := pg.SchemaReady(ctx); err != nil {
		t.Fatalf("a clean ahead schema must be ready, got: %v", err)
	}

	// A LATER upgrade must be reported again rather than swallowed by the latch.
	setRow(latest+2, true)
	if err := pg.SchemaReady(ctx); err != nil {
		t.Fatalf("readiness must survive the second migration too, got: %v", err)
	}
	if n := inFlightLines(); n != 2 {
		t.Errorf("in-flight logged %d times across two separate episodes, want 2", n)
	}

	// And the database dropping out from under the pool is still caught.
	if _, err := admin.Pool.Exec(ctx, "DROP TABLE "+scratch+".schema_migrations"); err != nil {
		t.Fatalf("dropping the fixture table: %v", err)
	}
	if err := pg.SchemaReady(ctx); !errors.Is(err, domain.ErrSchemaNotCurrent) {
		t.Errorf("a schema that vanished under a live pool: want domain.ErrSchemaNotCurrent, got %v", err)
	}
}

// skipOrFatal separates "this DSN is not allowed to build the fixture" from a
// real failure. The scratch schemas need CREATE on the database; a least
// privilege DSN (the read-only role:api identity of ADR 0049) cannot create
// them, and reporting that as a FAIL would read as a schema regression. CI uses
// the owning role, so it takes the Fatalf path and the coverage is real.
func skipOrFatal(t *testing.T, what string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" { // insufficient_privilege
		t.Skipf("%s: DATABASE_URL lacks CREATE on the database (%v); "+
			"this test needs the owning role, not the read-only role:api identity", what, err)
	}
	t.Fatalf("%s: %v", what, err)
}
