package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// defaultOptions returns the thresholds the harness ships with, so a test that
// changes behavior by moving a threshold has to say so out loud.
func defaultOptions() options {
	return options{
		queuedWedge:    180 * time.Second,
		scheduledWedge: 300 * time.Second,
		runWedge:       90 * time.Minute,
		recoveryBudget: 2 * time.Minute,
		healthTolerate: 0,
		maxDBBytes:     4 << 30,
		maxDataBytes:   4 << 30,
		minFreeBytes:   10 << 30,
	}
}

// healthySample is a sample in which nothing is wrong. Each test perturbs
// exactly one field, so a failure names the invariant rather than the fixture.
func healthySample() sample {
	return sample{
		At:              time.Now(),
		SchedulerHealth: "healthy",
		HealthSource:    "monitor_health",
		StepDowns:       0,
		Undispatchable:  0,
		DBBytes:         10 << 20,
		DataBytes:       10 << 20,
		FreeBytes:       100 << 30,
		RunsCreated:     map[string]int{},
		WorstLatenessS:  map[string]float64{},
		// The healthy state includes the API answering. Leaving this at the zero
		// value made every fixture describe a control plane that was not there,
		// which is exactly the situation the reachability gate now suppresses.
		APIReachable: true,
	}
}

// violationsFor counts how many violations of one check a sample produces, on
// top of the healthy fixture so a test states only the field it is about.
func violationsFor(t *testing.T, over sample, check string) int {
	t.Helper()
	s := healthySample()
	if over.WindowMin != 0 {
		s.WindowMin = over.WindowMin
	}
	// Give every DAG a healthy count by default. A fixture that names one DAG
	// leaves the other five looking starved, so a punctuality test would be
	// reading cadence violations and calling them its own.
	for dag, periodMin := range cadenceExpectations {
		s.RunsCreated[dag] = int(s.WindowMin/float64(periodMin)) + 1
	}
	s.InFaultWindow = over.InFaultWindow
	for dag, n := range over.RunsCreated {
		s.RunsCreated[dag] = n
	}
	if over.WorstLatenessS != nil {
		s.WorstLatenessS = over.WorstLatenessS
	}
	n := 0
	for _, v := range newChecker(defaultOptions()).Check(s) {
		if v.Check == check {
			n++
		}
	}
	return n
}

func checkNames(vs []violation) map[string]bool {
	out := map[string]bool{}
	for _, v := range vs {
		out[v.Check] = true
	}
	return out
}

// TestHealthySampleProducesNoViolations is the floor under every other test
// here: if the neutral fixture is already red, nothing below proves anything.
func TestHealthySampleProducesNoViolations(t *testing.T) {
	c := newChecker(defaultOptions())
	if vs := c.Check(healthySample()); len(vs) != 0 {
		t.Fatalf("healthy sample produced %d violations: %v", len(vs), checkNames(vs))
	}
}

// TestCorrectnessInvariantsFireEvenInsideAFaultWindow pins the rule that
// separates the two families of check: a wedge is something a fault can cause,
// while an impossible database state is never excusable. A fault window must not
// be a way to launder one into the other.
func TestCorrectnessInvariantsFireEvenInsideAFaultWindow(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*sample)
		check string
	}{
		{"active task on a terminal run", func(s *sample) { s.TerminalRunsWithActiveTIs = 1 }, "terminal_run_with_active_task"},
		{"try number past max tries", func(s *sample) { s.OverRetriedTIs = 1 }, "retry_budget_exceeded"},
		{"success archived into history", func(s *sample) { s.SuccessInHistory = 1 }, "success_archived"},
		{"upstream-failed task ran", func(s *sample) { s.UpstreamFailedExecuted = 1 }, "upstream_failed_task_ran"},
		{"dag import error", func(s *sample) { s.ImportErrors = 1 }, "import_error"},
		{"fan-out past max_active_tasks", func(s *sample) { s.FanoutMaxActive = 5 }, "max_active_tasks_exceeded"},
		{"leader stepped down", func(s *sample) { s.StepDowns = 1 }, "leader_churn"},
		{"task had no executor", func(s *sample) { s.Undispatchable = 1 }, "undispatchable_task"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newChecker(defaultOptions())
			s := healthySample()
			s.InFaultWindow = true // the suppression that must NOT apply here
			tc.mut(&s)
			got := checkNames(c.Check(s))
			if !got[tc.check] {
				t.Fatalf("expected %q inside a fault window, got %v", tc.check, got)
			}
		})
	}
}

