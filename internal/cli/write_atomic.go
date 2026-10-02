package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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
	// Keep the existing file's owner. os.WriteFile rewrote the SAME inode, so
	// ownership survived; a temp file plus rename creates a NEW one owned by
	// whoever is running. The installer prints `sudo leoflow lite
	// reset-password` as the password-recovery command, so that path is not
	// hypothetical: without this the user's ~/.dexaflow/config.yaml becomes
	// root-owned 0600, their next non-root `leoflow lite` cannot read it, the
	// control plane silently drops to no-auth, and every connection encrypted
	// under the per-install key becomes unreadable.
	//
	// Best effort: chown fails for a non-root user changing owner, which is the
	// normal case and where there is nothing to preserve anyway.
	preserveOwner(path, tmpName)

	if rerr := os.Rename(tmpName, path); rerr != nil {
		return fmt.Errorf("replacing %s: %w", path, rerr)
	}
	return nil
}

// preserveOwner gives tmp the uid/gid of an existing target, so replacing it by
// rename does not change who owns it.
func preserveOwner(target, tmp string) {
	fi, err := os.Stat(target)
	if err != nil {
		return // no existing file: the new owner is the right owner
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	_ = os.Chown(tmp, int(st.Uid), int(st.Gid)) //nolint:errcheck // best effort; fails for a non-root user, where there is nothing to preserve
}
