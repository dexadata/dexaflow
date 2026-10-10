//go:build !unix

package procgroup

import "errors"

const (
	sigTerm = 0
	sigKill = 0
)

var errUnsupported = errors.New("task process groups are not supported on this platform")

// GroupAlive cannot probe a process group on this platform, so liveness is
// unknown and the caller waits. Lite ships for Linux and macOS only.
func GroupAlive(int) (bool, error) { return false, errUnsupported }

func signalGroup(int, int) error { return errUnsupported }
