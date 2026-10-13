package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/dexadata/dexaflow/internal/domain"
)

// maxCountedCatchupRuns bounds how many catchup slots the deploy summary
// counts; past it the line says "more than".
const maxCountedCatchupRuns = 10000

// catchupRunsPerTick is how many catchup runs the scheduler creates per tick
// (maxCatchupSlotsPerTick in internal/scheduler), quoted when there are more.
const catchupRunsPerTick = 100

// dagState is what the deploy summary needs to say what happens next: the
// DAG's is_paused and the schedule fields that decide its runs.
type dagState struct {
	DagID     string
	IsPaused  bool
	Schedule  string // "" when the DAG has no cron schedule
	Catchup   bool
	StartDate *time.Time
}

// dagDetailsRead is the part of GET /api/v2/dags/{dag_id}/details (Airflow
// 3.2) the deploy summary reads.
type dagDetailsRead struct {
	IsPaused         bool    `json:"is_paused"`
	TimetableSummary *string `json:"timetable_summary"`
	Catchup          bool    `json:"catchup"`
	StartDate        *string `json:"start_date"`
}

// dagStateFromDetails maps the details of dagID to a dagState. The
// timetable_summary is the schedule expression; the server's text for a DAG
// without one is not a schedule and reads as none.
func dagStateFromDetails(dagID string, d dagDetailsRead) dagState {
	s := dagState{DagID: dagID, IsPaused: d.IsPaused, Catchup: d.Catchup}
	if d.TimetableSummary != nil {
		expr := strings.TrimSpace(*d.TimetableSummary)
		if domain.IsOnceSchedule(expr) || scheduleOf(expr) != nil {
			s.Schedule = expr
		}
	}
	if d.StartDate != nil {
		if t, err := time.Parse(time.RFC3339, *d.StartDate); err == nil {
			s.StartDate = &t
		}
	}
	return s
}

// scheduleOf parses a cron expression the way the scheduler does, nil when it
// is not one (no schedule, @once, @continuous, or text).
func scheduleOf(expr string) cron.Schedule {
	if domain.IsCronlessSchedule(expr) {
		return nil
	}
	s, err := cron.ParseStandard(expr)
	if err != nil {
		return nil
	}
	return s
}

// fetchDagState reads the DAG's details after a deploy. ok is false when they
// cannot be read; the summary then leaves the next-step line out.
func fetchDagState(ctx context.Context, serverURL, token, dagID string) (dagState, bool) {
	u := strings.TrimRight(serverURL, "/") + "/api/v2/dags/" + url.PathEscape(dagID) + "/details"
	var d dagDetailsRead
	ok, err := getControlPlaneJSON(ctx, io.Discard, u, token, dagID, &d)
	if !ok || err != nil {
		return dagState{}, false
	}
	return dagStateFromDetails(dagID, d), true
}

