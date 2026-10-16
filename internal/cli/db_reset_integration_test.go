//go:build integration

package cli

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The incident of #1185 on a real server: DEXAFLOW_DATABASE_URL names another
// database on the same Postgres Lite uses, and `db reset --yes` runs. The
// reset refuses before it connects, and the named database is still there.
func TestDBResetLeavesTheDatabaseTheVariableNames(t *testing.T) {
	serverURL := os.Getenv("DATABASE_URL")
	if serverURL == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgconn.ParseConfig(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, serverURL)
	if err != nil {
		t.Fatalf("connecting to %s: %v", cfg.Host, err)
	}
	defer func() { _ = admin.Close(ctx) }() //nolint:errcheck // test cleanup
	name := fmt.Sprintf("dexaflow_reset_guard_%d", rand.IntN(1_000_000))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)") //nolint:errcheck // test cleanup
	})

	// Lite's target is leoflow_dev on the same server and port.
	liteHomeOnPort(t, strconv.Itoa(int(cfg.Port)))
	t.Setenv("DEXAFLOW_DATABASE_URL", fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", cfg.User, cfg.Password, cfg.Host, cfg.Port, name))
	_, _, rerr := run(t, "db", "reset", "--yes")
	if rerr == nil || !strings.Contains(rerr.Error(), "refusing") || !strings.Contains(rerr.Error(), "database "+name) {
		t.Fatalf("db reset with the variable naming %s: err = %v, want a refusal naming it", name, rerr)
	}
	var n int
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_database WHERE datname = $1", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("database %s is gone after the refused reset", name)
	}
}
