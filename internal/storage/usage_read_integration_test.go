//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// eventsWithIDs keeps the events of page whose id is in ids, in page order:
// other tests leave events in the shared table.
func eventsWithIDs(page domain.UsagePage, ids []int64) []domain.UsageEvent {
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []domain.UsageEvent
	for _, e := range page.Events {
		if want[e.Cursor.ID] {
			out = append(out, e)
		}
	}
	return out
}

// TestUsageEventsReadInServingOrder covers ADR 0096 point 4: events come in
// (xid, id) order after the cursor, at most the limit, with the tenant name;
// a run that no longer exists leaves the DAG and run ids empty.
func TestUsageEventsReadInServingOrder(t *testing.T) {
	// Arrange
	pg, _, _, _, ctx := openExecPG(t)
	usage := storage.NewUsageStore(pg)
	enableUsageEvents(t, pg, usage, ctx)
	old := time.Now().Add(-time.Hour)
	ids := seedUsageEventsAt(t, pg.Pool, ctx, []struct {
		xid uint64
		at  time.Time
	}{{2, old}, {1, old}, {3, old}})

	// Act
	first, firstErr := usage.ReadEvents(ctx, domain.UsageCursor{}, 2)
	rest, restErr := usage.ReadEvents(ctx, domain.UsageCursor{Xid: 2, ID: ids[0]}, 100)

	// Assert
	if firstErr != nil || restErr != nil {
		t.Fatalf("ReadEvents: %v, %v", firstErr, restErr)
	}
	if !first.Enabled || first.Gone {
		t.Fatalf("page = %+v, want enabled and not gone", first)
	}
	got := eventsWithIDs(first, ids)
	if len(first.Events) != 2 || len(got) != 2 || got[0].Cursor != (domain.UsageCursor{Xid: 1, ID: ids[1]}) || got[1].Cursor != (domain.UsageCursor{Xid: 2, ID: ids[0]}) {
		t.Fatalf("first page = %+v, want the events of xid 1 then 2", first.Events)
	}
	if got[0].Tenant != "default" || got[0].DagID != "" || got[0].RunID != "" || got[0].State != domain.TaskStateSuccess {
		t.Errorf("event = %+v, want tenant default, no run, success", got[0])
	}
	if next := eventsWithIDs(rest, ids); len(next) != 1 || next[0].Cursor.ID != ids[2] {
		t.Errorf("after xid 2 = %+v, want only the event of xid 3", next)
	}
}

// TestUsageEventsWaitForOlderTransactions covers the cursor rule of ADR 0096
// point 4: an event is served only once every transaction older than it has
// finished, so a reader that moved past it can never miss an earlier commit.
func TestUsageEventsWaitForOlderTransactions(t *testing.T) {
	// Arrange
	pg, repo, sched, _, ctx := openExecPG(t)
	usage := storage.NewUsageStore(pg)
	enableUsageEvents(t, pg, usage, ctx)
	dagID := fmt.Sprintf("usage_horizon_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")
	older, err := pg.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = older.Rollback(context.Background()) }()
	var olderXid uint64
	if err := older.QueryRow(ctx, `SELECT pg_current_xact_id()::text::bigint`).Scan(&olderXid); err != nil {
		t.Fatal(err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateSuccess); err != nil {
		t.Fatalf("ApplyTransition: %v", err)
	}
	from := domain.UsageCursor{Xid: olderXid}

	// Act
	held, heldErr := usage.ReadEvents(ctx, from, 100)
	lag, lagErr := usage.EventsLag(ctx)
	if err := older.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	served, servedErr := usage.ReadEvents(ctx, from, 100)

	// Assert
	if heldErr != nil || servedErr != nil || lagErr != nil {
		t.Fatalf("ReadEvents: %v, %v; EventsLag: %v", heldErr, servedErr, lagErr)
	}
	for _, e := range held.Events {
		if e.DagID == dagID {
			t.Fatalf("event %+v served while an older transaction was open", e)
		}
	}
	if lag <= 0 {
		t.Errorf("lag = %v with an event held back, want above 0", lag)
	}
	var mine []domain.UsageEvent
	for _, e := range served.Events {
		if e.DagID == dagID {
			mine = append(mine, e)
		}
	}
	if len(mine) != 1 || mine[0].RunID == "" || mine[0].TaskID != "load" || mine[0].StartedAt == nil || mine[0].EndedAt == nil {
		t.Errorf("events of the run after the older transaction ended = %+v, want the one settle with its run and times", mine)
	}
}

// TestUsageEventsReadStates covers points 5 and 7: with recording off the page
// says so, and a cursor below the pruned mark is gone, with the mark as the
// oldest complete cursor.
func TestUsageEventsReadStates(t *testing.T) {
	// Arrange
	pg, _, _, _, ctx := openExecPG(t)
	usage := storage.NewUsageStore(pg)
	old := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	ids := seedUsageEventsAt(t, pg.Pool, ctx, []struct {
		xid uint64
		at  time.Time
	}{{1, old}, {2, old}, {3, time.Now()}})
	off, offErr := usage.ReadEvents(ctx, domain.UsageCursor{}, 10)
	enableUsageEvents(t, pg, usage, ctx)
	if _, err := storage.NewRetentionStore(pg).DeleteUsageEvents(ctx, time.Date(2002, 1, 1, 0, 0, 0, 0, time.UTC), 10); err != nil {
		t.Fatalf("DeleteUsageEvents: %v", err)
	}

	// Act
	gone, goneErr := usage.ReadEvents(ctx, domain.UsageCursor{Xid: 1, ID: ids[0]}, 10)
	atMark, atMarkErr := usage.ReadEvents(ctx, domain.UsageCursor{Xid: 2, ID: ids[1]}, 10)
	fresh, freshErr := usage.ReadEvents(ctx, domain.UsageCursor{}, 10)

	// Assert
	for name, err := range map[string]error{"off": offErr, "gone": goneErr, "at mark": atMarkErr, "fresh": freshErr} {
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if off.Enabled || len(off.Events) != 0 {
		t.Errorf("page with recording off = %+v, want not enabled and no events", off)
	}
	if !gone.Gone || gone.Oldest != (domain.UsageCursor{Xid: 2, ID: ids[1]}) || len(gone.Events) != 0 {
		t.Errorf("page below the mark = %+v, want gone with the mark as oldest", gone)
	}
	if atMark.Gone || len(eventsWithIDs(atMark, ids)) != 1 {
		t.Errorf("page at the mark = %+v, want the one event left", atMark)
	}
	if fresh.Gone || len(eventsWithIDs(fresh, ids)) != 1 {
		t.Errorf("page from the start = %+v, want not gone: a new consumer starts at the oldest kept event", fresh)
	}
}
