package scheduler

import (
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// scheduleParseable reports whether expr, read in the zone tz, is a cron
// schedule the scheduler can run (the same check nextScheduledRun applies). It
// lets the caller distinguish "not due yet" from "can never be due because the
// expression or its zone is malformed", so the latter can be surfaced instead
// of silently ignored.
func scheduleParseable(expr, tz string) bool {
	_, err := domain.ParseCronIn(expr, tz)
	return err == nil
}

// nextScheduledRun returns the logical date of the next due run for a cron
// expression, its fields read in the DAG's zone tz (#2453), given the latest run's logical date (nil if the DAG never ran) and
// the current time. due is false when nothing is due yet or the expression is
// unparseable.
//
// A DAG that has never run fires its most recent slot at or before now — i.e. it
// starts scheduling from registration (catchup=False semantics), without
// backfilling older slots. A DAG that has run advances to the next slot after its
// last logical date once that slot has arrived.
func nextScheduledRun(expr, tz string, last *time.Time, now time.Time) (logical time.Time, due bool) {
	schedule, err := domain.ParseCronIn(expr, tz)
	if err != nil {
		return time.Time{}, false
	}
	// ParseCronIn pins the zone the fields are read in, so the location of the
	// inputs no longer moves a slot. They are still coerced to UTC so every
	// slot comes back in UTC (mirrors dueScheduledSlots).
	now = now.UTC()
	if last != nil {
		u := last.UTC()
		last = &u
	}
	if last == nil {
		return latestSlotAtOrBefore(schedule, now)
	}
	next := schedule.Next(*last)
	// The zero time means the schedule never fires again (see dueScheduledSlots).
	if next.IsZero() || next.After(now) {
		return time.Time{}, false
	}
	return next, true
}
