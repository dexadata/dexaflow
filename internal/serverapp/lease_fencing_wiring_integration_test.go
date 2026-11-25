//go:build integration

package serverapp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestLeaseFencingWiring drives the server's own wiring (ADR 0106, #2329):
// with scheduler.lease_fencing on, the scheduler side writes through a
// dedicated pool whose sessions carry the lease's term, and with it off the
// scheduler keeps the main pool, untagged, as before.
func TestLeaseFencingWiring(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a Postgres for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()

	for _, fencing := range []bool{true, false} {
		t.Run(fmt.Sprintf("lease_fencing=%v", fencing), func(t *testing.T) {
			cfg := &config.ServerConfig{Database: config.DatabaseSection{URL: url}}
			cfg.Scheduler.Enabled = true
			cfg.Scheduler.LeaseFencing = fencing
			lease, closeLease, err := openSchedulerLease(ctx, cfg, logger)
			if err != nil {
				t.Fatal(err)
			}
			defer closeLease()
			schedPG, release, err := schedulerPool(ctx, pg, cfg, lease, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if ok, err := lease.TryAcquire(ctx); err != nil || !ok {
				t.Fatalf("acquire: ok=%v err=%v", ok, err)
			}
			defer lease.Release(ctx) //nolint:errcheck // cleanup

			var app, tag string
			if err := schedPG.Pool.QueryRow(ctx,
				`SELECT current_setting('application_name'), coalesce(current_setting('dexaflow.leader_term', true), '')`).Scan(&app, &tag); err != nil {
				t.Fatal(err)
			}
			if fencing {
				if schedPG == pg {
					t.Fatal("fencing on, but the scheduler shares the main pool")
				}
				if want := fmt.Sprint(lease.Term()); tag != want || app != "dexaflow-leader/"+want {
					t.Fatalf("scheduler session tag=%q app=%q, want term %s", tag, app, want)
				}
				return
			}
			if schedPG != pg || tag != "" {
				t.Fatalf("fencing off: dedicated=%v tag=%q, want the main pool untagged", schedPG != pg, tag)
			}
		})
	}
}
