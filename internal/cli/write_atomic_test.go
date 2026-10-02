package cli

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// os.WriteFile's mode applies only when it CREATES the file. Rewriting an
// existing world-readable config.yaml left it world-readable while adding an
// encryption key to it, and the test that was supposed to catch that seeded the
// file at 0600 and so never exercised the property it named.
//
// This one starts at 0644 on purpose.
func TestWriteFileAtomicTightensAnExistingLooseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil { //nolint:gosec // the loose mode is the point
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomic(path, []byte("secret_key: \"k\"\n")); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600: the file now holds an encryption key and anyone on the box can read it", fi.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `secret_key: "k"`) {
		t.Errorf("content not written: %q", raw)
	}
}

// Nothing is left behind on success: a stray temp file beside config.yaml holds
// the same secrets at an unmanaged name.
func TestWriteFileAtomicLeavesNoTempBehind(t *testing.T) {
	dir := t.TempDir()
	if err := writeFileAtomic(filepath.Join(dir, "config.yaml"), []byte("x\n")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("a temp file survived: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want exactly config.yaml", len(entries))
	}
}

// A rewrite must never record an empty key: that reads back as "configured and
// empty" rather than "absent", and the real key is gone.
func TestWriteLiteConfigNeverWritesAnEmptyKey(t *testing.T) {
	home := t.TempDir()
	lc := liteSettings{Workspace: "/w", Executor: "subprocess", AdminEmail: "a@b.c", Port: 8088}
	if err := writeLiteConfig(home, "parser", lc, "$2a$12$hash", liteFileSecrets{jwtSecret: "j"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if strings.Contains(string(raw), "secret_key:") {
		t.Errorf("wrote an empty secret_key, which blocks a later rotation from ever running:\n%s", raw)
	}
}

// A temp file plus rename creates a NEW inode, owned by whoever runs the
// command, where os.WriteFile rewrote the same inode and kept its owner. The
// installer prints `sudo dexaflow lite reset-password`, so a root-run rewrite of
// a user's config is a documented path: leaving it root-owned locks the user
// out of their own install and orphans every connection.
//
// Running as root is not available here, so this asserts the property that is
// testable: the owner does not change across the rewrite.
func TestWriteFileAtomicKeepsTheOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, ok := before.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no syscall.Stat_t on this platform")
	}

	if werr := writeFileAtomic(path, []byte("new\n")); werr != nil {
		t.Fatalf("writeFileAtomic: %v", werr)
	}

	after, serr := os.Stat(path)
	if serr != nil {
		t.Fatal(serr)
	}
	afterStat, _ := after.Sys().(*syscall.Stat_t)
	if afterStat.Uid != beforeStat.Uid || afterStat.Gid != beforeStat.Gid {
		t.Errorf("owner changed across the rewrite: %d:%d -> %d:%d",
			beforeStat.Uid, beforeStat.Gid, afterStat.Uid, afterStat.Gid)
	}
}