// TestWedgeChecksFireOutsideAWindowAndAreSuppressedInside pins the other half of
// that rule: a declared fault legitimately disturbs these, and only these.
func TestWedgeChecksFireOutsideAWindowAndAreSuppressedInside(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*sample)
		check string
	}{
		{"queued past the dispatch-lost threshold", func(s *sample) { s.OldestQueuedS = 181 }, "queued_wedge"},
		{"scheduled past its threshold", func(s *sample) { s.OldestScheduledS = 301 }, "scheduled_wedge"},
		// Derived from the option rather than hardcoded: the threshold moved once
		// already, when soak_token's forty-minute body turned 1800s into a
		// permanent false positive, and a literal here would have to be chased
		// every time it moves again.
		{"run running past its threshold", func(s *sample) { s.OldestRunningRun = defaultOptions().runWedge.Seconds() + 1 }, "run_wedge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outside := newChecker(defaultOptions())
			s := healthySample()
			tc.mut(&s)
			if got := checkNames(outside.Check(s)); !got[tc.check] {
				t.Fatalf("expected %q outside a fault window, got %v", tc.check, got)
			}

			inside := newChecker(defaultOptions())
			s2 := healthySample()
			tc.mut(&s2)
			s2.InFaultWindow = true
			if got := checkNames(inside.Check(s2)); got[tc.check] {
				t.Fatalf("%q fired inside a declared fault window; a declared fault is allowed to cause it", tc.check)
			}
		})
	}
}

// TestWedgeChecksDoNotFireExactlyAtTheThreshold guards the off-by-one that would
// make every soak red at the boundary: the threshold is the largest acceptable
// age, not the smallest unacceptable one.
func TestWedgeChecksDoNotFireExactlyAtTheThreshold(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.OldestQueuedS = 180
	s.OldestScheduledS = 300
	s.OldestRunningRun = 1800
	if vs := c.Check(s); len(vs) != 0 {
		t.Fatalf("a sample exactly at every threshold produced %v", checkNames(vs))
	}
}

// TestSchedulerUnhealthyNeedsAPriorHealthySample stops the harness from
// reporting a violation for the window between its own start and the control
// plane's first healthy answer, which is a harness artifact and not a defect.
func TestSchedulerUnhealthyNeedsAPriorHealthySample(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.SchedulerHealth = "unreachable"
	if got := checkNames(c.Check(s)); got["scheduler_unhealthy"] {
		t.Fatal("reported unhealthy before ever seeing the control plane healthy")
	}
	// Once healthy has been observed, a later unhealthy sample is real.
	if vs := c.Check(healthySample()); len(vs) != 0 {
		t.Fatalf("healthy sample produced %v", checkNames(vs))
	}
	s2 := healthySample()
	s2.SchedulerHealth = "unreachable"
	if got := checkNames(c.Check(s2)); !got["scheduler_unhealthy"] {
		t.Fatalf("expected scheduler_unhealthy after a healthy sample, got %v", got)
	}
}

// TestReadyzCountsAsHealthy pins the degraded liveness source: when
// /api/v2/monitor/health cannot be read, /readyz answering 200 is reported as
// "ready", and that must not itself be a violation.
func TestReadyzCountsAsHealthy(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.SchedulerHealth, s.HealthSource = "ready", "readyz"
	if vs := c.Check(s); len(vs) != 0 {
		t.Fatalf("readyz-sourced health produced %v", checkNames(vs))
	}
}

// TestCadenceStarvedIsTheWedgeThatHeartbeats is the check no other signal
// covers: a scheduler that answers healthy and has stopped creating runs.
func TestCadenceStarvedIsTheWedgeThatHeartbeats(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.WindowMin = 18 // soak_chain runs every 3 minutes, so 6 were due
	s.RunsCreated = map[string]int{
		"soak_ingest": 4, "soak_fanout": 4, "soak_operators": 4,
		"soak_flaky": 3, "soak_long": 1,
		// soak_chain deliberately absent
	}
	got := checkNames(c.Check(s))
	if !got["cadence_starved"] {
		t.Fatalf("expected cadence_starved for soak_chain, got %v", got)
	}

	// The same window with the runs present must be quiet.
	c2 := newChecker(defaultOptions())
	s2 := healthySample()
	s2.WindowMin = 18
	s2.RunsCreated = map[string]int{
		"soak_chain": 6, "soak_ingest": 4, "soak_fanout": 4,
		"soak_operators": 4, "soak_flaky": 3, "soak_long": 1,
	}
	if vs := c2.Check(s2); len(vs) != 0 {
		t.Fatalf("a window meeting every cadence produced %v", checkNames(vs))
	}
}

