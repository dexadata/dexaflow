//go:build unix

package procgroup

import (
	"errors"
	"syscall"
)

const (
	sigTerm = syscall.SIGTERM
	sigKill = syscall.SIGKILL
)

// GroupAlive reports whether any process of the group pgid exists, by sending
// signal 0 to the whole group. EPERM means a member exists under another user,
// so the group is alive; ESRCH means the group has no member left.
func GroupAlive(pgid int) (bool, error) {
	if pgid <= 1 {
		return false, errors.New("refusing to probe a process group id <= 1")
	}
	err := syscall.Kill(-pgid, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, err
	}
}

// signalGroup sends sig to every process of the group. A group that is already
// gone is not an error.
func signalGroup(pgid int, sig syscall.Signal) error {
	if pgid <= 1 {
		return errors.New("refusing to signal a process group id <= 1")
	}
	if err := syscall.Kill(-pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
