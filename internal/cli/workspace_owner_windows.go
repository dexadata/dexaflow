//go:build windows

package cli

// ownedByCurrentUser reports true on Windows, where file ownership is not
// exposed the way the Unix check reads it; the workspace file is trusted as
// git trusts a repository there by default.
func ownedByCurrentUser(string) bool { return true }
