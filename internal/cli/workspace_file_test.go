package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	root := t.TempDir()
	writeTree(t, filepath.Join(root, legacyWorkspaceFileName), "targets: {old: {server: https://old}}\n")
	writeTree(t, filepath.Join(root, workspaceFileName), sampleWorkspace)
	ws, err := findWorkspace(root)
	if err != nil || ws == nil || filepath.Base(ws.File) != workspaceFileName {
		t.Fatalf("findWorkspace = %+v, %v; want dexaflow.workspace.yaml", ws, err)
	}
}

func TestFindWorkspaceNoneIsNil(t *testing.T) {
	t.Parallel()
	ws, err := findWorkspace(t.TempDir())
	if err != nil || ws != nil {
		t.Errorf("findWorkspace = %+v, %v; want nil, nil", ws, err)
	}
}

func TestWorkspaceFileRejectsBadContent(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"unknown key":             "targts: {}\n",
		"target without server":   "targets: {dev: {registry: x}}\n",
		"server without scheme":   "targets: {dev: {server: dev.example}}\n",
		"default names no target": "targets: {dev: {server: https://d}}\ndefault_target: prod\n",
		"bad target name":         "targets: {\"Dev Box\": {server: https://d}}\n",
		"credentials in the url":  "targets: {dev: {server: \"https://me:secret@d.example\"}}\n",
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestFindWorkspaceResolvesSymlinks(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	writeTree(t, filepath.Join(target, workspaceFileName), sampleWorkspace)
	link := filepath.Join(t.TempDir(), "via-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ws, err := findWorkspace(link)
	if err != nil || ws == nil {
		t.Fatalf("findWorkspace = %+v, %v", ws, err)
	}
	want, _ := filepath.EvalSymlinks(target)
	if ws.Root != want {
		t.Errorf("root = %q, want the resolved path %q", ws.Root, want)
	}
}

// TestFindWorkspaceIgnoresAFileOfAnotherUser is the trust boundary: a
// workspace file chooses servers and registries, so one another user planted
// in a parent folder (a shared /tmp, a multi-user host) is ignored, like git's
// safe.directory.
func TestFindWorkspaceIgnoresAFileOfAnotherUser(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("changing a file's owner needs root")
	}
	outer := t.TempDir()
	writeTree(t, filepath.Join(outer, workspaceFileName), sampleWorkspace)
	inner := filepath.Join(outer, "inner")
	writeTree(t, filepath.Join(inner, workspaceFileName), sampleWorkspace)
	if err := os.Chown(filepath.Join(inner, workspaceFileName), 4242, 4242); err != nil {
		t.Fatal(err)
	}
	ws, err := findWorkspace(inner)
	if err != nil {
		t.Fatal(err)
	}
	if ws == nil || ws.Root != outer {
		t.Errorf("workspace = %+v, want the inner file skipped and %s found", ws, outer)
	}
}

func TestEditWorkspaceKeepsBlankLines(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, workspaceFileName)
	writeTree(t, path, workspaceFileTemplate)
	if err := editWorkspaceFile(path, func(e *workspaceEditor) error {
		return e.setTarget("dev", workspaceTarget{Server: "https://dev.example"})
	}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "\n\n# Named control planes") {
		t.Errorf("blank lines lost:\n%s", data)
	}
	if strings.Contains(string(data), blankLineMarker) {
		t.Errorf("marker leaked:\n%s", data)
	}
}

// foreignWorkspace writes a workspace file owned by another user, under a
// parent with no workspace of its own, and returns its root.
func foreignWorkspace(t *testing.T) string {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("changing a file's owner needs root")
	}
	root := t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), sampleWorkspace)
	if err := os.Chown(filepath.Join(root, workspaceFileName), 4242, 4242); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTrustedWorkspacesFromTheEnvironment(t *testing.T) {
	isolateCLIEnv(t)
	root := foreignWorkspace(t)
	if ws, _ := findWorkspace(root); ws != nil {
		t.Fatal("untrusted by default")
	}
	for name, value := range map[string]string{
		"the path":           root,
		"a list":             "/nowhere" + string(os.PathListSeparator) + root,
		"every workspace":    "*",
		"a path with a link": root + string(filepath.Separator) + ".",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DEXAFLOW_TRUSTED_WORKSPACES", value)
			if ws, err := findWorkspace(root); err != nil || ws == nil {
				t.Errorf("DEXAFLOW_TRUSTED_WORKSPACES=%q: workspace = %+v, %v", value, ws, err)
			}
		})
	}
	t.Run("legacy name", func(t *testing.T) {
		t.Setenv("LEOFLOW_TRUSTED_WORKSPACES", root)
		if ws, _ := findWorkspace(root); ws == nil {
			t.Error("LEOFLOW_TRUSTED_WORKSPACES must work too")
		}
	})
	t.Run("another path does not trust it", func(t *testing.T) {
		t.Setenv("DEXAFLOW_TRUSTED_WORKSPACES", t.TempDir())
		if ws, _ := findWorkspace(root); ws != nil {
			t.Error("trusting another folder must not trust this one")
		}
	})
}

func TestTrustedWorkspacesFromTheUserConfig(t *testing.T) {
	isolateCLIEnv(t)
	root := foreignWorkspace(t)
	cfg, err := config.DefaultConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	writeTree(t, cfg, "trusted_workspaces:\n  - "+root+"\n")
	if ws, err := findWorkspace(root); err != nil || ws == nil {
		t.Errorf("trusted_workspaces in %s: workspace = %+v, %v", cfg, ws, err)
	}
}
