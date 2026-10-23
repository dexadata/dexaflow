package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveDagVenv: deleting a DAG drops its per-DAG venv (the Airflow SDK lives
// there). Reports whether a venv was actually present so the caller can log it; a
// missing venv is a no-op, not an error.
func TestRemoveDagVenv(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := dagVenvDir(home, "my_dag")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	removed, err := removeDagVenv(home, "my_dag")
	if err != nil {
		t.Fatalf("removeDagVenv: %v", err)
	}
	if !removed {
		t.Error("removed = false, want true (the venv existed)")
	}
	if _, serr := os.Stat(dir); !os.IsNotExist(serr) {
		t.Error("venv dir still present after removal")
	}
	removed, err = removeDagVenv(home, "my_dag")
	if err != nil || removed {
		t.Errorf("second remove = (%v, %v), want (false, nil) — idempotent", removed, err)
	}
}
