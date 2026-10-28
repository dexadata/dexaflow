package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

// Exit codes of a followed run (`runs trigger --follow`, `deploy --trigger
// --follow`, `runs logs <dag> --follow`). Any other error exits 1.
const (
	// exitRunFailed is the exit code of a run that ended in a state other than success.
	exitRunFailed = 2
	// exitFollowTimeout is the exit code when --timeout elapsed before the run ended.
	exitFollowTimeout = 3
	// exitInterrupted is the exit code when Ctrl-C stopped the follow (128 + SIGINT).
	exitInterrupted = 130
)

// followPollInterval is the shortest wait between two polls of a followed run.
// While nothing changes the wait grows up to followMaxWaitFactor times this,
// and a change brings it back. Tests poll fast (cliDeps.followPollInterval).
const followPollInterval = time.Second

const (
	// followMaxWaitFactor caps the backoff at this multiple of followPollInterval.
	followMaxWaitFactor = 10
	// followMaxErrors is how many polls in a row may fail transiently (network,
	// 5xx) before the follow gives up.
	followMaxErrors = 5
	// followStallAfter is how long a run may stay queued before the follower
	// says it has not started and where to look.
	followStallAfter = 2 * time.Minute
	// followPageSize is the page size used to list a run's task instances.
	followPageSize = 100
)

// exitCodeError carries the process exit code an error maps to.
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

// exitCodeOf returns the process exit code for err: 0 for nil, the code of an
// exitCodeError anywhere in its chain, and 1 for any other error.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var coded *exitCodeError
	if errors.As(err, &coded) {
		return coded.code
	}
	return 1
}

// transientError marks a failed poll worth retrying: the server could not be
// reached or answered 5xx.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// errFollowTimeout is the cancel cause of a follow whose --timeout elapsed,
// which tells it apart from Ctrl-C.
var errFollowTimeout = errors.New("--timeout elapsed")

// followTarget names the run being followed and the control plane serving it.
type followTarget struct {
	base, token, dagID, runID string
}

func (t followTarget) apiRunURL() string {
	return strings.TrimRight(t.base, "/") + "/api/v2/dags/" + url.PathEscape(t.dagID) + "/dagRuns/" + url.PathEscape(t.runID)
}

// uiRunURL is the run's page in the web UI, which the control plane serves
// next to its API.
func (t followTarget) uiRunURL() string {
	return strings.TrimRight(t.base, "/") + "/dags/" + url.PathEscape(t.dagID) + "/runs/" + url.PathEscape(t.runID)
}

// statusHint is the command that shows the run after the follow stopped.
func (t followTarget) statusHint() string {
	return "dexaflow runs status " + t.dagID + " --run " + t.runID
}

// followOptions tunes a follow. Zero values mean no timeout and the default
// stall hint delay.
type followOptions struct {
	timeout    time.Duration
	stallAfter time.Duration
}

// taskInstance is the part of an Airflow task instance the follower reads.
type taskInstance struct {
	TaskID    string   `json:"task_id"`
	State     *string  `json:"state"`
	TryNumber int      `json:"try_number"`
	Duration  *float64 `json:"duration"`
}

// state is the instance's state, "none" when it has none yet.
func (ti taskInstance) state() string {
	if ti.State == nil || *ti.State == "" {
		return "none"
	}
	return *ti.State
}

// followWithSignals follows the run until it ends, the timeout elapses, or
// Ctrl-C stops the follow. Ctrl-C never cancels the run itself.
func followWithSignals(ctx context.Context, out io.Writer, t followTarget, o followOptions) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	return followRun(ctx, out, t, o)
}

// follower holds what a follow has printed so far.
type follower struct {
	out     io.Writer
	t       followTarget
	order   []string
	seen    map[string]taskInstance
	logged  map[string]bool
	width   int
	hinted  bool
	started time.Time
	werr    error // the first failed write to out; it ends the follow
	timeout time.Duration
}

// printf writes to out unless an earlier write failed, keeping the first
// write error for followRun to return.
func (f *follower) printf(format string, a ...any) {
	if f.werr == nil {
		_, f.werr = fmt.Fprintf(f.out, format, a...)
	}
}

