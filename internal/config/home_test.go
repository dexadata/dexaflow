package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Fresh installs keep their state in ~/.dexaflow.
func TestHomeDirFreshInstallUsesDexaflow(t *testing.T) {
	home := t.TempDir()
	got, err := HomeDirIn(home)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(home, ".dexaflow") {
		t.Fatalf("HomeDirIn = %q, want ~/.dexaflow", got)
	}
}

// An install from before the rename keeps its data where it is: ~/.dexaflow
// becomes a link to the existing ~/.leoflow, so nothing is moved under a running
// Lite (its Postgres data, its cluster's mounts) and both paths keep working.
func TestHomeDirLinksToAnExistingLeoflowHome(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".leoflow")
	if err := os.MkdirAll(filepath.Join(legacy, "dev"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.yaml"), []byte("x: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := HomeDirIn(home)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(home, ".dexaflow") {
		t.Fatalf("HomeDirIn = %q, want ~/.dexaflow", got)
	}
	data, err := os.ReadFile(filepath.Join(got, "config.yaml"))
	if err != nil || string(data) != "x: 1\n" {
		t.Fatalf("existing state not reachable through ~/.dexaflow: %q, %v", data, err)
	}
	if fi, err := os.Lstat(legacy); err != nil || !fi.IsDir() {
		t.Fatalf("~/.leoflow must stay a real directory with the data, got %v, %v", fi, err)
	}
	// Idempotent: a second call keeps the link and returns the same path.
	if again, err := HomeDirIn(home); err != nil || again != got {
		t.Fatalf("second call = %q, %v", again, err)
	}
}

// When both exist (the user created ~/.dexaflow, or a link already points
// somewhere), ~/.dexaflow wins and nothing is touched.
func TestHomeDirPrefersAnExistingDexaflowHome(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{".leoflow", ".dexaflow"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := HomeDirIn(home)
	if err != nil || got != filepath.Join(home, ".dexaflow") {
		t.Fatalf("HomeDirIn = %q, %v", got, err)
	}
	if fi, _ := os.Lstat(filepath.Join(home, ".dexaflow")); fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("an existing ~/.dexaflow directory must not be replaced by a link")
	}
}

// If the link cannot be created, the legacy home is used as is: state is never
// split between two directories.
func TestHomeDirFallsBackToLeoflowWhenTheLinkFails(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".leoflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o500); err != nil { // read-only parent: no new entries
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	got, err := HomeDirIn(home)
	if err != nil || got != filepath.Join(home, ".leoflow") {
		t.Fatalf("HomeDirIn = %q, %v; want the legacy home", got, err)
	}
}
