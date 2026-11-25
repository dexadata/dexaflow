package domain

import (
	"strings"
	"testing"
	"time"
)

func TestScheduleLocation(t *testing.T) {
	cases := []struct {
		tz         string
		wantName   string
		wantOffset int // seconds east of UTC on 2026-01-15
	}{
		{"", "UTC", 0},
		{"UTC", "UTC", 0},
		{"America/Sao_Paulo", "America/Sao_Paulo", -3 * 3600},
		{"+05:30", "+05:30", 5*3600 + 30*60},
		{"-03:00", "-03:00", -3 * 3600},
		{"+00:00", "UTC", 0},
	}
	for _, tc := range cases {
		loc, err := ScheduleLocation(tc.tz)
		if err != nil {
			t.Fatalf("ScheduleLocation(%q): %v", tc.tz, err)
		}
		if loc.String() != tc.wantName {
			t.Errorf("ScheduleLocation(%q) = %q, want %q", tc.tz, loc, tc.wantName)
		}
		_, off := time.Date(2026, 1, 15, 12, 0, 0, 0, loc).Zone()
		if off != tc.wantOffset {
			t.Errorf("ScheduleLocation(%q) offset = %d, want %d", tc.tz, off, tc.wantOffset)
		}
	}
	for _, bad := range []string{"Mars/Olympus", "+25:00", "+5:30", "03:00", "Local"} {
		if _, err := ScheduleLocation(bad); err == nil {
			t.Errorf("ScheduleLocation(%q) = nil error, want one", bad)
		}
	}
}

// The cron fields of a DAG whose start_date carries a zone are read in that
// zone, as Airflow does (#2453): "0 6 * * *" in Sao Paulo is 09:00 UTC.
func TestParseCronInEvaluatesTheFieldsInTheZone(t *testing.T) {
	s, err := ParseCronIn("0 6 * * *", "America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	got := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	want := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("Next = %v, want %v in UTC", got, want)
	}
	// Daylight saving time is followed: 06:00 in New York is 11:00 UTC in
	// winter and 10:00 UTC in summer.
	ny, err := ParseCronIn("0 6 * * *", "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if got := ny.Next(time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 1, 10, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("winter Next = %v, want 11:00 UTC", got)
	}
	if got := ny.Next(time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)); !got.Equal(time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("summer Next = %v, want 10:00 UTC", got)
	}
}

func TestParseCronInKeepsUTCAndExplicitZones(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tz := range []string{"", "UTC"} {
		s, err := ParseCronIn("0 6 * * *", tz)
		if err != nil {
			t.Fatal(err)
		}
		// A non-UTC argument must not move the fields either.
		got := s.Next(from.In(time.FixedZone("x", -3*3600)))
		if !got.Equal(time.Date(2026, 1, 1, 6, 0, 0, 0, time.UTC)) {
			t.Errorf("tz %q: Next = %v, want 06:00 UTC", tz, got)
		}
	}
	// A CRON_TZ prefix in the expression wins over the DAG zone.
	s, err := ParseCronIn("CRON_TZ=Asia/Tokyo 0 6 * * *", "America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Next(from); !got.Equal(time.Date(2026, 1, 1, 21, 0, 0, 0, time.UTC)) {
		t.Errorf("CRON_TZ Next = %v, want 21:00 UTC (06:00 Tokyo)", got)
	}
	// @every has no wall clock and ignores the zone.
	e, err := ParseCronIn("@every 1h", "America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Next(from); !got.Equal(from.Add(time.Hour)) {
		t.Errorf("@every Next = %v", got)
	}
	if _, err := ParseCronIn("0 6 * * *", "Mars/Olympus"); err == nil {
		t.Error("ParseCronIn with an unknown zone = nil error")
	}
}

func TestValidateScheduleRejectsAnUnknownTimezone(t *testing.T) {
	daily := "0 6 * * *"
	spec := DAGSpec{Schedule: &daily, ScheduleTZ: "Mars/Olympus"}
	err := spec.ValidateSchedule()
	if err == nil || !strings.Contains(err.Error(), "Mars/Olympus") {
		t.Fatalf("ValidateSchedule = %v, want an error naming the zone", err)
	}
	spec.ScheduleTZ = "Europe/Lisbon"
	if err := spec.ValidateSchedule(); err != nil {
		t.Fatalf("ValidateSchedule(Europe/Lisbon) = %v", err)
	}
	// A manual-only DAG may still carry the zone of its start_date.
	spec.Schedule, spec.ScheduleTZ = nil, "Europe/Lisbon"
	if err := spec.ValidateSchedule(); err != nil {
		t.Fatalf("manual-only with a zone = %v", err)
	}
}

// Registration runs Validate, not ValidateSchedule: an unknown zone in a
// hand-written dag.json is refused there too.
func TestValidateRejectsAnUnknownTimezone(t *testing.T) {
	spec := DAGSpec{
		SchemaVersion: "1.0", DagID: "d", DagVersion: "v1", Image: "img:v1", ScheduleTZ: "Mars/Olympus",
		Tasks: []TaskSpec{{TaskID: "t", Type: TaskTypeBash, Entrypoint: "true"}},
	}
	if err := spec.Validate(); err == nil || !strings.Contains(err.Error(), "Mars/Olympus") {
		t.Fatalf("Validate = %v, want an error naming the zone", err)
	}
	spec.ScheduleTZ = "-03:00"
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate(-03:00) = %v", err)
	}
}
