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

// recordPID writes the attempt's agent PID atomically (temp file + rename), so a
// reader never sees a partial record. A later spawn for the same attempt (an
// infra re-place keeps the try number) replaces the record.
func (e *SubprocessExecutor) recordPID(runID, taskID string, tryNumber, pid int) error {
	if err := os.MkdirAll(e.pidDir, 0o700); err != nil {
		return fmt.Errorf("creating agent pid dir: %w", err)
	}
	if err := checkPIDDir(e.pidDir); err != nil {
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
func (e *SubprocessExecutor) forgetPID(runID, taskID string, tryNumber, pid int) {
	path := e.pidPath(runID, taskID, tryNumber)
	recorded, err := readPID(path)
	if err != nil || recorded != pid {
		return
	}
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		e.logger.Warn("removing agent pid record", "path", path, "error", rerr)
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
// try) attempt is alive, by the PID this executor recorded when it spawned it.
// No record means no agent is known for the attempt: (false, nil). A record
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
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return processAlive(pid)
}
