package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/dexadata/dexaflow/internal/procgroup"
)

// execWaitDelay bounds how long Wait may spend on the two delays os/exec cannot
// otherwise escape: a child that has not exited after its context was canceled,
// and a child that has exited but left its I/O pipes open (#943).
//
// It is a BACKSTOP, not the mechanism. The process-group kill below is what
// normally ends both, and it leaves nothing holding the pipe, so this timer
// fires only for a descendant that escaped the group — one that called setsid,
// or was handed to a system service manager. Without it that descendant would
// hold the wait open for as long as it lives, and the agent's clock would stop
// being the authority on execution_timeout.
//
// The size is set by the pod deadline's shutdown tail: the kubelet must not
// preempt the agent between its own clock firing and its report landing. That
// budget is a pod's termination grace, which defaults to 30 seconds
// (corev1.DefaultTerminationGracePeriodSeconds, the value
// executor.podDeadlineGraceTerm falls back to), so ten seconds here leaves
// twenty for the durable outcome record and one report RPC. It is also far more
// than a healthy drain needs: once every writer is dead the pipe reaches EOF at
// once and only what is already buffered (a pipe's worth, 64 KiB) is left to
// copy. A task whose DAG declares a grace SHORTER than this delay can still be
// preempted by the kubelet in the escaped-descendant case; that corner predates
// this bound and is not made worse by it.
const execWaitDelay = 10 * time.Second

// execRunner is the production CommandRunner: it spawns the user task as a child
// process bound to the supplied context.
//
// waitDelay is a field rather than the constant so a test can bound a wait
// without spending the production delay; every caller outside tests goes through
// NewExecRunner.
//
// groupRecord, when set, is where the runner records the task's process group
// right after starting it (see NewExecRunnerRecordingGroup).
type execRunner struct {
	waitDelay   time.Duration
	groupRecord string
}

// NewExecRunner returns a CommandRunner that executes tasks as child processes.
func NewExecRunner() CommandRunner { return execRunner{waitDelay: execWaitDelay} }

// NewExecRunnerRecordingGroup is NewExecRunner that also records each task's
// process group at path (a procgroup.Record: this agent's pid, the group id and
// the group leader's start time) as soon as the task starts. Lite hands the
// path to its agent: the task leads its own group, so an agent killed outright
// leaves the task running, and only this record lets the server see that
// orphan and stop it before the attempt is placed again (#916). A task whose
// group cannot be recorded is stopped and the run fails, so no task runs that
// the server could not find. An empty path records nothing.
func NewExecRunnerRecordingGroup(path string) CommandRunner {
	return execRunner{waitDelay: execWaitDelay, groupRecord: path}
}

