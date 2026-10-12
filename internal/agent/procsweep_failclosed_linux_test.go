//go:build linux

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"golang.org/x/sys/unix"
)

// fakeKernel is a procKernel whose calls fail on demand and never touch a real
// process, so the error branches of hardening and the sweep can be driven
// without privileges (#1611).
type fakeKernel struct {
	prctlErr  map[uintptr]error // by prctl option
	openErr   error
	fdKillErr error
	killErr   error

	prctls  [][2]uintptr
	opens   int
	fdKills int
	closes  int
	kills   int
}

func (k *fakeKernel) prctl(option, arg uintptr) error {
	k.prctls = append(k.prctls, [2]uintptr{option, arg})
	return k.prctlErr[option]
}

func (k *fakeKernel) pidfdOpen(int) (int, error) {
	k.opens++
	if k.openErr != nil {
		return -1, k.openErr
	}
	return 1000, nil
}

func (k *fakeKernel) pidfdKill(int) error { k.fdKills++; return k.fdKillErr }
func (k *fakeKernel) closeFD(int)         { k.closes++ }
func (k *fakeKernel) kill(int) error      { k.kills++; return k.killErr }

// TestHardenWithSetsBothFlags: hardening asks for non dumpable, then child
// subreaper, and nothing else.
func TestHardenWithSetsBothFlags(t *testing.T) {
	k := &fakeKernel{}
	if err := hardenWith(k); err != nil {
		t.Fatalf("hardenWith: %v", err)
	}
	want := [][2]uintptr{{prSetDumpable, 0}, {prSetChildSubreaper, 1}}
	if fmt.Sprint(k.prctls) != fmt.Sprint(want) {
		t.Errorf("prctl calls = %v, want %v", k.prctls, want)
	}
}

// TestHardenWithFailsClosed: either prctl failing is an error that names the
// call, and a failed PR_SET_DUMPABLE stops before the subreaper call.
func TestHardenWithFailsClosed(t *testing.T) {
	cases := []struct {
		name      string
		failing   uintptr
		wantCall  string
		wantCalls int
	}{
		{"dumpable", prSetDumpable, "PR_SET_DUMPABLE", 1},
		{"subreaper", prSetChildSubreaper, "PR_SET_CHILD_SUBREAPER", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := &fakeKernel{prctlErr: map[uintptr]error{c.failing: syscall.EPERM}}
			err := hardenWith(k)
			if !errors.Is(err, syscall.EPERM) || !strings.Contains(err.Error(), c.wantCall) {
				t.Fatalf("hardenWith error = %v, want EPERM naming %s", err, c.wantCall)
			}
			if len(k.prctls) != c.wantCalls {
				t.Errorf("prctl calls = %v, want %d", k.prctls, c.wantCalls)
			}
		})
	}
}

// selfRef is a procRef for the test process itself, so sameProcess holds and the
// fake kernel is reached; the fake never signals it.
func selfRef(t *testing.T) procRef {
	t.Helper()
	pid := os.Getpid()
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") //nolint:gosec // fixed procfs path
	if err != nil {
		t.Fatalf("reading own stat: %v", err)
	}
	start, ok := procStartTime(string(raw))
	if !ok {
		t.Fatalf("no start time in %q", raw)
	}
	return procRef{pid: pid, start: start}
}

