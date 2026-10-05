package domain

import (
	"testing"
	"time"
)

// TestMinScheduleInterval: the shortest gap between two consecutive fire times,
// including the gap across midnight and across days the schedule skips.
func TestMinScheduleInterval(t *testing.T) {
	cases := []struct {
		expr   string
		want   time.Duration
		wantOK bool
	}{
		{"*/15 * * * *", 15 * time.Minute, true},
		{"*/10 * * * *", 10 * time.Minute, true},
		{"* * * * *", time.Minute, true},
		{"0,5 * * * *", 5 * time.Minute, true},
		{"0 * * * *", time.Hour, true},
		{"@hourly", time.Hour, true},
		{"@daily", 24 * time.Hour, true},
		{"@weekly", 7 * 24 * time.Hour, true},
		{"0 9,17 * * *", 8 * time.Hour, true},
		// Monday to Friday at 09:00: Monday to Tuesday is the shortest gap.
		{"0 9 * * 1-5", 24 * time.Hour, true},
		// 23:59 and 00:00 are one minute apart across midnight, though every gap
		// inside a day is at least 59 minutes.
		{"0,59 0,23 * * *", time.Minute, true},
		// The 1st of every month: February to March is the shortest gap.
		{"0 0 1 * *", 28 * 24 * time.Hour, true},
		// The 31st and the 1st: 31 Jan 23:00 to 1 Feb 01:00.
		{"0 1,23 1,31 * *", 2 * time.Hour, true},
		{"@every 5m", 5 * time.Minute, true},
		{"@every 1h30m", 90 * time.Minute, true},
		{" */20 * * * * ", 20 * time.Minute, true},
		// Schedules that never fire on a cron have no interval, including a
		// date that does not exist, whatever its times of day.
		{"", 0, false},
		{"@once", 0, false},
		{"@continuous", 0, false},
		{"not a cron", 0, false},
		{"0 1,2 31 2 *", 0, false},
		{"*/10 * 31 4,6,9,11 *", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			got, ok := MinScheduleInterval(tc.expr)

			if ok != tc.wantOK || got != tc.want {
				t.Errorf("MinScheduleInterval(%q) = %v, %v; want %v, %v", tc.expr, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestMinScheduleIntervalOfARareSchedule: 29 February fires once every four
// years, and the scan finds that gap rather than reporting none.
func TestMinScheduleIntervalOfARareSchedule(t *testing.T) {
	got, ok := MinScheduleInterval("0 0 29 2 *")

	if !ok || got < 4*365*24*time.Hour {
		t.Errorf("MinScheduleInterval(29 Feb) = %v, %v; want at least four years", got, ok)
	}
}

// TestTenantLimitsUpdateIsZero: an update with no field set changes nothing.
func TestTenantLimitsUpdateIsZero(t *testing.T) {
	n := 0

	if !(TenantLimitsUpdate{}).IsZero() {
		t.Error("empty update is not zero")
	}
	if (TenantLimitsUpdate{MaxRunsPerDay: &n}).IsZero() {
		t.Error("an update that sets a limit to 0 (unlimited) is zero")
	}
}