// TestCadenceIsSilentBeforeTheWindowIsMeaningful stops the check from firing in
// the first minutes, when no run can have been due yet.
func TestCadenceIsSilentBeforeTheWindowIsMeaningful(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.WindowMin = 5 // below 2 x the shortest schedule in the battery
	if got := checkNames(c.Check(s)); got["cadence_starved"] {
		t.Fatal("cadence_starved fired before any run could have been due")
	}
}

// TestCountersAreNotAssertedWhenTheScrapeFailed pins that an unreachable
// /metrics endpoint (NaN) is not silently read as zero, nor as a violation. The
// health check is what reports an unreachable control plane.
func TestCountersAreNotAssertedWhenTheScrapeFailed(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.StepDowns, s.Undispatchable, s.DispatchAtCap = nanMetric(), nanMetric(), nanMetric()
	if vs := c.Check(s); len(vs) != 0 {
		t.Fatalf("a failed metrics scrape produced %v", checkNames(vs))
	}
}

// TestInFaultWindowExtendsByTheRecoveryBudget pins the suppression geometry the
// self-test depends on: the window closes one recovery budget after the harness
// said the fault ended, and a fault that outlives its declared window is caught.
func TestInFaultWindowExtendsByTheRecoveryBudget(t *testing.T) {
	c := newChecker(defaultOptions())
	start := time.Now()
	ws := []faultWindow{{Kind: "pgpause", Start: start, End: start.Add(45 * time.Second)}}

	for _, tc := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before the fault", start.Add(-time.Second), false},
		{"during the fault", start.Add(20 * time.Second), true},
		{"inside the recovery budget", start.Add(45*time.Second + 90*time.Second), true},
		{"one second past the budget", start.Add(45*time.Second + 2*time.Minute + time.Second), false},
	} {
		if got := c.inFaultWindow(tc.at, ws); got != tc.want {
			t.Errorf("%s: inFaultWindow = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestStopConditionsAreBudgetsNotFailures pins that exceeding a budget stops the
// run and latches, and that the latch cannot be undone by a later sample: a
// directory that shrinks between samples must not un-stop a soak.
func TestStopConditionsAreBudgetsNotFailures(t *testing.T) {
	o := defaultOptions()
	o.maxDBBytes = 1000
	c := newChecker(o)

	s := healthySample()
	s.DBBytes = 999
	if r := c.stopCondition(s); r != "" {
		t.Fatalf("stopped under budget: %q", r)
	}
	s.DBBytes = 1001
	first := c.stopCondition(s)
	if first == "" {
		t.Fatal("did not stop when the database budget was exceeded")
	}
	s.DBBytes = 10
	if again := c.stopCondition(s); again != first {
		t.Fatalf("stop latch was released: %q then %q", first, again)
	}
}

// TestDiskFreeFloorStopsTheRun pins the guard that matters most on a laptop:
// filling the machine's disk is a worse outcome than filling the soak's budget.
func TestDiskFreeFloorStopsTheRun(t *testing.T) {
	o := defaultOptions()
	o.minFreeBytes = 10 << 30
	c := newChecker(o)
	s := healthySample()
	s.FreeBytes = 1 << 30
	if r := c.stopCondition(s); r == "" {
		t.Fatal("did not stop when free space dropped below the floor")
	}

	// An unknown free-space reading (-1) must not be read as "zero free".
	c2 := newChecker(o)
	s2 := healthySample()
	s2.FreeBytes = -1
	if r := c2.stopCondition(s2); r != "" {
		t.Fatalf("stopped on an unknown free-space reading: %q", r)
	}
}

// TestViolationCarriesItsEvidence pins that a violation is readable on Monday
// morning without re-deriving the state: it names the check, the value, the
// limit and says what went wrong in words.
func TestViolationCarriesItsEvidence(t *testing.T) {
	c := newChecker(defaultOptions())
	s := healthySample()
	s.OldestQueuedS = 240
	vs := c.Check(s)
	if len(vs) != 1 {
		t.Fatalf("expected exactly one violation, got %v", checkNames(vs))
	}
	v := vs[0]
	if v.Value != 240 || v.Limit != 180 {
		t.Errorf("value/limit = %v/%v, want 240/180", v.Value, v.Limit)
	}
	if v.Detail == "" || v.At.IsZero() {
		t.Errorf("violation is missing evidence: detail=%q at=%v", v.Detail, v.At)
	}
	if c.Totals()["queued_wedge"] != 1 {
		t.Errorf("violation was not counted: %v", c.Totals())
	}
}

// TestCadenceExpectationsCoverEveryShippedDag stops the battery from silently
// losing its cadence coverage when a DAG is added to test/soak/dags without a
// matching expectation, which would leave that DAG's schedule unasserted.
func TestCadenceExpectationsCoverEveryShippedDag(t *testing.T) {
	shipped := []string{"soak_ingest", "soak_fanout", "soak_chain", "soak_operators", "soak_flaky", "soak_long", "soak_token"}
	for _, dag := range shipped {
		if _, ok := cadenceExpectations[dag]; !ok {
			t.Errorf("%s ships in test/soak/dags but has no cadence expectation", dag)
		}
	}
	if len(cadenceExpectations) != len(shipped) {
		t.Errorf("cadenceExpectations has %d entries for %d shipped DAGs", len(cadenceExpectations), len(shipped))
	}
	for dag, period := range cadenceExpectations {
		if period < minCadencePeriodMin {
			t.Errorf("%s declares a %d-minute period, shorter than minCadencePeriodMin (%d), so the cadence check would wait too little",
				dag, period, minCadencePeriodMin)
		}
	}
}

// TestNeverRunsInvariantKeysOnExecutionNotOnSuccess is a contract between two
// files that have to agree or the invariant is decoration: soak_flaky.never_runs
// raises on entry, so it can NEVER reach `success`, and a query that looks for a
// successful never_runs is a query whose answer is always zero. The invariant is
// "it executed", and the durable evidence of execution is started_at.
func TestNeverRunsInvariantKeysOnExecutionNotOnSuccess(t *testing.T) {
	dag, err := os.ReadFile(filepath.Join("..", "dags", "soak_flaky", "dag.py"))
	if err != nil {
		t.Fatalf("reading the soak_flaky DAG: %v", err)
	}
	body := string(dag)
	if !strings.Contains(body, "def never_runs(") {
		t.Fatal("soak_flaky no longer defines never_runs; the invariant has no subject")
	}
	// The task raises, so `state = 'success'` is unreachable for it by construction.
	if !strings.Contains(body, "raise AssertionError") {
		t.Fatal("never_runs no longer raises; re-derive which states the query may key on")
	}
	if !strings.Contains(correctnessQuery, "task_id = 'never_runs' AND started_at IS NOT NULL") {
		t.Errorf("the never_runs invariant does not key on started_at; a task that raises can only be caught by evidence that it ran:\n%s", correctnessQuery)
	}
	if strings.Contains(correctnessQuery, "task_id = 'never_runs' AND state = 'success'") {
		t.Error("the never_runs invariant keys on success, which never_runs cannot reach: the check can never fire")
	}
}

// TestCadenceExpectationsMatchTheShippedSchedules closes the gap the literal
// table opens on purpose. Deriving the expectation from the database would let
// the system under test agree with itself, so the table is hand-written; the
// cost of that choice is that a DAG whose cron changes leaves the table wrong,
// and a wrong table is either a false red or, worse, a cadence check that a
// slower schedule can never trip. This asserts the two agree at build time,
// which is the one place it can be done without asking the scheduler.
func TestCadenceExpectationsMatchTheShippedSchedules(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "dags"))
	if err != nil {
		t.Fatalf("reading test/soak/dags: %v", err)
	}
	// Two forms, because the battery needs both. `*/N * * * *` is every N
	// minutes; `0 * * * *` is hourly, which a task longer than an hour's worth of
	// interval needs so a run cannot collide with the next one. Anything else is
	// rejected rather than guessed at: a cron this cannot read would silently get
	// no expectation, and a cadence check with no expectation is a check that
	// cannot fail.
	reEveryN := regexp.MustCompile(`schedule\s*=\s*"\*/(\d+) \* \* \* \*"`)
	reHourly := regexp.MustCompile(`schedule\s*=\s*"0 \* \* \* \*"`)
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		src, rerr := os.ReadFile(filepath.Join("..", "dags", e.Name(), "dag.py"))
		if rerr != nil {
			t.Errorf("%s has no dag.py: %v", e.Name(), rerr)
			continue
		}
		var period int
		switch m := reEveryN.FindSubmatch(src); {
		case m != nil:
			p, cerr := strconv.Atoi(string(m[1]))
			if cerr != nil {
				t.Fatalf("%s: unparsable period %q", e.Name(), m[1])
			}
			period = p
		case reHourly.Match(src):
			period = 60
		default:
			t.Errorf("%s declares no schedule the cadence check can express (`*/N * * * *` or `0 * * * *`)", e.Name())
			continue
		}
		seen[e.Name()] = true
		want, ok := cadenceExpectations[e.Name()]
		if !ok {
			t.Errorf("%s ships in test/soak/dags but has no cadence expectation", e.Name())
			continue
		}
		if want != period {
			t.Errorf("%s runs every %d min but cadenceExpectations says %d", e.Name(), period, want)
		}
	}
	for dag := range cadenceExpectations {
		if !seen[dag] {
			t.Errorf("cadenceExpectations names %s, which no longer ships in test/soak/dags", dag)
		}
	}
}

