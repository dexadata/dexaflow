package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dexadata/dexaflow/internal/procgroup"
)

// defaultOrphanGrace and defaultOrphanKillWait bound how long StopOrphanedTask
// waits for an orphaned task group after SIGTERM and then after SIGKILL. Their
// sum stays well inside one maintenance phase budget (30 s), so a pass that
// meets an orphan ignoring SIGTERM still reaches SIGKILL; an orphan that a
// pass runs out of budget for is simply handled by the next pass.
const (
	defaultOrphanGrace    = 10 * time.Second
	defaultOrphanKillWait = 5 * time.Second
)

// defaultAgentPIDDir is where the subprocess executor records the PID of every
// agent it spawns when no directory is configured. The name carries the user id
// because the system temp directory is shared on Linux (/tmp when TMPDIR is
// unset): one fixed name would let the first user to run Lite own it and fail
// every dispatch of the next one, and would let another user who created it
// first delete or plant records. It is shared by every Lite server of one host
// user; records are keyed by attempt, so the worst a collision between two
// servers can do is defer a reap, never authorize one.
func defaultAgentPIDDir() string {
	return filepath.Join(os.TempDir(), "dexaflow-agent-pids-"+strconv.Itoa(os.Getuid()))
}

// checkPIDDir refuses a record directory another user could tamper with: it must
// be a real directory (not a symlink), owned by this user, and not writable by
// group or others (it is created 0700; reading it reveals nothing worth hiding). MkdirAll does not check any of that for a
// directory that already exists, and a directory someone else controls could
// have records removed (a live agent then reads dead and is reaped beside a
// second one, #911) or planted. A missing directory is reported as such so a
// reader can treat it as "no record".
func checkPIDDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("agent pid dir %s is not a directory", dir)
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("agent pid dir %s is writable by other users (mode %v)", dir, fi.Mode().Perm())
	}
	if !ownedByCurrentUser(fi) {
		return fmt.Errorf("agent pid dir %s is owned by another user", dir)
	}
	return nil
}

// SetPIDDir sets the directory the executor records each spawned agent's PID
// in, one file per (run, task, try) attempt. The record lives on disk, not in
// memory, because the agent is detached and outlives a control-plane restart:
// the restarted server must still be able to tell that an attempt's agent is
// alive. Empty restores the default under the system temp directory.
func (e *SubprocessExecutor) SetPIDDir(dir string) {
	if dir == "" {
		dir = defaultAgentPIDDir()
	}
	e.pidDir = dir
}

// pidPath is the record file of one attempt. The name is a digest of the
// attempt's identity: run ids embed timestamps with characters that are not
// filename-safe, and a digest cannot escape the directory.
func (e *SubprocessExecutor) pidPath(runID, taskID string, tryNumber int) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + taskID + "\x00" + strconv.Itoa(tryNumber)))
	return filepath.Join(e.pidDir, hex.EncodeToString(sum[:16])+".pid")
}

// ensurePIDDir creates the record directory (0700) when missing and refuses one
// another user could tamper with (see checkPIDDir).
func (e *SubprocessExecutor) ensurePIDDir() error {
	if err := os.MkdirAll(e.pidDir, 0o700); err != nil {
		return fmt.Errorf("creating agent pid dir: %w", err)
	}
	return checkPIDDir(e.pidDir)
}

// groupPath is the attempt's task process group record, written by the agent
// (procgroup.Record) next to the server's record of the agent itself.
func (e *SubprocessExecutor) groupPath(runID, taskID string, tryNumber int) string {
	return strings.TrimSuffix(e.pidPath(runID, taskID, tryNumber), ".pid") + ".pgid"
}

// recordPID writes the attempt's agent PID atomically (temp file + rename), so a
// reader never sees a partial record. A later spawn for the same attempt (an
// infra re-place keeps the try number) replaces the record.
func (e *SubprocessExecutor) recordPID(runID, taskID string, tryNumber, pid int) error {
	if err := e.ensurePIDDir(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(e.pidDir, ".pid-*")
	if err != nil {
		return fmt.Errorf("creating agent pid record: %w", err)
	}
	_, werr := tmp.WriteString(strconv.Itoa(pid))
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name()) //nolint:errcheck // best-effort cleanup of a failed temp record
		return fmt.Errorf("writing agent pid record: %w", errors.Join(werr, cerr))
	}
	if err := os.Rename(tmp.Name(), e.pidPath(runID, taskID, tryNumber)); err != nil {
		_ = os.Remove(tmp.Name()) //nolint:errcheck // best-effort cleanup of a failed temp record
		return fmt.Errorf("publishing agent pid record: %w", err)
	}
	return nil
}

