//go:build integration

package storeerr_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/storeerr"
)

// TestTransientAgainstPostgres classifies the errors a real driver returns:
// a session terminated the way a failover terminates it (57P01 through
// pg_terminate_backend), a statement timeout, and a database that refuses
// connections. A unique violation stays non-transient.
func TestTransientAgainstPostgres(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a Postgres for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	victim, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = victim.Close(ctx) }()
	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()

	if _, kerr := admin.Exec(ctx, "SELECT pg_terminate_backend($1)", victim.PgConn().PID()); kerr != nil {
		t.Fatal(kerr)
	}
	_, termErr := victim.Exec(ctx, "SELECT 1")
	if termErr == nil {
		t.Fatal("a terminated session still answered")
	}
	if !storeerr.Transient(termErr) {
		t.Errorf("terminated session: Transient(%v) = false, want true", termErr)
	}

	_, timeoutErr := admin.Exec(ctx, "SET statement_timeout = 50; SELECT pg_sleep(1)")
	if timeoutErr == nil || !storeerr.Transient(timeoutErr) {
		t.Errorf("statement timeout: Transient(%v) = false, want true", timeoutErr)
	}
	if _, rerr := admin.Exec(ctx, "RESET statement_timeout"); rerr != nil {
		t.Fatal(rerr)
	}

	_, uniqueErr := admin.Exec(ctx, "CREATE TEMP TABLE storeerr_u (id int PRIMARY KEY); INSERT INTO storeerr_u VALUES (1), (1)")
	if uniqueErr == nil || storeerr.Transient(uniqueErr) {
		t.Errorf("unique violation: Transient(%v) = true, want false", uniqueErr)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Host = "127.0.0.1"
	cfg.ConnConfig.Port = 1 // nothing listens there
	cfg.ConnConfig.ConnectTimeout = 2 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, refusedErr := pool.Exec(ctx, "SELECT 1")
	if refusedErr == nil || !storeerr.Transient(refusedErr) {
		t.Errorf("connection refused: Transient(%v) = false, want true", refusedErr)
	}
}
