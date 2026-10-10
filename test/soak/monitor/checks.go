package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
)

// violation is one invariant that did not hold, with enough evidence attached
// that a reader on Monday morning does not have to re-derive the state.
type violation struct {
	At       time.Time `json:"at"`
	ElapsedS float64   `json:"elapsed_s"`
	Check    string    `json:"check"`
	Detail   string    `json:"detail"`
	Value    float64   `json:"value"`
	Limit    float64   `json:"limit"`
	Label    string    `json:"label"`
}

// faultWindow is a fault the harness declared. The monitor reads these but does
// not trust them: a window says when the harness THOUGHT the fault ran, and the
// recovery checks are what test whether reality agreed.
type faultWindow struct {
	Kind  string    `json:"kind"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Note  string    `json:"note,omitempty"`
}

// checker evaluates every invariant against one sample.
//
// The design rule is that each check names a property, not a mechanism, and
// carries its own suppression rule for declared fault windows. Nothing here is
// relaxed to make a run pass: a check either holds unconditionally or it holds
// after a stated recovery budget.
type checker struct {
	o      options
	counts map[string]int
	// firstUnhealthy tracks the start of the current unhealthy streak so the
	// health check can express itself as a duration rather than a sample count.
	firstUnhealthy time.Time
	sawHealthy     bool
	// stopLatch makes the stop condition sticky: once tripped it stays tripped,
	// so a directory that shrinks between samples cannot un-stop the run.
	stopLatch string
	// unreachable counts CONSECUTIVE samples with no API. Reset on any reachable
	// sample, so a blip during an injected restart does not accumulate.
	unreachable int
	// lastStepDowns is the previous sample's cumulative step-down counter, so
	// leader_churn can report EVENTS instead of re-reporting a total.
	//
	// A monotonic counter compared against a limit of zero fires on every sample
	// for the rest of the run once it has moved once. A 15.7h soak found a real
	// bug (#1199) that stepped the leader down 15 times and reported it 5198
	// times, which made one finding 44% of the run's violation total and buried
	// the other two. The condition here is an EVENT, not a state, and a count
	// that is really the sample rate tells a reader nothing about either.
	lastStepDowns float64
	seenStepDowns bool
}

func newChecker(o options) *checker {
	return &checker{o: o, counts: map[string]int{}}
}

// Totals returns the per-check violation counts accumulated so far.
func (c *checker) Totals() map[string]int { return c.counts }

// loadFaultWindows re-reads the declared fault file every sample. It is re-read
// rather than cached because the harness appends to it while the monitor runs.
func (c *checker) loadFaultWindows() []faultWindow {
	if c.o.faultsFile == "" {
		return nil
	}
	f, err := os.Open(c.o.faultsFile) //nolint:gosec // operator-supplied harness path
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // best-effort close of a read-only file
	var out []faultWindow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var w faultWindow
		if json.Unmarshal(sc.Bytes(), &w) == nil && !w.Start.IsZero() {
			out = append(out, w)
		}
	}
	return out
}

// inFaultWindow reports whether t falls inside a declared window, extended by
// the recovery budget. Checks that a fault legitimately disturbs are suppressed
// inside it; checks about recovery are not.
func (c *checker) inFaultWindow(t time.Time, ws []faultWindow) bool {
	for _, w := range ws {
		end := w.End
		if end.IsZero() {
			end = w.Start.Add(c.o.recoveryBudget)
		}
		if !t.Before(w.Start) && !t.After(end.Add(c.o.recoveryBudget)) {
			return true
		}
	}
	return false
}

// Check evaluates every invariant and returns the violations for this sample.
// The checks are split across three helpers by the rule that governs each group:
// correctness holds unconditionally, wedge and liveness hold outside a declared
// fault window, and cadence needs enough elapsed time to have an expectation.
func (c *checker) Check(s sample) []violation {
	var out []violation
	add := func(name, detail string, value, limit float64) {
		out = append(out, violation{
			At: s.At, ElapsedS: s.ElapsedS, Check: name, Detail: detail,
			Value: value, Limit: limit, Label: s.Label,
		})
		c.counts[name]++
	}
	// A control plane that is GONE is not a control plane behaving badly, and
	// every check below would describe the corpse rather than the illness: with
	// the API unreachable, every DAG looks starved, every schedule looks late and
	// the scheduler looks unhealthy, all of which are true statements about a
	// meaningless situation.
	//
	// This is not hypothetical. A weekend run lost its control plane 28 minutes
	// in and the monitor spent the next hour and a half accumulating 2332
	// violations against nothing, which would have produced a red verdict with
	// thousands of entries and no signal whatsoever. A soak that cannot tell
	// "unhealthy" from "absent" is a soak whose FAIL cannot be trusted.
	//
	// So an unreachable API suppresses the rest and is counted instead. Past the
	// grace period the run is over: the monitor stops and reports a HARNESS
	// failure rather than a product one, because the subject of the experiment
	// left.
	// Correctness runs regardless, because those invariants read the DATABASE and
	// not the API: a terminal run holding an active task is still wrong, and
	// still worth catching, when the control plane that produced it has exited.
	// Only the checks that describe a live system are suppressed.
	c.checkCorrectness(s, add)

	if !s.APIReachable {
		c.unreachable++
		if c.unreachable >= unreachableSamplesBeforeGivingUp {
			add("control_plane_gone",
				fmt.Sprintf("the API has been unreachable for %d consecutive samples; the soak has no subject left and every other check would be describing its absence",
					c.unreachable), float64(c.unreachable), float64(unreachableSamplesBeforeGivingUp))
		}
		return out
	}
	c.unreachable = 0

	c.checkWedges(s, add)
	c.checkLiveness(s, add)
	c.checkCadence(s, add)
	c.checkPunctuality(s, add)
	return out
}

// unreachableSamplesBeforeGivingUp is how many consecutive unreachable samples
// end the run. It is not 1: a restart fault deliberately takes the API away for
// a few seconds, and a single missed scrape during one is the harness working.
// At the shipped 10s cadence this is two minutes, which is far longer than any
// injected fault and far shorter than a wasted weekend.
const unreachableSamplesBeforeGivingUp = 12

// Gone reports whether the run ended because the control plane disappeared, so
// the caller can exit with the harness code instead of the violation one.
func (c *checker) Gone() bool { return c.unreachable >= unreachableSamplesBeforeGivingUp }

// addFunc records one violation. Passed down so each check group appends into the
// same slice without any of them owning it.
type addFunc func(name, detail string, value, limit float64)

// checkCorrectness evaluates the invariants that need no timing at all: they are
// either true or the system is wrong, right now. Nothing suppresses them.
func (c *checker) checkCorrectness(s sample, add addFunc) {
	if s.TerminalRunsWithActiveTIs > 0 {
		add("terminal_run_with_active_task",
			fmt.Sprintf("%d task instances are still non-terminal on a run the control plane already called terminal; the dashboard is lying about what is running",
				s.TerminalRunsWithActiveTIs), float64(s.TerminalRunsWithActiveTIs), 0)
	}
	if s.OverRetriedTIs > 0 {
		add("retry_budget_exceeded",
			fmt.Sprintf("%d task instances have try_number > max_tries", s.OverRetriedTIs),
			float64(s.OverRetriedTIs), 0)
	}
	if s.SuccessInHistory > 0 {
		add("success_archived",
			fmt.Sprintf("%d archived attempts are in state success; every rail that archives except the admin clear is guarded to a non-success state, so this is either an operator clearing a successful task or a reset rail that lost its guard. It is NOT proof of a replay, and its absence is not proof of at-most-once: see test/soak/README.md section 1",
				s.SuccessInHistory), float64(s.SuccessInHistory), 0)
	}
	if s.UpstreamFailedExecuted > 0 {
		add("upstream_failed_task_ran",
			fmt.Sprintf("%d instances of soak_flaky.never_runs were started; its upstream always fails, so it must never execute (it raises on entry, so it can never end in success: started_at is the evidence)",
				s.UpstreamFailedExecuted), float64(s.UpstreamFailedExecuted), 0)
	}
	if s.ImportErrors > 0 {
		add("import_error",
			fmt.Sprintf("%d DAGs are failing to import; a DAG that stopped compiling stops producing runs and the cadence check would only notice later",
				s.ImportErrors), float64(s.ImportErrors), 0)
	}
	// Reported per NEW step-down, not per sample on which the total is nonzero.
	// See lastStepDowns: the counter is cumulative, so the obvious `> 0` fires
	// forever after the first event and the violation count becomes the sample
	// rate. Every step-down is still a violation and still fails the run; what
	// changes is that fifteen of them are counted fifteen times.
	if !math.IsNaN(float64(s.StepDowns)) && s.StepDowns > 0 {
		cur := float64(s.StepDowns)
		if !c.seenStepDowns || cur > c.lastStepDowns {
			since := cur
			if c.seenStepDowns {
				since = cur - c.lastStepDowns
			}
			add("leader_churn",
				fmt.Sprintf("%.0f new scheduler step-down(s) since the last sample (leoflow_scheduler_step_downs_total = %.0f) in a single-process soak; nothing is contending for the lock, so a step-down means the leader lost it",
					since, cur), cur, 0)
		}
		c.lastStepDowns, c.seenStepDowns = cur, true
	}
	if !math.IsNaN(float64(s.Undispatchable)) && s.Undispatchable > 0 {
		add("undispatchable_task",
			fmt.Sprintf("dexaflow_tasks_undispatchable_total = %.0f; a task was queued with no executor able to launch it", float64(s.Undispatchable)),
			float64(s.Undispatchable), 0)
	}
}

// checkWedges evaluates what "the scheduler did not wedge" means as a
// measurement. A declared fault legitimately disturbs these, so they are
// suppressed inside a window plus the recovery budget, and only there.
func (c *checker) checkWedges(s sample, add addFunc) {
	if !s.InFaultWindow {
		if s.OldestQueuedS > c.o.queuedWedge.Seconds() {
			add("queued_wedge",
				fmt.Sprintf("a task instance has been in `queued` for %.0fs; the dispatch-lost threshold is %.0fs, so at this age the control plane itself considers the dispatch lost",
					s.OldestQueuedS, c.o.queuedWedge.Seconds()), s.OldestQueuedS, c.o.queuedWedge.Seconds())
		}
		if s.OldestScheduledS > c.o.scheduledWedge.Seconds() {
			add("scheduled_wedge",
				fmt.Sprintf("a task instance has been in `scheduled` for %.0fs without being dispatched", s.OldestScheduledS),
				s.OldestScheduledS, c.o.scheduledWedge.Seconds())
		}
		if s.OldestRunningRun > c.o.runWedge.Seconds() {
			add("run_wedge",
				fmt.Sprintf("a dag run has been `running` for %.0fs, past the %.0fs wedge threshold; the longest DAG in the battery (soak_token) is written to finish in forty minutes",
					s.OldestRunningRun, c.o.runWedge.Seconds()),
				s.OldestRunningRun, c.o.runWedge.Seconds())
		}
	}
}

// checkLiveness asserts that the scheduler is healthy outside a declared window
// extended by the recovery budget. A fault that outlives the window the harness
// declared for it lands here, which is exactly the "the incident we thought was
// over was not" case.
func (c *checker) checkLiveness(s sample, add addFunc) {
	healthy := s.SchedulerHealth == "healthy" || s.SchedulerHealth == "ready"
	if healthy {
		c.sawHealthy = true
		c.firstUnhealthy = time.Time{}
	} else if c.firstUnhealthy.IsZero() {
		c.firstUnhealthy = s.At
	}
	if !healthy && !s.InFaultWindow && c.sawHealthy {
		streak := s.At.Sub(c.firstUnhealthy).Seconds()
		if streak >= c.o.healthTolerate.Seconds() {
			add("scheduler_unhealthy",
				fmt.Sprintf("scheduler health is %q (source %s) for %.0fs with no declared fault window open",
					s.SchedulerHealth, s.HealthSource, streak), streak, c.o.healthTolerate.Seconds())
		}
	}
}

// checkCadence catches the wedge that heartbeats: a scheduler that looks healthy
// and has quietly stopped creating scheduled runs. Only evaluated once the soak
// has run long enough for the expectation to be meaningful.
func (c *checker) checkCadence(s sample, add addFunc) {
	if !s.InFaultWindow && s.WindowMin >= 2*minCadencePeriodMin {
		for dagID, periodMin := range cadenceExpectations {
			expected := s.WindowMin / float64(periodMin)
			floorRuns := math.Floor(expected * 0.5)
			if floorRuns < 1 {
				continue
			}
			got := float64(s.RunsCreated[dagID])
			if got < floorRuns {
				add("cadence_starved",
					fmt.Sprintf("%s created %.0f runs in the last %.0f min; its schedule is every %d min, so at least %.0f were due",
						dagID, got, s.WindowMin, periodMin, floorRuns), got, floorRuns)
			}
		}
	}
}

// checkPunctuality catches the scheduler that honors every schedule LATE.
//
// checkCadence asks whether the runs exist, and a scheduler twenty minutes
// behind still creates every run it owes, so volume alone reports that
// deployment as healthy. Lateness is the thing an operator actually feels: a
// pipeline due at 02:00 that starts at 02:40 has missed its window even though
// nothing failed.
//
// The threshold is a MULTIPLE of each DAG's own period rather than one constant.
// Thirty seconds late is nothing for an hourly DAG and is most of the interval
// for a two-minute one, so a single number would either be noise on the short
// schedules or blind on the long ones.
//
// Skipped inside a fault window, where being late is the correct behavior, and
// while the window is too short to have produced a scheduled run at all.
func (c *checker) checkPunctuality(s sample, add addFunc) {
	if s.InFaultWindow || s.WindowMin < float64(minCadencePeriodMin) {
		return
	}
	for dagID, periodMin := range cadenceExpectations {
		worst, ok := s.WorstLatenessS[dagID]
		if !ok || worst <= 0 {
			continue
		}
		budget := float64(periodMin) * 60 * latenessBudgetOfPeriod
		// A lateness measured in DAYS is not a late scheduler, it is a run whose
		// logical_date sits in the past by construction: a backdated start_date,
		// a catchup backfill, or a DAG registered long after its first interval.
		// Reporting those as punctuality would be arithmetically true and
		// operationally meaningless, and on this battery it was: soak_token
		// declared start_date=2026-01-01 and produced a single violation of
		// 20,843,777 seconds, then repeated it every ten seconds for hours.
		//
		// The ceiling is absolute rather than another multiple of the period,
		// because the thing being excluded is a different phenomenon and not a
		// larger amount of the same one.
		if worst > backfillLatenessCeilingS {
			continue
		}
		if worst > budget {
			add("schedule_late",
				fmt.Sprintf("%s created a scheduled run %.0fs after it was due; its schedule is every %d min, so the budget is %.0fs. Runs exist, which is what the cadence check sees, and they are arriving late",
					dagID, worst, periodMin, budget), worst, budget)
		}
	}
}

// latenessBudgetOfPeriod is how much of a DAG's own interval a run may burn
// before it is late. Half an interval is generous: past that the schedule is
// closer to the next slot than to its own, and two consecutive such runs would
// start colliding with each other.
const latenessBudgetOfPeriod = 0.5

// backfillLatenessCeilingS is where "late" stops meaning late. A day is far
// beyond any schedule this battery runs (the longest is hourly) and far below
// the gap a backdated logical_date produces, so nothing real is hidden by it.
const backfillLatenessCeilingS = 24 * 60 * 60

// cadenceExpectations maps each soak DAG to its cron period in minutes. It is a
// literal rather than something read out of the database on purpose: the check
// exists to catch a scheduler that stopped honoring a schedule, and deriving the
// expectation from the same system under test would let it agree with itself.
var cadenceExpectations = map[string]int{
	"soak_ingest":    5,
	"soak_fanout":    5,
	"soak_chain":     3,
	"soak_operators": 5,
	"soak_flaky":     7,
	"soak_long":      15,
	// Hourly on purpose: its body defaults to forty minutes, so a run must never
	// collide with the next. See test/soak/dags/soak_token/dag.py.
	"soak_token": 60,
}

// minCadencePeriodMin is the shortest schedule in the battery; the cadence check
// waits for two of those before it says anything.
const minCadencePeriodMin = 3

// stopCondition returns a non-empty reason when the soak must stop cleanly. A
// stop is not a failure: it is the harness refusing to keep consuming a resource
// it promised to bound, and it happens while the evidence is still writable.
func (c *checker) stopCondition(s sample) string {
	if c.stopLatch != "" {
		return c.stopLatch
	}
	db := s.DBBytes
	if s.DBClusterBytes > db {
		db = s.DBClusterBytes
	}
	switch {
	case c.o.maxDBBytes > 0 && db > c.o.maxDBBytes:
		c.stopLatch = fmt.Sprintf("db_budget_exceeded:%d>%d", db, c.o.maxDBBytes)
	case c.o.maxDataBytes > 0 && s.DataBytes > c.o.maxDataBytes:
		c.stopLatch = fmt.Sprintf("data_budget_exceeded:%d>%d", s.DataBytes, c.o.maxDataBytes)
	case c.o.minFreeBytes > 0 && s.FreeBytes >= 0 && s.FreeBytes < c.o.minFreeBytes:
		c.stopLatch = fmt.Sprintf("disk_free_floor:%d<%d", s.FreeBytes, c.o.minFreeBytes)
	}
	return c.stopLatch
}

// isEarlyStop reports whether a stop reason means the soak ended before the
// wall-clock ceiling it was given. Every budget latch does; a duration reached
// and a signal do not. The distinction is what an unattended operator needs on
// Monday: a report that covers 40 minutes of a 48 h request is not a clean run,
// whatever the violation count says.
func isEarlyStop(reason string) bool {
	for _, p := range []string{"db_budget_exceeded", "data_budget_exceeded", "disk_free_floor"} {
		if strings.HasPrefix(reason, p) {
			return true
		}
	}
	return false
}
