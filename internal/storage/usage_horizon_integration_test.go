//go:build integration

package storage_test

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestUsageHorizonIsWhereNewEventsStart covers ADR 0096 point 6: while an
// older transaction is open the horizon stays at it, so an event that
// transaction commits later still sorts after the horizon a new leader
// started at, and every event already served sorts before it.
func TestUsageHorizonIsWhereNewEventsStart(t *testing.T) {
	// Arrange
	pg, _, _, _, ctx := openExecPG(t)
	usage := storage.NewUsageStore(pg)
	enableUsageEvents(t, pg, usage, ctx)
	older, err := pg.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = older.Rollback(context.Background()) }()
	var olderXid uint64
	if err := older.QueryRow(ctx, `SELECT pg_current_xact_id()::text::bigint`).Scan(&olderXid); err != nil {
		t.Fatal(err)
	}
	served, err := usage.ReadEvents(ctx, domain.UsageCursor{}, 5000)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	// Act
	horizon, err := usage.Horizon(ctx)

	// Assert
	if err != nil {
		t.Fatalf("Horizon: %v", err)
	}
	if horizon.Xid > olderXid {
		t.Errorf("horizon %+v is past the open transaction %d: an event it commits would be missed", horizon, olderXid)
	}
	for _, e := range served.Events {
		if !e.Cursor.Before(horizon) {
			t.Fatalf("served event %+v does not sort before the horizon %+v", e.Cursor, horizon)
		}
	}
}
