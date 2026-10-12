package domain

import (
	"strings"
	"testing"
)

func TestValidateSchedule(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name     string
		schedule *string
		wantErr  bool
	}{
		{"nil is manual-only, valid", nil, false},
		{"empty is manual-only, valid", str(""), false},
		{"blank is manual-only, valid", str("   "), false},
		{"valid 5-field cron", str("*/3 * * * *"), false},
		{"every-minute cron", str("*/1 * * * *"), false},
		{"@daily preset", str("@daily"), false},
		{"@hourly preset", str("@hourly"), false},
		// Valid Airflow non-cron schedules must register (not be blocked at compile)
		// even though Dexaflow does not cron-schedule them.
		{"@once is a valid Airflow schedule", str("@once"), false},
		{"@continuous is a valid Airflow schedule", str("@continuous"), false},
		{"@once is case-insensitive", str("@Once"), false},
		// The exact bug a user hit: a 4-field cron. It must FAIL loudly at
		// compile, not be silently ignored by the scheduler.
		{"4-field cron is rejected", str("*/3 * * *"), true},
		{"garbage is rejected", str("not a cron"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := &DAGSpec{DagID: "d", Schedule: tc.schedule}
			err := spec.ValidateSchedule()
			if tc.wantErr != (err != nil) {
				t.Fatalf("ValidateSchedule(%v) err=%v, wantErr=%v", tc.schedule, err, tc.wantErr)
			}
			// A rejection must name the offending expression so the user can fix it.
			if tc.wantErr && tc.schedule != nil && !strings.Contains(err.Error(), strings.TrimSpace(*tc.schedule)) {
				t.Errorf("error %q should quote the bad schedule %q", err, *tc.schedule)
			}
		})
	}
}

// A TZ= or CRON_TZ= prefix with nothing after it made robfig/cron's parser
// slice past the end of the string and panic (it cuts the zone at the first
// space). A dag.json schedule reaches that parser at registration and on every
// scheduler tick, so it must come back as a validation error.
func TestParseCronRejectsBareTimeZonePrefix(t *testing.T) {
	for _, expr := range []string{"TZ=", "TZ=UTC", "CRON_TZ=Europe/Lisbon", "TZ=UTC\t0 * * * *"} {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("ParseCron(%q) = nil error, want one", expr)
		}
		spec := DAGSpec{Schedule: &expr}
		if err := spec.ValidateSchedule(); err == nil {
			t.Errorf("ValidateSchedule(%q) = nil, want an error", expr)
		}
		if _, ok := MinScheduleInterval(expr); ok {
			t.Errorf("MinScheduleInterval(%q) ok, want not ok", expr)
		}
	}
	if _, err := ParseCron("CRON_TZ=Europe/Lisbon 30 2 * * *"); err != nil {
		t.Errorf("a zone followed by an expression is valid: %v", err)
	}
}
