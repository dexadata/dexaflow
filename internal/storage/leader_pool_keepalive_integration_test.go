//go:build integration

package storage_test

import (
	"context"
	"os"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// The leader pool's session asks the server to probe it with TCP keepalives
// and to give up on unacknowledged data, so when the scheduler leader vanishes
// without a reset (a partition, a lost node) the server ends the session, and
// the advisory lock with it, within about a minute instead of the operating
// system's two hours. Until then no other replica can take leadership (#1599).
// It also checks the session keeps the main pool's statement_timeout off.
func TestLeaderPoolSessionKeepalives(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a Postgres for integration tests")
	}
	ctx := context.Background()
	pool, err := storage.NewLeaderPool(ctx, config.DatabaseSection{URL: url, StatementTimeoutMS: 30000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var local bool
	if err := conn.QueryRow(ctx, "SELECT inet_client_addr() IS NULL").Scan(&local); err != nil {
		t.Fatal(err)
	}
	for setting, want := range map[string]string{
		"tcp_keepalives_idle":     "30",
		"tcp_keepalives_interval": "10",
		"tcp_keepalives_count":    "3",
		"tcp_user_timeout":        "60000",
		"statement_timeout":       "0",
	} {
		if local && setting != "statement_timeout" {
			continue // Postgres ignores TCP settings on a Unix socket and shows 0
		}
		var got string
		if err := conn.QueryRow(ctx, "SHOW "+setting).Scan(&got); err != nil {
			t.Fatalf("SHOW %s: %v", setting, err)
		}
		if got != want {
			t.Errorf("leader session %s = %q, want %q", setting, got, want)
		}
	}
}
