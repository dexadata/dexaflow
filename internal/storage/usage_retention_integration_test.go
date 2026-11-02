//go:build integration

package storage_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// usagePrunedMark reads the pruned mark: the key of the newest deleted event,
// or ok=false when nothing was ever deleted.
func usagePrunedMark(t *testing.T, pool *pgxpool.Pool, ctx context.Context) (xid uint64, id int64, ok bool) {
	t.Helper()
	var x *uint64
	var i *int64
	if err := pool.QueryRow(ctx, `SELECT xid, event_id FROM usage_events_pruned WHERE id`).Scan(&x, &i); err != nil {
		t.Fatalf("read usage_events_pruned: %v", err)
	}
	if x == nil || i == nil {
		return 0, 0, false
	}
	return *x, *i, true
}

// seedUsageEventsAt inserts one event per entry, with the given transaction id
// and recording time, and returns their ids. The ids are far below every real
// transaction, so these events are the head of the serving order; the mark and
// the rows are reset when the test ends.
func seedUsageEventsAt(t *testing.T, pool *pgxpool.Pool, ctx context.Context, rows []struct {
	xid uint64
	at  time.Time
}) []int64 {
	t.Helper()
	var markXid, markID *int64
	if err := pool.QueryRow(ctx, `SELECT xid::text::bigint, event_id FROM usage_events_pruned WHERE id`).Scan(&markXid, &markID); err != nil {
		t.Fatalf("read usage_events_pruned: %v", err)
	}
	ids := make([]int64, len(rows))
	for i, r := range rows {
		if err := pool.QueryRow(ctx, `
			INSERT INTO usage_events (xid, tenant_id, dag_run_id, task_instance_id, task_id, map_index,
			    try_number, attempt_epoch, revision, state, previous_state, pool_slots, recorded_at)
			VALUES ($1::text::xid8, $2::uuid, gen_random_uuid(), gen_random_uuid(), 'seed', -1,
			    1, 0, 0, 'success', 'running', 1, $3)
			RETURNING id`, strconv.FormatUint(r.xid, 10), defaultTenantID(), r.at).Scan(&ids[i]); err != nil {
			t.Fatalf("seed usage event: %v", err)
		}
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM usage_events WHERE id = ANY($1)`, ids)
		_, _ = pool.Exec(ctx, `UPDATE usage_events_pruned SET xid = $1::bigint::text::xid8, event_id = $2 WHERE id`, markXid, markID)
	})
	return ids
}

// TestUsageEventRetentionDeletesAPrefix covers ADR 0096 point 5: the janitor
// deletes the oldest events in serving order, stops at the first one inside
// the window even when an older one follows it, honors the row limit, and
// moves the pruned mark to the last event it deleted.
func TestUsageEventRetentionDeletesAPrefix(t *testing.T) {
	// Arrange
	store, pool, ctx := openRetention(t)
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Now()
	ids := seedUsageEventsAt(t, pool, ctx, []struct {
		xid uint64
		at  time.Time
	}{{1, old}, {2, old}, {3, recent}, {4, old}})
	cutoff := time.Date(2002, 1, 1, 0, 0, 0, 0, time.UTC)

	// Act
	first, firstErr := store.DeleteUsageEvents(ctx, cutoff, 1)
	firstXid, firstID, firstOK := usagePrunedMark(t, pool, ctx)
	second, secondErr := store.DeleteUsageEvents(ctx, cutoff, 100)
	secondXid, secondID, _ := usagePrunedMark(t, pool, ctx)
	third, thirdErr := store.DeleteUsageEvents(ctx, cutoff, 100)
	thirdXid, thirdID, _ := usagePrunedMark(t, pool, ctx)

	// Assert
	for name, err := range map[string]error{"first": firstErr, "second": secondErr, "third": thirdErr} {
		if err != nil {
			t.Fatalf("%s DeleteUsageEvents: %v", name, err)
		}
	}
	if first != 1 || !firstOK || firstXid != 1 || firstID != ids[0] {
		t.Errorf("first batch deleted %d, mark (%d, %d, %v); want 1 and the mark on the first event", first, firstXid, firstID, firstOK)
	}
	if second != 1 || secondXid != 2 || secondID != ids[1] {
		t.Errorf("second batch deleted %d, mark (%d, %d); want 1, stopping at the recent event", second, secondXid, secondID)
	}
	if third != 0 || thirdXid != 2 || thirdID != ids[1] {
		t.Errorf("third batch deleted %d, mark (%d, %d); want nothing and the mark unchanged", third, thirdXid, thirdID)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM usage_events WHERE id = ANY($1)`, ids).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 2 {
		t.Errorf("events left = %d, want 2: the recent one and the old one after it", left)
	}
}

// TestUsageEventRetentionCountMatchesTheDelete: the dry-run count sees the
// same prefix the delete would remove.
func TestUsageEventRetentionCountMatchesTheDelete(t *testing.T) {
	// Arrange
	store, pool, ctx := openRetention(t)
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	seedUsageEventsAt(t, pool, ctx, []struct {
		xid uint64
		at  time.Time
	}{{1, old}, {2, old}, {3, time.Now()}, {4, old}})
	cutoff := time.Date(2002, 1, 1, 0, 0, 0, 0, time.UTC)

	// Act
	got, err := store.CountEligible(ctx, nil, nil, &cutoff)

	// Assert
	if err != nil {
		t.Fatalf("CountEligible: %v", err)
	}
	if got.UsageEvents != 2 || got.Total() != 2 {
		t.Errorf("counts = %+v, want the two events before the recent one", got)
	}
	if _, _, ok := usagePrunedMark(t, pool, ctx); ok {
		t.Error("the count moved the pruned mark")
	}
}

// TestUsageEventRetentionMarkOnlyMovesForward: an expired event below the
// mark (written by a transaction that stayed open past the window) is deleted
// without moving the mark back, so a reader between the two is still told it
// lost events.
func TestUsageEventRetentionMarkOnlyMovesForward(t *testing.T) {
	// Arrange
	store, pool, ctx := openRetention(t)
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := seedUsageEventsAt(t, pool, ctx, []struct {
		xid uint64
		at  time.Time
	}{{1, old}})
	mustExec(t, pool, ctx, `UPDATE usage_events_pruned SET xid = '50'::xid8, event_id = $1 WHERE id`, ids[0]+1000)

	// Act
	n, err := store.DeleteUsageEvents(ctx, time.Date(2002, 1, 1, 0, 0, 0, 0, time.UTC), 10)

	// Assert
	if err != nil {
		t.Fatalf("DeleteUsageEvents: %v", err)
	}
	if n != 1 {
		t.Errorf("deleted %d, want the expired event below the mark", n)
	}
	if xid, id, _ := usagePrunedMark(t, pool, ctx); xid != 50 || id != ids[0]+1000 {
		t.Errorf("mark = (%d, %d), want it left at (50, %d)", xid, id, ids[0]+1000)
	}
}