// followRun polls the run and its task instances until the run ends. It prints
// each task state change, the logs of each attempt once it finishes, and
// returns nil for a successful run or an exitCodeError otherwise. Canceling
// ctx stops the follow (exitInterrupted); it never changes the run.
func followRun(ctx context.Context, out io.Writer, t followTarget, o followOptions) error {
	f := &follower{out: out, t: t, seen: map[string]taskInstance{}, logged: map[string]bool{}, started: time.Now(), timeout: o.timeout}
	stallAfter := o.stallAfter
	if stallAfter <= 0 {
		stallAfter = followStallAfter
	}
	if o.timeout > 0 {
		// The deadline bounds every request too, so a hung read cannot
		// outlive --timeout.
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadlineCause(ctx, f.started.Add(o.timeout), errFollowTimeout)
		defer cancel()
	}
	if err := f.header(ctx); err != nil {
		return err
	}
	interval := depsFrom(ctx).followPollInterval
	wait, failures := interval, 0
	for {
		state, changed, err := f.poll(ctx)
		if f.werr != nil {
			return f.werr
		}
		switch {
		case ctx.Err() != nil:
			return f.stopped(ctx)
		case err != nil:
			failures++
			var transient *transientError
			if !errors.As(err, &transient) || failures >= followMaxErrors {
				return fmt.Errorf("following run %s of %s: %w", t.runID, t.dagID, err)
			}
		case state != "queued" && state != "running":
			return f.finish(state)
		default:
			failures = 0
			f.hintStall(ctx, state, stallAfter)
		}
		wait = nextFollowWait(interval, wait, changed)
		if werr := f.sleep(ctx, wait); werr != nil {
			return werr
		}
	}
}

// nextFollowWait resets the wait to interval after a change and grows it by
// half while nothing changes, up to followMaxWaitFactor times interval.
func nextFollowWait(interval, prev time.Duration, changed bool) time.Duration {
	if changed {
		return interval
	}
	next := prev + prev/2
	if limit := interval * followMaxWaitFactor; next > limit {
		return limit
	}
	return next
}

// sleep waits d, or returns the stop error when ctx ends first.
func (f *follower) sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return f.stopped(ctx)
	case <-timer.C:
		return nil
	}
}

// stopped is the error of a follow whose ctx ended: the timeout error when
// --timeout elapsed, the interrupt error otherwise (Ctrl-C).
func (f *follower) stopped(ctx context.Context) error {
	if errors.Is(context.Cause(ctx), errFollowTimeout) {
		return &exitCodeError{code: exitFollowTimeout, err: fmt.Errorf(
			"stopped following run %s after %s (--timeout); the run keeps going. Check it with: %s",
			f.t.runID, f.timeout, f.t.statusHint())}
	}
	return &exitCodeError{code: exitInterrupted, err: fmt.Errorf(
		"stopped following run %s; the run keeps going. Check it with: %s", f.t.runID, f.t.statusHint())}
}

// header prints which run is followed, its UI link, and a note when the DAG
// is paused: the scheduler holds every run of a paused DAG in queued, manual
// runs included (Airflow semantics), so the note says the run waits and how to
// unpause the DAG.
func (f *follower) header(ctx context.Context) error {
	if _, err := fmt.Fprintf(f.out, "Following run %s of %s (Ctrl-C stops following; the run keeps going)\n  %s\n",
		f.t.runID, f.t.dagID, f.t.uiRunURL()); err != nil {
		return err
	}
	return notePausedRun(ctx, f.out, f.t.base, f.t.token, f.t.dagID)
}

// notePausedRun prints, when dagID is paused, that a run just triggered waits
// in queued until the DAG is unpaused, with the command that unpauses it.
func notePausedRun(ctx context.Context, out io.Writer, base, token, dagID string) error {
	if !dagPaused(ctx, base, token, dagID) {
		return nil
	}
	_, err := fmt.Fprintf(out, "note: %s is paused: this run waits in queued until you unpause it: dexaflow admin dags unpause %s\n",
		dagID, dagID)
	return err
}