// TestStopConditionCountsEveryDatabaseInTheCluster pins the budget against what
// is actually on the disk. `pg_database_size(current_database())` sees the
// metadatabase only, while the operator leg writes into a second database
// (soak_warehouse) in the same container and on the same volume. A cap that
// measures one of the two is not the cap the README promises.
func TestStopConditionCountsEveryDatabaseInTheCluster(t *testing.T) {
	o := defaultOptions()
	o.maxDBBytes = 1000
	c := newChecker(o)
	s := healthySample()
	s.DBBytes = 400          // the metadatabase alone is under budget
	s.DBClusterBytes = 1_400 // every database on the volume is not
	if r := c.stopCondition(s); r == "" {
		t.Fatal("did not stop when the cluster exceeded the database budget")
	}
}

// TestStopConditionFallsBackToTheMetadatabase keeps the cap working when the
// cluster-wide reading is unavailable (an unprivileged role, an old server):
// an unknown total must never be read as zero.
func TestStopConditionFallsBackToTheMetadatabase(t *testing.T) {
	o := defaultOptions()
	o.maxDBBytes = 1000
	c := newChecker(o)
	s := healthySample()
	s.DBBytes = 1_400
	s.DBClusterBytes = 0
	if r := c.stopCondition(s); r == "" {
		t.Fatal("did not stop on the metadatabase reading when the cluster reading was missing")
	}
}

