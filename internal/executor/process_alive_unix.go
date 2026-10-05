//go:build unix

package executor

import (
	"errors"
	"syscall"
)

// processAlive probes a PID with signal 0, which checks existence and
// permission without delivering anything. EPERM means the process exists but
// belongs to someone else, so it is alive; ESRCH means there is no such process.
func processAlive(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	switch {
	case err == nil, errors.Is(err, syscall.EPERM):
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, err
	}
}
