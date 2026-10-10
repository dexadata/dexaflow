package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// writeRuntimeFixture builds a fake pysrc-style runtime tree under dir with the
// given file→content map; subdirs are created as needed. Returns the runtime root.
func writeRuntimeFixture(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	root := filepath.Join(dir, "runtime", "python", "leoflow_runtime")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "runtime", "python")
}

// TestRuntimeSrcChecksum_StableAcrossWalkOrder: the walk MUST be deterministic
// across runs and OSes — the marker check compares hex strings byte-for-byte, so
// any nondeterminism would force gratuitous reinstalls (or skip needed ones).
func TestRuntimeSrcChecksum_StableAcrossWalkOrder(t *testing.T) {
	src := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"__init__.py": "x = 1\n",
		"runner.py":   "def run(): pass\n",
		"xcom.py":     "def pull(): return None\n",
	})
	a, err := runtimeSrcChecksum(src)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}
	b, err := runtimeSrcChecksum(src)
	if err != nil {
		t.Fatalf("checksum 2nd call: %v", err)
	}
	if a != b {
		t.Errorf("checksum must be stable across calls: %q != %q", a, b)
	}
	if len(a) != 64 {
		t.Errorf("expected a hex SHA-256 (64 chars), got %d chars: %q", len(a), a)
	}
}

// TestRuntimeSrcChecksum_ChangesWithContent: editing ANY .py file inside the
// runtime must change the checksum so the venv reinstalls — this is the core
// invariant Lima bug #239 needs (binary upgrade ships a new runner.py with
// lifecycle log lines; the venv must pick it up).
func TestRuntimeSrcChecksum_ChangesWithContent(t *testing.T) {
	v1 := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"runner.py": "def run(): pass\n",
	})
	v2 := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"runner.py": "def run(): print('[leoflow] loading')\n", // simulated upgrade
	})
	a, _ := runtimeSrcChecksum(v1)
	b, _ := runtimeSrcChecksum(v2)
	if a == b {
		t.Errorf("checksum did not change for different content: both %q", a)
	}
}

// TestRuntimeSrcChecksum_IgnoresNonPyFiles: __pycache__, .pyc, README, etc.
// must NOT affect the checksum. Otherwise a stray cache dir would force constant
// reinstalls.
func TestRuntimeSrcChecksum_IgnoresNonPyFiles(t *testing.T) {
	src := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"runner.py": "def run(): pass\n",
	})
	base, _ := runtimeSrcChecksum(src)
	// Drop a stray .pyc and README alongside.
	if err := os.WriteFile(filepath.Join(src, "leoflow_runtime", "runner.cpython-311.pyc"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "leoflow_runtime", "README.md"), []byte("docs"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, _ := runtimeSrcChecksum(src)
	if base != after {
		t.Errorf("stray non-py files must not affect the checksum: %q != %q", base, after)
	}
}

// TestDevDepsSignatureOrderIndependent: the canonical signature ignores ordering,
// so a dexaflow.yaml that just reorders `dependencies:` does not force a needless
// reinstall.
func TestDevDepsSignatureOrderIndependent(t *testing.T) {
	a := devDepsSignature([]string{"requests==2.31.0", "duckdb==1.4.4"})
	b := devDepsSignature([]string{"duckdb==1.4.4", "requests==2.31.0"})
	if a != b {
		t.Errorf("signature must be order-independent: %q != %q", a, b)
	}
	c := devDepsSignature([]string{"requests==2.31.0"})
	if a == c {
		t.Errorf("a different dep set must yield a different signature: both %q", a)
	}
}

// TestDagVenvDepsRoundtrip: writing the per-DAG deps marker makes
// dagVenvDepsUpToDate report true for the same deps (in any order) and false
// when the deps change — gates the reinstall when one DAG's `dependencies:`
// is edited, without re-running pip for every other DAG in the workspace
// (#346 — the issue with the shared single-venv layout).
func TestDagVenvDepsRoundtrip(t *testing.T) {
	home := t.TempDir()
	dagID := "etl"
	venvDir := dagVenvDir(home, dagID)
	if err := os.MkdirAll(venvDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if dagVenvDepsUpToDate(home, dagID, []string{"x"}) {
		t.Errorf("absent marker must report not up-to-date")
	}
	deps := []string{"requests==2.31.0", "duckdb==1.4.4"}
	if err := os.WriteFile(dagVenvDepsMarkerPath(home, dagID), []byte(devDepsSignature(deps)), 0o600); err != nil {
		t.Fatal(err)
	}
	if !dagVenvDepsUpToDate(home, dagID, []string{"duckdb==1.4.4", "requests==2.31.0"}) {
		t.Errorf("same deps (any order) must report up-to-date after a successful install")
	}
	if dagVenvDepsUpToDate(home, dagID, []string{"requests==2.31.0"}) {
		t.Errorf("a different dep set must report not up-to-date (forces reinstall on dep edit)")
	}
	// And the per-DAG marker is scoped: a *different* DAG with no marker yet
	// is not up-to-date for the same deps — proves the gate is keyed.
	if dagVenvDepsUpToDate(home, "other", deps) {
		t.Errorf("the marker must be per-DAG; an unrelated DAG must not inherit it")
	}
}

// TestDagVenvRuntimeChecksumRoundtrip pins the per-DAG runtime checksum
// marker: empty when absent, returns the stamped value after a write. The
// binary-upgrade reinstall (#239) carries over from the single-venv layout —
// without this, a `dexaflow lite` rerun after `make dev-install` would keep
// the previous build's runtime in the venv.
func TestDagVenvRuntimeChecksumRoundtrip(t *testing.T) {
	home := t.TempDir()
	dagID := "etl"
	venvDir := dagVenvDir(home, dagID)
	if err := os.MkdirAll(venvDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if got := dagVenvRuntimeChecksum(home, dagID); got != "" {
		t.Errorf("absent marker must report empty, got %q", got)
	}
	want := "deadbeef" + "00000000000000000000000000000000000000000000000000000000"
	if err := os.WriteFile(dagVenvRuntimeMarkerPath(home, dagID), []byte(want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dagVenvRuntimeChecksum(home, dagID); got != want {
		t.Errorf("roundtrip mismatch: got %q want %q", got, want)
	}
}
