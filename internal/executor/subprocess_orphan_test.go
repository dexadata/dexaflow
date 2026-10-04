//go:build linux || darwin

package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/procgroup"
)

// startTaskGroup starts a long sleep as the leader of its own process group,
// the way the agent starts a user task, and returns its pgid.
func startTaskGroup(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting task group: %v", err)
	}
	go func() { _ = cmd.Wait() }()                                            //nolint:errcheck // exit status is irrelevant here
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }) //nolint:errcheck // best-effort cleanup
	return cmd.Process.Pid
}

// deadPID returns the pid of a process that has exited and been collected.
func deadPID(t *testing.T) int {
	t.Helper()
	c := exec.CommandContext(context.Background(), "true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

// orphanRecord writes the group record an agent with pid agentPID would have
// written for a task group pgid, with the leader's real start time.
func orphanRecord(t *testing.T, e *SubprocessExecutor, agentPID, pgid int) procgroup.Record {
	t.Helper()
	start, err := procgroup.StartTime(pgid)
	if err != nil {
		t.Fatal(err)
	}
	rec := procgroup.Record{AgentPID: agentPID, PGID: pgid, LeaderStart: start}
	if err := os.MkdirAll(e.pidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := procgroup.Write(e.groupPath("r", "t", 1), rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func waitGroupGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if alive, err := procgroup.GroupAlive(pgid); err == nil && !alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task group %d still alive", pgid)
}

// TestAttemptReadsAliveWhileItsTaskGroupRuns: an agent killed outright leaves
// its task running in the task's own process group. The attempt must read alive
// while any process of that group exists, or the reaper re-places it and a
// second copy runs beside the first on the same try.
func TestAttemptReadsAliveWhileItsTaskGroupRuns(t *testing.T) {
	e := NewSubprocessExecutor("/bin/true", discardLogger())
	e.SetPIDDir(t.TempDir())
	agent := deadPID(t)
	if err := e.recordPID("r", "t", 1, agent); err != nil {
		t.Fatal(err)
	}
	pgid := startTaskGroup(t)
	orphanRecord(t, e, agent, pgid)

	alive, err := e.AttemptProcessAlive(context.Background(), "r", "t", 1)
	if err != nil || !alive {
		t.Fatalf("dead agent, live task group: AttemptProcessAlive = (%v, %v), want (true, nil)", alive, err)
	}
	// The server saw the agent exit and forgot its pid: the group still counts.
	e.forgetPID("r", "t", 1, agent)
	if alive, err = e.AttemptProcessAlive(context.Background(), "r", "t", 1); err != nil || !alive {
		t.Fatalf("forgotten agent, live task group: AttemptProcessAlive = (%v, %v), want (true, nil)", alive, err)
	}

	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitGroupGone(t, pgid)
	if alive, err = e.AttemptProcessAlive(context.Background(), "r", "t", 1); err != nil || alive {
		t.Fatalf("dead agent, dead task group: AttemptProcessAlive = (%v, %v), want (false, nil)", alive, err)
	}
	// Once the group is gone, forgetting the agent clears its group record too.
	e.forgetPID("r", "t", 1, agent)
	if _, err := os.Stat(e.groupPath("r", "t", 1)); !os.IsNotExist(err) {
		t.Errorf("a dead group's record must be removed with its agent's: stat err=%v", err)
	}
}

// TestStopOrphanedTaskStopsOnlyAVerifiedOrphan: the reaper may stop a task
// group only when the agent that recorded it is dead and the group is still the
// one it recorded. A live agent's group, or a group whose leader no longer
// matches the record, is never signaled.
func TestStopOrphanedTaskStopsOnlyAVerifiedOrphan(t *testing.T) {
	e := NewSubprocessExecutor("/bin/true", discardLogger())
	e.SetPIDDir(t.TempDir())
	e.orphanGrace, e.orphanKillWait = 300*time.Millisecond, 5*time.Second
	ctx := context.Background()

	if stopped, err := e.StopOrphanedTask(ctx, "r", "t", 1); err != nil || stopped {
		t.Fatalf("no record: StopOrphanedTask = (%v, %v), want (false, nil)", stopped, err)
	}

	pgid := startTaskGroup(t)
	rec := orphanRecord(t, e, os.Getpid(), pgid)
	if stopped, err := e.StopOrphanedTask(ctx, "r", "t", 1); err != nil || stopped {
		t.Fatalf("live agent: StopOrphanedTask = (%v, %v), want (false, nil)", stopped, err)
	}

	rec.AgentPID = deadPID(t)
	rec.LeaderStart++
	if err := procgroup.Write(e.groupPath("r", "t", 1), rec); err != nil {
		t.Fatal(err)
	}
	if stopped, err := e.StopOrphanedTask(ctx, "r", "t", 1); err != nil || stopped {
		t.Fatalf("unverified group: StopOrphanedTask = (%v, %v), want (false, nil)", stopped, err)
	}
	if alive, _ := procgroup.GroupAlive(pgid); !alive {
		t.Fatal("a group that was not stopped must still be running")
	}

	rec.LeaderStart--
	if err := procgroup.Write(e.groupPath("r", "t", 1), rec); err != nil {
		t.Fatal(err)
	}
	stopped, err := e.StopOrphanedTask(ctx, "r", "t", 1)
	if err != nil || !stopped {
		t.Fatalf("verified orphan: StopOrphanedTask = (%v, %v), want (true, nil)", stopped, err)
	}
	waitGroupGone(t, pgid)
	if alive, err := e.AttemptProcessAlive(ctx, "r", "t", 1); err != nil || alive {
		t.Fatalf("after the stop: AttemptProcessAlive = (%v, %v), want (false, nil)", alive, err)
	}
}

// TestExecuteHandsTheAgentItsGroupRecord: the agent learns where to record its
// task's process group from its environment, and the record directory exists
// before the agent starts (the agent may write before the server records the
// agent's own pid).
func TestExecuteHandsTheAgentItsGroupRecord(t *testing.T) {
	work := t.TempDir()
	out := filepath.Join(work, "seen")
	e := NewSubprocessExecutor(writeScript(t,
		`if [ -d "$(dirname "$LEOFLOW_TASK_PGID_FILE")" ]; then d=yes; else d=no; fi; printf '%s %s' "$LEOFLOW_TASK_PGID_FILE" "$d" > `+out), discardLogger())
	e.SetWorkDir(work)
	e.SetPIDDir(filepath.Join(t.TempDir(), "pids"))

	if _, err := e.Execute(context.Background(), Request{RunID: "r", TaskID: "t", TryNumber: 1, TaskInstanceID: "ti"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := strings.TrimSpace(string(waitForFile(t, out)))
	if want := e.groupPath("r", "t", 1) + " yes"; got != want {
		t.Fatalf("agent saw %q, want %q", got, want)
	}
}
