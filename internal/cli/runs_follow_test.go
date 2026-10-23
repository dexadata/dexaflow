package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTI is one task instance as the fake control plane reports it.
type fakeTI struct {
	task  string
	state string
	try   int
	dur   float64
}

// runStep is what the fake control plane reports for the run at one poll.
type runStep struct {
	state string
	tis   []fakeTI
}

// fakeRunServer fakes the run, task instance, logs and DAG routes of the
// control plane for run "run-1" of DAG "etl". Each read of the run advances one
// step (the last step repeats); the task instance list answers the current step.
type fakeRunServer struct {
	mu        sync.Mutex
	paused    bool
	steps     []runStep
	idx       int
	read      bool
	logs      map[string]string // "task/try" -> body
	logReads  []string
	failReads int // run reads answered 503 before the normal answer
	writes    int // PATCH or DELETE on the run: a cancellation the follower must never send
	triggers  int
	hangLogs  bool   // logs requests block until the client gives up
	listQuery string // the query of the last run list request
}

func (s *fakeRunServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.hangLogs && strings.Contains(r.URL.Path, "/logs/") {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	const run = "/api/v2/dags/etl/dagRuns/run-1"
	p := r.URL.Path
	switch {
	case r.Method == http.MethodPost && p == "/api/v2/dags/etl/dagRuns":
		s.triggers++
		_, _ = io.WriteString(w, `{"dag_run_id":"run-1","state":"queued"}`)
	case r.Method != http.MethodGet && strings.HasPrefix(p, run):
		s.writes++
		w.WriteHeader(http.StatusOK)
	case p == "/api/v2/dags/etl":
		_, _ = fmt.Fprintf(w, `{"dag_id":"etl","is_paused":%t}`, s.paused)
	case p == "/api/v2/dags/etl/dagRuns":
		s.listQuery = r.URL.RawQuery
		_, _ = fmt.Fprintf(w, `{"dag_runs":[{"dag_run_id":"run-1","state":%q}],"total_entries":1}`, s.current().state)
	case p == run:
		if s.failReads > 0 {
			s.failReads--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if s.read && s.idx < len(s.steps)-1 {
			s.idx++
		}
		s.read = true
		_, _ = fmt.Fprintf(w, `{"dag_run_id":"run-1","state":%q}`, s.current().state)
	case p == run+"/taskInstances":
		s.writeTIs(w, r.URL.Query())
	case strings.HasPrefix(p, run+"/taskInstances/") && strings.Contains(p, "/logs/"):
		key := strings.Replace(strings.TrimPrefix(p, run+"/taskInstances/"), "/logs/", "/", 1)
		s.logReads = append(s.logReads, key)
		if body, ok := s.logs[key]; ok {
			_, _ = io.WriteString(w, body)
			return
		}
		_, _ = io.WriteString(w, "No logs available for this attempt.\n")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *fakeRunServer) current() runStep { return s.steps[s.idx] }

// writeTIs answers one page of the current step's task instances, honoring
// limit and offset as the control plane does.
func (s *fakeRunServer) writeTIs(w io.Writer, q url.Values) {
	type ti struct {
		TaskID    string   `json:"task_id"`
		State     *string  `json:"state"`
		TryNumber int      `json:"try_number"`
		Duration  *float64 `json:"duration"`
	}
	items := make([]ti, 0, len(s.current().tis))
	for _, t := range s.current().tis {
		item := ti{TaskID: t.task, TryNumber: t.try}
		if t.state != "" {
			st := t.state
			item.State = &st
		}
		if t.dur > 0 {
			d := t.dur
			item.Duration = &d
		}
		items = append(items, item)
	}
	total := len(items)
	if off, err := strconv.Atoi(q.Get("offset")); err == nil && off < len(items) {
		items = items[off:]
	} else if err == nil {
		items = items[:0]
	}
	if lim, err := strconv.Atoi(q.Get("limit")); err == nil && lim < len(items) {
		items = items[:lim]
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"task_instances": items, "total_entries": total})
}

func (s *fakeRunServer) logReadCount(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, k := range s.logReads {
		if k == key {
			n++
		}
	}
	return n
}

// fastFollow returns test dependencies whose follower polls every few
// milliseconds.
func fastFollow(t *testing.T) *cliDeps {
	t.Helper()
	d := testDeps()
	d.followPollInterval = 2 * time.Millisecond
	return d
}

func successSteps() []runStep {
	return []runStep{
		{state: "queued", tis: []fakeTI{{task: "extract"}, {task: "load"}}},
		{state: "running", tis: []fakeTI{{task: "extract", state: "running", try: 1}, {task: "load"}}},
		{state: "running", tis: []fakeTI{{task: "extract", state: "success", try: 1, dur: 4.2}, {task: "load", state: "running", try: 1}}},
		{state: "success", tis: []fakeTI{{task: "extract", state: "success", try: 1, dur: 4.2}, {task: "load", state: "success", try: 1, dur: 2.8}}},
	}
}

func newFakeRun(t *testing.T, fake *fakeRunServer) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return srv
}

// TestRunsTriggerFollowSucceeds drives `runs trigger --follow` end to end: it
// prints each task state change, the logs of each task once it finishes, the
// UI link of the run, and exits 0 when the run succeeds.
func TestRunsTriggerFollowSucceeds(t *testing.T) {
	d := fastFollow(t)
	fake := &fakeRunServer{steps: successSteps(), logs: map[string]string{
		"extract/1": "extracted 10 rows\n", "load/1": "loaded 10 rows\n",
	}}
	srv := newFakeRun(t, fake)

	out, _, err := runDeps(t, d, "runs", "trigger", "etl", "--follow", "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("runs trigger --follow: %v\n%s", err, out)
	}
	for _, want := range []string{
		srv.URL + "/dags/etl/runs/run-1",
		"extract  running", "extract  success  4.2s", "load     success  2.8s",
		"| extracted 10 rows", "| loaded 10 rows",
		"Run run-1 succeeded",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "| extracted 10 rows") > strings.Index(out, "load     success") {
		t.Errorf("extract's logs should print as soon as it finished, before load finished:\n%s", out)
	}
	if n := fake.logReadCount("extract/1"); n != 1 {
		t.Errorf("extract's logs were read %d time(s), want once", n)
	}
}

// TestRunsTriggerFollowFailedRun: a failed run exits with the run-failed code
// and names the failing task and the tasks that did not run because of it.
func TestRunsTriggerFollowFailedRun(t *testing.T) {
	d := fastFollow(t)
	fake := &fakeRunServer{steps: []runStep{
		{state: "running", tis: []fakeTI{{task: "extract", state: "running", try: 1}, {task: "report"}}},
		{state: "failed", tis: []fakeTI{{task: "extract", state: "failed", try: 1}, {task: "report", state: "upstream_failed"}}},
	}, logs: map[string]string{"extract/1": "Traceback: boom\n"}}
	srv := newFakeRun(t, fake)

	out, _, err := runDeps(t, d, "runs", "trigger", "etl", "--follow", "--server", srv.URL, "--token", "x")
	if err == nil {
		t.Fatalf("a failed run must fail the command:\n%s", out)
	}
	if got := exitCodeOf(err); got != exitRunFailed {
		t.Errorf("exit code = %d, want %d (run failed)", got, exitRunFailed)
	}
	for _, want := range []string{"failed task: extract", "upstream_failed: report", srv.URL + "/dags/etl/runs/run-1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	if !strings.Contains(out, "| Traceback: boom") {
		t.Errorf("the failing task's logs are missing:\n%s", out)
	}
}

// TestRunsTriggerFollowTimeout: --timeout stops following with its own exit
// code, says the run keeps going, and never cancels the run.
func TestRunsTriggerFollowTimeout(t *testing.T) {
	d := fastFollow(t)
	fake := &fakeRunServer{steps: []runStep{{state: "running", tis: []fakeTI{{task: "extract", state: "running", try: 1}}}}}
	srv := newFakeRun(t, fake)

	_, _, err := runDeps(t, d, "runs", "trigger", "etl", "--follow", "--timeout", "50ms", "--server", srv.URL, "--token", "x")
	if err == nil {
		t.Fatal("following past --timeout must fail")
	}
	if got := exitCodeOf(err); got != exitFollowTimeout {
		t.Errorf("exit code = %d, want %d (timeout)", got, exitFollowTimeout)
	}
	if !strings.Contains(err.Error(), "keeps going") || !strings.Contains(err.Error(), "dexaflow runs status etl --run run-1") {
		t.Errorf("error %q should say the run keeps going and how to check it", err)
	}
	if fake.writes != 0 {
		t.Errorf("the follower changed the run %d time(s); it must never cancel it", fake.writes)
	}
}

// TestFollowRunInterruptLeavesTheRun: Ctrl-C (a canceled context) stops
// following with the interrupt code and leaves the run alone.
func TestFollowRunInterruptLeavesTheRun(t *testing.T) {
	t.Parallel()
	d := fastFollow(t)
	fake := &fakeRunServer{steps: []runStep{{state: "running", tis: []fakeTI{{task: "extract", state: "running", try: 1}}}}}
	srv := newFakeRun(t, fake)
	ctx, cancel := context.WithTimeout(testCtx(d), 30*time.Millisecond)
	defer cancel()

	err := followRun(ctx, io.Discard, followTarget{base: srv.URL, token: "x", dagID: "etl", runID: "run-1"}, followOptions{})
	if got := exitCodeOf(err); got != exitInterrupted {
		t.Errorf("exit code = %d (err %v), want %d (interrupted)", got, err, exitInterrupted)
	}
	if err == nil || !strings.Contains(err.Error(), "keeps going") {
		t.Errorf("error %v should say the run keeps going", err)
	}
	if fake.writes != 0 {
		t.Errorf("the follower changed the run %d time(s); it must never cancel it", fake.writes)
	}
}

// TestFollowRunSaysTheDAGIsPaused: a run of a paused DAG, manual ones
// included, waits in queued until the DAG is unpaused (Airflow semantics), and
// the follower says so with the command that unpauses it.
func TestFollowRunSaysTheDAGIsPaused(t *testing.T) {
	t.Parallel()
	d := fastFollow(t)
	fake := &fakeRunServer{paused: true, steps: successSteps()}
	srv := newFakeRun(t, fake)
	var out strings.Builder

	err := followRun(testCtx(d), &out, followTarget{base: srv.URL, token: "x", dagID: "etl", runID: "run-1"}, followOptions{})
	if err != nil {
		t.Fatalf("followRun: %v", err)
	}
	for _, want := range []string{"etl is paused", "this run waits in queued until you unpause it", "dexaflow admin dags unpause etl"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not say %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "starts anyway") {
		t.Errorf("a run of a paused DAG does not start anyway:\n%s", out.String())
	}
}

// TestRunsTriggerSaysAPausedRunWaits: `runs trigger` on a paused DAG says
// once that the run waits until the unpause, with or without --follow.
func TestRunsTriggerSaysAPausedRunWaits(t *testing.T) {
	d := fastFollow(t)
	for _, follow := range []bool{false, true} {
		srv := newFakeRun(t, &fakeRunServer{paused: true, steps: successSteps()})
		args := []string{"runs", "trigger", "etl", "--server", srv.URL, "--token", "x"}
		if follow {
			args = append(args, "--follow")
		}

		out, _, err := runDeps(t, d, args...)
		if err != nil {
			t.Fatalf("runs trigger (follow %t): %v\n%s", follow, err, out)
		}
		if n := strings.Count(out, "etl is paused: this run waits in queued until you unpause it: dexaflow admin dags unpause etl"); n != 1 {
			t.Errorf("follow %t: the paused note printed %d time(s), want once:\n%s", follow, n, out)
		}
	}
}

// TestFollowRunNamesThePauseOfAStalledRun: when the run is still queued
// because its DAG is paused, the stall note names the pause as the cause and
// the unpause command, not the agent or max_active_runs.
func TestFollowRunNamesThePauseOfAStalledRun(t *testing.T) {
	t.Parallel()
	d := fastFollow(t)
	fake := &fakeRunServer{paused: true, steps: []runStep{{state: "queued", tis: []fakeTI{{task: "extract"}}}}}
	srv := newFakeRun(t, fake)
	var out strings.Builder
	ctx, cancel := context.WithTimeout(testCtx(d), 80*time.Millisecond)
	defer cancel()

	_ = followRun(ctx, &out, followTarget{base: srv.URL, token: "x", dagID: "etl", runID: "run-1"},
		followOptions{stallAfter: 10 * time.Millisecond})
	if !strings.Contains(out.String(), "run run-1 is still queued after") ||
		!strings.Contains(out.String(), "because etl is paused") {
		t.Errorf("the stall note should name the pause as the cause:\n%s", out.String())
	}
	if n := strings.Count(out.String(), "dexaflow admin dags unpause etl"); n != 2 {
		t.Errorf("the unpause command printed %d time(s), want twice (header and stall note):\n%s", n, out.String())
	}
	if strings.Contains(out.String(), "agent is connected") {
		t.Errorf("a paused DAG explains the wait; the agent hint misleads:\n%s", out.String())
	}
}

// TestFollowRunHintsAtAStalledQueue: a run that stays queued gets one hint
// instead of a silent wait.
func TestFollowRunHintsAtAStalledQueue(t *testing.T) {
	t.Parallel()
	d := fastFollow(t)
	fake := &fakeRunServer{steps: []runStep{{state: "queued", tis: []fakeTI{{task: "extract"}}}}}
	srv := newFakeRun(t, fake)
	var out strings.Builder
	ctx, cancel := context.WithTimeout(testCtx(d), 80*time.Millisecond)
	defer cancel()

	_ = followRun(ctx, &out, followTarget{base: srv.URL, token: "x", dagID: "etl", runID: "run-1"},
		followOptions{stallAfter: 10 * time.Millisecond})
	if n := strings.Count(out.String(), "no task has started"); n != 1 {
		t.Errorf("stall hint printed %d time(s), want once:\n%s", n, out.String())
	}
}

// TestFollowRunRidesOutTransientErrors: a 503 while polling is retried, not
// fatal.
func TestFollowRunRidesOutTransientErrors(t *testing.T) {
	t.Parallel()
	d := fastFollow(t)
	fake := &fakeRunServer{failReads: 2, steps: successSteps()}
	srv := newFakeRun(t, fake)

	if err := followRun(testCtx(d), io.Discard, followTarget{base: srv.URL, token: "x", dagID: "etl", runID: "run-1"}, followOptions{}); err != nil {
		t.Fatalf("followRun after two 503s: %v", err)
	}
}

// TestFollowRunGivesUpOnAPersistentError: an error that keeps coming back
// (here a 404: the run was deleted) ends the follow instead of polling forever.
func TestFollowRunGivesUpOnAPersistentError(t *testing.T) {
	t.Parallel()
	d := fastFollow(t)
	srv := newFakeRun(t, &fakeRunServer{steps: successSteps()})

	err := followRun(testCtx(d), io.Discard, followTarget{base: srv.URL, token: "x", dagID: "etl", runID: "nope"}, followOptions{})
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want the 404", err)
	}
	if exitCodeOf(err) != 1 {
		t.Errorf("exit code = %d, want 1 for an API error", exitCodeOf(err))
	}
}

