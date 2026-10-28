package main

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// fakeUsageSource serves queued pages and records the cursors it was read at.
type fakeUsageSource struct {
	horizon  domain.UsageCursor
	pages    []domain.UsagePage
	reads    []domain.UsageCursor
	horizons int
}

func (f *fakeUsageSource) Horizon(context.Context) (domain.UsageCursor, error) {
	f.horizons++
	return f.horizon, nil
}

func (f *fakeUsageSource) ReadEvents(_ context.Context, after domain.UsageCursor, _ int) (domain.UsagePage, error) {
	f.reads = append(f.reads, after)
	if len(f.pages) == 0 {
		return domain.UsagePage{Enabled: true}, nil
	}
	p := f.pages[0]
	f.pages = f.pages[1:]
	return p, nil
}

// fakeUsageCounters sums what the counter records.
type fakeUsageCounters struct {
	attempts    map[[2]string]int
	slotSeconds map[string]float64
}

func newFakeUsageCounters() *fakeUsageCounters {
	return &fakeUsageCounters{attempts: map[[2]string]int{}, slotSeconds: map[string]float64{}}
}

func (f *fakeUsageCounters) RecordTaskAttempt(tenant, state string, slotSeconds float64) {
	f.attempts[[2]string{tenant, state}]++
	f.slotSeconds[tenant] += slotSeconds
}

func usageEvent(xid uint64, id int64, tenant string, state domain.TaskState, revision, slots int, dur time.Duration) domain.UsageEvent {
	e := domain.UsageEvent{Cursor: domain.UsageCursor{Xid: xid, ID: id}, Tenant: tenant, State: state, Revision: revision, PoolSlots: slots}
	if dur > 0 {
		start := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
		end := start.Add(dur)
		e.StartedAt, e.EndedAt = &start, &end
	}
	return e
}

