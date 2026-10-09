//go:build linux || darwin

package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/agent"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/procgroup"
)

// ceilingAgentLogEnv names the file the helper agent writes its end-of-task
// line to, standing in for the try log the real agent streams over gRPC.
const ceilingAgentLogEnv = "DEXAFLOW_TEST_CEILING_AGENT_LOG"

// TestHelperCeilingAgent is not a test on its own: it is the agent process the
// ceiling tests spawn through SubprocessExecutor.Execute. It runs a sleeping
// task through the real agent exec runner, which records the task's process
// group where Execute told it to, and once the task ends it writes the line the
// real agent's emitTaskEnded writes, then exits.
func TestHelperCeilingAgent(t *testing.T) {
	logPath := os.Getenv(ceilingAgentLogEnv)
	if logPath == "" {
		t.Skip("helper process only")
	}
	var out bytes.Buffer
	code, err := agent.NewExecRunnerRecordingGroup(os.Getenv("DEXAFLOW_TASK_PGID_FILE")).Run(
		context.Background(), []string{"sleep", "60"}, os.Environ(), &out, &out)
	line := "task succeeded"
	if err != nil || code != 0 {
		line = fmt.Sprintf("task failed (exit %d)", code)
	}
	// The real agent flushes its streams and reports before it logs the end
	// and exits; the pause makes a stop that does not wait for the agent lose
	// the race with this line.
	time.Sleep(300 * time.Millisecond)
	// Best-effort: the parent test may already be gone when a cleanup kills
	// this task, and a failure here must not print into its output.
	if f, oerr := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); oerr == nil { //nolint:gosec // test-owned temp file
		_, _ = f.WriteString(line + "\n") //nolint:errcheck // best-effort, see above
		_ = f.Close()                     //nolint:errcheck // best-effort, see above
	}
	// A non-zero exit keeps the testing package from printing a PASS line into
	// the parent test's output, which Execute wires the agent's stdout to.
	os.Exit(3)
}

// appendLine appends one line to path.
func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // test-owned temp file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// ceilingHelperAgent writes the agent script Execute runs: it re-executes this
// test binary as TestHelperCeilingAgent, writing its try-log line to logPath.
func ceilingHelperAgent(t *testing.T, logPath string) string {
	t.Helper()
	bin, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return writeScript(t, fmt.Sprintf("%s=%q exec %q -test.run='^TestHelperCeilingAgent$'", ceilingAgentLogEnv, logPath, bin))
}

