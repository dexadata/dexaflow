package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// grandchildScript is the canonical shape of the task that defeated
// execution_timeout (#943): a shell that BACKGROUNDS a long-running child and
// waits. The background subshell inherits the shell's stdout, which — because
// the agent wires stdout to a log writer rather than to an *os.File — is a pipe
// the standard library's copying goroutine reads until every writer closes it.
// Killing the direct shell alone therefore does not end the wait.
//
// The loop is self-limiting (400 × 0.05s ≈ 20s) so a regression fails the
// elapsed-time assertion instead of hanging the package for its whole timeout,
// and it appends a byte per iteration so a test can see whether the work is
// still happening AFTER the call returned.
func grandchildScript(pidFile, marker string) string {
	return fmt.Sprintf(
		// Both, and on one line: the premise "the task leads its own group" has
		// to be checkable AFTER the kill, and Getpgid on a dead leader returns
		// ESRCH — which is indistinguishable from "the group is gone".
		"echo \"$$ $(ps -o pgid= -p $$ | tr -d ' ')\" > %[1]s\n"+
			"( i=0; while [ $i -lt 400 ]; do printf x >> %[2]s; sleep 0.05; i=$((i+1)); done ) &\n"+
			"echo started\n"+
			"wait\n", pidFile, marker)
}

