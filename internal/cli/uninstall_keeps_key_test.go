package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `leoflow uninstall` promises, in its own help text, that it does not remove
// your datastore "so a reinstall keeps your data". That promise used to be
// free: the encryption key was a constant compiled into the binary, so it came
// back with the reinstall.
//
// Per-install keys (#486) break it. The key lives only in config.yaml, which
// uninstall deletes, while pgdata survives. A reinstall then generates a
// different key and every connection password in the preserved datastore is
// permanently unreadable, with the user having followed the documented path.
//
// So the datastore's key is preserved with the datastore.
func TestUninstallKeepsTheKeyThatOpensThePreservedDatastore(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "config.yaml"), "jwt_secret: \"j\"\nsecret_key: \"THE-KEY\"\nsecret_key_previous: \"OLDER\"\n")
	mustWrite(t, filepath.Join(root, "bin", "leoflow"), "binary")
	mustWrite(t, filepath.Join(root, "pgdata", "PG_VERSION"), "16")

	if err := removeHomeExcept(root, "pgdata"); err != nil {
		t.Fatalf("removeHomeExcept: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "bin")); !os.IsNotExist(err) {
		t.Error("bin/ should have been removed")
	}
	if _, err := os.Stat(filepath.Join(root, "pgdata", "PG_VERSION")); err != nil {
		t.Errorf("pgdata must survive a plain uninstall: %v", err)
	}

	kept := configFileSecrets(filepath.Join(root, "pgdata", "keys.yaml"))
	if kept.secretKey != "THE-KEY" {
		t.Errorf("the key that decrypts the preserved datastore was not preserved with it (got %q); a reinstall would orphan every credential", kept.secretKey)
	}
	if kept.secretKeyPrevious != "OLDER" {
		t.Errorf("the predecessor was not preserved (got %q); rows still under it would be orphaned too", kept.secretKeyPrevious)
	}
}

// With no datastore to keep, there is nothing the key could open, so nothing is
// written: an uninstall that leaves a stray key file behind is worse than one
// that does not.
func TestUninstallWritesNoKeyWhenNoDatastoreSurvives(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "config.yaml"), "secret_key: \"THE-KEY\"\n")
	if err := removeHomeExcept(root, "pgdata"); err != nil {
		t.Fatalf("removeHomeExcept: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		entries, _ := os.ReadDir(root)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("with no datastore the home should be gone; it still holds %s", strings.Join(names, ", "))
	}
}

// Preserving the key is only half: a reinstall has to adopt it. Generating a
// fresh key next to a datastore whose rows are under the preserved one is the
// same orphaning, one step later.
func TestSetupAdoptsThePreservedKey(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "pgdata"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "pgdata", "keys.yaml"),
		[]byte("secret_key: \"PRESERVED-KEY\"\nsecret_key_previous: \"PRESERVED-OLDER\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := adoptPreservedKey(home)
	if got.secretKey != "PRESERVED-KEY" {
		t.Errorf("a reinstall must adopt the key left with the datastore, got %q", got.secretKey)
	}
	if got.secretKeyPrevious != "PRESERVED-OLDER" {
		t.Errorf("the predecessor must be adopted too, got %q", got.secretKeyPrevious)
	}
}

// A genuinely fresh machine has nothing to adopt and must generate its own.
func TestSetupGeneratesWhenNothingWasPreserved(t *testing.T) {
	if got := adoptPreservedKey(t.TempDir()); got.secretKey != "" {
		t.Errorf("a fresh install must not adopt anything, got %q", got.secretKey)
	}
}
