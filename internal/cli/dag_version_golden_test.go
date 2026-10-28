package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dexadata/dexaflow/internal/version"
)

// goldenContentHash is the content hash of goldenProject. It changes only
// when what the label hashes changes, which moves every deployed label: a
// refactor of the walk (deploy --changed-since shares it) must keep it.
const goldenContentHash = "4664967005b0fd41dfd8e80d7391af91983c7e7509490b035a99672b769ea539"

// goldenProject writes a fixed project: a DAG source, an include_paths
// directory holding a kept file, an excluded file and a symlink, a
// .dockerignore, and files the generated image does not copy.
func goldenProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeProjectFile(t, dir, "dag.py", "# dag\n")
	writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: golden\ninclude_paths: [lib]\n")
	writeProjectFile(t, dir, "lib/util.py", "X = 1\n")
	writeProjectFile(t, dir, "lib/sub/more.py", "Y = 2\n")
	writeProjectFile(t, dir, "lib/util.pyc", "bytecode\n")
	writeProjectFile(t, dir, "lib/skip.txt", "ignored by .dockerignore\n")
	writeProjectFile(t, dir, ".dockerignore", "lib/skip.txt\n")
	writeProjectFile(t, dir, "notes.md", "not in the image\n")
	if err := os.Symlink("util.py", filepath.Join(dir, "lib", "link.py")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestContentHashGolden(t *testing.T) {
	t.Parallel()
	if v := version.Get().Version; v != "dev" {
		t.Skipf("the golden hash is for the dev build, not %s", v)
	}
	dir := goldenProject(t)
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := contentHash(dir, cfg, "Dockerfile", filepath.Join(dir, "dag.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got != goldenContentHash {
		t.Errorf("contentHash = %s, want %s: every deployed label would move", got, goldenContentHash)
	}
}
