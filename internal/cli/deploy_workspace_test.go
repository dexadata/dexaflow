package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// plainWorkspace is a workspace with no registry anywhere, holding the
// project dags/orders, so a deploy stops at the registry guard before any
// builder runs.
func plainWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), "targets: {}\n")
	if _, _, err := run(t, "init", filepath.Join(root, "dags", "orders")); err != nil {
		t.Fatalf("init: %v", err)
	}
	return root
}

// TestDeployAllReadsTheWorkspaceAroundTheCwd is F8: deploy --all inside a
// repository read ~/dexaflow (or the config's workspace) instead.
func TestDeployAllReadsTheWorkspaceAroundTheCwd(t *testing.T) {
	isolateCLIEnv(t)
	root := plainWorkspace(t)
	elsewhere := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeTree(t, cfg, "workspace: "+elsewhere+"\n")
	t.Chdir(filepath.Join(root, "dags"))
	out, _, err := run(t, "deploy", "--all", "--config", cfg, "--server", "http://127.0.0.1:1")
	if err == nil {
		t.Fatal("the registry-less project must fail")
	}
	if !strings.Contains(out, filepath.Join(root, "dags", "orders")) {
		t.Errorf("deploy --all did not reach the workspace's project:\n%s\nerr: %v", out, err)
	}
	if !strings.Contains(out, "workspace: "+root+" (from "+filepath.Join(root, workspaceFileName)+")") {
		t.Errorf("deploy --all must say which workspace it read and why:\n%s", out)
	}
}

func TestDeployByDagIDReadsTheWorkspaceAroundTheCwd(t *testing.T) {
	isolateCLIEnv(t)
	root := plainWorkspace(t)
	t.Chdir(root)
	_, _, err := run(t, "deploy", "orders", "--config", emptyConfig(t), "--server", "http://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "registry") {
		t.Errorf("want the registry guard of dags/orders, got %v", err)
	}
}

func TestDeployByDagIDUsesTheGitRootWithoutAWorkspaceFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolateCLIEnv(t)
	root := t.TempDir()
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if _, _, err := run(t, "init", filepath.Join(root, "pipelines", "orders")); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Chdir(filepath.Join(root, "pipelines"))
	_, _, err := run(t, "deploy", "orders", "--config", emptyConfig(t), "--server", "http://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "registry") {
		t.Errorf("want the registry guard of the repository's project (not lstat ~/dexaflow), got %v", err)
	}
}

func TestBareDeployAtAWorkspaceRootDeploysItsDAGs(t *testing.T) {
	isolateCLIEnv(t)
	root := plainWorkspace(t)
	t.Chdir(root)
	out, _, err := run(t, "deploy", "--config", emptyConfig(t), "--server", "http://127.0.0.1:1")
	if err == nil {
		t.Fatal("the registry-less project must fail")
	}
	if !strings.Contains(out, "deploying orders") {
		t.Errorf("a bare deploy at the workspace root must deploy its DAGs:\n%s\nerr: %v", out, err)
	}
}

func TestLiteWorkspaceIsTheWorkspaceFileRoot(t *testing.T) {
	isolateCLIEnv(t)
	root := plainWorkspace(t)
	t.Chdir(filepath.Join(root, "dags", "orders"))
	cmd := newLiteCommand()
	if got := liteWorkspace(cmd); got != root {
		t.Errorf("liteWorkspace = %q, want the workspace root %q", got, root)
	}
	t.Chdir(t.TempDir())
	if got := liteWorkspace(cmd); got != defaultWorkspace(cmd) {
		t.Errorf("outside a workspace, lite keeps its default; got %q", got)
	}
}

// TestDeployAllDoesNotWidenToTheGitRoot: without a workspace file, --all
// never deploys every DAG of a repository it happens to run in.
func TestDeployAllDoesNotWidenToTheGitRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolateCLIEnv(t)
	root := t.TempDir()
	if out, err := exec.CommandContext(t.Context(), "git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if _, _, err := run(t, "init", filepath.Join(root, "pipelines", "orders")); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Chdir(filepath.Join(root, "pipelines"))
	out, _, err := run(t, "deploy", "--all", "--config", emptyConfig(t), "--server", "http://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "workspace init") {
		t.Errorf("err = %v, want a refusal pointing at `workspace init`", err)
	}
	if strings.Contains(out, "deploying orders") {
		t.Errorf("--all widened to the repository:\n%s", out)
	}
	t.Chdir(filepath.Join(root, "pipelines", "orders"))
	out, _, _ = run(t, "deploy", "--all", "--config", emptyConfig(t), "--server", "http://127.0.0.1:1")
	if !strings.Contains(out, "deploying orders") {
		t.Errorf("in a project folder, --all covers that project:\n%s", out)
	}
}

// TestBareDeployInAWorkspaceSubfolderRefuses: only the workspace root means
// "every DAG"; anywhere else a folder that is not a project is an error.
func TestBareDeployInAWorkspaceSubfolderRefuses(t *testing.T) {
	isolateCLIEnv(t)
	root := plainWorkspace(t)
	t.Chdir(filepath.Join(root, "dags"))
	out, _, err := run(t, "deploy", "--config", emptyConfig(t), "--server", "http://127.0.0.1:1")
	if err == nil || !strings.Contains(err.Error(), "--all") {
		t.Errorf("err = %v, want a not-a-project error suggesting --all", err)
	}
	if strings.Contains(out, "deploying orders") {
		t.Errorf("a bare deploy in a subfolder deployed the workspace:\n%s", out)
	}
}

func TestBareDeployAtAWorkspaceRootReachedThroughASymlink(t *testing.T) {
	isolateCLIEnv(t)
	root := plainWorkspace(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Chdir(link)
	out, _, _ := run(t, "deploy", "--config", emptyConfig(t), "--server", "http://127.0.0.1:1")
	if !strings.Contains(out, "deploying orders") {
		t.Errorf("the root reached through a link is still the root:\n%s", out)
	}
}