// Run executes argv with env, streaming output to stdout and stderr. A non-zero
// process exit is returned as the exit code with a nil error; only failure to
// start or wait on the process yields an error.
//
// The child leads its OWN process group and the context's cancel kills that
// group, not just the child (#943). This is what makes execution_timeout apply
// to a task rather than to its first process. Two properties combine to defeat
// the default otherwise: the default cancel signals the direct child alone, and
// stdout here is a log writer rather than an *os.File, so the standard library
// interposes a pipe whose copying goroutine Wait blocks on until EVERY writer
// closes it. A surviving grandchild holds that pipe — and the bash operator runs
// its entrypoint under `bash -c`, so any compound command makes the user's real
// programs grandchildren — so the deadline would fire, the child would die, and
// the task would keep running with the agent still blocked in Wait.
//
// Unix-only, deliberately without a build-tagged Windows twin: the agent is
// built for linux and darwin only (.goreleaser.yaml) and runs inside a Linux
// task container.
func (r execRunner) Run(ctx context.Context, argv, env []string, stdout, stderr io.Writer) (int, error) {
	if len(argv) == 0 {
		return -1, errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is derived from the validated task spec
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Same signal the default cancel sends, widened to the group: an immediate
	// SIGKILL. cmd.Process is always set here — Start installs the watcher that
	// calls this only after the process exists — but the process may already
	// have been REAPED: os/exec's watcher races its result handoff against the
	// context, so Wait can collect the child and this still run afterwards. That
	// is wanted rather than guarded against, because a process group outlives
	// its leader for exactly as long as a descendant is still in it, and that
	// descendant is what this call exists to kill. The residue is a pid reused
	// in the microseconds between the reap and this call, which would address
	// some other group; closing it would need a pidfd, and it costs a pid-space
	// wraparound inside that window to happen at all.
	cmd.Cancel = func() error { return killProcessGroup(cmd.Process) }
	cmd.WaitDelay = r.waitDelay

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("running command: %w", err)
	}
	if rerr := r.recordGroup(cmd.Process.Pid); rerr != nil {
		_ = killProcessGroup(cmd.Process) //nolint:errcheck // best-effort: Wait below collects the exit either way
		_ = cmd.Wait()                    //nolint:errcheck // the task was stopped on purpose; its exit status is noise
		reapProcessGroup(cmd.Process)
		return -1, fmt.Errorf("recording the task process group: %w", rerr)
	}
	err := cmd.Wait()

	// Reap whatever the task left behind, on EVERY path out of Run and not only
	// on cancellation.
	//
	// cmd.Cancel above is invoked by os/exec when the CONTEXT ends. A task that
	// simply exits leaves its process group untouched, so a descendant that
	// outlived its parent keeps running. Under pod-per-task that is invisible:
	// the agent exits, the container ends, the runtime reaps it.
	//
	// Under warm pools it is an ISOLATION BREAK, which is the reason this is not
	// optional. A WarmRunner serves attempt after attempt in one container
	// ("a failed TASK is a normal outcome and never ends serve()"), same PID
	// namespace and same uid, so a survivor from attempt N holds attempt N's
	// environment (its secrets, its AIRFLOW_CONN_* values, its attempt token)
	// and can read AND WRITE attempt N+1's scratch directory and /proc entries.
	// resetScratch wipes the filesystem between attempts (#728); nothing wiped
	// the processes (#1216). It also holds cgroup memory the next attempt was
	// sized for, but that is the smaller half.
	//
	// SIGKILL with no SIGTERM first is correct HERE and would not be elsewhere:
	// the task has already exited, so nothing is left to shut down gracefully,
	// and anything still alive is by definition outside the task contract. Do
	// not "improve" this into an escalation; it would add seconds per attempt to
	// a path that must be fast.
	//
	// This kills one process group. A descendant that called setsid() escapes
	// it, which is the same gap WaitDelay exists for above.
	if cmd.Process != nil {
		reapProcessGroup(cmd.Process)
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		// The process itself exited with a SUCCESSFUL status and the delay expired
		// with its pipes still held open by something it left behind; os/exec
		// reports the forced close this way. That is an agent-side I/O outcome,
		// not the task's verdict, so the task keeps the exit status it chose. The
		// cost is the tail of its output, which is why this is logged.
		slog.Warn("task output was truncated: the process exited but left its output pipe open",
			"wait_delay", r.waitDelay)
		if cmd.ProcessState != nil {
			return cmd.ProcessState.ExitCode(), nil
		}
		return 0, nil
	}
	if err != nil {
		return -1, fmt.Errorf("running command: %w", err)
	}
	return 0, nil
}

// recordGroup writes the task's process group record when the runner has a
// record path. The task leads its own group, so the group id is its pid. A
// leader start time that cannot be read is recorded as 0: the group still
// counts as alive for the server, it just can never be verified and so is
// never signaled by it.
func (r execRunner) recordGroup(pid int) error {
	if r.groupRecord == "" {
		return nil
	}
	start, err := procgroup.StartTime(pid)
	if err != nil {
		slog.Warn("could not read the task leader's start time; the server will wait for an orphaned task instead of stopping it",
			"pid", pid, "error", err)
		start = 0
	}
	return procgroup.Write(r.groupRecord, procgroup.Record{AgentPID: os.Getpid(), PGID: pid, LeaderStart: start})
}

// killProcessGroup SIGKILLs every process in the group led by p, falling back to
// p alone when no such group exists.
//
// A process group is named by its LEADER's pid, so kill(-pid) reaches nothing at
// all — ESRCH, harming no other group — when the process is not a leader. That
// is the case whenever the Setpgid above did not take effect, and answering it
// with "already done" would leave the child running with the cancel reporting
// success. Falling back to killing the process is what the default cancel does,
// so the worst case degrades to the old behavior rather than to no behavior.
// p.Kill reports an already-exited process as os.ErrProcessDone, which os/exec
// treats as nothing to do rather than as a failure of the run.
func killProcessGroup(p *os.Process) error {
	err := syscall.Kill(-p.Pid, syscall.SIGKILL)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("killing process group %d: %w", p.Pid, err)
	}
	return p.Kill()
}

