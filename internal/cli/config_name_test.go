package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A project is configured by dexaflow.yaml. Projects created before the rename
// carry leoflow.yaml, which keeps working as a fallback; when both exist the
// new file wins and the user is told, so an edit to the stale one is not lost
// silently.
func TestProjectConfigPathPrefersDexaflowYAML(t *testing.T) {
	cases := []struct {
		name      string
		files     []string
		wantFile  string
		wantNotes bool
	}{
		{"new only", []string{"dexaflow.yaml"}, "dexaflow.yaml", false},
		{"legacy only", []string{"leoflow.yaml"}, "leoflow.yaml", true},
		{"both", []string{"dexaflow.yaml", "leoflow.yaml"}, "dexaflow.yaml", true},
		{"neither", nil, "dexaflow.yaml", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, f := range c.files {
				writeFile(t, filepath.Join(dir, f), "dag_id: x\n")
			}
			if got := projectConfigPath(dir); got != filepath.Join(dir, c.wantFile) {
				t.Errorf("projectConfigPath = %q, want %q", got, filepath.Join(dir, c.wantFile))
			}
			note := projectConfigNote(dir)
			if (note != "") != c.wantNotes {
				t.Errorf("projectConfigNote = %q, want note=%v", note, c.wantNotes)
			}
			if note != "" && !strings.Contains(note, "dexaflow.yaml") {
				t.Errorf("note %q must name dexaflow.yaml", note)
			}
		})
	}
}

// The config a project is read from is the one its values come from.
func TestLoadProjectConfigReadsLegacyFileWhenAlone(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "leoflow.yaml"), "dag_id: legacy_dag\n")
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatalf("loadProjectConfig: %v", err)
	}
	if cfg.DagID != "legacy_dag" {
		t.Errorf("DagID = %q, want legacy_dag", cfg.DagID)
	}
}

func TestLoadProjectConfigNewFileWins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "leoflow.yaml"), "dag_id: stale\n")
	writeFile(t, filepath.Join(dir, "dexaflow.yaml"), "dag_id: current\n")
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatalf("loadProjectConfig: %v", err)
	}
	if cfg.DagID != "current" {
		t.Errorf("DagID = %q, want current", cfg.DagID)
	}
}

// Discovery must recognize a project configured only by dexaflow.yaml (a pure
// dbt project has no dag.py, so the yaml is all there is).
func TestProjectAtFindsDbtProjectByDexaflowYAML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "dexaflow.yaml"), "dag_id: dbt_only\ndbt:\n  project_dir: .\n")
	if _, ok := projectAt(dir); !ok {
		t.Fatal("a dbt-only project with dexaflow.yaml was not discovered")
	}
}
