package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestLoadProjectConfigAppliesDefaults verifies that loading a minimal
// dexaflow.yaml fills in every schema default via LeoflowConfig.ApplyDefaults().
// This is what lets a yaml that only declares `dag_id` still produce a working
// build (python 3.11, dag.py source, default exclude paths, etc.) and is the
// foundation for the multi-DAG workspace contract (a subdir without a yaml
// gets the same treatment with dag_id synthesized later).
func TestLoadProjectConfigAppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	yaml := "dag_id: minimal\n"
	if err := os.WriteFile(filepath.Join(dir, "dexaflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatalf("loadProjectConfig: %v", err)
	}
	if cfg.DagID != "minimal" {
		t.Errorf("DagID: got %q, want %q", cfg.DagID, "minimal")
	}
	if cfg.SchemaVersion != "1.0" {
		t.Errorf("SchemaVersion default not applied: got %q", cfg.SchemaVersion)
	}
	if cfg.PythonVersion != "3.11" {
		t.Errorf("PythonVersion default not applied: got %q", cfg.PythonVersion)
	}
	if cfg.DagSource != "dag.py" {
		t.Errorf("DagSource default not applied: got %q", cfg.DagSource)
	}
	if !reflect.DeepEqual(cfg.ExcludePaths, []string{".git", "__pycache__", "*.pyc", ".venv", "venv"}) {
		t.Errorf("ExcludePaths default not applied: got %v", cfg.ExcludePaths)
	}
	if cfg.Build == nil || cfg.Build.Context != "." {
		t.Errorf("Build.Context default not applied: got %+v", cfg.Build)
	}
	if cfg.Registry == nil || cfg.Registry.AuthMethod != "docker_config" {
		t.Errorf("Registry default not applied: got %+v", cfg.Registry)
	}
}

// TestDagSourcePathUsesAppliedDefault asserts that dagSourcePath relies on the
// centralized default (DagSource set to "dag.py" by ApplyDefaults) instead of
// its own inline fallback — keeping defaults in one place per
// [[python-minimal-go-max]] / configuration-defaults-table doc.
func TestDagSourcePathUsesAppliedDefault(t *testing.T) {
	dir := t.TempDir()
	yaml := "dag_id: minimal\n"
	if err := os.WriteFile(filepath.Join(dir, "dexaflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatalf("loadProjectConfig: %v", err)
	}
	got := dagSourcePath(dir, cfg)
	want := filepath.Join(dir, "dag.py")
	if got != want {
		t.Errorf("dagSourcePath: got %q, want %q", got, want)
	}
}

// TestLoadProjectConfigRejectsDuplicateTaskID guards the YAML↔task binding: a
// duplicated task_id key in the tasks block is a copy-paste hazard, so parsing
// must reject it rather than silently keeping the last entry (ADR 0023).
func TestLoadProjectConfigRejectsDuplicateTaskID(t *testing.T) {
	dir := t.TempDir()
	yaml := strings.Join([]string{
		"dag_id: proj",
		"tasks:",
		"  transform:",
		"    retries: 1",
		"  transform:",
		"    retries: 2",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "dexaflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := loadProjectConfig(dir)
	if err == nil {
		t.Fatal("expected error for duplicate task_id key, got nil")
	}
	if !strings.Contains(err.Error(), "transform") && !strings.Contains(strings.ToLower(err.Error()), "already") {
		t.Errorf("error %q should flag the duplicate key", err.Error())
	}
}

// New installs get ~/dexaflow as their workspace. An install from before the
// rename keeps using the ~/leoflow it already has, so its DAG projects are not
// left behind; when both exist the new name wins.
func TestDefaultWorkspaceIn(t *testing.T) {
	mkdir := func(t *testing.T, dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name     string
		existing []string
		want     string
	}{
		{"fresh home", nil, "dexaflow"},
		{"install from before the rename", []string{"leoflow"}, "leoflow"},
		{"both exist", []string{"leoflow", "dexaflow"}, "dexaflow"},
		{"only the new one", []string{"dexaflow"}, "dexaflow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			for _, d := range tc.existing {
				mkdir(t, filepath.Join(home, d))
			}
			if got, want := defaultWorkspaceIn(home), filepath.Join(home, tc.want); got != want {
				t.Errorf("defaultWorkspaceIn = %q, want %q", got, want)
			}
		})
	}
}

// A file named ~/leoflow is not a workspace.
func TestDefaultWorkspaceInIgnoresAFileNamedLeoflow(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "leoflow"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := defaultWorkspaceIn(home), filepath.Join(home, "dexaflow"); got != want {
		t.Errorf("defaultWorkspaceIn = %q, want %q", got, want)
	}
}