// TestFollowRunPrintsEveryAttemptsLogs: a task that fails and is retried shows
// the logs of the failed attempt and of the retry.
func TestFollowRunPrintsEveryAttemptsLogs(t *testing.T) {
	t.Parallel()
	d := fastFollow(t)
	fake := &fakeRunServer{steps: []runStep{
		{state: "running", tis: []fakeTI{{task: "extract", state: "running", try: 1}}},
		{state: "running", tis: []fakeTI{{task: "extract", state: "up_for_retry", try: 1}}},
		{state: "running", tis: []fakeTI{{task: "extract", state: "running", try: 2}}},
		{state: "success", tis: []fakeTI{{task: "extract", state: "success", try: 2, dur: 1}}},
	}, logs: map[string]string{"extract/1": "first try failed\n", "extract/2": "second try worked\n"}}
	srv := newFakeRun(t, fake)
	var out strings.Builder

	if err := followRun(testCtx(d), &out, followTarget{base: srv.URL, token: "x", dagID: "etl", runID: "run-1"}, followOptions{}); err != nil {
		t.Fatalf("followRun: %v", err)
	}
	for _, want := range []string{"extract  up_for_retry", "| first try failed", "extract  running  (try 2)", "| second try worked"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, out.String())
		}
	}
}

// TestExitCodeOf pins the mapping Execute uses: 0 for no error, the carried
// code when one is wrapped anywhere in the chain, 1 otherwise.
func TestExitCodeOf(t *testing.T) {
	t.Parallel()
	coded := &exitCodeError{code: exitFollowTimeout, err: errors.New("timeout")}
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{errors.New("plain"), 1},
		{coded, exitFollowTimeout},
		{fmt.Errorf("deploying etl: %w", coded), exitFollowTimeout},
	}
	for _, c := range cases {
		if got := exitCodeOf(c.err); got != c.want {
			t.Errorf("exitCodeOf(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// TestRunsLogsDefaultsToLatestRunAndAllTasks: `runs logs <dag>` reads the
// latest run and prints the logs of every task, each under a header.
func TestRunsLogsDefaultsToLatestRunAndAllTasks(t *testing.T) {
	fake := &fakeRunServer{steps: successSteps()[3:], logs: map[string]string{
		"extract/1": "extracted 10 rows\n", "load/1": "loaded 10 rows\n",
	}}
	srv := newFakeRun(t, fake)

	for _, args := range [][]string{{"etl"}, {"etl", "run-1"}} {
		out, _, err := run(t, append(append([]string{"runs", "logs"}, args...), "--server", srv.URL, "--token", "x")...)
		if err != nil {
			t.Fatalf("runs logs %v: %v", args, err)
		}
		for _, want := range []string{"==> extract (try 1, success) <==", "extracted 10 rows", "==> load (try 1, success) <==", "loaded 10 rows"} {
			if !strings.Contains(out, want) {
				t.Errorf("runs logs %v: output does not contain %q:\n%s", args, want, out)
			}
		}
	}
}

// TestRunsLogsSkipsTasksThatNeverRan: a task with no attempt gets a header
// line saying so, and no logs request.
func TestRunsLogsSkipsTasksThatNeverRan(t *testing.T) {
	fake := &fakeRunServer{steps: []runStep{{state: "failed", tis: []fakeTI{
		{task: "extract", state: "failed", try: 1}, {task: "report", state: "upstream_failed"},
	}}}}
	srv := newFakeRun(t, fake)

	out, _, err := run(t, "runs", "logs", "etl", "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("runs logs: %v", err)
	}
	if !strings.Contains(out, "==> report (upstream_failed): no attempt <==") {
		t.Errorf("output does not mark report as never run:\n%s", out)
	}
	if n := fake.logReadCount("report/0") + fake.logReadCount("report/1"); n != 0 {
		t.Errorf("read logs of a task that never ran %d time(s)", n)
	}
}

// TestRunsLogsFollowWithoutTaskFollowsTheRun: `runs logs <dag> --follow`
// follows the whole run, like `runs trigger --follow` does.
func TestRunsLogsFollowWithoutTaskFollowsTheRun(t *testing.T) {
	d := fastFollow(t)
	fake := &fakeRunServer{steps: successSteps(), logs: map[string]string{"load/1": "loaded 10 rows\n"}}
	srv := newFakeRun(t, fake)

	out, _, err := runDeps(t, d, "runs", "logs", "etl", "run-1", "--follow", "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("runs logs --follow: %v", err)
	}
	if !strings.Contains(out, "| loaded 10 rows") || !strings.Contains(out, "Run run-1 succeeded") {
		t.Errorf("output does not follow the run:\n%s", out)
	}
}

// TestRunsLogsTryNeedsATask: --try names one attempt of one task.
func TestRunsLogsTryNeedsATask(t *testing.T) {
	_, _, err := run(t, "runs", "logs", "etl", "--try", "2", "--server", "http://127.0.0.1:1", "--token", "x")
	if err == nil || !strings.Contains(err.Error(), "--try needs a task_id") {
		t.Fatalf("err = %v, want --try to require a task_id", err)
	}
}

// TestExecuteRootExitsWithTheRunResult locks the wiring from a followed run to
// the process exit code: the root command's error keeps its code through
// executeRoot, which Execute returns to main.
func TestExecuteRootExitsWithTheRunResult(t *testing.T) {
	d := fastFollow(t)
	fake := &fakeRunServer{steps: []runStep{{state: "failed", tis: []fakeTI{{task: "extract", state: "failed", try: 1}}}}}
	srv := newFakeRun(t, fake)
	root := NewRootCommand()
	root.SetOut(io.Discard)
	root.SetArgs([]string{"runs", "trigger", "etl", "--follow", "--server", srv.URL, "--token", "x"})

	got, err := executeRoot(testCtx(d), root)
	if got != exitRunFailed {
		t.Errorf("exit code = %d, want %d", got, exitRunFailed)
	}
	if err == nil || !strings.Contains(err.Error(), "run run-1 of etl ended failed") {
		t.Errorf("err = %v, want the run failure", err)
	}
}

// TestRunsLogsReadsEveryPageOfTasks: a run with more tasks than one page of
// the task instance list still prints every task.
func TestRunsLogsReadsEveryPageOfTasks(t *testing.T) {
	tis := make([]fakeTI, 0, 150)
	for i := range 150 {
		tis = append(tis, fakeTI{task: fmt.Sprintf("t%03d", i), state: "success", try: 1})
	}
	srv := newFakeRun(t, &fakeRunServer{steps: []runStep{{state: "success", tis: tis}}})

	out, _, err := run(t, "runs", "logs", "etl", "run-1", "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("runs logs: %v", err)
	}
	if n := strings.Count(out, "==> t"); n != 150 {
		t.Errorf("printed %d task headers, want 150", n)
	}
}

// failingWriter fails every write after the first n.
type failingWriter struct{ n int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if w.n <= 0 {
		return 0, errors.New("broken pipe")
	}
	w.n--
	return len(p), nil
}

// TestFollowRunStopsOnAWriteError: when the output goes away (a closed pipe),
// the follow ends with that error instead of polling on with nobody reading.
func TestFollowRunStopsOnAWriteError(t *testing.T) {
	t.Parallel()
	d := fastFollow(t)
	fake := &fakeRunServer{steps: []runStep{{state: "running", tis: []fakeTI{{task: "extract", state: "running", try: 1}}}}}
	srv := newFakeRun(t, fake)
	ctx, cancel := context.WithTimeout(testCtx(d), 2*time.Second)
	defer cancel()

	err := followRun(ctx, &failingWriter{n: 1}, followTarget{base: srv.URL, token: "x", dagID: "etl", runID: "run-1"}, followOptions{})
	if err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatalf("err = %v, want the write error", err)
	}
}

// hostileLog is a task log line carrying an OSC 52 clipboard write, a bell, a
// DEL and a carriage return: none of them may reach the terminal.
const hostileLog = "ok\x1b]52;c;ZXZpbA==\x07do\x7fne\r\n"

func assertNoControl(t *testing.T, out string) {
	t.Helper()
	for _, c := range []string{"\x1b", "\x07", "\x7f", "\r"} {
		if strings.Contains(out, c) {
			t.Errorf("output carries control character %q:\n%q", c, out)
		}
	}
	if !strings.Contains(out, "ok]52;c;ZXZpbA==done") {
		t.Errorf("output lost the printable text of the log line:\n%q", out)
	}
}

// TestTaskLogsAreSanitized: task logs are written by user code, so every path
// that prints them strips terminal control characters (the follow, every task
// of a run, and one task's attempt).
func TestTaskLogsAreSanitized(t *testing.T) {
	d := fastFollow(t)
	steps := []runStep{{state: "success", tis: []fakeTI{{task: "extract", state: "success", try: 1}}}}
	srv := newFakeRun(t, &fakeRunServer{steps: steps, logs: map[string]string{"extract/1": hostileLog}})
	for _, args := range [][]string{
		{"runs", "logs", "etl", "run-1", "--follow"},
		{"runs", "logs", "etl", "run-1"},
		{"runs", "logs", "etl", "run-1", "extract", "--try", "1"},
	} {
		out, _, err := runDeps(t, d, append(args, "--server", srv.URL, "--token", "x")...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		assertNoControl(t, out)
	}
}

// TestFollowRunPrintsALineLongerThanAnyBuffer: a log line over 1 MiB is
// printed whole, and the lines after it too.
func TestFollowRunPrintsALineLongerThanAnyBuffer(t *testing.T) {
	t.Parallel()
	d := fastFollow(t)
	long := strings.Repeat("a", 2<<20)
	fake := &fakeRunServer{steps: []runStep{{state: "success", tis: []fakeTI{{task: "extract", state: "success", try: 1}}}},
		logs: map[string]string{"extract/1": long + "\nafter\n"}}
	srv := newFakeRun(t, fake)
	var out strings.Builder

	if err := followRun(testCtx(d), &out, followTarget{base: srv.URL, token: "x", dagID: "etl", runID: "run-1"}, followOptions{}); err != nil {
		t.Fatalf("followRun: %v", err)
	}
	if !strings.Contains(out.String(), "    | "+long+"\n") || !strings.Contains(out.String(), "    | after\n") {
		t.Errorf("the long line or the line after it is missing (output %d bytes)", out.Len())
	}
}

// TestFollowRunPrintsTriesItDidNotSee: when the polls miss the up_for_retry
// state, the logs of the earlier attempt still print.
func TestFollowRunPrintsTriesItDidNotSee(t *testing.T) {
	t.Parallel()
	d := fastFollow(t)
	fake := &fakeRunServer{steps: []runStep{
		{state: "running", tis: []fakeTI{{task: "extract", state: "running", try: 1}}},
		{state: "running", tis: []fakeTI{{task: "extract", state: "running", try: 2}}},
		{state: "success", tis: []fakeTI{{task: "extract", state: "success", try: 2, dur: 1}}},
	}, logs: map[string]string{"extract/1": "first try failed\n", "extract/2": "second try worked\n"}}
	srv := newFakeRun(t, fake)
	var out strings.Builder

	if err := followRun(testCtx(d), &out, followTarget{base: srv.URL, token: "x", dagID: "etl", runID: "run-1"}, followOptions{}); err != nil {
		t.Fatalf("followRun: %v", err)
	}
	for _, want := range []string{"| first try failed", "| second try worked"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, out.String())
		}
	}
	if n := fake.logReadCount("extract/1"); n != 1 {
		t.Errorf("try 1 logs read %d time(s), want once", n)
	}
}

// TestFollowTimeoutIsAHardBound: --timeout holds even while a request hangs
// (here the logs of a finished task).
func TestFollowTimeoutIsAHardBound(t *testing.T) {
	d := fastFollow(t)
	fake := &fakeRunServer{hangLogs: true, steps: []runStep{{state: "running", tis: []fakeTI{{task: "extract", state: "success", try: 1}}}}}
	srv := newFakeRun(t, fake)
	start := time.Now()

	_, _, err := runDeps(t, d, "runs", "trigger", "etl", "--follow", "--timeout", "100ms", "--server", srv.URL, "--token", "x")
	if got := exitCodeOf(err); got != exitFollowTimeout {
		t.Errorf("exit code = %d (err %v), want %d", got, err, exitFollowTimeout)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("--timeout 100ms took %s", took)
	}
}

// TestFollowRejectsANegativeTimeout: a negative --timeout is a typo, not "no
// limit".
func TestFollowRejectsANegativeTimeout(t *testing.T) {
	for _, args := range [][]string{
		{"runs", "trigger", "etl", "--follow", "--timeout", "-1m"},
		{"deploy", t.TempDir(), "--trigger", "--follow", "--timeout", "-1m", "--yes"},
	} {
		_, _, err := run(t, append(args, "--server", "http://127.0.0.1:1", "--token", "x")...)
		if err == nil || !strings.Contains(err.Error(), "--timeout must not be negative") {
			t.Errorf("%v: err = %v, want a negative --timeout refused", args, err)
		}
	}
}

// TestRunsLogsAsksForTheLatestRun: the latest run is requested explicitly
// (newest first, one run), not taken from the head of a default listing.
func TestRunsLogsAsksForTheLatestRun(t *testing.T) {
	fake := &fakeRunServer{steps: successSteps()[3:]}
	srv := newFakeRun(t, fake)

	if _, _, err := run(t, "runs", "logs", "etl", "--server", srv.URL, "--token", "x"); err != nil {
		t.Fatalf("runs logs: %v", err)
	}
	q, _ := url.ParseQuery(fake.listQuery)
	if q.Get("order_by") != "-logical_date" || q.Get("limit") != "1" {
		t.Errorf("run list query = %q, want order_by=-logical_date&limit=1", fake.listQuery)
	}
}

// TestRunsLogsEscapesTheDagID: the dag_id is a path segment, escaped.
func TestRunsLogsEscapesTheDagID(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.EscapedPath()
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, _, _ = run(t, "runs", "logs", "etl%zz", "--server", srv.URL, "--token", "x")
	if got != "/api/v2/dags/etl%25zz/dagRuns" {
		t.Errorf("requested %q, want the escaped dag_id", got)
	}
}

// TestRunsLogsHelpNamesTheExitCodes: -f without a task exits with the run's
// result, and the help says so.
func TestRunsLogsHelpNamesTheExitCodes(t *testing.T) {
	out, _, err := run(t, "runs", "logs", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "exits 2 when the run failed") {
		t.Errorf("help does not name the exit codes:\n%s", out)
	}
}

// TestFollowRunEndsALogWithoutNewline: a log whose last line has no newline
// does not run into the next line of output.
func TestFollowRunEndsALogWithoutNewline(t *testing.T) {
	t.Parallel()
	d := fastFollow(t)
	fake := &fakeRunServer{steps: []runStep{{state: "success", tis: []fakeTI{{task: "extract", state: "success", try: 1}}}},
		logs: map[string]string{"extract/1": "no newline at the end"}}
	srv := newFakeRun(t, fake)
	var out strings.Builder

	if err := followRun(testCtx(d), &out, followTarget{base: srv.URL, token: "x", dagID: "etl", runID: "run-1"}, followOptions{}); err != nil {
		t.Fatalf("followRun: %v", err)
	}
	if !strings.Contains(out.String(), "    | no newline at the end\nRun run-1 succeeded") {
		t.Errorf("the last log line runs into the next output:\n%q", out.String())
	}
}
