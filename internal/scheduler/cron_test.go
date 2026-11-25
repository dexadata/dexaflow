package scheduler

import (
	"testing"
	"time"
)

func TestNextScheduledRun(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 30, 0, 0, time.UTC)

	last := time.Date(2026, 5, 22, 11, 0, 0, 0, time.UTC)
	got, due := nextScheduledRun("@hourly", "", &last, now)
	if !due {
		t.Fatal("a slot at 12:00 should be due at 12:30")
	}
	if !got.Equal(time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("logical = %v, want 12:00", got)
	}

	recent := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	if _, ok := nextScheduledRun("@hourly", "", &recent, now); ok {
		t.Error("next slot (13:00) is in the future; should not be due")
	}

	// A never-run scheduled DAG fires its most recent slot (catchup=False), so it
	// starts running rather than waiting forever.
	got, due = nextScheduledRun("@hourly", "", nil, now)
	if !due {
		t.Fatal("a never-run scheduled DAG should fire its most recent slot")
	}
	if !got.Equal(time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("never-run logical = %v, want 12:00 (most recent slot)", got)
	}
	// Every-minute, never run: fires the current minute boundary.
	minNow := time.Date(2026, 5, 22, 12, 30, 30, 0, time.UTC)
	if got, due := nextScheduledRun("* * * * *", "", nil, minNow); !due || !got.Equal(time.Date(2026, 5, 22, 12, 30, 0, 0, time.UTC)) {
		t.Errorf("every-minute never-run = (%v, %v), want (12:30:00, true)", got, due)
	}

	if _, ok := nextScheduledRun("not a cron expression", "", &last, now); ok {
		t.Error("an unparseable expression should not be due")
	}
}
