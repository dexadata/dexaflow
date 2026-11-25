package domain

import (
	"testing"
	"time"
)

func nextN(t *testing.T, expr, tz string, from time.Time, n int) []time.Time {
	t.Helper()
	s, err := ParseCronIn(expr, tz)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]time.Time, 0, n)
	for range n {
		from = s.Next(from)
		out = append(out, from)
	}
	return out
}

func wantTimes(t *testing.T, got []time.Time, want ...time.Time) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func z(month time.Month, day, hour, minute int) time.Time {
	return time.Date(2026, month, day, hour, minute, 0, 0, time.UTC)
}

// The day New York's clocks go back (2026-11-01, 02:00 EDT becomes 01:00
// EST), a daily 01:30 runs once, at the first 01:30 (05:30 UTC), as in
// Airflow. Read in absolute time it ran again at 06:30 UTC.
func TestWallClockRepeatedHourFiresOnce(t *testing.T) {
	got := nextN(t, "30 1 * * *", "America/New_York", z(10, 31, 0, 0), 3)
	wantTimes(t, got, z(10, 31, 5, 30), z(11, 1, 5, 30), z(11, 2, 6, 30))
}

// The day they go forward (2026-03-08, 02:00 EST becomes 03:00 EDT), a daily
// 02:30 that does not exist runs at 03:30 EDT (07:30 UTC), as Airflow does.
// Read in absolute time that day had no run.
func TestWallClockSkippedTimeRunsAtTheShiftedInstant(t *testing.T) {
	got := nextN(t, "30 2 * * *", "America/New_York", z(3, 7, 0, 0), 3)
	wantTimes(t, got, z(3, 7, 7, 30), z(3, 8, 7, 30), z(3, 9, 6, 30))
}

// Next is strictly after its argument even when the argument falls in the
// second pass of a repeated hour, whose slot maps to the first pass.
func TestWallClockNextFromInsideTheRepeatedHour(t *testing.T) {
	secondPass := z(11, 1, 6, 15) // 01:15 EST
	got := nextN(t, "30 1 * * *", "America/New_York", secondPass, 1)
	wantTimes(t, got, z(11, 2, 6, 30))
	// Slots inside the hour run in its first pass only: after 01:45 EDT the
	// next slot is 01:00 the following day, as Airflow reads a cron that does
	// not cover every hour.
	got = nextN(t, "*/15 1 * * *", "America/New_York", z(11, 1, 5, 40), 2)
	wantTimes(t, got, z(11, 1, 5, 45), z(11, 2, 6, 0))
}

// An hourly cron keeps its cadence in absolute time across both changes:
// both passes of the repeated hour run, and the skipped hour does not.
func TestEveryHourCronKeepsAbsoluteCadence(t *testing.T) {
	got := nextN(t, "0 * * * *", "America/New_York", z(11, 1, 4, 30), 3)
	wantTimes(t, got, z(11, 1, 5, 0), z(11, 1, 6, 0), z(11, 1, 7, 0))
	got = nextN(t, "0 * * * *", "America/New_York", z(3, 8, 6, 30), 2)
	wantTimes(t, got, z(3, 8, 7, 0), z(3, 8, 8, 0))
}

// A schedule that never fires stays that way in a zone.
func TestWallClockNeverFires(t *testing.T) {
	s, err := ParseCronIn("0 0 31 2 *", "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Next(z(1, 1, 0, 0)); !got.IsZero() {
		t.Fatalf("Next = %v, want zero", got)
	}
}
