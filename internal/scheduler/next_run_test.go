package scheduler

import (
	"fmt"
	"testing"
	"time"
)

// TestNextDueAtIsWhenTheDAGBecomesDue pins the contract of nextDueAt (#2175):
// the time it returns is the first instant at which dueSlots has a slot for
// the DAG. One second earlier nothing is due, and at that instant something
// is, for every shape of DAG the planner knows: with and without a previous
// run, with a start_date in the past or the future, catchup on or off, and
// cron expressions with uneven gaps or a time zone.
func TestNextDueAtIsWhenTheDAGBecomesDue(t *testing.T) {
	now := time.Date(2026, 10, 26, 10, 17, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time {
		v := now.Add(d)
		return &v
	}
	schedules := []string{
		"@hourly", "@daily", "*/15 * * * *", "0 9 * * 1-5", "0,59 0,23 * * *",
		"CRON_TZ=America/Sao_Paulo 30 2 * * *", "@every 45m", "0 0 1 * *",
	}
	shapes := []struct {
		name        string
		last, start *time.Time
	}{
		{name: "never ran, no start_date"},
		{name: "ran an hour ago", last: at(-time.Hour)},
		{name: "ran a minute ago", last: at(-time.Minute)},
		{name: "start_date in the future", start: at(50 * time.Hour)},
		{name: "start_date in the past", start: at(-72 * time.Hour)},
		{name: "ran, start_date later than the run", last: at(-time.Hour), start: at(30 * time.Hour)},
	}
	for _, expr := range schedules {
		for _, shape := range shapes {
			for _, catchup := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/catchup=%v", expr, shape.name, catchup), func(t *testing.T) {
					// Arrange
					d := ScheduledDAG{Schedule: expr, LastLogical: shape.last, StartDate: shape.start, Catchup: catchup}
					if len(dueSlots(d, now)) > 0 {
						// Already due: settle what is due now, as a tick would.
						slots := dueSlots(d, now)
						last := slots[len(slots)-1]
						d.LastLogical = &last
					}

					// Act
					next, ok := nextDueAt(d, now)

					// Assert
					if !ok {
						t.Fatalf("nextDueAt = never, want a time")
					}
					if before := next.Add(-time.Second); before.After(now) && len(dueSlots(d, before)) > 0 {
						t.Errorf("due at %s, one second before nextDueAt %s", before, next)
					}
					if len(dueSlots(d, next.Add(2*nextDueMargin))) == 0 {
						t.Errorf("nothing due shortly after nextDueAt %s", next)
					}
				})
			}
		}
	}
}

// TestNextDueAtNever: a schedule that never fires and an unparseable one have
// no next time, so the DAG is not listed again until it changes.
func TestNextDueAtNever(t *testing.T) {
	for _, expr := range []string{"0 0 31 2 *", "not a cron"} {
		t.Run(expr, func(t *testing.T) {
			// Arrange
			d := ScheduledDAG{Schedule: expr}

			// Act
			next, ok := nextDueAt(d, time.Now())

			// Assert
			if ok {
				t.Errorf("nextDueAt = %s, want never", next)
			}
		})
	}
}
