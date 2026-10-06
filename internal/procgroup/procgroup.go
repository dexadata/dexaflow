// Package procgroup records and probes the process group a Lite agent runs its
// user task in.
//
// The agent starts each task as the leader of its own process group so it can
// stop the whole task, not just its first process. The same property lets the
// task outlive an agent that is killed outright: the group is reparented and
// keeps running. The server only knows the agent's pid, and the task's group id
// is the pid the kernel gave the task at the agent's fork, which the server
// cannot derive once the agent is gone. So the agent writes a Record next to
// the server's per-attempt pid record, and the server reads it to tell an
// orphaned task from a finished one, and to stop that orphan before the
// attempt is placed again.
package procgroup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Record names one task process group and the agent that started it.
type Record struct {
	// AgentPID is the pid of the agent that started the task.
	AgentPID int
	// PGID is the task's process group id, which is the pid of its leader.
	PGID int
	// LeaderStart is the start time of the group leader as StartTime reports
	// it, or 0 when it could not be read. A group is only ever signaled when its
	// leader still has this start time, so a group id the OS has since handed
	// to an unrelated process is never killed.
	LeaderStart uint64
}

// Write stores r at path atomically (a temp file in the same directory, then a
// rename), so a reader never sees a partial record. The file is 0600.
func Write(path string, r Record) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pgid-*")
	if err != nil {
		return fmt.Errorf("creating task group record: %w", err)
	}
	_, werr := fmt.Fprintf(tmp, "%d %d %d", r.AgentPID, r.PGID, r.LeaderStart)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name()) //nolint:errcheck // best-effort cleanup of a failed temp record
		return fmt.Errorf("writing task group record: %w", errors.Join(werr, cerr))
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name()) //nolint:errcheck // best-effort cleanup of a failed temp record
		return fmt.Errorf("publishing task group record: %w", err)
	}
	return nil
}

// Read parses the record at path. A missing file is returned as the os error
// (fs.ErrNotExist); a malformed one is an error, never a zero group id, which
// would address the caller's own process group.
func Read(path string) (Record, error) {
	data, err := os.ReadFile(path) //nolint:gosec // the caller owns the record directory
	if err != nil {
		return Record{}, err
	}
	fields := strings.Fields(string(data))
	if len(fields) != 3 {
		return Record{}, fmt.Errorf("malformed task group record %s", path)
	}
	agent, aerr := strconv.Atoi(fields[0])
	pgid, perr := strconv.Atoi(fields[1])
	start, serr := strconv.ParseUint(fields[2], 10, 64)
	if aerr != nil || perr != nil || serr != nil || agent <= 0 || pgid <= 1 {
		return Record{}, fmt.Errorf("malformed task group record %s", path)
	}
	return Record{AgentPID: agent, PGID: pgid, LeaderStart: start}, nil
}

// Alive reports whether the group r names still has a process. It is
// GroupAlive with one refinement: when the record carries the leader's start
// time and a process with the group id's pid exists under a different start
// time, the group id now belongs to an unrelated process and the recorded group
// is gone. The kernel never hands out a pid that is still in use as a process
// group id, so a reused leader pid means no member of the old group is left.
// Without that refinement a group id an unrelated group leader picked up (any
// shell job, more likely once pids wrap) would read alive for as long as that
// group lives, and the attempt would never be reaped. A leader that exited, or
// a start time that cannot be read, falls back to the group probe.
func Alive(r Record) (bool, error) {
	alive, err := GroupAlive(r.PGID)
	if err != nil || !alive || r.LeaderStart == 0 {
		return alive, err
	}
	if start, serr := StartTime(r.PGID); serr == nil && start != r.LeaderStart {
		return false, nil
	}
	return true, nil
}

// pollInterval is how often Stop re-probes the group while it waits.
const pollInterval = 50 * time.Millisecond

// Stop ends the orphaned task group r names: SIGTERM to the whole group, up to
// grace for it to exit, then SIGKILL and up to killWait more. It reports true
// once no process of the group is left, including when none was left to begin
// with.
//
// It signals nothing unless the group's leader is still the process that was
// recorded (same pid, same start time). A leader that exited, or a record
// without a start time, cannot be verified, so Stop reports (false, nil) and
// the caller keeps waiting rather than risk signaling a group id the OS reused.
// Stop does not check the agent; that is the caller's decision.
func Stop(ctx context.Context, r Record, grace, killWait time.Duration) (bool, error) {
	alive, err := GroupAlive(r.PGID)
	if err != nil {
		return false, err
	}
	if !alive {
		return true, nil
	}
	if r.LeaderStart == 0 {
		return false, nil
	}
	start, err := StartTime(r.PGID)
	if err != nil || start != r.LeaderStart {
		return false, nil //nolint:nilerr // an unverifiable leader is a reason to wait, not a failure
	}
	if err := signalGroup(r.PGID, sigTerm); err != nil {
		return false, err
	}
	if gone, werr := awaitGone(ctx, r.PGID, grace); gone || werr != nil {
		return gone, werr
	}
	if err := signalGroup(r.PGID, sigKill); err != nil {
		return false, err
	}
	return awaitGone(ctx, r.PGID, killWait)
}

// awaitGone polls the group until it has no process left or d elapses.
func awaitGone(ctx context.Context, pgid int, d time.Duration) (bool, error) {
	deadline := time.Now().Add(d)
	for {
		alive, err := GroupAlive(pgid)
		if err != nil {
			return false, err
		}
		if !alive {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
