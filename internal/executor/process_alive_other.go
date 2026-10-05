//go:build !unix

package executor

import "errors"

// processAlive cannot probe a PID on this platform, so liveness is unknown and
// the reapers defer. Lite ships for Linux and macOS only.
func processAlive(int) (bool, error) {
	return false, errors.New("agent process liveness is not supported on this platform")
}
