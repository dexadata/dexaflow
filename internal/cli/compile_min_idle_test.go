package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// compiledMinIdle compiles the project at dir with the real compile command
// and returns the min_idle_workers the written dag.json carries.
func compiledMinIdle(t *testing.T, dir string) int {
	t.Helper()
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(t.TempDir(), "parser.log")))
	if out, errOut, err := run(t, "compile", dir, "--dag-version", "v1"); err != nil {
		t.Fatalf("compile: %v\n%s%s", err, out, errOut)
	}
	return readSpec(t, filepath.Join(dir, "dag.json")).MinIdleWorkers
}

// min_idle_workers in dexaflow.yaml reaches dag.json, where the reconciler
// reads it to prewarm the version on register (ADR 0082), and leaving it out
// leaves the key out, so a DAG that declares no warmth compiles as before.
func TestCompileCarriesMinIdleWorkersIntoDagJSON(t *testing.T) {
	if got := compiledMinIdle(t, versionProject(t, "min_idle_workers: 2\n")); got != 2 {
		t.Errorf("dag.json min_idle_workers = %d, want 2", got)
	}
	dir := versionProject(t, "")
	if compiledMinIdle(t, dir) != 0 {
		t.Error("a project without min_idle_workers compiled one")
	}
	if body := readProjectFile(t, dir, "dag.json"); strings.Contains(body, "min_idle_workers") {
		t.Errorf("dag.json carries the key without a declaration:\n%s", body)
	}
}

// A legacy leoflow.yaml carries it the same way.
func TestCompileCarriesMinIdleWorkersFromLeoflowYAML(t *testing.T) {
	dir := versionProject(t, "")
	if err := os.Rename(filepath.Join(dir, "dexaflow.yaml"), filepath.Join(dir, "leoflow.yaml")); err != nil {
		t.Fatal(err)
	}
	writeProjectFile(t, dir, "leoflow.yaml", "dag_id: etl\nmin_idle_workers: 1\n")
	if got := compiledMinIdle(t, dir); got != 1 {
		t.Errorf("dag.json min_idle_workers = %d, want 1", got)
	}
}
