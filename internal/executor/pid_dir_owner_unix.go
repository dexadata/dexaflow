//go:build unix

package executor

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether fi belongs to this process's effective
// user.
func ownedByCurrentUser(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}