// TestKillProcWithErrorBranches covers how killProc treats each kernel answer:
// a process that already exited (ESRCH) is not an error, a kernel without pidfds
// (ENOSYS) falls back to kill(2), and any other failure is returned with the call
// that failed, without falling back, so the sweep's next walk sees the survivor.
func TestKillProcWithErrorBranches(t *testing.T) {
	cases := []struct {
		name     string
		usePidfd bool
		k        fakeKernel
		wantErr  string // substring; empty means no error
		wantIs   error
		fdKills  int
		kills    int
	}{
		{name: "pidfd ok", usePidfd: true, fdKills: 1},
		{name: "pidfd_open ESRCH", usePidfd: true, k: fakeKernel{openErr: unix.ESRCH}},
		{name: "pidfd_open EMFILE", usePidfd: true, k: fakeKernel{openErr: unix.EMFILE}, wantErr: "pidfd_open(", wantIs: unix.EMFILE},
		{name: "pidfd_open ENOSYS falls back to kill", usePidfd: true, k: fakeKernel{openErr: unix.ENOSYS}, kills: 1},
		{name: "pidfd_open ENOSYS then kill EPERM", usePidfd: true, k: fakeKernel{openErr: unix.ENOSYS, killErr: syscall.EPERM}, wantErr: "kill(", wantIs: syscall.EPERM, kills: 1},
		{name: "pidfd_send_signal ESRCH", usePidfd: true, k: fakeKernel{fdKillErr: unix.ESRCH}, fdKills: 1},
		{name: "pidfd_send_signal EPERM", usePidfd: true, k: fakeKernel{fdKillErr: unix.EPERM}, wantErr: "pidfd_send_signal(", wantIs: unix.EPERM, fdKills: 1},
		{name: "kill ok", kills: 1},
		{name: "kill ESRCH", k: fakeKernel{killErr: syscall.ESRCH}, kills: 1},
		{name: "kill EPERM", k: fakeKernel{killErr: syscall.EPERM}, wantErr: "kill(", wantIs: syscall.EPERM, kills: 1},
	}
	ref := selfRef(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k := c.k
			err := killProcWith(&k, ref, c.usePidfd)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("killProcWith: %v, want nil", err)
			case c.wantErr != "" && (!errors.Is(err, c.wantIs) || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("killProcWith error = %v, want %v naming %s", err, c.wantIs, c.wantErr)
			}
			if k.fdKills != c.fdKills || k.kills != c.kills {
				t.Errorf("pidfd kills = %d, kills = %d, want %d and %d", k.fdKills, k.kills, c.fdKills, c.kills)
			}
			if k.opens != k.closes+boolInt(k.openErr != nil) {
				t.Errorf("pidfds opened = %d, closed = %d: an opened pidfd leaked", k.opens, k.closes)
			}
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// startChild starts a long sleep as a child of the test process, so it is a
// descendant the sweep finds, and kills it when the test ends.
func startChild(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sleep", "300")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sleep: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // may fail with ECHILD once a sweep reaped it
	})
	return pid
}

// TestSweepWithFailsWhenADescendantSurvives: a descendant the kernel will not
// kill (a setuid binary answering EPERM) is still alive when the budget runs
// out, and the sweep reports it instead of returning success.
func TestSweepWithFailsWhenADescendantSurvives(t *testing.T) {
	pid := startChild(t)
	k := &fakeKernel{fdKillErr: unix.EPERM}

	err := sweepWith(k, 100*time.Millisecond)

	if err == nil || !strings.Contains(err.Error(), "survived the sweep") || !strings.Contains(err.Error(), strconv.Itoa(pid)) {
		t.Fatalf("sweepWith error = %v, want a survivor error naming pid %d", err, pid)
	}
	if k.fdKills == 0 {
		t.Error("the sweep never tried to kill the survivor")
	}
	if !processAlive(pid) {
		t.Error("the fake kernel killed the child; the test no longer exercises a survivor")
	}
}

// TestSweepWithLiveKernelKillsADescendant is the control for the survivor test:
// the same child under the real kernel is killed and the sweep succeeds.
func TestSweepWithLiveKernelKillsADescendant(t *testing.T) {
	pid := startChild(t)

	if err := sweepWith(liveKernel{}, sweepBudget); err != nil {
		t.Fatalf("sweepWith: %v", err)
	}
	if processAlive(pid) {
		t.Errorf("child %d survived the sweep", pid)
	}
}

// TestWarmWorkerEndsWhenAnEscapedProcessSurvives drives the real Run with an
// attempt that escapes its process group with setsid and a kernel that cannot
// kill it. The worker must end with the survivor error before SlotFree, and the
// second assignment must never run beside the survivor.
func TestWarmWorkerEndsWhenAnEscapedProcessSurvives(t *testing.T) {
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
	k := &fakeKernel{fdKillErr: unix.EPERM}
	w := &WarmRunner{
		StreamClient:  client,
		WorkClient:    client,
		AttemptTokens: tokens,
		Cmd:           cmd,
		Env:           []string{"PATH=" + os.Getenv("PATH")},
		ScratchDir:    filepath.Join(t.TempDir(), "scratch"),
		// harden stays the real one: the setsid orphan is only a descendant
		// the sweep can find because the agent is the child subreaper.
		sweepProcs: func() error { return sweepWith(k, 100*time.Millisecond) },
	}

	err := w.Run(context.Background(), "dagver-1")

	if err == nil || !strings.Contains(err.Error(), `sweeping processes after assignment "asg-1"`) ||
		!strings.Contains(err.Error(), "survived the sweep") {
		t.Fatalf("Run error = %v, want the survivor error for asg-1", err)
	}
	if cmd.runs != 1 {
		t.Errorf("attempts run = %d, want 1: the second assignment ran beside a survivor", cmd.runs)
	}
	if _, slotFrees := sentKinds(stream.sent); slotFrees != 0 {
		t.Errorf("SlotFree sent = %d, want 0", slotFrees)
	}
	pid, perr := readPidFile(pidFile)
	if perr != nil {
		t.Fatalf("the attempt never escaped: %v", perr)
	}
	if !processAlive(pid) {
		t.Error("the escaped process is gone; the test no longer exercises a survivor")
	}
}
