//go:build integration

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// Against a real Postgres: a Lite server refuses to boot while a migration
// holds the lock, boots once it is released, and is canceled with
// errKeyLockLost when its lock session is terminated (ADR 0065 section 3).
func TestServerKeyLockAgainstPostgres(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a Postgres for integration tests")
	}
	ctx := context.Background()
	migration, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = migration.Close(ctx) }()
	cfg := &config.ServerConfig{Database: config.DatabaseSection{URL: url}, SecretKeyMigrationLock: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if ok, lerr := storage.TryKeyMigrationLockExclusive(ctx, migration); lerr != nil || !ok {
		t.Fatalf("taking the migration lock: %v %v", ok, lerr)
	}
	if _, _, herr := holdKeyLock(ctx, cfg, logger); !errors.Is(herr, errKeyMigrationInProgress) {
		t.Fatalf("a server started during a migration: %v", herr)
	}
	if _, uerr := migration.Exec(ctx, "SELECT pg_advisory_unlock($1)", storage.KeyMigrationLockID); uerr != nil {
		t.Fatal(uerr)
	}

	sctx, release, herr := holdKeyLock(ctx, cfg, logger)
	if herr != nil {
		t.Fatalf("server boot with no migration running: %v", herr)
	}
	defer release()
	if ok, lerr := storage.TryKeyMigrationLockExclusive(ctx, migration); lerr != nil || ok {
		t.Fatalf("a migration took the lock from a running server: %v %v", ok, lerr)
	}

	classid, objid := storage.KeyMigrationLockID>>32, storage.KeyMigrationLockID&0xFFFFFFFF
	if _, terr := migration.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks
		WHERE locktype = 'advisory' AND classid::bigint = $1 AND objid::bigint = $2 AND pid <> pg_backend_pid()`,
		classid, objid); terr != nil {
		t.Fatal(terr)
	}
	select {
	case <-sctx.Done():
	case <-time.After(15 * time.Second):
		t.Fatal("the server kept running after its lock session was terminated")
	}
	if !errors.Is(context.Cause(sctx), errKeyLockLost) {
		t.Errorf("cause %v, want errKeyLockLost", context.Cause(sctx))
	}
}
