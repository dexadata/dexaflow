//go:build integration

package storage_test

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestUsageHorizonIsWhereNewEventsStart covers ADR 0096 point 6: the horizon a
// new leader starts its counters at sorts after every event already served
// and before every event committed later.
func TestUsageHorizonIsWhereNewEventsStart(t *testing.T) {
	// Arrange
	pg, _, _, _, ctx := openExecPG(t)
	usage := storage.NewUsageStore(pg)
	enableUsageEvents(t, usage, ctx)
	before, err := usage.ReadEvents(ctx, domain.UsageCursor{}, 5000)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	// Act
	horizon, err := usage.Horizon(ctx)

	// Assert
	if err != nil {
		t.Fatalf("Horizon: %v", err)
	}
	for _, e := range before.Events {
		if !e.Cursor.Before(horizon) {
			t.Fatalf("served event %+v does not sort before the horizon %+v", e.Cursor, horizon)
		}
	}
	var next uint64
	if err := pg.Pool.QueryRow(context.Background(), `SELECT pg_current_xact_id()::text::bigint`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if later := (domain.UsageCursor{Xid: next}); later.Before(horizon) {
		t.Errorf("a later transaction %d sorts before the horizon %+v", next, horizon)
	}
}
