package retention

import (
	"context"
	"testing"
	"time"
)

// TestRunOnceDeletesUsageEventsUntilABatchComesBackShort covers ADR 0096
// point 5: the usage events are one lane across all tenants, deleted in
// paused batches older than the window, and counted under their own table.
func TestRunOnceDeletesUsageEventsUntilABatchComesBackShort(t *testing.T) {
	// Arrange
	store := &fakeStore{tenants: []string{"t1"}, usageBatches: []int64{1000, 1000, 3}}
	cfg := baseConfig()
	cfg.UsageEventsDays = 30
	rec := newFakeRecorder()
	j, pauses := newTestJanitor(store, cfg, rec)

	// Act
	got, err := j.RunOnce(context.Background())

	// Assert
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got.UsageEvents != 2003 || got.Total() != 2003 {
		t.Fatalf("counts = %+v, want 2003 usage events and nothing else", got)
	}
	if len(store.usageCalls) != 3 || len(store.runCalls) != 0 || len(store.auditCalls) != 0 {
		t.Fatalf("calls: usage=%d runs=%d audit=%d, want three usage batches only", len(store.usageCalls), len(store.runCalls), len(store.auditCalls))
	}
	want := fixedNow.Add(-30 * 24 * time.Hour)
	for i, c := range store.usageCalls {
		if !c.cutoff.Equal(want) || c.limit != 1000 {
			t.Errorf("call %d = %+v, want cutoff %v and limit 1000", i, c, want)
		}
	}
	if rec.deleted[TableUsageEvents] != 2003 {
		t.Errorf("deleted metric = %v, want 2003 under %s", rec.deleted, TableUsageEvents)
	}
	if len(*pauses) != 3 {
		t.Errorf("pauses = %v, want one after each batch that deleted rows", *pauses)
	}
	if len(store.purges) != 0 {
		t.Errorf("purge records = %+v, want none: only audit purges are audited", store.purges)
	}
}

// TestRunOnceDryRunCountsUsageEvents: a dry run passes the usage window to the
// count and sets its gauge.
func TestRunOnceDryRunCountsUsageEvents(t *testing.T) {
	// Arrange
	store := &fakeStore{tenants: []string{"t1"}, eligible: Counts{UsageEvents: 42}}
	cfg := baseConfig()
	cfg.UsageEventsDays = 7
	cfg.DryRun = true
	rec := newFakeRecorder()
	j, _ := newTestJanitor(store, cfg, rec)

	// Act
	_, err := j.RunOnce(context.Background())

	// Assert
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(store.usageCalls) != 0 {
		t.Fatalf("dry run deleted usage events: %+v", store.usageCalls)
	}
	usage := store.countCutoff[2]
	if store.countCutoff[0] != nil || store.countCutoff[1] != nil || usage == nil || !usage.Equal(fixedNow.Add(-7*24*time.Hour)) {
		t.Fatalf("count cutoffs = %v, want only the usage window", store.countCutoff)
	}
	if rec.eligible[TableUsageEvents] != 42 {
		t.Errorf("eligible gauge = %v, want 42 under %s", rec.eligible, TableUsageEvents)
	}
}