// dagPaused reports whether the DAG is paused. The answer only decides
// whether a note is printed, so a failed read counts as not paused.
func dagPaused(ctx context.Context, base, token, dagID string) bool {
	var dag struct {
		IsPaused bool `json:"is_paused"`
	}
	dagURL := strings.TrimRight(base, "/") + "/api/v2/dags/" + url.PathEscape(dagID)
	return getJSON(ctx, dagURL, token, &dag) == nil && dag.IsPaused
}

// hintStall says once that the run is still queued after stallAfter. When
// the DAG is paused at that moment the pause is the cause, and the note says
// so; otherwise it points at the agents and max_active_runs.
func (f *follower) hintStall(ctx context.Context, state string, stallAfter time.Duration) {
	if f.hinted || state != "queued" || time.Since(f.started) < stallAfter {
		return
	}
	f.hinted = true
	if dagPaused(ctx, f.t.base, f.t.token, f.t.dagID) {
		f.printf("note: run %s is still queued after %s because %s is paused; it starts once you unpause it: dexaflow admin dags unpause %s\n",
			f.t.runID, stallAfter.Round(time.Second), f.t.dagID, f.t.dagID)
		return
	}
	f.printf("note: run %s is still queued after %s and no task has started. "+
		"Check that an agent is connected and has room (dexaflow admin health) and that no other run of %s holds its max_active_runs.\n",
		f.t.runID, stallAfter.Round(time.Second), f.t.dagID)
}

// poll reads the run state and its task instances once and prints what
// changed since the previous poll.
func (f *follower) poll(ctx context.Context) (state string, changed bool, err error) {
	var run struct {
		State string `json:"state"`
	}
	if rerr := getJSON(ctx, f.t.apiRunURL(), f.t.token, &run); rerr != nil {
		return "", false, rerr
	}
	tis, err := listTaskInstances(ctx, f.t)
	if err != nil {
		return "", false, err
	}
	for _, ti := range tis {
		if f.report(ctx, ti) {
			changed = true
		}
	}
	return run.State, changed, nil
}

// report prints ti's state when it changed, then the logs of its attempt when
// that attempt has finished. It reports whether anything was printed.
func (f *follower) report(ctx context.Context, ti taskInstance) bool {
	prev, known := f.seen[ti.TaskID]
	if !known {
		f.order = append(f.order, ti.TaskID)
		f.width = max(f.width, len(ti.TaskID))
	}
	f.seen[ti.TaskID] = ti
	changed := (known || ti.state() != "none") && (prev.state() != ti.state() || prev.TryNumber != ti.TryNumber)
	if changed {
		f.printf("  %-*s  %s%s\n", f.width, ti.TaskID, ti.state(), attemptDetails(ti))
	}
	// Every try before the current one has finished, even when no poll saw
	// it end (up_for_retry can come and go between two polls).
	for try := 1; try <= ti.TryNumber; try++ {
		if try == ti.TryNumber && !attemptFinished(ti.state()) {
			break
		}
		key := ti.TaskID + "/" + strconv.Itoa(try)
		if !f.logged[key] {
			f.logged[key] = true
			f.printLogs(ctx, ti.TaskID, try)
		}
	}
	return changed
}

// attemptDetails is the duration of a finished attempt and the try number
// from the second try on.
func attemptDetails(ti taskInstance) string {
	var b strings.Builder
	if ti.Duration != nil && attemptFinished(ti.state()) {
		b.WriteString("  " + formatSeconds(*ti.Duration))
	}
	if ti.TryNumber > 1 {
		fmt.Fprintf(&b, "  (try %d)", ti.TryNumber)
	}
	return b.String()
}

// attemptFinished reports whether a task in this state has an attempt whose
// logs are complete.
func attemptFinished(state string) bool {
	switch state {
	case "success", "failed", "up_for_retry":
		return true
	}
	return false
}

func formatSeconds(s float64) string {
	if s < 60 {
		return strconv.FormatFloat(s, 'f', 1, 64) + "s"
	}
	return time.Duration(s * float64(time.Second)).Round(time.Second).String()
}

