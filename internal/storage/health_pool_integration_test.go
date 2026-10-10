//go:build integration

package storage_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestHealthChecksSurviveASaturatedRequestPool pins #1042: readiness must not
// fail because the control plane is busy.
//
// Readiness takes two pool acquires per probe — Ping borrows a connection, the
// schema SELECT borrows another — and both used to come from the pool serving
// requests. Under saturation the probe blocks waiting for a connection and
// fails on its own deadline. Every replica shares that condition, because load
// is what caused it, so they all fail readiness in the same window and the
// Service can empty: a load-induced outage reported as a database problem, and
// one that feeds itself as the pods still in rotation absorb the load of the
// ones that left.
//
// The test saturates the request pool literally — maxOpenConns 1, and the one
// connection held for the duration — and then asserts the probe answers anyway.
// On the pre-fix code both calls block until the deadline and return
// context.DeadlineExceeded.
func TestHealthChecksSurviveASaturatedRequestPool(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()

	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url, MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)

	// Hold the request pool's only connection for the rest of the test. Nothing
	// on the request path can acquire while this is out.
	held, err := pg.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquiring the only request connection: %v", err)
	}
	defer held.Release()
	if got := pg.Pool.Stat().AcquiredConns(); got != 1 {
		t.Fatalf("request pool has %d acquired conns, want the pool fully lent out (1)", got)
	}

	// A budget in the same order as the probe's own, so a test that starts
	// queueing fails fast instead of hanging the suite.
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	if perr := pg.Ping(probeCtx); perr != nil {
		t.Errorf("Ping with the request pool saturated: %v — the connectivity check is queueing behind application traffic", perr)
	}
	if _, _, exists, serr := pg.SchemaVersion(probeCtx); serr != nil {
		t.Errorf("SchemaVersion with the request pool saturated: %v — the schema read is queueing behind application traffic", serr)
	} else if !exists {
		t.Error("schema_migrations is absent; DATABASE_URL must point at a MIGRATED database")
	}

	// The probe must not have stolen the request path's connection either: the
	// held one is still the only one out, and it is still ours.
	if got := pg.Pool.Stat().AcquiredConns(); got != 1 {
		t.Errorf("request pool has %d acquired conns after the probe, want 1 (only the held one)", got)
	}
}
