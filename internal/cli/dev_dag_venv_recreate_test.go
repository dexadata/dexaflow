package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A per-DAG venv can be recreated WITHOUT its directory being removed: the
// directory and its pyvenv.cfg are intact (so nothing looks stale) while the
// interpreter itself is gone — a partial delete, an interrupted rebuild, a
// python symlink into a Homebrew build that was upgraded away.
//
// `python -m venv dir` then runs over the existing directory with no --clear,
// and the markers, which live INSIDE the venv, survive. `.leoflow-deps` then
// asserts that a freshly created interpreter already has the project's
// dependencies, and it never gets them: a venv that looks provisioned and fails
// at import time (#1096).
//
// The marker means "this interpreter has X", so it must not outlive the
// interpreter.
func TestRecreatedVenvDoesNotTrustTheOldDepsMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh stubs are POSIX-only")
	}
	home := filepath.Join(t.TempDir(), "dev")
	dagID := "etl"
	dir := dagVenvDir(home, dagID)
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o750); err != nil {
		t.Fatal(err)
	}
	// A pyvenv.cfg reporting the declared minor, so nothing is considered stale.
	if err := os.WriteFile(filepath.Join(dir, "pyvenv.cfg"), []byte("version = 3.11.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Markers from the PREVIOUS interpreter, both current.
	deps := []string{"requests==2.31.0"}
	if err := os.WriteFile(dagVenvDepsMarkerPath(home, dagID), []byte(devDepsSignature(deps)), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeRoot := writeRuntimeFixture(t, t.TempDir(), map[string]string{"runner.py": "def run(): pass\n"})
	sum, cerr := runtimeSrcChecksum(runtimeRoot)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if err := os.WriteFile(dagVenvRuntimeMarkerPath(home, dagID), []byte(sum), 0o600); err != nil {
		t.Fatal(err)
	}
	// The interpreter itself is absent: dagVenvPython(home, dagID) does not exist.
	if _, err := os.Stat(dagVenvPython(home, dagID)); err == nil {
		t.Fatal("fixture is wrong: the interpreter must be missing")
	}

	// A managed base that reports 3.11 and, for `-m venv <dir>`, materializes an
	// interpreter in it — what a real `python -m venv` does over an existing dir.
	managedBin := filepath.Join(filepath.Dir(home), "python", "bin")
	if err := os.MkdirAll(managedBin, 0o750); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(managedBin, "python3.11")
	// The created interpreter logs its argv, so the test can assert that the
	// project's dependencies were actually installed into it.
	installLog := filepath.Join(t.TempDir(), "install-argv")
	venvPy := "#!/bin/sh\nprintf '%s ' \"$@\" >> " + installLog + "\nprintf '\\n' >> " + installLog + "\nexit 0\n"
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  --version) echo 'Python 3.11.9'; exit 0;;\n" +
		"  -m) if [ \"$2\" = venv ]; then mkdir -p \"$3/bin\"; cat > \"$3/bin/python\" <<'VENVPY'\n" + venvPy + "VENVPY\n chmod +x \"$3/bin/python\"; exit 0; fi;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(base, []byte(script), 0o755); err != nil { //nolint:gosec // a test stub must be executable
		t.Fatal(err)
	}

	if _, err := ensureDagVenv(context.Background(), devTestCmd(), home, dagID, runtimeRoot, "3.11", deps); err != nil {
		t.Fatalf("ensureDagVenv: %v", err)
	}

	// The decisive assertion: the dependencies reached the NEW interpreter. The
	// marker is rewritten afterwards, and legitimately so, which is why asserting
	// on the marker would pass either way.
	logged, rerr := os.ReadFile(installLog)
	if rerr != nil {
		t.Fatalf("the recreated interpreter was never invoked at all: %v", rerr)
	}
	if !strings.Contains(string(logged), "requests==2.31.0") {
		t.Errorf("the project's dependencies were never installed into the recreated interpreter; the stale marker claimed they already were.\ninvocations:\n%s", logged)
	}
}