// forgetPID removes the attempt's record once its agent has exited, but only if
// the record still names that agent: a newer agent for the same attempt keeps
// its record. Best-effort; a leftover record names a dead PID and reads gone.
// The agent's task group record goes with it unless the group is still alive
// (see forgetGroup).
func (e *SubprocessExecutor) forgetPID(runID, taskID string, tryNumber, pid int) {
	e.forgetGroup(runID, taskID, tryNumber, pid)
	path := e.pidPath(runID, taskID, tryNumber)
	recorded, err := readPID(path)
	if err != nil || recorded != pid {
		return
	}
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		e.logger.Warn("removing agent pid record", "path", path, "error", rerr)
	}
}

// forgetGroup removes the attempt's task group record once the group is gone,
// and only when the record was written by agent pid. A group still alive is an
// orphan of an agent killed outright: its record stays so the attempt keeps
// reading alive until the reaper stops the orphan or it exits.
func (e *SubprocessExecutor) forgetGroup(runID, taskID string, tryNumber, pid int) {
	path := e.groupPath(runID, taskID, tryNumber)
	rec, err := procgroup.Read(path)
	if err != nil || rec.AgentPID != pid {
		return
	}
	if alive, aerr := procgroup.GroupAlive(rec.PGID); aerr != nil || alive {
		return
	}
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		e.logger.Warn("removing task group record", "path", path, "error", rerr)
	}
}

// readPID parses one record file.
func readPID(path string) (int, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is a digest under the executor's own pid dir
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("malformed agent pid record %s", path)
	}
	return pid, nil
}

// AttemptProcessAlive reports whether the agent spawned for the (run, task,
// try) attempt is alive, by the PID this executor recorded when it spawned it,
// or, when that agent is gone, whether any process of the task group the agent
// recorded is still running (an agent killed outright leaves its task behind,
// #916). No record of either means nothing is known for the attempt: (false, nil). A record
// that cannot be read, or a PID whose liveness cannot be probed, is an error so
// the caller defers, and so is a record directory another user could have
// tampered with (see checkPIDDir). A recorded PID that the OS reuses for an
// unrelated process reads alive; that only defers a reap, it never authorizes
// one.
func (e *SubprocessExecutor) AttemptProcessAlive(_ context.Context, runID, taskID string, tryNumber int) (bool, error) {
	if err := checkPIDDir(e.pidDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	pid, err := readPID(e.pidPath(runID, taskID, tryNumber))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return false, err
	default:
		alive, perr := processAlive(pid)
		if perr != nil || alive {
			return alive, perr
		}
	}
	return e.taskGroupAlive(runID, taskID, tryNumber)
}

// taskGroupAlive reports whether any process of the attempt's recorded task
// group exists (kill(-pgid, 0); EPERM counts as alive). No record means no task
// group is known: (false, nil).
func (e *SubprocessExecutor) taskGroupAlive(runID, taskID string, tryNumber int) (bool, error) {
	rec, err := procgroup.Read(e.groupPath(runID, taskID, tryNumber))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return procgroup.GroupAlive(rec.PGID)
}

// StopOrphanedTask stops the attempt's task process group when it is an orphan
// of a dead agent, so the attempt can be reaped and placed again without a
// second copy of the task running beside the first (#916). It acts only on a
// group recorded in this server's own record directory, only when the agent
// that recorded it is dead (signal 0 answers ESRCH), and only when the group's
// leader is still the process that was recorded (procgroup.Stop checks its
// start time). It sends SIGTERM, then SIGKILL after the grace, and reports true
// once the group is gone. Any other case (no record, agent alive or unknown,
// leader unverifiable, group still alive after SIGKILL) reports false and
// changes nothing, so the reaper keeps deferring.
func (e *SubprocessExecutor) StopOrphanedTask(ctx context.Context, runID, taskID string, tryNumber int) (bool, error) {
	if err := checkPIDDir(e.pidDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	path := e.groupPath(runID, taskID, tryNumber)
	rec, err := procgroup.Read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if agentAlive, aerr := processAlive(rec.AgentPID); aerr != nil || agentAlive {
		return false, aerr
	}
	stopped, err := procgroup.Stop(ctx, rec, e.orphanGrace, e.orphanKillWait)
	if err != nil || !stopped {
		return false, err
	}
	e.logger.Warn("stopped the orphaned task process group of a dead agent",
		"run", runID, "task", taskID, "try", tryNumber, "agent_pid", rec.AgentPID, "pgid", rec.PGID)
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		e.logger.Warn("removing task group record", "path", path, "error", rerr)
	}
	return true, nil
}
