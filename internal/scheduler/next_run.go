package scheduler

import (
	"time"

	"github.com/robfig/cron/v3"

	"github.com/dexadata/dexaflow/internal/domain"
)

// nextDueAt returns, within nextDueMargin, the first instant at which
// dueSlots has a slot for d, so the store can leave d out of the scheduled
// DAG list until then (#2175). ok
// is false when no slot can ever be due: the schedule never fires or cannot
// be parsed. The scheduler stores at most nextRunHorizon ahead either way.
//
// The time follows the planner's rules: the first slot strictly after the
// latest run, not before start_date; with neither, the first slot after now,
// which the first-sight rule then creates. It does not depend on catchup:
// both modes become due at the same slot and differ only in how many slots
// they create once due. When d still has slots due at now (a catchup cut
// short by the per-tick cap), the time is at or before now and the DAG is
// listed on the next tick.
func nextDueAt(d ScheduledDAG, now time.Time) (time.Time, bool) {
	schedule, err := domain.ParseCron(d.Schedule)
	if err != nil {
		return time.Time{}, false
	}
	if every, ok := schedule.(cron.ConstantDelaySchedule); ok {
		return nextEveryDueAt(every, d, now)
	}
	// UTC at the boundary, like dueScheduledSlots: Next evaluates the cron
	// fields in the argument's location.
	cursor := now.UTC()
	if d.LastLogical != nil {
		cursor = d.LastLogical.UTC()
	}
	if d.StartDate != nil {
		// Next is strict, so the start_date slot itself is the first one found.
		if floor := d.StartDate.UTC().Add(-time.Nanosecond); d.LastLogical == nil || floor.After(cursor) {
			cursor = floor
		}
	}
	next := schedule.Next(cursor)
	if next.IsZero() {
		return time.Time{}, false
	}
	return next.Add(-nextDueMargin), true
}

// nextEveryDueAt is nextDueAt for an @every schedule, whose slots are the
// steps of the interval after the last run, or start at the start_date for a
// DAG that never ran (latestEverySlot). A start_date later than the next step
// moves it to the first step at or after the start_date.
func nextEveryDueAt(every cron.ConstantDelaySchedule, d ScheduledDAG, now time.Time) (time.Time, bool) {
	if every.Delay <= 0 {
		return time.Time{}, false
	}
	var next time.Time
	switch {
	case d.LastLogical != nil:
		next = every.Next(d.LastLogical.UTC())
		if d.StartDate != nil && d.StartDate.After(next) {
			gap := d.StartDate.Sub(next)
			if r := gap % every.Delay; r != 0 {
				gap += every.Delay - r
			}
			next = next.Add(gap)
		}
	case d.StartDate != nil:
		next = d.StartDate.UTC()
	default:
		next = every.Next(now.UTC())
	}
	return next.Add(-nextDueMargin), true
}

// nextDueMargin is how much earlier than the computed slot nextDueAt answers.
// The computation matches the planner exactly (TestNextDueAtIsWhenTheDAGBecomesDue),
// but the two are written apart, and the costs are uneven: a time that is
// early costs one more look at the DAG, one that is late delays its run. The
// margin keeps a rounding difference between them on the early side.
const nextDueMargin = time.Second
