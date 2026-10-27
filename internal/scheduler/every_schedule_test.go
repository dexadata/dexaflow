package scheduler

import (
	"context"
	"testing"
	"time"
)

// #2187: an @every schedule has no wall-clock grid: the cron library computes its
// next slot from whatever time it is given. With catchup=false the planner
// looks for the latest slot at or before now by asking from a window before
// now, and for @every that always lands on now itself, so every tick found a
// slot after the last run and created one: a DAG on "@every 1h" ran once per
// scheduler tick. The slots of an @every DAG follow from its last run (or its
// start_date) in steps of the interval, as with catchup=true.

// TestStepEveryScheduleWaitsForTheInterval: a DAG on @every 1h that ran a
// minute ago gets no run on this tick.
func TestStepEveryScheduleWaitsForTheInterval(t *testing.T) {
	// Arrange
	last := time.Now().UTC().Add(-time.Minute)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{DagID: "hourly", Schedule: "@every 1h", LastLogical: &last, MaxActiveRuns: 16}}

	// Act
	err := newScheduler(store).Step(context.Background())

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	if len(store.createdLogical) != 0 {
		t.Errorf("created %v a minute after the last run, want nothing until the hour is up", store.createdLogical)
	}
}

// TestStepEveryScheduleCreatesTheLatestSlotOnItsGrid: once the interval has
// passed, catchup=false creates one run, dated at the latest step of the
// interval after the last run, not at the time of the tick.
func TestStepEveryScheduleCreatesTheLatestSlotOnItsGrid(t *testing.T) {
	// Arrange
	last := time.Now().UTC().Add(-150 * time.Minute).Truncate(time.Second)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{DagID: "hourly", Schedule: "@every 1h", LastLogical: &last, MaxActiveRuns: 16}}

	// Act
	err := newScheduler(store).Step(context.Background())

	// Assert
	if err != nil {
		t.Fatal(err)
	}
	want := last.Add(2 * time.Hour)
	if len(store.createdLogical) != 1 || !store.createdLogical[0].Equal(want) {
		t.Errorf("created %v, want one run at %s (two intervals after the last run)", store.createdLogical, want)
	}
}

// TestLatestDueSlotEveryHonorsTheStartDate: a DAG that never ran is anchored
// on its start_date, and nothing is due before the start_date.
func TestLatestDueSlotEveryHonorsTheStartDate(t *testing.T) {
	// Arrange
	now := time.Date(2026, 10, 26, 10, 17, 0, 0, time.UTC)
	past := time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)

	// Act
	fromPast := dueScheduledSlots("@every 1h", nil, &past, now, false, maxCatchupSlotsPerTick)
	fromFuture := dueScheduledSlots("@every 1h", nil, &future, now, false, maxCatchupSlotsPerTick)

	// Assert
	if want := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC); len(fromPast) != 1 || !fromPast[0].Equal(want) {
		t.Errorf("start_date in the past: slots %v, want [%s]", fromPast, want)
	}
	if len(fromFuture) != 0 {
		t.Errorf("start_date in the future: slots %v, want none", fromFuture)
	}
}
