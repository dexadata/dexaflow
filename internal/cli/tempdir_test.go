package cli

import (
	"os"
	"path/filepath"
)

// init resolves TMPDIR for the whole package. On macOS it sits under /var, a
// symlink to /private/var, and the commands report the resolved path of a
// workspace, so a test comparing that with t.TempDir() failed on the spelling
// of the same directory.
func init() {
	if dir, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		_ = os.Setenv("TMPDIR", dir)
	}
}
