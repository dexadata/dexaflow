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
