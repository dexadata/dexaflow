//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether the file at path belongs to the user
// running the CLI. A file whose owner cannot be read counts as not owned.
func ownedByCurrentUser(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	euid := os.Geteuid()
	return euid >= 0 && uint64(st.Uid) == uint64(euid) //nolint:gosec // G115: euid is checked non-negative first.
}
