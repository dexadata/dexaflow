package scheduler

import (
	"context"
	"testing"
	"time"
)

func utc(year int, month time.Month, day, hour, minute int) time.Time {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
}

// A DAG whose start_date is in America/Sao_Paulo (UTC-3, no daylight saving
// time since 2019) runs "0 6 * * *" at 06:00 there, 09:00 UTC, as Airflow
// does (#2453). Before, the fields were read in UTC and it ran at 03:00 local.
func TestDueSlotsReadsTheCronInTheDAGTimezone(t *testing.T) {
	start := utc(2026, 1, 1, 3, 0) // midnight in Sao Paulo
	now := utc(2026, 1, 3, 10, 0)
	d := ScheduledDAG{Schedule: "0 6 * * *", Timezone: "America/Sao_Paulo", StartDate: &start, Catchup: true}

	got := dueSlots(d, now)

	want := []time.Time{utc(2026, 1, 1, 9, 0), utc(2026, 1, 2, 9, 0), utc(2026, 1, 3, 9, 0)}
	assertSlots(t, got, want)

	d.Catchup = false
	assertSlots(t, dueSlots(d, now), want[2:])
}

// The first run of a DAG with neither a start_date nor a run is the latest
// slot at or before now, in the DAG's zone.
func TestDueSlotsFirstSightUsesTheDAGTimezone(t *testing.T) {
	d := ScheduledDAG{Schedule: "0 6 * * *", Timezone: "America/Sao_Paulo"}
	assertSlots(t, dueSlots(d, utc(2026, 1, 3, 8, 0)), []time.Time{utc(2026, 1, 2, 9, 0)})
	assertSlots(t, dueSlots(d, utc(2026, 1, 3, 9, 0)), []time.Time{utc(2026, 1, 3, 9, 0)})
}

// Daylight saving time moves the UTC time of a slot: in New York 06:00 is
// 11:00 UTC before 2026-03-08 and 10:00 UTC from that day on.
func TestDueSlotsFollowsDaylightSavingTime(t *testing.T) {
	last := utc(2026, 3, 7, 11, 0)
	d := ScheduledDAG{Schedule: "0 6 * * *", Timezone: "America/New_York", LastLogical: &last, Catchup: true}

	assertSlots(t, dueSlots(d, utc(2026, 3, 9, 12, 0)), []time.Time{utc(2026, 3, 8, 10, 0), utc(2026, 3, 9, 10, 0)})

	next, ok := nextDueAt(d, utc(2026, 3, 7, 12, 0))
	if want := utc(2026, 3, 8, 10, 0).Add(-nextDueMargin); !ok || !next.Equal(want) {
		t.Errorf("nextDueAt = %v %v, want %v", next, ok, want)
	}
}

// The first sight on the day the clocks change finds that day's slot. The
// old search stepped back one period measured after now (24 hours), which
// across the change lands on the day before.
func TestDueSlotsFirstSightOnTheDaylightSavingDay(t *testing.T) {
	d := ScheduledDAG{Schedule: "0 6 * * *", Timezone: "America/New_York"}
	assertSlots(t, dueSlots(d, utc(2026, 3, 8, 10, 30)), []time.Time{utc(2026, 3, 8, 10, 0)})
}

// An unknown zone cannot be scheduled: nothing is due and there is no next
// time, as for an unparseable expression.
func TestUnknownTimezoneIsNeverDue(t *testing.T) {
	d := ScheduledDAG{Schedule: "0 6 * * *", Timezone: "Mars/Olympus"}
	if got := dueSlots(d, utc(2026, 1, 3, 10, 0)); len(got) != 0 {
		t.Errorf("dueSlots = %v, want none", got)
	}
	if next, ok := nextDueAt(d, utc(2026, 1, 3, 10, 0)); ok {
		t.Errorf("nextDueAt = %v, want never", next)
	}
	if scheduleParseable(d.Schedule, d.Timezone) {
		t.Error("scheduleParseable = true for an unknown zone")
	}
}

// The tick passes the DAG's zone through: a Step with a Sao Paulo DAG
// creates its run at 09:00 UTC and stores the next due time a day later.
func TestStepCreatesTheRunInTheDAGTimezone(t *testing.T) {
	store := newFakeStore()
	last := utc(2026, 1, 1, 9, 0)
	store.scheduled = []ScheduledDAG{{
		DagID: "sp", Schedule: "0 6 * * *", Timezone: "America/Sao_Paulo",
		LastLogical: &last, MaxActiveRuns: 16,
	}}
	s := newScheduler(store)
	s.clock = func() time.Time { return utc(2026, 1, 2, 9, 30) }

	if err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}

	assertSlots(t, store.createdLogical, []time.Time{utc(2026, 1, 2, 9, 0)})
}

func assertSlots(t *testing.T, got, want []time.Time) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("slots = %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("slots = %v, want %v", got, want)
		}
	}
}
