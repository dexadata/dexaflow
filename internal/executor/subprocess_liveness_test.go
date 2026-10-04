package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// TestAttemptProcessAliveWithoutRecordIsGone: an attempt this executor never
// spawned (or one dispatched before the record existed) has no live agent as
// far as the executor can tell, so the answer is "not alive", not an error.
func TestAttemptProcessAliveWithoutRecordIsGone(t *testing.T) {
	e := NewSubprocessExecutor("/bin/true", discardLogger())
	e.SetPIDDir(t.TempDir())
	alive, err := e.AttemptProcessAlive(context.Background(), "run", "task", 1)
	if err != nil || alive {
		t.Fatalf("AttemptProcessAlive = (%v, %v), want (false, nil) with no record", alive, err)
	}
}

// TestAttemptProcessAliveReadsTheRecordedPID: the liveness answer comes from the
// on-disk record, so it survives a control-plane restart (the agent subprocess
// is detached and outlives the server). A live PID is alive; the PID of a
// process that has exited is not.
func TestAttemptProcessAliveReadsTheRecordedPID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process liveness by signal 0 is POSIX-only")
	}
	dir := t.TempDir()
	e := NewSubprocessExecutor("/bin/true", discardLogger())
	e.SetPIDDir(dir)

	if err := e.recordPID("run", "task", 1, os.Getpid()); err != nil {
		t.Fatalf("recordPID: %v", err)
	}
	alive, err := e.AttemptProcessAlive(context.Background(), "run", "task", 1)
	if err != nil || !alive {
		t.Fatalf("live PID: AttemptProcessAlive = (%v, %v), want (true, nil)", alive, err)
	}

	exited := exec.CommandContext(context.Background(), "true")
	if runErr := exited.Run(); runErr != nil {
		t.Fatalf("running true: %v", runErr)
	}
	if recErr := e.recordPID("run", "task", 2, exited.Process.Pid); recErr != nil {
		t.Fatalf("recordPID: %v", recErr)
	}
	alive, err = e.AttemptProcessAlive(context.Background(), "run", "task", 2)
	if err != nil || alive {
		t.Fatalf("exited PID: AttemptProcessAlive = (%v, %v), want (false, nil)", alive, err)
	}
}

// TestAttemptProcessAliveKeysOnTheAttempt: the record is pinned to (run, task,
// try), so a run id carrying characters that are not filename-safe (manual run
// ids embed a timestamp with colons and a plus sign) still resolves, and a
// different try of the same task is a different attempt.
func TestAttemptProcessAliveKeysOnTheAttempt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process liveness by signal 0 is POSIX-only")
	}
	e := NewSubprocessExecutor("/bin/true", discardLogger())
	e.SetPIDDir(t.TempDir())
	run := "manual__2026-10-04T12:00:00+00:00/../x"
	if err := e.recordPID(run, "task", 1, os.Getpid()); err != nil {
		t.Fatalf("recordPID: %v", err)
	}
	if alive, _ := e.AttemptProcessAlive(context.Background(), run, "task", 1); !alive {
		t.Error("the recorded attempt must read alive")
	}
	if alive, _ := e.AttemptProcessAlive(context.Background(), run, "task", 2); alive {
		t.Error("another try of the same task is another attempt and must not read alive")
	}
}

// TestSubprocessExecuteRecordsAgentLiveness: Execute records the spawned
// agent's PID for the attempt, so the reapers can tell a live agent from a dead
// one; once the agent exits the record is removed and the attempt reads gone.
func TestSubprocessExecuteRecordsAgentLiveness(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash agent stub is POSIX-only")
	}
	work := t.TempDir()
	release := filepath.Join(work, "release")
	e := NewSubprocessExecutor(writeScript(t, "while [ ! -f "+release+" ]; do sleep 0.05; done"), discardLogger())
	e.SetWorkDir(work)
	pidDir := t.TempDir()
	e.SetPIDDir(pidDir)

	req := Request{RunID: "r", TaskID: "t", TryNumber: 3, TaskInstanceID: "ti"}
	if _, err := e.Execute(context.Background(), req); err != nil {
		t.Fatalf("execute: %v", err)
	}
	alive, err := e.AttemptProcessAlive(context.Background(), "r", "t", 3)
	if err != nil || !alive {
		t.Fatalf("running agent: AttemptProcessAlive = (%v, %v), want (true, nil)", alive, err)
	}

	if werr := os.WriteFile(release, []byte("go"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		alive, err = e.AttemptProcessAlive(context.Background(), "r", "t", 3)
		if err == nil && !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("exited agent still reads (%v, %v), want (false, nil)", alive, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	waitNoFiles(t, pidDir)
}

// TestForgetPIDKeepsANewerRecord: the exit of an older agent must not erase the
// record of a newer agent for the same attempt (an infra re-place keeps the
// try number), or the newer one would read as dead while it runs.
func TestForgetPIDKeepsANewerRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process liveness by signal 0 is POSIX-only")
	}
	e := NewSubprocessExecutor("/bin/true", discardLogger())
	e.SetPIDDir(t.TempDir())
	if err := e.recordPID("r", "t", 1, os.Getpid()); err != nil {
		t.Fatalf("recordPID: %v", err)
	}
	e.forgetPID("r", "t", 1, os.Getpid()+1_000_000)
	if alive, _ := e.AttemptProcessAlive(context.Background(), "r", "t", 1); !alive {
		t.Error("forgetting a different PID must leave the current record in place")
	}
	e.forgetPID("r", "t", 1, os.Getpid())
	if alive, _ := e.AttemptProcessAlive(context.Background(), "r", "t", 1); alive {
		t.Error("forgetting the recorded PID must remove the record")
	}
}

// waitNoFiles polls until dir is empty: the record is removed by the goroutine
// that waits on the agent, which runs shortly after the process exits.
func waitNoFiles(t *testing.T, dir string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	entries, _ := os.ReadDir(dir)
	names := make([]string, 0, len(entries))
	for _, en := range entries {
		names = append(names, en.Name())
	}
	t.Fatalf("liveness records left behind after the agent exited: %v (%s)", names, strconv.Itoa(len(names)))
}
