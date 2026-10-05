package cli

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Two commands that rewrite config.yaml must not interleave: two concurrent
// migrations each generating a key would let the second erase the key the
// first committed rows under (ADR 0065 section 3).
func TestConfigLockExcludesASecondWriter(t *testing.T) {
	dir := t.TempDir()
	release, err := lockConfigDir(dir, configLockExclusive, false)
	if err != nil {
		t.Fatalf("first writer: %v", err)
	}
	if _, err2 := lockConfigDir(dir, configLockExclusive, false); !errors.Is(err2, errConfigLocked) {
		t.Errorf("second writer got %v, want errConfigLocked", err2)
	}
	if _, err3 := lockConfigDir(dir, configLockShared, false); !errors.Is(err3, errConfigLocked) {
		t.Errorf("a reader got in during a write: %v", err3)
	}
	release()
	again, err := lockConfigDir(dir, configLockExclusive, false)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}

// Readers (backup, the Lite boot) share the lock with each other.
func TestConfigLockSharedReaders(t *testing.T) {
	dir := t.TempDir()
	r1, err := lockConfigDir(dir, configLockShared, false)
	if err != nil {
		t.Fatal(err)
	}
	defer r1()
	r2, err := lockConfigDir(dir, configLockShared, false)
	if err != nil {
		t.Fatalf("second reader: %v", err)
	}
	r2()
}

// A Lite boot that arrives during a migration waits for it, then reads the
// migrated config, rather than booting with the keys from before it.
func TestConfigLockWaitingReaderBlocksUntilTheWriterIsDone(t *testing.T) {
	dir := t.TempDir()
	release, err := lockConfigDir(dir, configLockExclusive, false)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		r, lerr := lockConfigDir(dir, configLockShared, true)
		if lerr == nil {
			r()
		}
		got <- lerr
	}()
	select {
	case e := <-got:
		t.Fatalf("the reader did not wait for the writer: %v", e)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	select {
	case e := <-got:
		if e != nil {
			t.Fatalf("waiting reader: %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reader never got the lock after the writer released it")
	}
}

// The lock file is private and owned like config.yaml, so a command run with
// sudo does not leave a root-owned lock the user's next command cannot open.
func TestConfigLockFileIsPrivateAndOwnedLikeTheConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("x: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(cfg, 4321, 4321); err != nil {
			t.Fatal(err)
		}
	}
	release, err := lockConfigDir(dir, configLockExclusive, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fi, err := os.Stat(filepath.Join(dir, configLockName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("lock file mode %v, want 0600", fi.Mode().Perm())
	}
	if os.Geteuid() == 0 {
		st, _ := fi.Sys().(*syscall.Stat_t)
		if st.Uid != 4321 || st.Gid != 4321 {
			t.Errorf("lock file owned by %d:%d, want the config's 4321:4321", st.Uid, st.Gid)
		}
	}
}

// A read-only look (`migrate-key --dry-run`) creates nothing: with no lock file
// it proceeds unlocked rather than writing one.
func TestConfigLockNoCreateLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	release, err := lockConfigDirIfPresent(dir, configLockShared)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, serr := os.Stat(filepath.Join(dir, configLockName)); !os.IsNotExist(serr) {
		t.Errorf("a dry run created %s", configLockName)
	}
}
