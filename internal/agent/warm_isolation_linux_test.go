//go:build linux

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// survivorProbeCmd runs attempt 1 for real (so its user code can escape with
// setsid) and, on attempt 2, records whether the escaped process is still alive.
type survivorProbeCmd struct {
	real     CommandRunner
	pidFile  string
	runs     int
	survived bool
	probed   bool
}

func (c *survivorProbeCmd) Run(ctx context.Context, argv, env []string, stdout, stderr io.Writer) (int, error) {
	c.runs++
	if c.runs == 1 {
		return c.real.Run(ctx, argv, env, stdout, stderr)
	}
	pid, err := readPidFile(c.pidFile)
	if err != nil {
		return 1, err
	}
	c.probed = true
	c.survived = processAlive(pid)
	return 0, nil
}

func readPidFile(path string) (int, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // test-owned temp file
	if err != nil {
		return 0, fmt.Errorf("reading escaped pid: %w", err)
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}

// processAlive reports whether pid names a process that is not a zombie.
func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	_, state, _, ok := parseProcStat(string(raw))
	return ok && state != 'Z'
}

// TestWarmWorkerSetsidChildDoesNotSurviveIntoNextAttempt is X3.3: a task that
// detaches a child with setsid escapes its process group, so the group kill
// after the attempt misses it, and it would keep running (holding that
// attempt's env and secrets) while the next attempt runs on the same worker. The
// worker must kill every descendant and confirm none is left before it takes the
// next assignment.
func TestWarmWorkerSetsidChildDoesNotSurviveIntoNextAttempt(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "escaped.pid")
	escape := fmt.Sprintf(
		"setsid sh -c 'echo $$ > %[1]s; exec sleep 300' </dev/null >/dev/null 2>&1 & "+
			"while [ ! -s %[1]s ]; do sleep 0.01; done", pidFile)
	t.Cleanup(func() {
		if pid, err := readPidFile(pidFile); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	stream := &fakeAssignmentStream{
		ctx: context.Background(),
		assignments: []*agentv1.WorkAssignment{
			{AssignmentId: "asg-1", AttemptToken: "tok-1"},
			{AssignmentId: "asg-2", AttemptToken: "tok-2"},
		},
	}
	tokens := NewTokenSource("bootstrap")
	client := &warmFake{fakeClient: &fakeClient{}, stream: stream, tokens: tokens, specs: []*agentv1.TaskSpec{
		{Operator: "bash", Entrypoint: escape},
		{Operator: "bash", Entrypoint: "true"},
	}}
	cmd := &survivorProbeCmd{real: NewExecRunner(), pidFile: pidFile}
	w := &WarmRunner{
		StreamClient:  client,
		WorkClient:    client,
		AttemptTokens: tokens,
		Cmd:           cmd,
		Env:           []string{"PATH=" + os.Getenv("PATH")},
		ScratchDir:    filepath.Join(t.TempDir(), "scratch"),
	}
	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("WarmRunner.Run: %v", err)
	}
	if !cmd.probed {
		t.Fatalf("attempt 2 never ran (runs=%d)", cmd.runs)
	}
	if cmd.survived {
		t.Error("a setsid child of attempt 1 was still running when attempt 2 started")
	}
}

// TestWarmWorkerIsNotDumpable is X3.4: the warm agent holds its bootstrap token
// in its environment and the current attempt token in memory, and runs as the
// same uid as the task. A dumpable agent lets the task read both from
// /proc/<agent>/environ and /proc/<agent>/mem. Serving makes the process non
// dumpable first.
func TestWarmWorkerIsNotDumpable(t *testing.T) {
	stream := &fakeAssignmentStream{ctx: context.Background()}
	tokens := NewTokenSource("bootstrap")
	client := &warmFake{fakeClient: &fakeClient{}, stream: stream, tokens: tokens}
	w := &WarmRunner{
		StreamClient:  client,
		WorkClient:    client,
		AttemptTokens: tokens,
		Cmd:           &scratchProbeCmd{},
		ScratchDir:    filepath.Join(t.TempDir(), "scratch"),
	}
	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("WarmRunner.Run: %v", err)
	}
	dumpable, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prGetDumpable, 0, 0)
	if errno != 0 {
		t.Fatalf("prctl(PR_GET_DUMPABLE): %v", errno)
	}
	if dumpable != 0 {
		t.Errorf("warm agent is dumpable (PR_GET_DUMPABLE=%d), want 0", dumpable)
	}
}