// TestEveryEarlyStopIsReportedAsAnEarlyStop pins the one thing an unattended
// operator reads on Monday: whether the run they asked for actually ran. All
// three stop conditions end the soak before its ceiling, so all three must be
// annotated the same way and must not be reported as a completed clean run.
func TestEveryEarlyStopIsReportedAsAnEarlyStop(t *testing.T) {
	for _, reason := range []string{"db_budget_exceeded:2>1", "data_budget_exceeded:2>1", "disk_free_floor:1<2"} {
		if !isEarlyStop(reason) {
			t.Errorf("%q is a stop condition but is not reported as an early stop", reason)
		}
	}
	for _, reason := range []string{"duration_reached", "signal"} {
		if isEarlyStop(reason) {
			t.Errorf("%q is not a budget stop but was reported as one", reason)
		}
	}
}

// TestShapeTableCarriesTheBaselineProbe pins the control that makes the headline
// reading interpretable. The tick probe is timed from another process on a
// laptop that is also doing other things, so a bucket where the probe rose is
// ambiguous between "ActiveRuns got more expensive" and "everything did". A
// trivial round trip taken in the same sample separates the two, and it has to
// reach the table a reader actually looks at.
func TestShapeTableCarriesTheBaselineProbe(t *testing.T) {
	r := &reporter{o: defaultOptions()}
	for i := 0; i < 8; i++ {
		r.trend = append(r.trend, trendPoint{
			elapsedS: float64(i * 10), tickProbe: 2.0, baseline: 0.4,
			totalRuns: int64(i), activeRuns: 1,
		})
	}
	md := r.renderShape()
	if !strings.Contains(md, "baseline") {
		t.Errorf("the shape table has no baseline column, so a reader cannot tell a slower ActiveRuns from a slower machine:\n%s", md)
	}
}