// startCeilingAttempt spawns the helper agent for attempt r/t/1 and waits for
// it to record its task's process group.
func startCeilingAttempt(t *testing.T, logPath string) (*SubprocessExecutor, procgroup.Record) {
	t.Helper()
	e := NewSubprocessExecutor(ceilingHelperAgent(t, logPath), discardLogger())
	e.SetWorkDir(t.TempDir())
	e.SetPIDDir(filepath.Join(t.TempDir(), "pids"))
	e.orphanGrace, e.orphanKillWait = 2*time.Second, 5*time.Second
	if _, err := e.Execute(context.Background(), Request{RunID: "r", TaskID: "t", TryNumber: 1, TaskInstanceID: "ti"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec, err := procgroup.Read(e.groupPath("r", "t", 1))
		if err == nil {
			t.Cleanup(func() { _ = syscall.Kill(-rec.PGID, syscall.SIGKILL) }) //nolint:errcheck // best-effort cleanup
			return e, rec
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent never recorded its task group: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestStopAttemptStopsALiveAgentsTask: the subprocess executor can stop the
// task of an attempt whose agent is still alive (#1511), which is what lets the
// reaper end an attempt that outlived the credential ceiling. The stop signals
// the task's whole process group, and it returns only once the agent has seen
// its task end and exited, so whatever the agent logs about it is written
// before the caller's own marker.
func TestStopAttemptStopsALiveAgentsTask(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "try.log")
	e, rec := startCeilingAttempt(t, logPath)
	ctx := context.Background()
	if alive, err := e.AttemptProcessAlive(ctx, "r", "t", 1); err != nil || !alive {
		t.Fatalf("before the stop: AttemptProcessAlive = (%v, %v), want (true, nil)", alive, err)
	}

	stopped, err := e.StopAttempt(ctx, "r", "t", 1)
	if err != nil || !stopped {
		t.Fatalf("StopAttempt = (%v, %v), want (true, nil)", stopped, err)
	}
	if alive, _ := procgroup.GroupAlive(rec.PGID); alive {
		t.Error("the task group must be gone once StopAttempt returns")
	}
	if alive, aerr := e.AttemptProcessAlive(ctx, "r", "t", 1); aerr != nil || alive {
		t.Errorf("after the stop: AttemptProcessAlive = (%v, %v), want (false, nil): the agent must have exited", alive, aerr)
	}
	data, err := os.ReadFile(logPath) //nolint:gosec // test-owned temp file
	if err != nil || !strings.Contains(string(data), "task failed") {
		t.Errorf("the agent must have logged its task's end before StopAttempt returned, got %q (%v)", data, err)
	}
}

// TestStopAttemptWithoutARecordStopsNothing: an attempt with no task group
// record (nothing spawned, or an agent too old to write one) has nothing the
// executor can verify, so nothing is signaled.
func TestStopAttemptWithoutARecordStopsNothing(t *testing.T) {
	e := NewSubprocessExecutor("/bin/true", discardLogger())
	e.SetPIDDir(t.TempDir())
	if stopped, err := e.StopAttempt(context.Background(), "r", "t", 1); err != nil || stopped {
		t.Fatalf("no record: StopAttempt = (%v, %v), want (false, nil)", stopped, err)
	}
	e.SetPIDDir(filepath.Join(t.TempDir(), "missing"))
	if stopped, err := e.StopAttempt(context.Background(), "r", "t", 1); err != nil || stopped {
		t.Fatalf("no record dir: StopAttempt = (%v, %v), want (false, nil)", stopped, err)
	}
}

// fileMarkerSink appends each marker to the same file the helper agent writes
// its lines to, so the file reads like the attempt's try log.
type fileMarkerSink struct {
	t    *testing.T
	path string
	mu   sync.Mutex
}

func (s *fileMarkerSink) AppendEvent(_ logs.Ref, ev logs.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	appendLine(s.t, s.path, ev.Message)
	return nil
}

// TestCeilingTryLogEndsWithTheCeilingLine drives #1511 end to end in Lite with
// a short ceiling: a real agent whose task never exits is still running past
// the ceiling, the reaper fails the attempt as credential_ceiling and stops it,
// and the try log ends with the ceiling line. No "task succeeded" line appears,
// and nothing of the attempt is left running.
func TestCeilingTryLogEndsWithTheCeilingLine(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "try.log")
	e, rec := startCeilingAttempt(t, logPath)
	const ceiling = 300 * time.Millisecond
	time.Sleep(2 * ceiling)

	tl := &ceilingTimeline{}
	store := &ceilingStore{tl: tl}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, &capturingRecorder{})
	r.procs = e
	r.running = &ceilingRunning{cands: []PodLostCandidate{{
		TaskInstanceID: "ti", TenantID: "ten", DagRunID: "r", DagID: "d", TaskID: "t",
		TryNumber: 1, AttemptEpoch: 1, RunningSince: time.Now().UTC().Add(-2 * ceiling), Heartbeated: true,
	}}}
	r.sink = &fileMarkerSink{t: t, path: logPath}
	r.ceiling = ceiling
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run err = %v", err)
	}
	r.waitCeilingStops()

	if got := tl.snapshot(); len(got) != 1 || got[0] != "mark ti/1/1" {
		t.Fatalf("the attempt must be failed as credential_ceiling exactly once, got %q", got)
	}
	data, err := os.ReadFile(logPath) //nolint:gosec // test-owned temp file
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "killed: credential_ceiling") {
		t.Errorf("the try log must end with the ceiling line, got %q", lines)
	}
	if n := strings.Count(string(data), "credential_ceiling"); n != 1 {
		t.Errorf("the try log must carry exactly one ceiling line, got %d in %q", n, lines)
	}
	if !strings.Contains(string(data), "task failed") {
		t.Errorf("the agent's own end-of-task line must come before the ceiling line, got %q", lines)
	}
	if strings.Contains(string(data), "task succeeded") {
		t.Errorf("the try log must not claim the task succeeded, got %q", lines)
	}
	if alive, _ := procgroup.GroupAlive(rec.PGID); alive {
		t.Error("the task must be stopped")
	}
	if alive, err := e.AttemptProcessAlive(context.Background(), "r", "t", 1); err != nil || alive {
		t.Errorf("nothing of the attempt may be left running: AttemptProcessAlive = (%v, %v)", alive, err)
	}
}
