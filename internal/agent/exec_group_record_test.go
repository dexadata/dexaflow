//go:build linux || darwin

package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/procgroup"
)

// TestExecRunnerRecordsTheTaskGroup: with a record path, the runner writes the
// task's process group, the agent's own pid and the group leader's start time,
// so a server that outlives this agent can still find (and verify) the task.
func TestExecRunnerRecordsTheTaskGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.pgid")
	var out bytes.Buffer
	code, err := NewExecRunnerRecordingGroup(path).Run(context.Background(), []string{"/bin/sh", "-c", "exit 0"}, os.Environ(), &out, &out)
	if err != nil || code != 0 {
		t.Fatalf("Run = (%d, %v), want (0, nil)", code, err)
	}
	rec, err := procgroup.Read(path)
	if err != nil {
		t.Fatalf("reading the group record: %v", err)
	}
	if rec.AgentPID != os.Getpid() || rec.PGID <= 0 || rec.LeaderStart == 0 {
		t.Fatalf("record = %+v, want this agent's pid, a pgid and a leader start time", rec)
	}
}

// TestExecRunnerStopsATaskItCannotRecord: a task whose group the server could
// not find after this agent dies must not run at all, the same rule the server
// applies to an agent whose pid it cannot record.
func TestExecRunnerStopsATaskItCannotRecord(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	path := filepath.Join(dir, "missing", "a.pgid")
	var out bytes.Buffer
	_, err := NewExecRunnerRecordingGroup(path).Run(context.Background(), []string{"/bin/sh", "-c", "sleep 0.5; touch " + marker}, os.Environ(), &out, &out)
	if err == nil {
		t.Fatal("Run must fail when the task group cannot be recorded")
	}
	time.Sleep(time.Second)
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("the unrecorded task must have been stopped before it finished")
	}
}

// TestHelperAgentRunsATask is not a test on its own: it is the agent process
// TestKilledAgentLeavesAStoppableOrphan starts and kills.
func TestHelperAgentRunsATask(t *testing.T) {
	path := os.Getenv("DEXAFLOW_TEST_HELPER_GROUP_RECORD")
	if path == "" {
		t.Skip("helper process only")
	}
	var out bytes.Buffer
	_, _ = NewExecRunnerRecordingGroup(path).Run(context.Background(), []string{"sleep", "60"}, os.Environ(), &out, &out) //nolint:errcheck // killed by the parent test
}

// TestKilledAgentLeavesAStoppableOrphan reproduces the Lite duplicate-execution
// gap end to end: an agent killed with SIGKILL leaves its task running in the
// task's own process group. The group record names that group, the group reads
// alive, and Stop ends it, which is what lets the reaper re-place the attempt
// without a second copy running.
func TestKilledAgentLeavesAStoppableOrphan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.pgid")
	helper := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperAgentRunsATask$") //nolint:gosec // re-executes this test binary
	helper.Env = append(os.Environ(), "DEXAFLOW_TEST_HELPER_GROUP_RECORD="+path)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	var rec procgroup.Record
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := procgroup.Read(path)
		if err == nil {
			rec = r
			break
		}
		if time.Now().After(deadline) {
			_ = helper.Process.Kill() //nolint:errcheck // test cleanup
			t.Fatalf("the agent never recorded its task group: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(-rec.PGID, syscall.SIGKILL) }) //nolint:errcheck // best-effort cleanup
	if rec.AgentPID != helper.Process.Pid {
		t.Fatalf("record names agent %d, want %d", rec.AgentPID, helper.Process.Pid)
	}

	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait() //nolint:errcheck // killed on purpose

	alive, err := procgroup.GroupAlive(rec.PGID)
	if err != nil || !alive {
		t.Fatalf("the orphaned task group must still be running: (%v, %v)", alive, err)
	}
	stopped, err := procgroup.Stop(context.Background(), rec, time.Second, 5*time.Second)
	if err != nil || !stopped {
		t.Fatalf("Stop = (%v, %v), want (true, nil)", stopped, err)
	}
	if alive, _ := procgroup.GroupAlive(rec.PGID); alive {
		t.Fatal("the orphaned task group must be gone after Stop")
	}
}
