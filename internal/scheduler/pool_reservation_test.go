package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// A large task is not starved (ADR 0066 §4): once a task that does not fit its
// pool has waited past the starvation threshold, the pool is reserved for it
// and admits nothing else until it fits.

// --- PlanRun: how a reservation gates a run ---

// TestPlanRunReservedPoolHoldsOtherTasks: a pool reserved for a task of
// another run admits none of this run's tasks, even ones that fit; a task in
// another pool is unaffected.
func TestPlanRunReservedPoolHoldsOtherTasks(t *testing.T) {
	// Arrange
	tasks := []domain.TaskSpec{
		{TaskID: "small", Type: domain.TaskTypePython, Pool: "p"},
		{TaskID: "other", Type: domain.TaskTypePython, Pool: "q"},
	}
	run := poolRun(tasks, map[string]int{PoolKey(testTenant, "p"): 6, PoolKey(testTenant, "q"): 2}, nil)
	run.RunID = "r2"
	run.PoolReservations = map[string]PoolReservation{PoolKey(testTenant, "p"): {RunID: "r1", TaskID: "big"}}

	// Act
	out := PlanRun(run)

	// Assert
	if len(out) != 1 || out[0].TaskID != "other" {
		t.Errorf("planned %+v, want only other (pool p is reserved)", out)
	}
}

// TestPlanRunReservingTaskIsAdmittedWhenItFits: the reserving task itself is
// admitted as soon as it fits; until then it waits like any other.
func TestPlanRunReservingTaskIsAdmittedWhenItFits(t *testing.T) {
	// Arrange
	tasks := []domain.TaskSpec{{TaskID: "big", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 4}}
	budgets := map[string]int{PoolKey(testTenant, "p"): 6}
	reserved := map[string]PoolReservation{PoolKey(testTenant, "p"): {RunID: "r1", TaskID: "big"}}
	waiting := poolRun(tasks, budgets, map[string]int{PoolKey(testTenant, "p"): 3})
	fits := poolRun(tasks, budgets, map[string]int{PoolKey(testTenant, "p"): 2})
	for _, r := range []*RunState{&waiting, &fits} {
		r.RunID = "r1"
		r.PoolReservations = reserved
	}

	// Act
	waitOut, fitsOut := PlanRun(waiting), PlanRun(fits)

	// Assert
	if len(waitOut) != 0 {
		t.Errorf("planned %+v with 3 of 6 slots taken, want nothing (4 does not fit)", waitOut)
	}
	if countQueued(fitsOut) != 1 {
		t.Errorf("planned %+v with 2 of 6 slots taken, want big queued", fitsOut)
	}
}

// TestPlanRunReportsPoolWaits: only a task held by the pool is reported as
// waiting on it; one held by its dispatch backoff or by max_active_tasks is not.
func TestPlanRunReportsPoolWaits(t *testing.T) {
	// Arrange
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Minute)
	tasks := []domain.TaskSpec{
		{TaskID: "big", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 4},
		{TaskID: "fits", Type: domain.TaskTypePython, Pool: "p"},
		{TaskID: "backoff", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 4},
		{TaskID: "capped", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 4},
	}
	run := poolRun(tasks, map[string]int{PoolKey(testTenant, "p"): 6}, map[string]int{PoolKey(testTenant, "p"): 3})
	run.Now = now
	run.NextDispatchAt = map[string]*time.Time{"backoff": &later}
	run.MaxActiveTasks = 1 // big is held by the pool, fits takes the one admission, capped hits the cap

	// Act
	_, waits := planRun(run)

	// Assert
	if len(waits) != 1 || waits[0].TaskID != "big" || waits[0].Slots != 4 || waits[0].Pool != PoolKey(testTenant, "p") {
		t.Errorf("waits = %+v, want only big (4 slots, pool p)", waits)
	}
}

// --- Step: when a reservation is made and dropped ---

// starvationFixture is pool p with 6 slots: run "busy" holds running tasks,
// run "r1" waits with a 4-slot task, run "r2" keeps offering 1-slot tasks.
type starvationFixture struct {
	store *fakeStore
	d     *fakeDispatcher
	s     *Scheduler
	now   time.Time
}

