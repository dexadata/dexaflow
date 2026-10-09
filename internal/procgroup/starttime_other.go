//go:build !linux && !darwin

package procgroup

import "errors"

// StartTime is not available on this platform, so no task group is ever
// verified, and therefore never signaled; the caller keeps waiting instead.
func StartTime(int) (uint64, error) {
	return 0, errors.New("process start time is not supported on this platform")
}