// TestPunctualityIsNotSatisfiedByVolume pins the gap punctuality exists to fill.
//
// checkCadence asks whether the runs exist. A scheduler running far behind still
// creates every run it owes, so a deployment that is late but complete reads as
// healthy to it. These two cases share a sample that cadence is happy with, and
// differ only in lateness.
func TestPunctualityIsNotSatisfiedByVolume(t *testing.T) {
	onTime := sample{
		WindowMin:      60,
		RunsCreated:    map[string]int{"soak_ingest": 30},
		WorstLatenessS: map[string]float64{"soak_ingest": 12},
	}
	late := sample{
		WindowMin:      60,
		RunsCreated:    map[string]int{"soak_ingest": 30},
		WorstLatenessS: map[string]float64{"soak_ingest": 900},
	}

	if got := violationsFor(t, onTime, "schedule_late"); got != 0 {
		t.Errorf("a punctual scheduler produced %d violations", got)
	}
	if got := violationsFor(t, late, "schedule_late"); got == 0 {
		t.Error("a scheduler 15 minutes behind produced no violation, and cadence cannot see it because every run it owed exists")
	}
	if got := violationsFor(t, onTime, "cadence_starved"); got != 0 {
		t.Errorf("the shared fixture is not cadence-clean (%d), so this test would not isolate punctuality", got)
	}
}

// TestPunctualityBudgetScalesWithThePeriod keeps one constant from being either
// noise on a short schedule or blind on a long one. It picks the actual shortest
// and longest schedules in the battery and asserts that the SAME lateness is
// judged differently, which is the whole reason the budget is a ratio.
func TestPunctualityBudgetScalesWithThePeriod(t *testing.T) {
	shortest, longest := "", ""
	for dag, p := range cadenceExpectations {
		if shortest == "" || p < cadenceExpectations[shortest] {
			shortest = dag
		}
		if longest == "" || p > cadenceExpectations[longest] {
			longest = dag
		}
	}
	if cadenceExpectations[shortest] == cadenceExpectations[longest] {
		t.Skip("every schedule has the same period; there is no ratio to test")
	}
	// Between the two budgets: over half the short DAG's interval, a fraction of
	// the long one's.
	late := float64(cadenceExpectations[shortest])*60*latenessBudgetOfPeriod + 1

	if got := violationsFor(t, sample{WindowMin: 120, WorstLatenessS: map[string]float64{shortest: late}}, "schedule_late"); got == 0 {
		t.Errorf("%.0fs late on the %d-minute schedule was accepted; the budget is not scaling down", late, cadenceExpectations[shortest])
	}
	if got := violationsFor(t, sample{WindowMin: 120, WorstLatenessS: map[string]float64{longest: late}}, "schedule_late"); got != 0 {
		t.Errorf("%.0fs late on the %d-minute schedule was reported; a fixed threshold would be noise on the long schedules", late, cadenceExpectations[longest])
	}
}

// TestPunctualityIsSilentDuringAFault keeps the check honest about injected
// outages: being late while the database is down is the correct behavior, and
// reporting it would train an operator to ignore the signal.
func TestPunctualityIsSilentDuringAFault(t *testing.T) {
	s := sample{
		WindowMin:      60,
		InFaultWindow:  true,
		WorstLatenessS: map[string]float64{"soak_ingest": 9000},
	}
	if got := violationsFor(t, s, "schedule_late"); got != 0 {
		t.Errorf("reported %d lateness violations inside a fault window", got)
	}
}

// TestAnUnreachableControlPlaneSuppressesEveryOtherCheck covers the failure that
// wasted an aborted weekend run.
//
// With the API gone, every DAG looks starved, every schedule looks late and the
// scheduler looks unhealthy. All of those are true statements about a
// meaningless situation, and the monitor accumulated 2332 of them over ninety
// minutes against a control plane that had already exited. A soak that cannot
// tell "unhealthy" from "absent" is a soak whose FAIL nobody can trust.
func TestAnUnreachableControlPlaneSuppressesEveryOtherCheck(t *testing.T) {
	c := newChecker(defaultOptions())
	gone := healthySample()
	gone.APIReachable = false
	gone.SchedulerHealth = ""
	gone.WindowMin = 120
	gone.RunsCreated = map[string]int{}
	gone.WorstLatenessS = map[string]float64{}

	// Well inside the grace period: nothing at all, because a brief gap is what
	// an injected restart looks like.
	for i := 0; i < 3; i++ {
		if vs := c.Check(gone); len(vs) != 0 {
			t.Fatalf("sample %d produced %v; a short gap is the restart fault working, not a finding", i, checkNames(vs))
		}
	}
	if c.Gone() {
		t.Fatal("gave up inside the grace period")
	}

	// Past it: exactly one finding, naming the absence.
	var last []violation
	for i := 0; i < unreachableSamplesBeforeGivingUp; i++ {
		last = c.Check(gone)
	}
	names := checkNames(last)
	if !names["control_plane_gone"] {
		t.Fatalf("past the grace period the monitor reported %v, and never that its subject had left", names)
	}
	for _, noisy := range []string{"cadence_starved", "schedule_late", "scheduler_unhealthy"} {
		if names[noisy] {
			t.Errorf("%s fired against a control plane that is not there; this is the noise that buried the real signal", noisy)
		}
	}
	if !c.Gone() {
		t.Error("Gone() is false past the grace period, so the run would keep going and keep describing an absence")
	}
}

