package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// The scheduler stores, for each DAG it looks at, when the DAG is next due
// (#2175), so the store can leave it out of later ticks until then. It must
// never store a time later than the planner would act, and it stores nothing
// while the DAG still has a due slot it could not create.

var nextRunNow = time.Date(2026, 10, 26, 10, 17, 0, 0, time.UTC)

// stepAt runs one tick of a scheduler whose clock reads nextRunNow.
func stepAt(t *testing.T, store *fakeStore, rec *fakeRecorder) {
	t.Helper()
	s := newScheduler(store)
	s.clock = func() time.Time { return nextRunNow }
	if rec != nil {
		s.SetRecorder(rec)
	}
	if err := s.Step(context.Background()); err != nil {
		t.Fatalf("Step: %v", err)
	}
}

func hourAt(h int) *time.Time {
	v := time.Date(2026, 10, 26, h, 0, 0, 0, time.UTC)
	return &v
}

// TestStepStoresTheNextRunAfterCreatingOne: an hourly DAG last run at 09:00
// gets its 10:00 run, and the next run stored is 11:00 (less the margin),
// conditioned on the epoch the list returned. The list is asked with the
// scheduler's own clock.
func TestStepStoresTheNextRunAfterCreatingOne(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{TenantID: tenantA, DagID: "etl", Schedule: "@hourly", LastLogical: hourAt(9), MaxActiveRuns: 16, ScheduleEpoch: 7}}
	rec := &fakeRecorder{}

	// Act
	stepAt(t, store, rec)

	// Assert
	if len(store.createdLogical) != 1 || !store.createdLogical[0].Equal(*hourAt(10)) {
		t.Fatalf("created %v, want the 10:00 run", store.createdLogical)
	}
	got, ok := store.nextRuns[dagRef{tenantA, "etl"}]
	if want := hourAt(11).Add(-nextDueMargin); !ok || got.epoch != 7 || !got.at.Equal(want) {
		t.Errorf("next run write = %+v (written %v), want %s at epoch 7", got, ok, want)
	}
	if len(store.listedAt) != 1 || !store.listedAt[0].Equal(nextRunNow) {
		t.Errorf("ScheduledDAGs asked with %v, want the tick's clock %s", store.listedAt, nextRunNow)
	}
	if len(rec.scheduleDelays) != 1 || rec.scheduleDelays[0] != 17*time.Minute {
		t.Errorf("schedule delays = %v, want one of 17m (10:00 created at 10:17)", rec.scheduleDelays)
	}
}

// TestStepStoresTheNextRunOfADAGNotDueYet: a DAG listed without a stored time
// and nothing due gets its next time stored, and no run.
func TestStepStoresTheNextRunOfADAGNotDueYet(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{TenantID: tenantA, DagID: "etl", Schedule: "@hourly", LastLogical: hourAt(10), MaxActiveRuns: 16}}

	// Act
	stepAt(t, store, nil)

	// Assert
	if len(store.createdLogical) != 0 {
		t.Errorf("created %v, want nothing before 11:00", store.createdLogical)
	}
	if got := store.nextRuns[dagRef{tenantA, "etl"}]; !got.at.Equal(hourAt(11).Add(-nextDueMargin)) {
		t.Errorf("next run stored = %s, want 11:00 less the margin", got.at)
	}
}

// TestStepSkipsAnUnchangedNextRun: a DAG whose stored time is already right is
// not written again.
func TestStepSkipsAnUnchangedNextRun(t *testing.T) {
	// Arrange
	stored := hourAt(11).Add(-nextDueMargin)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{TenantID: tenantA, DagID: "etl", Schedule: "@hourly", LastLogical: hourAt(10), MaxActiveRuns: 16, NextRunAt: &stored}}

	// Act
	stepAt(t, store, nil)

	// Assert
	if len(store.nextRuns) != 0 {
		t.Errorf("next run writes = %v, want none for an unchanged time", store.nextRuns)
	}
}

// TestStepKeepsADAGWithASlotItCouldNotCreate: a due slot held back by
// max_active_runs, by the tenant's daily cap, or by a failed insert leaves the
// stored time alone, so the DAG is listed again on the next tick.
func TestStepKeepsADAGWithASlotItCouldNotCreate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeStore)
	}{
		{name: "max_active_runs", setup: func(f *fakeStore) {
			f.scheduled[0].MaxActiveRuns = 1
			f.runs = []RunState{{RunID: "r1", TenantID: tenantA, DagID: "etl", State: domain.DagRunStateRunning}}
		}},
		{name: "daily cap", setup: func(f *fakeStore) { f.limitedTenants = map[string]bool{tenantA: true} }},
		{name: "create error", setup: func(f *fakeStore) { f.createErr = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			store := newFakeStore()
			store.scheduled = []ScheduledDAG{{TenantID: tenantA, DagID: "etl", Schedule: "@hourly", LastLogical: hourAt(9), MaxActiveRuns: 16}}
			tc.setup(store)

			// Act
			stepAt(t, store, nil)

			// Assert
			if w, ok := store.nextRuns[dagRef{tenantA, "etl"}]; ok {
				t.Errorf("next run stored %+v, want none while 10:00 is still due", w)
			}
		})
	}
}

