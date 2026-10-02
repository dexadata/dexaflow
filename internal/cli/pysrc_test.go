package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsurePysrcReextractsOnDrift: a stale or missing extracted parser tree
// (the binary-upgrade case, #239) is re-extracted so `dexaflow compile` never runs
// against a parser predating a feature like dbt. A drifted checksum marker forces
// a re-extract; a matching marker is a no-op.
func TestEnsurePysrcReextractsOnDrift(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pysrc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A stale marker with no real parser package on disk = drift.
	if err := os.WriteFile(filepath.Join(dir, pysrcMarker), []byte("STALE\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ensurePysrcIn(dir, nil); err != nil {
		t.Fatalf("ensurePysrcIn (drift): %v", err)
	}

	// The bundled parser package must now be on disk.
	if _, err := os.Stat(filepath.Join(dir, "parser", "leoflow_parser", "__init__.py")); err != nil {
		t.Errorf("parser was not re-extracted: %v", err)
	}
	// The dbt-capable shim in particular must be present (the whole point).
	if _, err := os.Stat(filepath.Join(dir, "parser", "leoflow_parser", "_shim", "leoflow", "__init__.py")); err != nil {
		t.Errorf("dbt shim (_shim/leoflow) missing after re-extract: %v", err)
	}
	// The marker now matches this binary's embedded checksum.
	want, err := pythonSourcesChecksum()
	if err != nil {
		t.Fatalf("pythonSourcesChecksum: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, pysrcMarker))
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("marker not updated to the embedded checksum")
	}

	// Idempotent: a second call with a current marker is a clean no-op.
	if err := ensurePysrcIn(dir, nil); err != nil {
		t.Fatalf("ensurePysrcIn (current): %v", err)
	}
}
