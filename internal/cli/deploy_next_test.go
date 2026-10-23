package cli

import (
	"strings"
	"testing"
	"time"
)

func TestNextStepLine(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 13, 10, 30, 0, 0, time.UTC)
	oct1 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	nov1 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	longAgo := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		state     dagState
		created   bool
		want      []string
		not       []string
		wasPaused bool
	}{
		{"manual active", dagState{DagID: "etl"}, true,
			[]string{"active", "no schedule", "dexaflow runs trigger etl"}, []string{"unpause"}, false},
		{"manual paused", dagState{DagID: "etl", IsPaused: true}, true,
			[]string{"paused", "no schedule", "dexaflow admin dags unpause etl"}, nil, false},
		{"paused catchup since start_date", dagState{DagID: "etl", IsPaused: true, Schedule: "@daily", Catchup: true, StartDate: &oct1}, true,
			[]string{"paused", "dexaflow admin dags unpause etl", "13 catchup runs", "since start_date 2026-10-01T00:00:00Z", "next tick 2026-10-14T00:00:00Z"}, nil, false},
		{"paused redeploy catchup is an upper bound", dagState{DagID: "etl", IsPaused: true, Schedule: "@daily", Catchup: true, StartDate: &oct1}, false,
			[]string{"up to 13 catchup runs", "fewer if it already ran"}, nil, false},
		{"paused without catchup", dagState{DagID: "etl", IsPaused: true, Schedule: "@daily", StartDate: &oct1}, true,
			[]string{"paused", "one run for the latest tick 2026-10-13T00:00:00Z", "next tick 2026-10-14T00:00:00Z"}, []string{"catchup run"}, false},
		{"active redeploy", dagState{DagID: "etl", Schedule: "@daily", Catchup: true, StartDate: &oct1}, false,
			[]string{"active", "next run 2026-10-14T00:00:00Z"}, []string{"catchup", "unpause"}, false},
		{"active new with catchup", dagState{DagID: "etl", Schedule: "@daily", Catchup: true, StartDate: &oct1}, true,
			[]string{"active", "13 catchup runs", "next tick 2026-10-14T00:00:00Z"}, []string{"unpause"}, false},
		{"active new without start_date", dagState{DagID: "etl", Schedule: "0 6 * * *"}, true,
			[]string{"active", "one run for the latest tick 2026-10-13T06:00:00Z", "next tick 2026-10-14T06:00:00Z"}, nil, false},
		{"start_date in the future", dagState{DagID: "etl", Schedule: "@daily", Catchup: true, StartDate: &nov1}, true,
			[]string{"active", "first run 2026-11-01T00:00:00Z"}, []string{"catchup run"}, false},
		{"many catchup runs", dagState{DagID: "etl", IsPaused: true, Schedule: "* * * * *", Catchup: true, StartDate: &longAgo}, true,
			[]string{"more than 10000 catchup runs", "up to 100 per scheduler tick"}, nil, false},
		{"once", dagState{DagID: "etl", Schedule: "@once"}, true,
			[]string{"active", "@once", "runs once"}, nil, false},
		{"unpaused by this deploy", dagState{DagID: "etl", Schedule: "@daily", Catchup: true, StartDate: &oct1}, false,
			[]string{"active", "the scheduler creates up to 13 catchup runs", "fewer if it already ran"}, []string{"unpause"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nextStepLine(c.state, dagHistory{created: c.created, wasPaused: c.wasPaused}, now)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("line %q does not say %q", got, w)
				}
			}
			for _, n := range c.not {
				if strings.Contains(got, n) {
					t.Errorf("line %q should not say %q", got, n)
				}
			}
		})
	}
}

func TestDagStateFromDetails(t *testing.T) {
	t.Parallel()
	never := "Never, external triggers only"
	daily := "@daily"
	start := "2026-10-01T00:00:00Z"
	s := dagStateFromDetails("etl", dagDetailsRead{IsPaused: true, TimetableSummary: &daily, Catchup: true, StartDate: &start})
	if s.Schedule != "@daily" || !s.IsPaused || !s.Catchup || s.StartDate == nil || s.StartDate.Day() != 1 {
		t.Errorf("state = %+v", s)
	}
	if m := dagStateFromDetails("etl", dagDetailsRead{TimetableSummary: &never}); m.Schedule != "" {
		t.Errorf("the no-schedule summary became schedule %q", m.Schedule)
	}
}
