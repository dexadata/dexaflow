//go:build !unix

package executor

import "io/fs"

// ownedByCurrentUser has no uid to compare on this platform. Lite ships for
// Linux and macOS only, and liveness probing already reports an error here.
func ownedByCurrentUser(fs.FileInfo) bool { return true }
