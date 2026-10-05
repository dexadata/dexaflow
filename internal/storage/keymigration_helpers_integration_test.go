//go:build integration

package storage_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // pgx5:// driver for the migrator
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/migrations"
)

// freshKeyMigrationDB creates a database of its own on the server DATABASE_URL
// points at, applies the embedded migrations, and drops it afterwards.
//
// The key migration reads EVERY encrypted column across every tenant, by
// design. Sharing the integration database with the rest of the suite would
// make its counts depend on whatever another package left behind, and a
// concurrent package run could add a row under a key these tests never heard
// of. A private database makes "every row" mean the rows the test wrote.
func freshKeyMigrationDB(t *testing.T) (dsn string) {
	t.Helper()
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Skip("DATABASE_URL must point at a Postgres server for integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = admin.Close(ctx) }()

	var b [6]byte
	if _, rerr := rand.Read(b[:]); rerr != nil {
		t.Fatal(rerr)
	}
	name := "kmig_" + hex.EncodeToString(b[:])
	if _, cerr := admin.Exec(ctx, "CREATE DATABASE "+name); cerr != nil {
		t.Skipf("cannot create a private test database (%v); the role needs CREATEDB", cerr)
	}
	t.Cleanup(func() {
		c, cerr := pgx.Connect(context.Background(), base)
		if cerr != nil {
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})

	u, perr := url.Parse(base)
	if perr != nil {
		t.Fatal(perr)
	}
	u.Path = "/" + name
	dsn = u.String()

	src, serr := iofs.New(migrations.Files, ".")
	if serr != nil {
		t.Fatal(serr)
	}
	mu := *u
	mu.Scheme = "pgx5"
	m, merr := migrate.NewWithSourceInstance("iofs", src, mu.String())
	if merr != nil {
		t.Fatalf("migrator: %v", merr)
	}
	defer func() { _, _ = m.Close() }()
	if uerr := m.Up(); uerr != nil && !errors.Is(uerr, migrate.ErrNoChange) {
		t.Fatalf("migrating: %v", uerr)
	}
	return dsn
}

// keyMigrationRepo opens a repository on dsn with the given cipher, for seeding.
func keyMigrationRepo(t *testing.T, dsn string) *storage.Repository {
	t.Helper()
	pg, err := storage.NewPostgres(context.Background(), config.DatabaseSection{URL: dsn})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	return storage.NewRepository(pg)
}

// keyMigrationConn opens one session on dsn.
func keyMigrationConn(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}
