package scheduler

import (
	"time"

	"github.com/robfig/cron/v3"

	"github.com/dexadata/dexaflow/internal/domain"
)

// dueScheduledSlots returns the logical dates for which the scheduler should
// create runs on the current tick (#129). It is the pure planner-side
// decision: given the cron expression, the latest run's logical date (nil
// if the DAG has never run), the DAG's start_date floor (nil for "no floor"),
// "now", a catchup flag, and a per-tick cap, return the chronological list
// of due slots.
//
// Behavior mirrors Airflow:
//   - catchup=true  → backfill every missed slot, capped at maxSlots.
//   - catchup=false → fire exactly one run for the most recent slot at or
//     before now, skipping older missed slots however many there are (#1919).
//
// Two safety guards:
//   - maxSlots bounds the per-tick backfill of catchup=true. A multi-hour
//     outage that produced thousands of missed slots will not stall the tick; the remainder is
//     picked up on the next interval (the scheduler is reconciliation-loop
//     based, ADR 0031, so retry next tick is the correct shape).
//   - The start_date floor prevents creating a run for a logical_date earlier
//     than the DAG's official start (Airflow semantics).
//
// An unparseable cron expression yields nothing. nextScheduledRun + the
// scheduleParseable backstop in createDueRuns already log the bad expression
// once; this helper stays silent.
func dueScheduledSlots(expr string, lastLogical, startDate *time.Time, now time.Time, catchup bool, maxSlots int) []time.Time {
	schedule, err := domain.ParseCron(expr)
	if err != nil {
		return nil
	}
	// Normalise all time inputs to UTC. SpecSchedule.Next evaluates the cron
	// fields in the argument's Location, so a seed in -0300 would treat
	// "@daily = 0 0 * * *" as "local midnight" (03:00 UTC) instead of UTC
	// midnight. pgx returns timestamptz values in the session TZ, which on
	// many Lite deployments is not UTC — that produced the post-delete
	// double-fire (one run at 00:00Z, another at 03:00Z for -0300 hosts).
	// All cron logic in this package speaks UTC; do the coercion at the
	// boundary so the inner loop never has to think about it.
	now = now.UTC()
	if lastLogical != nil {
		u := lastLogical.UTC()
		lastLogical = &u
	}
	if startDate != nil {
		u := startDate.UTC()
		startDate = &u
	}
	// Seed the iteration. For a first-ever sight (lastLogical == nil) we seed
	// from one period before the start_date so the start_date slot itself can
	// be emitted (cron.Next returns the strict next slot after the argument).
	// If there is no start_date and no last_logical, fall back to the
	// single-slot legacy semantics: emit the most recent slot at or before
	// now (catchup=false equivalent), since backfilling all of history would
	// be unsafe by default.
	seed, ok := catchupSeed(schedule, lastLogical, startDate, now)
	if !ok {
		return nil
	}
	if !catchup {
		return latestDueSlot(schedule, seed, startDate, now)
	}

	// Enumerate slots strictly after seed and at or before now. Skip any slot
	// before start_date (the floor) — handles the case where lastLogical is
	// older than start_date.
	var slots []time.Time
	cursor := seed
	for {
		cursor = schedule.Next(cursor)
		// Next returns the zero time for a schedule that never fires (31
		// February, an empty hour list). Taking it for a slot dated year 1
		// would create a bogus run, or, below the start_date floor, ask Next
		// again forever and hang the tick.
		if cursor.IsZero() || cursor.After(now) {
			break
		}
		if startDate != nil && cursor.Before(*startDate) {
			continue
		}
		slots = append(slots, cursor)
		if len(slots) >= maxSlots {
			break
		}
	}
	if len(slots) == 0 {
		return nil
	}
	return slots
}

