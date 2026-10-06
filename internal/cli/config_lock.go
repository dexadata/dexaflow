package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// configLockName is the lock file every command that reads or rewrites
// ~/.dexaflow/config.yaml takes (ADR 0065 section 3).
const configLockName = ".config.lock"

// configLockMode selects a shared (read) or exclusive (read-modify-write) lock.
type configLockMode int

const (
	// configLockShared is for readers that need a config consistent with the
	// datastore: `dexaflow lite` while it reads the keys at boot, and `backup`.
	configLockShared configLockMode = iota
	// configLockExclusive is for every command that rewrites config.yaml:
	// setup, reset-password, restore, uninstall and migrate-key.
	configLockExclusive
)

// errConfigLocked reports that another dexaflow command holds the config lock.
var errConfigLocked = errors.New("another dexaflow command is changing ~/.dexaflow/config.yaml " +
	"(a `dexaflow lite migrate-key`, setup, reset-password, restore or uninstall); wait for it to finish and run this again")

// lockConfigDir takes the config lock in dir and returns its release.
//
// The order between locks is fixed for every command: this flock first, then
// any Postgres advisory lock, so two commands cannot deadlock. Without it, two
// `migrate-key` runs against different datastores (whose advisory locks live
// in different clusters and do not conflict) could each read a Legacy config,
// each generate a key, and the second write would erase the key the first had
// already committed rows under.
//
// With wait false a held lock is errConfigLocked; with wait true the call
// blocks until the lock is free. The lock file is created 0600 and owned like
// config.yaml, so a command run under sudo does not leave a root-owned lock the
// user's next command cannot open.
func lockConfigDir(dir string, mode configLockMode, wait bool) (func(), error) {
	path := filepath.Join(dir, configLockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // fixed name under the user's own ~/.dexaflow
	if err != nil {
		return nil, fmt.Errorf("opening the config lock %s: %w", path, err)
	}
	if cerr := f.Chmod(0o600); cerr != nil {
		_ = f.Close() //nolint:errcheck // the chmod error is the one worth reporting
		return nil, fmt.Errorf("restricting %s: %w", path, cerr)
	}
	preserveOwner(filepath.Join(dir, "config.yaml"), path)
	return flockFile(f, mode, wait)
}

// lockConfigDirIfPresent takes the lock only when the lock file already exists,
// and otherwise proceeds unlocked: a read-only run (`migrate-key --dry-run`)
// promises to create nothing, not even the lock file.
func lockConfigDirIfPresent(dir string, mode configLockMode) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, configLockName), os.O_RDONLY, 0) //nolint:gosec // fixed name under the user's own ~/.dexaflow
	if errors.Is(err, os.ErrNotExist) {
		return func() {}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening the config lock: %w", err)
	}
	return flockFile(f, mode, false)
}

func flockFile(f *os.File, mode configLockMode, wait bool) (func(), error) {
	how := syscall.LOCK_SH
	if mode == configLockExclusive {
		how = syscall.LOCK_EX
	}
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil { //nolint:gosec // a file descriptor fits an int
		_ = f.Close() //nolint:errcheck // the lock error is the one worth reporting
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errConfigLocked
		}
		return nil, fmt.Errorf("locking %s: %w", f.Name(), err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck,gosec // closing releases it anyway
		_ = f.Close()                                   //nolint:errcheck // best effort
	}, nil
}

// lockConfigOrWait takes the lock shared for the Lite boot, waiting (and
// saying so) when a migration or another writer holds it, so the boot reads the
// config that writer leaves rather than the one from before it.
func lockConfigOrWait(dir string, say func(string)) (func(), error) {
	release, err := lockConfigDir(dir, configLockShared, false)
	if !errors.Is(err, errConfigLocked) {
		return release, err
	}
	say("  waiting for another dexaflow command that is changing ~/.dexaflow/config.yaml (a key migration?) to finish …")
	return lockConfigDir(dir, configLockShared, true)
}