// reapProcessGroup kills a finished task's survivors and then collects them.
//
// Two halves, and the second is not optional. The agent is PID 1 of the pod's
// PID namespace, so every orphaned descendant reparents to IT, and Go does not
// reap arbitrary children. Killing without waiting therefore CREATES unreaped
// zombies in a warm worker, one per survivor per attempt, each holding a pid
// slot for the worker's whole life: a leak introduced by the fix for a leak.
//
// Wait4(-1) is safe at this call site specifically, and must not be lifted out
// of it. It can steal an exit status from a concurrent os/exec, but attempts on
// a warm worker are strictly sequential and cmd.Wait has already returned by
// the time this runs. In a background goroutine it would be a bug.
func reapProcessGroup(p *os.Process) {
	// ESRCH and ErrProcessDone are the NORMAL answers, not failures: after
	// cmd.Run the child is already reaped, so kill(-pgid) finds no group and the
	// fallback finds no process. Warning on those meant warning on every clean
	// task while staying silent on the one case this exists to report, which is
	// a survivor, where the kill succeeds and returns nil.
	if err := killProcessGroup(p); err != nil &&
		!errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		slog.Warn("could not reap the task's process group; a survivor may reach the next attempt on a warm worker",
			"error", err)
	}
	// Drain what the kill just orphaned onto us.
	//
	// The obvious version of this does not work, and measurement is the only way
	// to find that out. SIGKILL delivery is asynchronous, so a single WNOHANG
	// pass immediately after the kill runs BEFORE the corpse exists: measured 0
	// collections out of 200 attempts on both macOS and Linux. The corpse
	// becomes reapable after a few hundred microseconds to a few milliseconds
	// (worst observed 35 ms on Linux).
	//
	// So pid 0 is polled through rather than returned on. It is the correct
	// reading of the POSIX return value, "children exist, none have exited yet",
	// and returning on it treats "not yet" as "done", which is what made the
	// first version collect nothing while claiming otherwise.
	//
	// Bounded twice, by iterations and by a deadline, because an unbounded reap
	// hands a pathological task the ability to stall the worker between
	// attempts. WNOHANG throughout for the same reason: a descendant stopped in
	// D state or under ptrace would block a plain wait forever.
	deadline := time.Now().Add(zombieDrainBudget)
	for i := 0; i < maxZombieDrain; i++ {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		switch {
		case pid > 0:
			continue // collected one, there may be more
		case errors.Is(err, syscall.EINTR):
			continue // interrupted, not finished: retrying is the whole point
		case err != nil:
			return // ECHILD and anything else: nothing left to collect
		}
		// pid == 0: alive but not yet exited. Wait a little unless the budget is
		// spent, in which case leaving a zombie is the lesser cost.
		if time.Now().After(deadline) {
			slog.Warn("gave up collecting the task's leftover processes; some may remain as zombies on this worker",
				"budget", zombieDrainBudget)
			return
		}
		time.Sleep(zombieDrainPoll)
	}
	slog.Warn("stopped reaping the task's leftover processes at the iteration cap; some may remain as zombies on this worker",
		"cap", maxZombieDrain)
}

// maxZombieDrain bounds the reap so one task cannot stall the worker between
// attempts. Far above any plausible descendant count for a single task, and
// hitting it is itself worth a line in the log.
const maxZombieDrain = 1024

// zombieDrainBudget is how long the drain will wait for corpses that the kernel
// has not produced yet. Measured worst case for one corpse to become reapable
// was 35 ms on Linux; this leaves an order of magnitude of headroom and still
// bounds the pause between attempts on a warm worker.
const zombieDrainBudget = 300 * time.Millisecond

// zombieDrainPoll is the gap between passes. Short enough that the common case
// (hundreds of microseconds) costs one sleep, long enough not to spin.
const zombieDrainPoll = 2 * time.Millisecond