// readPGID waits for the script above to publish its pid and its pgid, and
// asserts they are equal — i.e. that the task really did lead its own group.
//
// That assertion is the premise the kill probe rests on and cannot make for
// itself: `kill(-pgid, 0)` returns ESRCH both when the group is gone and when
// it never existed, so without this, dropping Setpgid makes the probe report
// success having tested nothing. Measured: the wiring test then passed in 1.1s
// while `ps` still showed the bash tree and its sleep running — the exact
// "returns on time, work keeps going" shape #943 is about, reproduced inside
// its own regression test.
func readPGID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidFile) //nolint:gosec // test-owned temp path
		if err == nil {
			fields := strings.Fields(string(b))
			if len(fields) == 2 {
				pid, perr := strconv.Atoi(fields[0])
				pgid, gerr := strconv.Atoi(fields[1])
				if perr == nil && gerr == nil && pid > 0 && pgid > 0 {
					if pid != pgid {
						t.Fatalf("the task ran in process group %d rather than leading its own (%d): "+
							"the kill probe would then ESRCH on a group that never existed and report success", pgid, pid)
					}
					return pid
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the task never published its pid to %s", pidFile)
	return 0
}

// requireProcessGroupGone polls until no process remains in pgid. Signal 0 only
// probes for existence; it delivers nothing. Polling (rather than a single
// check) absorbs the moment in which the killed grandchild is still a zombie
// awaiting reaping by init, which would otherwise keep the group "alive".
func requireProcessGroupGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if err == syscall.ESRCH { //nolint:errorlint // syscall.Kill returns a bare syscall.Errno
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still has live members 5s after the call returned (kill probe: %v) — "+
				"the timeout returned but the task's work did not stop", pgid, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestExecRunnerTimeoutKillsAGrandchildHoldingStdout is the regression test for
// #943. Before the fix this shape defeats execution_timeout entirely: the cancel
// SIGKILLs the direct child only, the surviving grandchild keeps the stdout pipe
// open, and Wait blocks on the copying goroutine until that grandchild exits on
// its own — here about twenty seconds after a deadline of half a second.
//
// Returning is necessary but not sufficient, so this asserts all three halves:
// the call returns near the deadline, the process group is gone, and the work
// the grandchild was doing has actually stopped.
func TestExecRunnerTimeoutKillsAGrandchildHoldingStdout(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	marker := filepath.Join(dir, "marker")

	const deadline = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	var out bytes.Buffer
	start := time.Now()
	// The writers are deliberately NOT *os.File: that is what makes the standard
	// library interpose a pipe and a copying goroutine, which is half the defect.
	_, err := NewExecRunner().Run(ctx, []string{"sh", "-c", grandchildScript(pidFile, marker)}, nil, &out, &out)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 3s is far below the ~20s the grandchild's loop runs for and far above the
	// 500ms deadline plus any plausible scheduling jitter, so this separates
	// "the deadline was honored" from "the grandchild was waited out".
	if elapsed > 3*time.Second {
		t.Fatalf("Run returned after %v for a %v deadline: the grandchild holding stdout kept the wait open", elapsed, deadline)
	}

	t.Logf("Run returned %v after a %v deadline", elapsed.Round(time.Millisecond), deadline)

	pgid := readPGID(t, pidFile)
	requireProcessGroupGone(t, pgid)

	// The grandchild must have run (otherwise this test proves nothing about
	// killing it) and must have stopped (otherwise the timeout freed the agent
	// while the task kept burning the pod's resources).
	before := fileSize(t, marker)
	if before == 0 {
		t.Fatal("the grandchild never wrote to its marker file; the test is not exercising the shape it claims")
	}
	time.Sleep(500 * time.Millisecond)
	if after := fileSize(t, marker); after != before {
		t.Errorf("the grandchild is still working after the timeout returned: marker grew %d -> %d bytes", before, after)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Size()
}

// slowWriter accepts bytes at a fixed cost per Write, standing in for a log sink
// streaming to a control plane that is not keeping up.
type slowWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	delay time.Duration
}

func (w *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(w.delay)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *slowWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestExecRunnerCapturesEveryLineOfANormalExit guards the fix's own risk. A
// bounded WaitDelay closes the child's pipes when the delay expires, and the
// timer starts when Wait observes the child exit — not when the copying finishes
// — so a delay set too short truncates the tail of a task that exited perfectly
// well. The writer here consumes deliberately slowly, which puts that drain on
// the clock the WaitDelay bounds; the assertion is the COMPLETE output, in
// order, with the exit code the task chose.
func TestExecRunnerCapturesEveryLineOfANormalExit(t *testing.T) {
	const lines = 4000
	var want strings.Builder
	for i := range lines {
		fmt.Fprintf(&want, "line-%04d\n", i)
	}

	out := &slowWriter{delay: 300 * time.Millisecond}
	var errb bytes.Buffer
	code, err := NewExecRunner().Run(context.Background(),
		[]string{"sh", "-c", fmt.Sprintf("i=0; while [ $i -lt %d ]; do printf 'line-%%04d\\n' $i; i=$((i+1)); done", lines)},
		nil, out, &errb)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got := out.String(); got != want.String() {
		t.Errorf("stdout was not captured whole: got %d bytes / %d lines, want %d bytes / %d lines",
			len(got), strings.Count(got, "\n"), want.Len(), lines)
	}
}

// TestExecRunnerLeakedPipeOnCleanExitKeepsTheTasksVerdict covers the other side
// of the WaitDelay: a task whose own process exits 0 but leaves a background
// child holding stdout. No context is canceled here, so nothing kills the group
// and the agent would otherwise wait for that child for as long as it lives.
// The delay stops the wait — and the task's verdict must survive that, because
// the leaked pipe is the agent's I/O problem, not a failure of the user's task.
// os/exec reports the forced pipe close as ErrWaitDelay in exactly this case
// (successful exit status), which is why it needs mapping rather than passing
// through as a run error.
func TestExecRunnerLeakedPipeOnCleanExitKeepsTheTasksVerdict(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")

	// A short delay keeps the test fast; the production value is exercised by the
	// timeout test above.
	runner := execRunner{waitDelay: 300 * time.Millisecond}
	var out bytes.Buffer
	start := time.Now()
	code, err := runner.Run(context.Background(),
		[]string{"sh", "-c", fmt.Sprintf("( sleep 10; printf x >> %s ) &\necho done\nexit 0\n", marker)},
		nil, &out, &out)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a task that exited 0 must not be turned into a failure by a leaked pipe: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Run took %v: the wait was not bounded by the WaitDelay", elapsed)
	}
	if !strings.Contains(out.String(), "done") {
		t.Errorf("stdout = %q, want it to contain the output written before the exit", out.String())
	}
}

// TestRunnerExecutionTimeoutSurvivesAGrandchild drives the REAL entry point:
// Runner.Run with the production command runner and a bash task, which is the
// wiring the leaf test cannot prove. The bash operator runs the entrypoint under
// `bash -c`, so a task doing anything compound — an `&&`, a pipe, a background
// job — already makes the user's own programs grandchildren of the process the
// agent kills.
//
// It asserts what #943 says is missing today: the agent's clock is authoritative
// (the call returns near the deadline), the task's work stops, and the failure
// is reported AND recorded as a timeout rather than as a generic failure.
func TestRunnerExecutionTimeoutSurvivesAGrandchild(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	marker := filepath.Join(dir, "marker")
	logPath := filepath.Join(dir, "termination-log")

	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator:                "bash",
		Entrypoint:              grandchildScript(pidFile, marker),
		ExecutionTimeoutSeconds: 1,
	}}
	r := newRunner(client, nil, &recordingSink{})
	r.Cmd = NewExecRunner()
	r.Env = os.Environ() // the task needs a PATH to resolve `sleep`
	r.TerminationLogPath = logPath

	start := time.Now()
	err := r.Run(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a task that exceeds its execution_timeout_seconds must fail")
	}
	// The grandchild's loop runs ~20s; anything under 10s means the agent's own
	// clock ended the attempt rather than the task finishing on its own.
	if elapsed > 10*time.Second {
		t.Fatalf("Run took %v for a 1s execution_timeout: the timeout did not stop the task", elapsed)
	}
	if !strings.Contains(err.Error(), "execution_timeout") {
		t.Errorf("error = %q, want it to name execution_timeout", err)
	}

	t.Logf("Run returned %v for a 1s execution_timeout", elapsed.Round(time.Millisecond))

	pgid := readPGID(t, pidFile)
	requireProcessGroupGone(t, pgid)

	if last := client.states[len(client.states)-1]; last != agentv1.TaskState_TASK_STATE_FAILED {
		t.Errorf("final reported state = %v, want failed", last)
	}
	if n := len(client.reports); n > 0 {
		if got := client.reports[n-1].GetErrorMessage(); !strings.Contains(got, "execution_timeout") {
			t.Errorf("reported message = %q, want it to name execution_timeout", got)
		}
	}
	rec := readOutcome(t, logPath)
	if rec.Outcome != taskoutcome.Failed {
		t.Errorf("durable outcome = %q, want failed", rec.Outcome)
	}
	if !strings.Contains(rec.Reason, "execution_timeout") {
		t.Errorf("durable reason = %q, want it to name execution_timeout", rec.Reason)
	}
	if rec.ExitCode == nil || *rec.ExitCode == 0 {
		t.Errorf("durable exit_code = %v, want a non-zero code (a killed task never exited 0)", rec.ExitCode)
	}
}

// TestKillProcessGroupFallsBackWhenTheChildLeadsNoGroup pins the degrade path.
// A process group is named by its leader's pid, so kill(-pid) on a process that
// leads no group reaches NOTHING — it returns ESRCH rather than signaling the
// group the process happens to belong to (measured, not assumed). Reporting
// that as "already done" would leave a live child behind while the cancel
// claimed success, so the fallback kills the process itself.
func TestKillProcessGroupFallsBackWhenTheChildLeadsNoGroup(t *testing.T) {
	// The context is deliberately never canceled: this test drives the kill
	// helper directly, not through the context path.
	cmd := exec.CommandContext(context.Background(), "sleep", "30") // no SysProcAttr: inherits this group
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Getpgid: %v", err)
	}
	// The premise: this child is NOT its own group leader, so the group-kill
	// below must miss and the fallback must be what ends it.
	if pgid == cmd.Process.Pid {
		t.Fatalf("child pid %d already leads its own group; this test cannot exercise the fallback", cmd.Process.Pid)
	}

	if err := killProcessGroup(cmd.Process); err != nil {
		t.Fatalf("killProcessGroup: %v", err)
	}
	werr := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(werr, &exitErr) {
		t.Fatalf("child did not exit on a signal: %v", werr)
	}
	if !exitErr.ProcessState.Sys().(syscall.WaitStatus).Signaled() { //nolint:errcheck,forcetypeassert // unix-only test
		t.Errorf("child exited %v, want it killed by a signal", exitErr.ProcessState)
	}
}