// TestReachabilityResetsOnRecovery keeps an injected restart from accumulating
// toward the give-up threshold across the whole run.
func TestReachabilityResetsOnRecovery(t *testing.T) {
	c := newChecker(defaultOptions())
	gone := healthySample()
	gone.APIReachable = false

	for cycle := 0; cycle < 5; cycle++ {
		for i := 0; i < unreachableSamplesBeforeGivingUp-1; i++ {
			c.Check(gone)
		}
		c.Check(healthySample()) // recovered
		if c.Gone() {
			t.Fatalf("cycle %d: a recovered control plane still counted as gone; brief outages would add up across a weekend", cycle)
		}
	}
}

// TestPunctualityIgnoresABackdatedLogicalDate covers a false positive this check
// produced against the battery's own DAG.
//
// soak_token declared start_date=2026-01-01, so its first run's logical_date sat
// months in the past and queued_at minus logical_date came out at 20,843,777
// seconds. The check reported it, correctly as arithmetic and uselessly as
// signal, once every ten seconds for hours: 2127 violations in one run, all of
// them the same non-event.
//
// A lateness measured in days is a different phenomenon from a late scheduler.
// It means the logical_date is in the past by construction: a backdated
// start_date, a catchup backfill, or a DAG registered long after its first
// interval.
func TestPunctualityIgnoresABackdatedLogicalDate(t *testing.T) {
	backdated := sample{
		WindowMin:      120,
		WorstLatenessS: map[string]float64{"soak_ingest": 20843777},
	}
	if got := violationsFor(t, backdated, "schedule_late"); got != 0 {
		t.Errorf("reported %d violations for a run 241 days behind its logical date; that is a backfill, not a late scheduler", got)
	}

	// The exclusion must not swallow real lateness. Just under the ceiling still
	// reports, so the escape hatch cannot become a blanket.
	genuine := sample{
		WindowMin:      120,
		WorstLatenessS: map[string]float64{"soak_ingest": backfillLatenessCeilingS - 60},
	}
	if got := violationsFor(t, genuine, "schedule_late"); got == 0 {
		t.Error("an hours-late scheduler just under the ceiling was ignored; the backfill exclusion swallowed a real finding")
	}
}

// TestRunWedgeClearsTheBatterysOwnLongestDag covers a threshold that measured
// the fixture instead of the system.
//
// soak_token's body runs for forty minutes by design. The wedge threshold was
// thirty, so the battery reported its own longest DAG as wedged on every sample
// that DAG was alive: 1392 violations in one weekend run, every one of them a
// task working exactly as written.
//
// A threshold below the longest legitimate run is not a loose threshold, it is a
// different measurement.
func TestRunWedgeClearsTheBatterysOwnLongestDag(t *testing.T) {
	const soakTokenBodyS = 40 * 60

	o := defaultOptions()
	if o.runWedge.Seconds() <= soakTokenBodyS {
		t.Fatalf("run-wedge default is %.0fs, at or below soak_token's %ds body; the DAG trips it while working", o.runWedge.Seconds(), soakTokenBodyS)
	}

	working := healthySample()
	working.OldestRunningRun = soakTokenBodyS + 60 // finishing late, still not wedged
	if vs := newChecker(o).Check(working); checkNames(vs)["run_wedge"] {
		t.Error("the battery's own longest DAG reported as wedged while running normally")
	}

	// The threshold must still catch something genuinely stuck, or raising it
	// traded a false positive for a blind spot.
	stuck := healthySample()
	stuck.OldestRunningRun = o.runWedge.Seconds() + 1
	if vs := newChecker(o).Check(stuck); !checkNames(vs)["run_wedge"] {
		t.Error("a run past the threshold was not reported; the fix removed the check rather than calibrating it")
	}
}

