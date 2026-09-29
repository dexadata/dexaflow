package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// writeFileAtomic writes a file that holds secrets, at 0600, without a window
// where it is truncated or half written.
//
// os.WriteFile is wrong for this twice over. Its mode argument applies only when
// the file is CREATED, so rewriting an existing world-readable config.yaml
// leaves it world-readable while adding an encryption key to it. And it
// truncates before writing, so a crash or a concurrent writer can leave a file
// holding neither the old secrets nor the new ones, taking the encryption key,
// the JWT secret and the admin hash together.
//
// Write to a temp file in the same directory, chmod it explicitly, then rename:
// rename within a directory is atomic, so a reader sees the old file or the new
// one and never a partial.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("creating a temp file next to %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() //nolint:errcheck // best effort; a successful rename makes this a no-op

	if _, werr := tmp.Write(data); werr != nil {
		_ = tmp.Close() //nolint:errcheck // the write error is the one worth reporting
		return fmt.Errorf("writing %s: %w", tmpName, werr)
	}
	// Explicit, because CreateTemp makes the file 0600 but an umask or a future
	// change should not be what keeps a key private.
	if cerr := tmp.Chmod(0o600); cerr != nil {
		_ = tmp.Close() //nolint:errcheck // the chmod error is the one worth reporting
		return fmt.Errorf("restricting %s: %w", tmpName, cerr)
	}
	if serr := tmp.Sync(); serr != nil {
		_ = tmp.Close() //nolint:errcheck // the sync error is the one worth reporting
		return fmt.Errorf("flushing %s: %w", tmpName, serr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("closing %s: %w", tmpName, cerr)
	}
	if rerr := os.Rename(tmpName, path); rerr != nil {
		return fmt.Errorf("replacing %s: %w", path, rerr)
	}
	return nil
}
