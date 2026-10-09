//go:build linux || darwin

package procgroup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// startGroup starts a shell script as the leader of its own process group, the
// way the agent starts a task, and returns its pid (which is the pgid). The
// group is killed at cleanup if the test left it running.
func startGroup(t *testing.T, script string) int {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting group: %v", err)
	}
	pgid := cmd.Process.Pid
	// Collect the leader in the background so it never lingers as a zombie of
	// the test process, which would keep the group readable as alive.
	go func() { _ = cmd.Wait() }()                                 //nolint:errcheck // exit status is irrelevant here
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) }) //nolint:errcheck // best-effort cleanup
	return pgid
}

// waitGone polls until the group has no process left.
func waitGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if alive, err := GroupAlive(pgid); err == nil && !alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("group %d still alive", pgid)
}

// TestRecordRoundTrip: the record the agent writes is the record the server
// reads, and a malformed one is an error rather than a zero group (a zero pgid
// would address the caller's own group).
func TestRecordRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.pgid")
	want := Record{AgentPID: 101, PGID: 202, LeaderStart: 303}
	if err := Write(path, want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(path)
	if err != nil || got != want {
		t.Fatalf("Read = (%+v, %v), want (%+v, nil)", got, err, want)
	}
	for _, bad := range []string{"", "1 2", "1 0 3", "0 2 3", "a b c", "1 -2 3"} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(path); err == nil {
			t.Errorf("Read(%q) must fail", bad)
		}
	}
}

// TestGroupAliveFollowsTheWholeGroup: the group reads alive while ANY member
// exists, including after its leader exited, which is exactly the orphan an
// agent killed outright leaves behind.
func TestGroupAliveFollowsTheWholeGroup(t *testing.T) {
	pgid := startGroup(t, "sleep 30 & exit 0")
	time.Sleep(200 * time.Millisecond) // let the leader exit, leaving the sleep
	alive, err := GroupAlive(pgid)
	if err != nil || !alive {
		t.Fatalf("GroupAlive with a surviving member = (%v, %v), want (true, nil)", alive, err)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pgid)
}

// TestStartTimeIdentifiesAProcess: the start time is stable for one process
// and unavailable for a pid with no process, so a reused pid can be told apart
// from the leader that was recorded.
func TestStartTimeIdentifiesAProcess(t *testing.T) {
	a, err := StartTime(os.Getpid())
	if err != nil || a == 0 {
		t.Fatalf("StartTime(self) = (%d, %v), want non-zero", a, err)
	}
	b, err := StartTime(os.Getpid())
	if err != nil || a != b {
		t.Fatalf("StartTime(self) changed: %d then %d (%v)", a, b, err)
	}
	exited := exec.CommandContext(context.Background(), "true")
	if err := exited.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := StartTime(exited.Process.Pid); err == nil {
		t.Error("StartTime of a reaped process must fail")
	}
}

// TestStopEscalatesToSIGKILL: Stop asks the group to terminate, then kills it
// when a member ignores SIGTERM, and reports the group gone.
func TestStopEscalatesToSIGKILL(t *testing.T) {
	pgid := startGroup(t, "trap '' TERM; sleep 30 & wait")
	time.Sleep(200 * time.Millisecond)
	start, err := StartTime(pgid)
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := Stop(context.Background(), Record{AgentPID: 1, PGID: pgid, LeaderStart: start}, 300*time.Millisecond, 5*time.Second)
	if err != nil || !stopped {
		t.Fatalf("Stop = (%v, %v), want (true, nil)", stopped, err)
	}
	waitGone(t, pgid)
}

// TestStopRefusesAnUnverifiedGroup: a group whose leader's start time does not
// match the record (the pgid now names another process), or whose leader is
// gone so the match cannot be checked, is never signaled.
func TestStopRefusesAnUnverifiedGroup(t *testing.T) {
	pgid := startGroup(t, "sleep 30")
	time.Sleep(100 * time.Millisecond)
	start, err := StartTime(pgid)
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range []Record{
		{AgentPID: 1, PGID: pgid, LeaderStart: start + 1},
		{AgentPID: 1, PGID: pgid, LeaderStart: 0},
	} {
		stopped, serr := Stop(context.Background(), rec, 100*time.Millisecond, time.Second)
		if serr != nil || stopped {
			t.Errorf("Stop(%+v) = (%v, %v), want (false, nil)", rec, stopped, serr)
		}
	}
	if alive, _ := GroupAlive(pgid); !alive {
		t.Fatal("an unverified group must be left running")
	}

	orphan := startGroup(t, "sleep 30 & exit 0")
	time.Sleep(200 * time.Millisecond) // the leader is gone, only the sleep is left
	stopped, err := Stop(context.Background(), Record{AgentPID: 1, PGID: orphan, LeaderStart: 1}, 100*time.Millisecond, time.Second)
	if err != nil || stopped {
		t.Errorf("Stop with the leader gone = (%v, %v), want (false, nil)", stopped, err)
	}
	if alive, _ := GroupAlive(orphan); !alive {
		t.Fatal("a group whose leader cannot be verified must be left running")
	}
}

// TestStopOfAGoneGroupIsDone: nothing left to stop is a success.
func TestStopOfAGoneGroupIsDone(t *testing.T) {
	pgid := startGroup(t, "exit 0")
	waitGone(t, pgid)
	stopped, err := Stop(context.Background(), Record{AgentPID: 1, PGID: pgid, LeaderStart: 1}, 100*time.Millisecond, time.Second)
	if err != nil || !stopped {
		t.Fatalf("Stop of a gone group = (%v, %v), want (true, nil)", stopped, err)
	}
}

// TestAliveIgnoresAReusedGroupID: a group id whose leader pid now belongs to a
// process with another start time is an unrelated group, so the recorded group
// reads gone; the recorded leader, or a record without a start time, reads alive.
func TestAliveIgnoresAReusedGroupID(t *testing.T) {
	pgid := startGroup(t, "sleep 30")
	time.Sleep(100 * time.Millisecond)
	start, err := StartTime(pgid)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		rec  Record
		want bool
	}{
		{Record{AgentPID: 1, PGID: pgid, LeaderStart: start}, true},
		{Record{AgentPID: 1, PGID: pgid, LeaderStart: 0}, true},
		{Record{AgentPID: 1, PGID: pgid, LeaderStart: start + 1}, false},
	} {
		got, aerr := Alive(tc.rec)
		if aerr != nil || got != tc.want {
			t.Errorf("Alive(%+v) = (%v, %v), want (%v, nil)", tc.rec, got, aerr, tc.want)
		}
	}
}
