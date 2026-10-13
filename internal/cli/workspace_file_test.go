package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeTree(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const sampleWorkspace = `schema_version: "1.0"
# shared with the team
targets:
  dev:
    server: https://dev.example
    registry: ghcr.io/acme/dev
  prod:
    server: https://prod.example
default_target: dev
defaults:
  registry: ghcr.io/acme
  platforms: [linux/arm64]
  base_image: ghcr.io/acme/base:1
`

func TestFindWorkspaceWalksUpFromASubfolder(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), sampleWorkspace)
	deep := filepath.Join(root, "dags", "orders", "sql")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	ws, err := findWorkspace(deep)
	if err != nil {
		t.Fatalf("findWorkspace: %v", err)
	}
	if ws == nil || ws.Root != root {
		t.Fatalf("workspace = %+v, want root %s", ws, root)
	}
	if ws.Spec.DefaultTarget != "dev" || ws.Spec.Targets["prod"].Server != "https://prod.example" {
		t.Errorf("spec not parsed: %+v", ws.Spec)
	}
	if ws.Spec.Defaults.Registry != "ghcr.io/acme" || ws.Spec.Targets["dev"].Registry != "ghcr.io/acme/dev" {
		t.Errorf("registry values not parsed: %+v", ws.Spec)
	}
}

func TestFindWorkspaceAcceptsTheLegacyName(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, legacyWorkspaceFileName), sampleWorkspace)
	ws, err := findWorkspace(root)
	if err != nil || ws == nil {
		t.Fatalf("findWorkspace = %+v, %v; leoflow.workspace.yaml must be found", ws, err)
	}
	if filepath.Base(ws.File) != legacyWorkspaceFileName {
		t.Errorf("file = %s", ws.File)
	}
}

func TestFindWorkspacePrefersTheCurrentName(t *testing.T) {
	root := t.TempDir()
	writeTree(t, filepath.Join(root, legacyWorkspaceFileName), "targets: {old: {server: https://old}}\n")
	writeTree(t, filepath.Join(root, workspaceFileName), sampleWorkspace)
	ws, err := findWorkspace(root)
	if err != nil || ws == nil || filepath.Base(ws.File) != workspaceFileName {
		t.Fatalf("findWorkspace = %+v, %v; want dexaflow.workspace.yaml", ws, err)
	}
}

func TestFindWorkspaceNoneIsNil(t *testing.T) {
	ws, err := findWorkspace(t.TempDir())
	if err != nil || ws != nil {
		t.Errorf("findWorkspace = %+v, %v; want nil, nil", ws, err)
	}
}

func TestWorkspaceFileRejectsBadContent(t *testing.T) {
	cases := map[string]string{
		"unknown key":             "targts: {}\n",
		"target without server":   "targets: {dev: {registry: x}}\n",
		"server without scheme":   "targets: {dev: {server: dev.example}}\n",
		"default names no target": "targets: {dev: {server: https://d}}\ndefault_target: prod\n",
		"bad target name":         "targets: {\"Dev Box\": {server: https://d}}\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeTree(t, filepath.Join(root, workspaceFileName), content)
			_, err := findWorkspace(root)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), workspaceFileName) {
				t.Errorf("error must name the file, got %v", err)
			}
		})
	}
}

func TestEditWorkspaceTargetsKeepsComments(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, workspaceFileName)
	writeTree(t, path, sampleWorkspace)
	if err := editWorkspaceFile(path, func(e *workspaceEditor) error {
		if err := e.setTarget("staging", workspaceTarget{Server: "https://staging.example", deployDefaults: deployDefaults{Registry: "ghcr.io/acme/stg"}}); err != nil {
			return err
		}
		e.removeTarget("prod")
		e.setDefaultTarget("staging")
		return nil
	}); err != nil {
		t.Fatalf("editWorkspaceFile: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# shared with the team") {
		t.Errorf("comment lost:\n%s", data)
	}
	ws, err := findWorkspace(root)
	if err != nil {
		t.Fatalf("re-reading: %v\n%s", err, data)
	}
	if _, ok := ws.Spec.Targets["prod"]; ok {
		t.Error("prod was not removed")
	}
	if got := ws.Spec.Targets["staging"]; got == nil || got.Server != "https://staging.example" || got.Registry != "ghcr.io/acme/stg" {
		t.Errorf("staging = %+v", got)
	}
	if ws.Spec.DefaultTarget != "staging" || ws.Spec.Targets["dev"] == nil {
		t.Errorf("spec after edit = %+v", ws.Spec)
	}
}

func TestEditWorkspaceAddsToAnEmptyFlowMapping(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, workspaceFileName)
	writeTree(t, path, workspaceFileTemplate)
	if err := editWorkspaceFile(path, func(e *workspaceEditor) error {
		return e.setTarget("dev", workspaceTarget{Server: "https://dev.example"})
	}); err != nil {
		t.Fatalf("editWorkspaceFile: %v", err)
	}
	ws, err := findWorkspace(root)
	if err != nil || ws.Spec.Targets["dev"] == nil {
		data, _ := os.ReadFile(path)
		t.Fatalf("dev not added (%v):\n%s", err, data)
	}
}

func TestGitRootOf(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	got := gitRootOf(t.Context(), sub)
	want, _ := filepath.EvalSymlinks(root)
	if gotReal, _ := filepath.EvalSymlinks(got); gotReal != want {
		t.Errorf("gitRootOf = %q, want %q", got, want)
	}
	if got := gitRootOf(t.Context(), t.TempDir()); got != "" {
		t.Errorf("outside git: %q, want empty", got)
	}
}