// TestExecRunnerTimeoutKillsATaskThatTrapsSIGTERM pins the signal, which the
// rest of the suite does not.
//
// Swapping SIGKILL for SIGTERM in killProcessGroup leaves every other test in
// this file green — measured. That is the shape of a plausible future
// refactor ("let's shut down gracefully"), and it silently re-opens #943 for
// any task that traps the signal: a `trap ” TERM` shell, a JVM with a
// shutdown hook that blocks, a Python process with a SIGTERM handler that
// swallows it. The timeout is the last resort, so it does not negotiate.
func TestExecRunnerTimeoutKillsATaskThatTrapsSIGTERM(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	marker := filepath.Join(dir, "marker")

	// Ignores TERM entirely, then does the same grandchild-holding-stdout
	// thing: only an un-trappable signal ends this.
	script := "trap '' TERM\n" + grandchildScript(pidFile, marker)

	const deadline = 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	var out bytes.Buffer
	start := time.Now()
	if _, err := NewExecRunner().Run(ctx, []string{"sh", "-c", script}, nil, &out, &out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run returned after %v for a %v deadline against a SIGTERM-trapping task: "+
			"the timeout is negotiating with something that refuses", elapsed, deadline)
	}
	requireProcessGroupGone(t, readPGID(t, pidFile))

	// Same two-sample check the sibling test uses: a marker that never grew
	// would mean the fixture never ran, not that the kill worked.
	before := fileSize(t, marker)
	if before == 0 {
		t.Fatal("the grandchild never wrote to its marker file; the test is not exercising the shape it claims")
	}
	time.Sleep(300 * time.Millisecond)
	if after := fileSize(t, marker); after != before {
		t.Errorf("a SIGTERM-trapping task is still working after the timeout returned: marker grew %d -> %d bytes", before, after)
	}
}
