//go:build linux

package agent

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// prctl options this file uses. Spelled out rather than pulled from
// golang.org/x/sys, which the module only carries indirectly.
const (
	prSetDumpable       = 4  // PR_SET_DUMPABLE
	prGetDumpable       = 3  // PR_GET_DUMPABLE
	prSetChildSubreaper = 36 // PR_SET_CHILD_SUBREAPER
)

// hardenWarmProcess prepares the warm agent to share a container with untrusted
// attempts (X3.3, X3.4).
//
// Non dumpable: the agent runs as the task's uid, and a dumpable process lets
// any same-uid process read /proc/<pid>/environ (the bootstrap token under the
// env-var transport) and /proc/<pid>/mem (the current attempt token). Clearing
// the flag makes those files require CAP_SYS_PTRACE, which the warm container
// drops. execve resets the flag, so attempts themselves are unaffected.
//
// Child subreaper: an attempt that detaches with setsid and lets its parent exit
// is reparented to the nearest subreaper instead of to PID 1. The agent is PID 1
// in the shipped image, but a wrapper entrypoint would break that, so it claims
// the role explicitly and every orphan of an attempt stays its descendant, where
// sweepDescendants can find it.
func hardenWarmProcess() error {
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_DUMPABLE, 0): %w", errno)
	}
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_CHILD_SUBREAPER, 1): %w", errno)
	}
	return nil
}

// sweepBudget bounds sweepDescendants. A healthy attempt leaves nothing and
// costs one /proc walk; a survivor needs one kill pass plus a few milliseconds
// for the corpse. A task that keeps forking faster than the passes can kill
// exhausts the budget and takes the worker down, which is the intended outcome.
const sweepBudget = 2 * time.Second

// sweepDescendants kills every descendant of the agent and returns only once a
// walk of /proc finds none alive (X3.3). The process-group kill in execRunner
// misses a descendant that called setsid; this does not, because it follows the
// parent links instead of the group. Processes that are not descendants (a
// kubectl exec session, the agent's ancestors) are left alone.
//
// It is fail-closed: an error means a process may still be running, and the
// caller must not hand this worker another attempt.
func sweepDescendants() error {
	self := os.Getpid()
	deadline := time.Now().Add(sweepBudget)
	for {
		alive, err := liveDescendants(self)
		if err != nil {
			return err
		}
		if len(alive) == 0 {
			reapChildren()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%d processes of a finished attempt survived the sweep: %v", len(alive), alive)
		}
		for _, pid := range alive {
			// ESRCH: it exited between the walk and the kill. EPERM (a setuid
			// binary) is caught by the next walk and the deadline.
			_ = syscall.Kill(pid, syscall.SIGKILL) //nolint:errcheck // the next walk is the check
		}
		reapChildren()
		time.Sleep(zombieDrainPoll)
	}
}

// reapChildren collects every exited child without blocking. Killed
// descendants reparent to the agent (subreaper), so their corpses are its to
// collect; attempts are sequential and the attempt's own Wait has returned, so
// Wait4(-1) cannot steal an exit status here.
func reapChildren() {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if pid <= 0 || err != nil {
			return
		}
	}
}

// liveDescendants walks /proc and returns every non-zombie process whose
// parent chain leads to root.
func liveDescendants(root int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("listing /proc: %w", err)
	}
	children := map[int][]int{}
	for _, e := range entries {
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil {
			continue
		}
		raw, readErr := os.ReadFile("/proc/" + e.Name() + "/stat") //nolint:gosec // fixed procfs path
		if readErr != nil {
			continue // exited during the walk
		}
		_, state, ppid, ok := parseProcStat(string(raw))
		if !ok || state == 'Z' || state == 'X' {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	var out []int
	queue := []int{root}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			out = append(out, c)
			queue = append(queue, c)
		}
	}
	return out, nil
}

// parseProcStat reads pid, state and parent pid from a /proc/<pid>/stat line.
// The command name sits in parentheses and is chosen by the task, so the fields
// after it are located from the LAST closing parenthesis.
func parseProcStat(line string) (pid int, state byte, ppid int, ok bool) {
	open := strings.IndexByte(line, '(')
	end := strings.LastIndexByte(line, ')')
	if open < 1 || end < open {
		return 0, 0, 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line[:open]))
	if err != nil {
		return 0, 0, 0, false
	}
	fields := strings.Fields(line[end+1:])
	if len(fields) < 2 || len(fields[0]) != 1 {
		return 0, 0, 0, false
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, 0, false
	}
	return pid, fields[0][0], ppid, true
}