// formatTick prints a schedule time the way the scheduler reads it, in UTC.
func formatTick(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// dagHistory is what the deploy knows about the DAG before it: whether this
// deploy created it, and whether it was paused (so --active released it).
type dagHistory struct {
	created   bool
	wasPaused bool
}

// nextStepLine says, in one sentence, what happens to the DAG after this
// deploy: active with its next run, or paused with what unpausing does. The
// scheduler starts an active DAG from scratch when the deploy created it,
// and releases its backlog when the deploy unpaused it (h.wasPaused); an
// active DAG that was already active just runs at its next tick. It mirrors
// the scheduler (internal/scheduler dueSlots): with catchup and a start_date
// every missed tick since start_date (or since the last run) gets a run,
// otherwise only the latest missed tick does, and cron times are UTC.
func nextStepLine(s dagState, h dagHistory, now time.Time) string {
	sched := scheduleOf(s.Schedule)
	if sched == nil {
		return noCronLine(s)
	}
	now = now.UTC()
	floor := now
	if s.StartDate != nil && s.StartDate.After(now) {
		floor = s.StartDate.UTC().Add(-time.Nanosecond)
	}
	next := sched.Next(floor)
	if !s.IsPaused && !h.created && !h.wasPaused {
		return fmt.Sprintf("%s is active; next run %s (UTC).", s.DagID, formatTick(next))
	}
	start := startClause(sched, s, h.created, now, next)
	if !s.IsPaused {
		return fmt.Sprintf("%s is active; the scheduler %s.", s.DagID, start)
	}
	return fmt.Sprintf("%s is paused, so nothing runs until `dexaflow admin dags unpause %s`; unpausing %s.",
		s.DagID, s.DagID, start)
}

// noCronLine is nextStepLine for a DAG without a cron schedule: manual only,
// or @once.
func noCronLine(s dagState) string {
	if domain.IsOnceSchedule(s.Schedule) {
		if s.IsPaused {
			return fmt.Sprintf("%s is paused (@once); after `dexaflow admin dags unpause %s` it runs once, if it has not run yet.", s.DagID, s.DagID)
		}
		return fmt.Sprintf("%s is active (@once): it runs once at the next scheduler tick, if it has not run yet.", s.DagID)
	}
	if s.IsPaused {
		return fmt.Sprintf("%s is paused and has no schedule; unpause it with `dexaflow admin dags unpause %s`, then trigger runs with `dexaflow runs trigger %s`.",
			s.DagID, s.DagID, s.DagID)
	}
	return fmt.Sprintf("%s is active and has no schedule; it runs only when triggered (`dexaflow runs trigger %s`).", s.DagID, s.DagID)
}

// startClause says what the scheduler does when it first sees the DAG active:
// the catchup runs since start_date, or one run for the latest tick, then one
// per tick. For a DAG that existed before, the catchup count is an upper
// bound: catchup resumes after its last run.
func startClause(sched cron.Schedule, s dagState, created bool, now, next time.Time) string {
	if s.StartDate != nil && s.StartDate.After(now) {
		return "creates the first run " + formatTick(next) + " (UTC), at start_date"
	}
	if s.Catchup && s.StartDate != nil {
		n := countTicks(sched, s.StartDate.UTC(), now, maxCountedCatchupRuns+1)
		if n > 0 {
			return fmt.Sprintf("creates %s since start_date %s%s, then one run per tick; next tick %s (UTC)",
				catchupCount(n, created), formatTick(*s.StartDate), perTickNote(n), formatTick(next))
		}
	}
	if latest, ok := latestTick(sched, s.StartDate, now); ok {
		return fmt.Sprintf("creates one run for the latest tick %s, then one per tick; next tick %s (UTC)",
			formatTick(latest), formatTick(next))
	}
	return "creates the first run " + formatTick(next) + " (UTC)"
}

// catchupCount words n catchup runs, an upper bound for a DAG that may have
// run before.
func catchupCount(n int, created bool) string {
	count := fmt.Sprintf("%d catchup runs", n)
	if n == 1 {
		count = "1 catchup run"
	}
	if n > maxCountedCatchupRuns {
		count = fmt.Sprintf("more than %d catchup runs", maxCountedCatchupRuns)
	}
	if created {
		return count
	}
	return "up to " + count + " (fewer if it already ran)"
}

// perTickNote says that a long catchup is spread over scheduler ticks.
func perTickNote(n int) string {
	if n <= catchupRunsPerTick {
		return ""
	}
	return fmt.Sprintf(" (up to %d per scheduler tick)", catchupRunsPerTick)
}

// countTicks counts the schedule's ticks in [from, to], stopping at limit.
func countTicks(sched cron.Schedule, from, to time.Time, limit int) int {
	n := 0
	for t := sched.Next(from.Add(-time.Nanosecond)); !t.After(to) && n < limit; t = sched.Next(t) {
		n++
	}
	return n
}

// latestTick is the schedule's last tick at or before now and not before
// start (when set), as the scheduler picks it without catchup.
func latestTick(sched cron.Schedule, start *time.Time, now time.Time) (time.Time, bool) {
	n1 := sched.Next(now)
	period := sched.Next(n1).Sub(n1)
	if period <= 0 {
		return time.Time{}, false
	}
	prev := sched.Next(now.Add(-period))
	for {
		after := sched.Next(prev)
		if after.After(now) {
			break
		}
		prev = after
	}
	if prev.After(now) || (start != nil && prev.Before(start.UTC())) {
		return time.Time{}, false
	}
	return prev, true
}