// TestLeaderChurnCountsEventsNotSamples pins that a cumulative counter is
// reported once per step-down and not once per sample thereafter.
//
// leoflow_scheduler_step_downs_total only goes up. Compared against a limit of
// zero, the obvious check fires on every remaining sample of the run: a 15.7h
// soak turned 15 real step-downs (#1199) into 5198 violations, which made one
// finding 44% of the total and buried the two beside it. The condition is an
// event; a count that is really the sample rate describes the monitor, not the
// scheduler.
//
// Both directions are pinned. Under-reporting is the worse failure, so the
// second half asserts that a LATER step-down is still reported after a quiet
// stretch: a fix that only reported the first one would satisfy "not 5198".
func TestLeaderChurnCountsEventsNotSamples(t *testing.T) {
	c := newChecker(defaultOptions())

	churn := func(vs []violation) int {
		n := 0
		for _, v := range vs {
			if v.Check == "leader_churn" {
				n++
			}
		}
		return n
	}

	s := healthySample()
	s.StepDowns = 0
	if got := churn(c.Check(s)); got != 0 {
		t.Fatalf("a scheduler that never stepped down reported %d leader_churn violations", got)
	}

	// First step-down: reported.
	s.StepDowns = 1
	if got := churn(c.Check(s)); got != 1 {
		t.Fatalf("the first step-down reported %d times, want 1", got)
	}
	// The counter is cumulative, so it stays at 1 while nothing more happens.
	// This is the sample that used to re-report forever.
	for i := range 20 {
		if got := churn(c.Check(s)); got != 0 {
			t.Fatalf("sample %d re-reported a step-down that had already been counted; the violation total would become the sample rate", i+2)
		}
	}
	// A NEW step-down must still be caught. Without this the "fix" is silence.
	s.StepDowns = 2
	if got := churn(c.Check(s)); got != 1 {
		t.Fatalf("a second step-down reported %d times, want 1; a check that only ever reports the first one has stopped watching", got)
	}
	// And a jump of more than one between samples is still one report, which
	// says how many.
	s.StepDowns = 5
	vs := c.Check(s)
	if got := churn(vs); got != 1 {
		t.Fatalf("a jump of three step-downs reported %d times, want 1", got)
	}
	for _, v := range vs {
		if v.Check == "leader_churn" && !strings.Contains(v.Detail, "3 new") {
			t.Fatalf("the violation does not say how many step-downs it covers: %q", v.Detail)
		}
	}
}

// TestAnUnreachableDatastoreAbortsInsteadOfFailing is #1257: Docker Desktop
// stopped 29 hours into a 60 hour run and took the soak's Postgres with it.
// The control plane stayed up and retried, and the monitor kept sampling for
// eighteen more hours, recording 31,485 violations against a database that
// was not there. A datastore the monitor cannot reach is not a failing
// product, it is a run that stopped running.
func TestAnUnreachableDatastoreAbortsInsteadOfFailing(t *testing.T) {
	c := newChecker(defaultOptions())
	gone := healthySample()
	gone.DatastoreUnreachable = true
	gone.ElapsedS = 29.2 * 3600
	gone.WindowMin = 120
	gone.RunsCreated = map[string]int{}
	gone.WorstLatenessS = map[string]float64{}
	gone.SchedulerHealth = "unhealthy"

	for i := 0; i < unreachableSamplesBeforeGivingUp; i++ {
		if vs := c.Check(gone); len(vs) != 0 {
			t.Fatalf("sample %d produced %v against a database that is not there", i, checkNames(vs))
		}
		gone.ElapsedS += 10
	}
	if !c.DatastoreGone() {
		t.Fatal("DatastoreGone() is false past the grace period, so the run would keep sampling into the void")
	}
	if got := c.DatastoreLostAtS(); got != 29.2*3600 {
		t.Errorf("DatastoreLostAtS() = %v, want the first unreachable sample, %v", got, 29.2*3600)
	}

	// A blip shorter than the grace period resets, like the API one.
	c2 := newChecker(defaultOptions())
	for i := 0; i < unreachableSamplesBeforeGivingUp-1; i++ {
		c2.Check(gone)
	}
	c2.Check(healthySample())
	if c2.DatastoreGone() {
		t.Error("a recovered datastore still counted as gone")
	}
}