func newStarvationFixture(busy int) *starvationFixture {
	running := make(map[string]domain.TaskState, busy)
	busyTasks := pooledTasks(busy, "p")
	for _, t := range busyTasks {
		running[t.TaskID] = domain.TaskStateRunning
	}
	big := []domain.TaskSpec{{TaskID: "big", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 4}}
	small := []domain.TaskSpec{{TaskID: "small", Type: domain.TaskTypePython, Pool: "p"}}
	mk := func(id string, tasks []domain.TaskSpec, states map[string]domain.TaskState) RunState {
		return RunState{
			RunID: id, DagID: "etl-" + id, TenantID: testTenant, State: domain.DagRunStateRunning,
			Tasks: tasks, States: states,
			Tries: map[string]int{}, MaxTries: map[string]int{},
		}
	}
	store := newFakeStore(
		mk("busy", busyTasks, running),
		mk("r1", big, scheduledStates(big)),
		mk("r2", small, scheduledStates(small)),
	)
	store.poolBudgets = map[string]int{PoolKey(testTenant, "p"): 6}
	f := &starvationFixture{store: store, d: &fakeDispatcher{}, now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	f.s = newScheduler(store)
	f.s.SetDispatcher(f.d)
	f.s.EnablePools()
	f.s.SetPoolStarvationThreshold(time.Minute)
	f.s.clock = func() time.Time { return f.now }
	return f
}

// tick runs one Step at the fixture's clock and returns what it dispatched.
func (f *starvationFixture) tick(t *testing.T) []string {
	t.Helper()
	f.d.dispatched = nil
	if err := f.s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f.d.dispatched
}

// TestStepReservesThePoolForAStarvedTask: the 4-slot task waits while 1-slot
// tasks keep fitting; past the threshold the pool is reserved and the 1-slot
// task is held, until the big one fits and is admitted, which releases it.
func TestStepReservesThePoolForAStarvedTask(t *testing.T) {
	// Arrange
	f := newStarvationFixture(3)

	// Act
	first := f.tick(t)
	f.now = f.now.Add(61 * time.Second)
	starved := f.tick(t) // reservation is made at the end of this tick
	held := f.tick(t)
	f.store.runs[0] = RunState{RunID: "busy", DagID: "etl-busy", TenantID: testTenant, State: domain.DagRunStateRunning}
	freed := f.tick(t)
	f.store.runs[1].States = map[string]domain.TaskState{"big": domain.TaskStateQueued}
	after := f.tick(t)

	// Assert
	if len(first) != 1 || first[0] != "small" || len(starved) != 1 || starved[0] != "small" {
		t.Fatalf("before the threshold dispatched %v then %v, want small each time", first, starved)
	}
	if len(held) != 0 {
		t.Errorf("with the pool reserved dispatched %v, want nothing", held)
	}
	if len(freed) != 1 || freed[0] != "big" {
		t.Errorf("once 4 slots are free dispatched %v, want big", freed)
	}
	if len(after) != 1 || after[0] != "small" {
		t.Errorf("after big was admitted dispatched %v, want small (reservation released)", after)
	}
}

// TestStepNeverReservesForATaskLargerThanItsPool: a task that can never fit
// would freeze the pool, so it is not reserved for.
func TestStepNeverReservesForATaskLargerThanItsPool(t *testing.T) {
	// Arrange
	f := newStarvationFixture(3)
	f.store.runs[1].Tasks[0].PoolSlots = 7

	// Act
	f.tick(t)
	f.now = f.now.Add(2 * time.Minute)
	f.tick(t)
	got := f.tick(t)

	// Assert
	if len(got) != 1 || got[0] != "small" {
		t.Errorf("dispatched %v, want small (no reservation for a 7-slot task in a 6-slot pool)", got)
	}
}

// TestStepDropsAReservationWhoseTaskLeftScheduled: a canceled or skipped
// reserving task releases the pool on the next tick.
func TestStepDropsAReservationWhoseTaskLeftScheduled(t *testing.T) {
	// Arrange
	f := newStarvationFixture(3)
	f.tick(t)
	f.now = f.now.Add(61 * time.Second)
	f.tick(t)

	// Act
	f.store.runs[1].States = map[string]domain.TaskState{"big": domain.TaskStateSkipped}
	got := f.tick(t)

	// Assert
	if len(got) != 1 || got[0] != "small" {
		t.Errorf("dispatched %v, want small (reservation dropped with its task)", got)
	}
}

// TestStepDropsAReservationWhenThePoolTurnsUnlimited: a budget change that
// makes the pool unlimited releases it.
func TestStepDropsAReservationWhenThePoolTurnsUnlimited(t *testing.T) {
	// Arrange
	f := newStarvationFixture(3)
	f.tick(t)
	f.now = f.now.Add(61 * time.Second)
	f.tick(t)

	// Act
	f.store.poolBudgets = map[string]int{}
	got := f.tick(t)

	// Assert
	if len(got) != 2 {
		t.Errorf("dispatched %v, want big and small (pool unlimited)", got)
	}
}

// TestStepStarvationThresholdZeroDisablesReservations: 0 keeps admission as
// it was before the reservation existed.
func TestStepStarvationThresholdZeroDisablesReservations(t *testing.T) {
	// Arrange
	f := newStarvationFixture(3)
	f.s.SetPoolStarvationThreshold(0)

	// Act
	f.tick(t)
	f.now = f.now.Add(time.Hour)
	f.tick(t)
	got := f.tick(t)

	// Assert
	if len(got) != 1 || got[0] != "small" {
		t.Errorf("dispatched %v, want small (reservations disabled)", got)
	}
}

// TestStepLosingLeadershipForgetsWaits: the waits live only in the leader, so
// a step-down forgets them and a new term starts counting from zero.
func TestStepLosingLeadershipForgetsWaits(t *testing.T) {
	// Arrange
	f := newStarvationFixture(3)
	f.tick(t)
	f.now = f.now.Add(61 * time.Second)

	// Act
	f.s.SetLeading(false)
	f.tick(t)
	f.s.SetLeading(true)
	f.tick(t) // first sight again: waiting starts now
	got := f.tick(t)

	// Assert
	if len(got) != 1 || got[0] != "small" {
		t.Errorf("dispatched %v, want small (wait restarted after the step-down)", got)
	}
}

// TestPlanRunTaskHeldOnlyByAReservationIsNotAWait: a task that fits the
// pool's free slots but is held because the pool is reserved for another
// task is not waiting on capacity, so it is not reported and cannot age
// into a reservation of its own.
func TestPlanRunTaskHeldOnlyByAReservationIsNotAWait(t *testing.T) {
	// Arrange
	tasks := []domain.TaskSpec{
		{TaskID: "fits", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 2},
		{TaskID: "toobig", Type: domain.TaskTypePython, Pool: "p", PoolSlots: 5},
	}
	run := poolRun(tasks, map[string]int{PoolKey(testTenant, "p"): 6}, map[string]int{PoolKey(testTenant, "p"): 3})
	run.RunID = "r2"
	run.PoolReservations = map[string]PoolReservation{PoolKey(testTenant, "p"): {RunID: "r1", TaskID: "big"}}

	// Act
	out, waits := planRun(run)

	// Assert
	if len(out) != 0 {
		t.Errorf("planned %+v, want nothing (pool p is reserved)", out)
	}
	if len(waits) != 1 || waits[0].TaskID != "toobig" {
		t.Errorf("waits = %+v, want only toobig (fits is held by the reservation, not by capacity)", waits)
	}
}

// TestStepUnweightedBacklogKeepsThroughput: with no size set anywhere, a
// backlog older than the threshold must not reserve the pool. Every freed
// slot is filled in the same tick, exactly as with reservations off (review
// of #1482: the pool admitted one task per tick).
func TestStepUnweightedBacklogKeepsThroughput(t *testing.T) {
	// Arrange
	busyTasks := pooledTasks(4, "p")
	running := make(map[string]domain.TaskState, len(busyTasks))
	for _, bt := range busyTasks {
		running[bt.TaskID] = domain.TaskStateRunning
	}
	waiting := make([]domain.TaskSpec, 0, 6)
	for _, id := range []string{"w0", "w1", "w2", "w3", "w4", "w5"} {
		waiting = append(waiting, domain.TaskSpec{TaskID: id, Type: domain.TaskTypePython, Pool: "p"})
	}
	mk := func(id string, tasks []domain.TaskSpec, states map[string]domain.TaskState) RunState {
		return RunState{
			RunID: id, DagID: "etl-" + id, TenantID: testTenant, State: domain.DagRunStateRunning,
			Tasks: tasks, States: states, Tries: map[string]int{}, MaxTries: map[string]int{},
		}
	}
	store := newFakeStore(mk("busy", busyTasks, running), mk("r1", waiting, scheduledStates(waiting)))
	store.poolBudgets = map[string]int{PoolKey(testTenant, "p"): 4}
	f := &starvationFixture{store: store, d: &fakeDispatcher{}, now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	f.s = newScheduler(store)
	f.s.SetDispatcher(f.d)
	f.s.EnablePools()
	f.s.SetPoolStarvationThreshold(time.Minute)
	f.s.clock = func() time.Time { return f.now }

	// Act
	f.tick(t)
	f.now = f.now.Add(2 * time.Minute)
	f.tick(t)
	f.store.runs[0] = RunState{RunID: "busy", DagID: "etl-busy", TenantID: testTenant, State: domain.DagRunStateRunning}
	freed := f.tick(t)

	// Assert
	if len(freed) != 4 {
		t.Errorf("4 slots free and 6 size-1 tasks waiting past the threshold dispatched %v, want 4", freed)
	}
	if len(f.s.poolReservations) != 0 {
		t.Errorf("reservations = %v, want none (no task larger than 1 slot)", f.s.poolReservations)
	}
}

// TestStepWaitAgeSurvivesATickHeldByAnotherGate: a starved task held for one
// tick by its dispatch backoff keeps the time it has waited, so it reserves
// the pool on the next tick it is held by the pool instead of starting over.
func TestStepWaitAgeSurvivesATickHeldByAnotherGate(t *testing.T) {
	// Arrange
	f := newStarvationFixture(3)
	f.tick(t) // big starts waiting
	f.now = f.now.Add(30 * time.Second)
	backoff := f.now.Add(10 * time.Second)
	f.store.runs[1].Now = f.now
	f.store.runs[1].NextDispatchAt = map[string]*time.Time{"big": &backoff}
	f.tick(t) // big held by its backoff, not by the pool

	// Act
	f.store.runs[1].NextDispatchAt = nil
	f.now = f.now.Add(31 * time.Second) // 61s since big was first held by the pool
	f.tick(t)                           // reservation is made at the end of this tick
	held := f.tick(t)

	// Assert
	if len(held) != 0 {
		t.Errorf("dispatched %v, want nothing (pool reserved for big after 61s of waiting)", held)
	}
}

// TestKeepStillScheduledKeepsAnUnheldWaitWhileANewOneStarts: an earlier wait
// not held this tick is kept while its task is still scheduled, even when a
// different task starts waiting the same tick, and is forgotten once the task
// leaves scheduled.
func TestKeepStillScheduledKeepsAnUnheldWaitWhileANewOneStarts(t *testing.T) {
	// Arrange
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	a, b, c := taskRef{"r1", "a"}, taskRef{"r1", "b"}, taskRef{"r1", "c"}
	prev := map[taskRef]time.Time{a: t0, c: t0}
	since := map[taskRef]time.Time{b: t0.Add(time.Minute)}
	runs := []RunState{{RunID: "r1", States: map[string]domain.TaskState{
		"a": domain.TaskStateScheduled, "b": domain.TaskStateScheduled, "c": domain.TaskStateQueued,
	}}}

	// Act
	keepStillScheduled(since, prev, runs)

	// Assert
	if got, ok := since[a]; !ok || !got.Equal(t0) {
		t.Errorf("a = %v, %v; want its first wait %v kept (still scheduled)", got, ok, t0)
	}
	if _, ok := since[c]; ok {
		t.Error("c kept, want it forgotten (no longer scheduled)")
	}
	if len(since) != 2 {
		t.Errorf("since = %v, want a and b", since)
	}
}