// TestUsageCounterStartsAtTheHorizonAndCountsFirstRevisions covers ADR 0096
// point 6: a leader that takes over starts at the current horizon instead of
// replaying what is kept, then counts each attempt once, by its first state,
// with its size times its duration, and follows the pages to the end.
func TestUsageCounterStartsAtTheHorizonAndCountsFirstRevisions(t *testing.T) {
	// Arrange
	src := &fakeUsageSource{horizon: domain.UsageCursor{Xid: 100}, pages: []domain.UsagePage{
		{Enabled: true, Events: []domain.UsageEvent{
			usageEvent(101, 1, "acme", domain.TaskStateSuccess, 0, 2, 90*time.Second),
			usageEvent(101, 2, "acme", domain.TaskStateFailed, 1, 2, 90*time.Second),
		}},
		{Enabled: true, Events: []domain.UsageEvent{usageEvent(102, 3, "beta", domain.TaskStateSkipped, 0, 1, 0)}},
	}}
	rec := newFakeUsageCounters()
	c := newUsageCounter(src, rec, func() bool { return true }, 2, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	// Act
	c.cycle(context.Background())
	afterTakeover := len(src.reads)
	c.cycle(context.Background())

	// Assert
	if afterTakeover != 0 || src.horizons != 1 {
		t.Fatalf("takeover cycle read %d pages and %d horizons, want only the horizon", afterTakeover, src.horizons)
	}
	if len(src.reads) != 2 || src.reads[0] != (domain.UsageCursor{Xid: 100}) || src.reads[1] != (domain.UsageCursor{Xid: 101, ID: 2}) {
		t.Fatalf("reads = %+v, want the horizon, then after the full page, and no read after the short one", src.reads)
	}
	if c.cursor == nil || *c.cursor != (domain.UsageCursor{Xid: 102, ID: 3}) {
		t.Errorf("cursor = %+v, want after the last event counted", c.cursor)
	}
	if rec.attempts[[2]string{"acme", "success"}] != 1 || rec.attempts[[2]string{"acme", "failed"}] != 0 || rec.attempts[[2]string{"beta", "skipped"}] != 1 {
		t.Errorf("attempts = %v, want each attempt once by its first state", rec.attempts)
	}
	if rec.slotSeconds["acme"] != 180 || rec.slotSeconds["beta"] != 0 {
		t.Errorf("slot seconds = %v, want 2 slots for 90 s for acme and nothing for a skip", rec.slotSeconds)
	}
}

// TestUsageCounterRestartsAfterLosingLeadership: a replica that loses the lead
// drops its cursor, so when it leads again it starts at the new horizon and
// never counts what another leader counted meanwhile.
func TestUsageCounterRestartsAfterLosingLeadership(t *testing.T) {
	// Arrange
	src := &fakeUsageSource{horizon: domain.UsageCursor{Xid: 10}}
	leading := true
	c := newUsageCounter(src, newFakeUsageCounters(), func() bool { return leading }, 100, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	// Act
	c.cycle(context.Background())
	leading = false
	c.cycle(context.Background())
	leading = true
	src.horizon = domain.UsageCursor{Xid: 50}
	c.cycle(context.Background())
	c.cycle(context.Background())

	// Assert
	if src.horizons != 2 {
		t.Fatalf("horizons read = %d, want one per takeover", src.horizons)
	}
	if len(src.reads) != 1 || src.reads[0] != (domain.UsageCursor{Xid: 50}) {
		t.Errorf("reads = %+v, want one read from the new horizon", src.reads)
	}
}

// TestUsageCounterSkipsAGapAndWaitsWhileOff: a cursor that fell behind
// retention resumes at the oldest complete cursor in the same cycle, and a
// page that says recording is off ends the cycle and keeps the cursor.
func TestUsageCounterSkipsAGapAndWaitsWhileOff(t *testing.T) {
	// Arrange
	var logs bytes.Buffer
	src := &fakeUsageSource{horizon: domain.UsageCursor{Xid: 1}, pages: []domain.UsagePage{
		{Enabled: true, Gone: true, Oldest: domain.UsageCursor{Xid: 40, ID: 7}},
		{Enabled: false},
	}}
	c := newUsageCounter(src, newFakeUsageCounters(), func() bool { return true }, 100, slog.New(slog.NewTextHandler(&logs, nil)))

	// Act
	for range 3 {
		c.cycle(context.Background())
	}

	// Assert
	want := []domain.UsageCursor{{Xid: 1}, {Xid: 40, ID: 7}, {Xid: 40, ID: 7}}
	if len(src.reads) != len(want) {
		t.Fatalf("reads = %+v, want %+v", src.reads, want)
	}
	for i := range want {
		if src.reads[i] != want[i] {
			t.Errorf("read %d at %+v, want %+v", i, src.reads[i], want[i])
		}
	}
	if !bytes.Contains(logs.Bytes(), []byte("usage counters skipped events deleted by retention")) {
		t.Errorf("no warning for the gap:\n%s", logs.String())
	}
}

// TestStartUsageCountersNeedsBothSettings: the counters run only with usage
// metrics and recording both on, and say so when recording is missing.
func TestStartUsageCountersNeedsBothSettings(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	src, rec, never := &fakeUsageSource{}, newFakeUsageCounters(), func() bool { return false }

	// Act
	metricsOff := startUsageCounters(ctx, true, false, src, rec, never, logger)
	recordingOff := startUsageCounters(ctx, false, true, src, rec, never, logger)
	both := startUsageCounters(ctx, true, true, src, rec, never, logger)

	// Assert
	if metricsOff || recordingOff || !both {
		t.Fatalf("started: metrics off %v, recording off %v, both %v; want only both", metricsOff, recordingOff, both)
	}
	if !bytes.Contains(logs.Bytes(), []byte("usage.events.enabled is off")) {
		t.Errorf("no warning that recording is off:\n%s", logs.String())
	}
}