// latestDueSlot is the catchup=false decision (#1919): the most recent slot
// at or before now, as a single-element list, when it is strictly after seed
// and not before the start_date floor; nil otherwise. Older missed slots are
// skipped, as Airflow does for catchup=False. The slot is found by searching
// back from now, never by walking forward from seed, so a start_date or last
// run far in the past costs the same as a recent one and the per-tick cap
// cannot stop the walk short of now.
func latestDueSlot(schedule cron.Schedule, seed time.Time, startDate *time.Time, now time.Time) []time.Time {
	if every, ok := schedule.(cron.ConstantDelaySchedule); ok {
		return latestEverySlot(every, seed, startDate, now)
	}
	latest, ok := latestSlotAtOrBefore(schedule, now)
	if !ok || !latest.After(seed) {
		return nil
	}
	if startDate != nil && latest.Before(*startDate) {
		return nil
	}
	return []time.Time{latest}
}

// latestEverySlot is latestDueSlot for an @every schedule. Its slots have no
// wall-clock grid: Next adds the interval to whatever time it is given, so the
// search back from now that latestSlotAtOrBefore does would always land on now
// itself and find a slot on every tick. The slots are the steps of the
// interval after seed (the last run, or one interval before the start_date),
// the same ones catchup=true walks, and the latest of them at or before now is
// found from the remainder instead of by walking.
func latestEverySlot(every cron.ConstantDelaySchedule, seed time.Time, startDate *time.Time, now time.Time) []time.Time {
	first := every.Next(seed)
	if first.After(now) || every.Delay <= 0 {
		return nil
	}
	latest := now.Add(-(now.Sub(first) % every.Delay))
	if startDate != nil && latest.Before(*startDate) {
		return nil
	}
	return []time.Time{latest}
}

// maxSlotLookback bounds how far back latestSlotAtOrBefore searches for a
// slot. It stays well inside time.Duration's range (about 292 years).
const maxSlotLookback = 200 * 365 * 24 * time.Hour

// latestSlotAtOrBefore returns the latest activation of schedule at or before
// now. The cron library only exposes Next, so it looks back over a window that
// starts at one period and doubles until Next from the window's start lands at
// or before now, then walks forward to the last such slot. Doubling handles
// uneven gaps (a weekday schedule seen on a Monday) that a single period
// estimate would miss, and the walk is short because the previous, smaller
// window held no slot. ok is false when the schedule has no slot within
// maxSlotLookback.
func latestSlotAtOrBefore(schedule cron.Schedule, now time.Time) (time.Time, bool) {
	window := schedulePeriod(schedule, now)
	if window <= 0 {
		return time.Time{}, false
	}
	for ; window <= maxSlotLookback; window *= 2 {
		cursor := schedule.Next(now.Add(-window))
		if cursor.IsZero() || cursor.After(now) {
			continue
		}
		for {
			next := schedule.Next(cursor)
			if next.IsZero() || next.After(now) {
				return cursor, true
			}
			cursor = next
		}
	}
	return time.Time{}, false
}

// catchupSeed picks the time to feed schedule.Next from on the first
// iteration. ok=false means "no slot can possibly be due" (no seed available).
func catchupSeed(schedule cron.Schedule, lastLogical, startDate *time.Time, now time.Time) (time.Time, bool) {
	if lastLogical != nil {
		return *lastLogical, true
	}
	if startDate != nil {
		// Step back one period so the start_date slot itself is emittable
		// (Next is strict).
		period := schedulePeriod(schedule, now)
		if period <= 0 {
			return time.Time{}, false
		}
		return startDate.Add(-period), true
	}
	// Fall back to "most recent slot at or before now" semantics: emit nothing
	// from the catchup helper. createDueRuns will pick up the legacy
	// nextScheduledRun path for this shape (see scheduler.go).
	return time.Time{}, false
}

// schedulePeriod returns the period between two consecutive slots, estimated
// from the next two slots after now. 0 means the schedule is degenerate.
func schedulePeriod(schedule cron.Schedule, now time.Time) time.Duration {
	a := schedule.Next(now)
	b := schedule.Next(a)
	return b.Sub(a)
}