// TestStepStoresTheHorizonForADAGWithNothingToSchedule: a continuous DAG, an
// @once DAG that already ran, and an unparseable schedule are stored an hour
// ahead, the horizon, so they are looked at hourly instead of every tick.
func TestStepStoresTheHorizonForADAGWithNothingToSchedule(t *testing.T) {
	for _, tc := range []struct {
		name     string
		schedule string
		last     *time.Time
	}{
		{name: "continuous", schedule: "@continuous"},
		{name: "once, already ran", schedule: "@once", last: hourAt(9)},
		{name: "unparseable", schedule: "* * * *"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			store := newFakeStore()
			store.scheduled = []ScheduledDAG{{TenantID: tenantA, DagID: "etl", Schedule: tc.schedule, LastLogical: tc.last, MaxActiveRuns: 16}}

			// Act
			stepAt(t, store, nil)

			// Assert
			w, ok := store.nextRuns[dagRef{tenantA, "etl"}]
			if !ok || !w.at.Equal(nextRunNow.Add(horizonOf(tenantA, "etl"))) {
				t.Errorf("next run write = %+v (written %v), want the horizon", w, ok)
			}
		})
	}
}

// TestStepStoresTheHorizonAfterTheOnceRun: the @once DAG gets its run and is
// then stored at the horizon.
func TestStepStoresTheHorizonAfterTheOnceRun(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{TenantID: tenantA, DagID: "etl", Schedule: "@once", MaxActiveRuns: 16}}

	// Act
	stepAt(t, store, nil)

	// Assert
	w, ok := store.nextRuns[dagRef{tenantA, "etl"}]
	if len(store.createdLogical) != 1 || !ok || !w.at.Equal(nextRunNow.Add(horizonOf(tenantA, "etl"))) {
		t.Errorf("created %v, next run write %+v (written %v); want one run, then the horizon", store.createdLogical, w, ok)
	}
}

// TestStepStoresAPastNextRunForACatchupCutShort: a catchup that hits the
// per-tick cap stores the next missed slot, which is already due, so the DAG
// is listed again on the next tick and the backfill goes on.
func TestStepStoresAPastNextRunForACatchupCutShort(t *testing.T) {
	// Arrange
	last := nextRunNow.Add(-200 * time.Hour).Truncate(time.Hour)
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{TenantID: tenantA, DagID: "etl", Schedule: "@hourly", LastLogical: &last, Catchup: true}}

	// Act
	stepAt(t, store, nil)

	// Assert
	if len(store.createdLogical) != maxCatchupSlotsPerTick {
		t.Fatalf("created %d runs, want the per-tick cap %d", len(store.createdLogical), maxCatchupSlotsPerTick)
	}
	w := store.nextRuns[dagRef{tenantA, "etl"}]
	if want := store.createdLogical[len(store.createdLogical)-1].Add(time.Hour - nextDueMargin); !w.at.Equal(want) || w.at.After(nextRunNow) {
		t.Errorf("next run stored = %s, want %s, at or before now", w.at, want)
	}
}

// TestStepCapsADistantNextRunAtTheHorizon: a daily DAG that just ran is next
// due tomorrow, but is stored an hour ahead.
func TestStepCapsADistantNextRunAtTheHorizon(t *testing.T) {
	// Arrange
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{{TenantID: tenantA, DagID: "etl", Schedule: "@daily", LastLogical: hourAt(0), MaxActiveRuns: 16}}

	// Act
	stepAt(t, store, nil)

	// Assert
	if w := store.nextRuns[dagRef{tenantA, "etl"}]; !w.at.Equal(nextRunNow.Add(horizonOf(tenantA, "etl"))) {
		t.Errorf("next run stored = %s, want the horizon %s", w.at, nextRunNow.Add(horizonOf(tenantA, "etl")))
	}
}

// TestStepSpreadsTheHorizonAcrossDAGs: DAGs first listed in the same tick,
// as every DAG is after the upgrade, must not all come back in the same tick
// an hour later, and then every hour after that. Each DAG's horizon is fixed
// for that DAG and falls between half the horizon and the horizon, so a
// change that did not clear the stored time is still picked up within the
// hour and the idle DAGs are spread over the second half of it.
func TestStepSpreadsTheHorizonAcrossDAGs(t *testing.T) {
	// Arrange
	const n = 1000
	store := newFakeStore()
	for i := 0; i < n; i++ {
		store.scheduled = append(store.scheduled, ScheduledDAG{TenantID: tenantA, DagID: fmt.Sprintf("dag-%04d", i), Schedule: "@daily", LastLogical: hourAt(0), MaxActiveRuns: 16})
	}

	// Act
	stepAt(t, store, nil)

	// Assert
	perMinute := map[int64]int{}
	for i := 0; i < n; i++ {
		w, ok := store.nextRuns[dagRef{tenantA, fmt.Sprintf("dag-%04d", i)}]
		ahead := w.at.Sub(nextRunNow)
		if !ok || ahead < nextRunHorizon/2 || ahead > nextRunHorizon {
			t.Fatalf("dag-%04d stored %s ahead (written %v), want between %s and %s", i, ahead, ok, nextRunHorizon/2, nextRunHorizon)
		}
		perMinute[int64(ahead/time.Minute)]++
	}
	for minute, count := range perMinute {
		if count > n/10 {
			t.Errorf("%d of %d DAGs come back in minute %d; the horizon does not spread them", count, n, minute)
		}
	}
}
