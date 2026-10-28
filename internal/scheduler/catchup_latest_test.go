package scheduler

import (
	"context"
	"testing"
	"time"
)

// TestStepCatchupFalseFarPastStartCreatesOnlyLatest drives the scheduler tick
// with a DAG whose start_date lies far more than maxCatchupSlotsPerTick slots
// in the past and catchup=false (#1919). Airflow 3.2 schedules only the most
// recent interval and skips the older ones, so one tick creates exactly one
// run, dated at the latest slot at or before now. Before the fix the planner
// stopped at the 100th slot after the start_date and returned that one, so
// the run was dated 2026-01-01T01:39 for a start_date of 2026-01-01.
func TestStepCatchupFalseFarPastStartCreatesOnlyLatest(t *testing.T) {
	start := time.Now().UTC().Add(-90 * 24 * time.Hour).Truncate(24 * time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "every_minute", Schedule: "* * * * *", StartDate: &start,
		Catchup: false, MaxActiveRuns: 16,
	}}
	before := time.Now().UTC()
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()

	if len(store.createdLogical) != 1 {
		t.Fatalf("catchup=false should create exactly one run, got %d: %v",
			len(store.createdLogical), store.createdLogical)
	}
	assertLatestMinuteSlot(t, store.createdLogical[0], before, after)
}

// TestStepCatchupFalseLongGapAfterLastRunCreatesOnlyLatest is the same rule
// for a DAG that already ran: a gap of more than maxCatchupSlotsPerTick slots
// since its last logical date still yields the most recent slot, not the
// 100th one after the last run (#1919).
func TestStepCatchupFalseLongGapAfterLastRunCreatesOnlyLatest(t *testing.T) {
	last := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Minute)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "every_minute", Schedule: "* * * * *", LastLogical: &last,
		Catchup: false, MaxActiveRuns: 16,
	}}
	before := time.Now().UTC()
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()

	if len(store.createdLogical) != 1 {
		t.Fatalf("catchup=false should create exactly one run, got %d: %v",
			len(store.createdLogical), store.createdLogical)
	}
	assertLatestMinuteSlot(t, store.createdLogical[0], before, after)
}

// TestStepCatchupTrueFarPastStartStillBackfills keeps the catchup=true side
// of the rule: the same far-past start_date backfills from the start_date,
// oldest first, capped per tick by max_active_runs.
func TestStepCatchupTrueFarPastStartStillBackfills(t *testing.T) {
	start := time.Now().UTC().Add(-90 * 24 * time.Hour).Truncate(24 * time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{
		DagID: "every_minute", Schedule: "* * * * *", StartDate: &start,
		Catchup: true, MaxActiveRuns: 16,
	}}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdLogical) != 16 {
		t.Fatalf("catchup=true should backfill up to max_active_runs (16), got %d", len(store.createdLogical))
	}
	for i, got := range store.createdLogical {
		if want := start.Add(time.Duration(i) * time.Minute); !got.Equal(want) {
			t.Errorf("run %d: logical date %s, want %s", i, got, want)
		}
	}
}

// assertLatestMinuteSlot checks that got is the minute slot at or before the
// tick's wall clock, which lies between before and after.
func assertLatestMinuteSlot(t *testing.T, got, before, after time.Time) {
	t.Helper()
	lo, hi := before.Truncate(time.Minute), after.Truncate(time.Minute)
	if got.Before(lo) || got.After(hi) {
		t.Errorf("logical date %s, want the latest minute slot (between %s and %s)", got, lo, hi)
	}
}

// TestDueScheduledSlotsCatchupFalseIrregularSchedule pins the latest slot for
// a schedule whose gaps are uneven: on a Monday morning the latest weekday
// 09:00 slot is the previous Friday, three days back, more than one period
// estimated from the slots after now.
func TestDueScheduledSlotsCatchupFalseIrregularSchedule(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC) // a Monday
	got := dueScheduledSlots("0 9 * * 1-5", nil, &start, now, false, maxCatchupSlotsPerTick)
	want := time.Date(2026, 5, 29, 9, 0, 0, 0, time.UTC) // the Friday before
	if len(got) != 1 || !got[0].Equal(want) {
		t.Errorf("got %v, want [%s]", got, want)
	}
}

// TestDueScheduledSlotsCatchupFalseRespectsFloors checks the two floors of the
// catchup=false shortcut: nothing is due when the latest slot is not after the
// last logical date, or lies before the start_date.
func TestDueScheduledSlotsCatchupFalseRespectsFloors(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 30, 0, 0, time.UTC)
	last := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	if got := dueScheduledSlots("@hourly", &last, nil, now, false, maxCatchupSlotsPerTick); len(got) != 0 {
		t.Errorf("latest slot equals last logical: got %v, want none", got)
	}
	start := time.Date(2026, 6, 1, 12, 15, 0, 0, time.UTC)
	if got := dueScheduledSlots("@hourly", nil, &start, now, false, maxCatchupSlotsPerTick); len(got) != 0 {
		t.Errorf("latest slot before start_date: got %v, want none", got)
	}
}
