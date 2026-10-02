package domain

import (
	"fmt"
	"strings"

	"github.com/robfig/cron/v3"
)

// cronlessSchedules are Airflow string schedules that are valid but are NOT cron
// expressions (they select a non-cron timetable). Dexaflow does not cron-schedule
// them, but they must still register, so they are accepted at compile and skipped
// — not flagged as malformed — by the scheduler.
var cronlessSchedules = map[string]bool{"@once": true, "@continuous": true}

// IsCronlessSchedule reports whether expr is empty (manual-only) or a recognized
// non-cron Airflow schedule. Such a schedule is valid but is never run on a cron,
// so callers skip cron handling for it without treating it as an error.
func IsCronlessSchedule(expr string) bool {
	e := strings.ToLower(strings.TrimSpace(expr))
	return e == "" || cronlessSchedules[e]
}

// IsOnceSchedule reports whether expr is Airflow's "@once" — a DAG that runs
// exactly one time (on first scheduler sight) and never again.
func IsOnceSchedule(expr string) bool {
	return strings.EqualFold(strings.TrimSpace(expr), "@once")
}

// ValidateSchedule checks that a DAG's cron schedule is parseable. An empty or
// absent schedule (manual-only) and the recognized non-cron Airflow schedules
// (@once, @continuous) are valid. A malformed cron expression — a 4-field cron,
// a typo — is rejected here so it fails loudly at compile time; otherwise the
// scheduler silently can't parse it and the DAG simply never runs, with no error
// surfaced anywhere (the worst failure mode). The parser is robfig/cron's
// ParseStandard, the same one the scheduler uses, so what validates here is
// exactly what the scheduler can run (see scheduler/cron.go).
func (d *DAGSpec) ValidateSchedule() error {
	if d.Schedule == nil {
		return nil
	}
	expr := strings.TrimSpace(*d.Schedule)
	if IsCronlessSchedule(expr) {
		return nil
	}
	if _, err := cron.ParseStandard(expr); err != nil {
		return fmt.Errorf("invalid schedule %q: %v; Dexaflow supports standard 5-field cron "+
			`(e.g. "*/3 * * * *" for every 3 minutes), the @hourly/@daily/@weekly/@monthly/@yearly presets, and @once`, expr, err)
	}
	return nil
}