// printLogs prints one finished attempt's logs, each line indented under the
// task. A failed read is reported in place and does not stop the follow.
func (f *follower) printLogs(ctx context.Context, taskID string, try int) {
	if f.werr != nil {
		return
	}
	err := streamTaskLogs(ctx, f.out, "    | ", f.t.base, f.t.token, f.t.dagID, f.t.runID, taskID, try, false)
	if err != nil && ctx.Err() == nil {
		// A failed write to out fails this note too, and followRun stops on it.
		f.printf("    (logs of %s try %d unavailable: %v)\n", taskID, try, err)
	}
}

// finish reports the end of the run: nil for success, an exitRunFailed error
// naming the failed tasks otherwise.
func (f *follower) finish(state string) error {
	elapsed := time.Since(f.started).Round(100 * time.Millisecond)
	if state == "success" {
		_, err := fmt.Fprintf(f.out, "Run %s succeeded (followed for %s).\n", f.t.runID, elapsed)
		return err
	}
	byState := map[string][]string{}
	for _, id := range f.order {
		st := f.seen[id].state()
		byState[st] = append(byState[st], id)
	}
	var parts []string
	if failed := byState["failed"]; len(failed) > 0 {
		parts = append(parts, plural(len(failed), "failed task", "failed tasks")+": "+strings.Join(failed, ", "))
	}
	if upstream := byState["upstream_failed"]; len(upstream) > 0 {
		parts = append(parts, "upstream_failed: "+strings.Join(upstream, ", "))
	}
	detail := ""
	if len(parts) > 0 {
		detail = " (" + strings.Join(parts, "; ") + ")"
	}
	return &exitCodeError{code: exitRunFailed, err: fmt.Errorf("run %s of %s ended %s%s. See %s",
		f.t.runID, f.t.dagID, state, detail, f.t.uiRunURL())}
}

// listTaskInstances lists every task instance of the run, page by page.
func listTaskInstances(ctx context.Context, t followTarget) ([]taskInstance, error) {
	var all []taskInstance
	for {
		var page struct {
			TaskInstances []taskInstance `json:"task_instances"`
			TotalEntries  int            `json:"total_entries"`
		}
		u := fmt.Sprintf("%s/taskInstances?limit=%d&offset=%d", t.apiRunURL(), followPageSize, len(all))
		if err := getJSON(ctx, u, t.token, &page); err != nil {
			return nil, err
		}
		all = append(all, page.TaskInstances...)
		if len(page.TaskInstances) == 0 || len(all) >= page.TotalEntries {
			return all, nil
		}
	}
}

// getJSON GETs u and decodes the JSON answer into v. A network error or a 5xx
// answer comes back as a transientError.
func getJSON(ctx context.Context, u, token string, v any) error {
	status, raw, err := apiRequest(ctx, http.MethodGet, u, token, nil)
	if err != nil {
		return &transientError{err: err}
	}
	if status >= http.StatusInternalServerError {
		return &transientError{err: apiStatusError(status, raw)}
	}
	if status >= http.StatusMultipleChoices {
		return apiStatusError(status, raw)
	}
	if jerr := json.Unmarshal(raw, v); jerr != nil {
		return fmt.Errorf("parsing %s: %w", u, jerr)
	}
	return nil
}

// printRunLogs prints the logs of the latest attempt of every task of a run,
// each under a header naming the task, its try and its state. A task that
// never ran gets a header that says so and no logs request.
func printRunLogs(ctx context.Context, w io.Writer, t followTarget) error {
	tis, err := listTaskInstances(ctx, t)
	if err != nil {
		return err
	}
	for i, ti := range tis {
		sep := ""
		if i > 0 {
			sep = "\n"
		}
		if ti.TryNumber < 1 {
			if _, err := fmt.Fprintf(w, "%s==> %s (%s): no attempt <==\n", sep, ti.TaskID, ti.state()); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%s==> %s (try %d, %s) <==\n", sep, ti.TaskID, ti.TryNumber, ti.state()); err != nil {
			return err
		}
		if err := streamTaskLogs(ctx, w, "", t.base, t.token, t.dagID, t.runID, ti.TaskID, ti.TryNumber, false); err != nil {
			return fmt.Errorf("logs of %s: %w", ti.TaskID, err)
		}
	}
	return nil
}