// TestParseProcStat covers the /proc/<pid>/stat parse the sweep relies on,
// including a command name that itself contains spaces and parentheses, which a
// task controls and could use to forge the parent pid field.
func TestParseProcStat(t *testing.T) {
	cases := []struct {
		line      string
		pid, ppid int
		state     byte
	}{
		{"42 (sleep) S 7 42 42 0 -1", 42, 7, 'S'},
		{"43 (a) Z 1 (b) R 9 1 1) R 99 43 43 0", 43, 99, 'R'},
		{"44 (zombie) Z 7 0 0", 44, 7, 'Z'},
	}
	for _, c := range cases {
		pid, state, ppid, ok := parseProcStat(c.line)
		if !ok || pid != c.pid || state != c.state || ppid != c.ppid {
			t.Errorf("parseProcStat(%q) = %d %c %d %v, want %d %c %d true", c.line, pid, state, ppid, ok, c.pid, c.state, c.ppid)
		}
	}
	if _, _, _, ok := parseProcStat("garbage"); ok {
		t.Error("parseProcStat accepted a malformed line")
	}
}

// TestProcStartTime reads field 22 of /proc/<pid>/stat, the process start time
// the sweep pairs with the pid so a recycled pid is never mistaken for the
// process the walk found. The fields are counted from the last ')', so a command
// name with spaces and parentheses cannot shift them.
func TestProcStartTime(t *testing.T) {
	cases := []struct {
		line  string
		start uint64
	}{
		{"42 (sleep) S 7 42 42 0 -1 4194304 100 0 0 0 1 2 0 0 20 0 1 0 987654 1000 10", 987654},
		{"43 (a) b) c) R 99 43 43 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5 0 0", 5},
	}
	for _, c := range cases {
		got, ok := procStartTime(c.line)
		if !ok || got != c.start {
			t.Errorf("procStartTime(%q) = %d %v, want %d true", c.line, got, ok, c.start)
		}
	}
	if _, ok := procStartTime("42 (short) S 7 42"); ok {
		t.Error("procStartTime accepted a line without field 22")
	}
}

// TestKillProcSkipsARecycledPid: between the /proc walk and the kill, a
// descendant can exit and its pid be handed to an unrelated process (a kubectl
// exec session, say). The kill must check the start time the walk recorded and
// leave a process whose start time differs alone, on both the pidfd path and
// the plain kill fallback.
func TestKillProcSkipsARecycledPid(t *testing.T) {
	for _, usePidfd := range []bool{true, false} {
		t.Run(fmt.Sprintf("pidfd=%v", usePidfd), func(t *testing.T) {
			cmd := exec.Command("sleep", "30")
			if err := cmd.Start(); err != nil {
				t.Fatalf("starting sleep: %v", err)
			}
			done := make(chan struct{})
			go func() { _ = cmd.Wait(); close(done) }()
			defer func() { _ = cmd.Process.Kill(); <-done }()

			pid := cmd.Process.Pid
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if err != nil {
				t.Fatalf("reading stat: %v", err)
			}
			start, ok := procStartTime(string(raw))
			if !ok {
				t.Fatalf("no start time in %q", raw)
			}

			if err := killProc(procRef{pid: pid, start: start + 1}, usePidfd); err != nil {
				t.Fatalf("killProc(stale ref): %v", err)
			}
			select {
			case <-done:
				t.Fatal("a process whose start time did not match the walk was killed")
			case <-time.After(100 * time.Millisecond):
			}

			if err := killProc(procRef{pid: pid, start: start}, usePidfd); err != nil {
				t.Fatalf("killProc(live ref): %v", err)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the process the walk found was not killed")
			}
		})
	}
}
