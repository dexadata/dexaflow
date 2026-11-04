package build

import "testing"

// isolateUserCache gives a test its own user cache directory. os.UserCacheDir
// reads XDG_CACHE_HOME on Linux and $HOME/Library/Caches on macOS, so setting
// only the first left macOS tests reading and writing the developer's real
// cache, where a listing from an earlier run answers for the same digest.
func isolateUserCache(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	t.Setenv("HOME", dir)
}
