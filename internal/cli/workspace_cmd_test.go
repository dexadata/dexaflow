package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceInitWritesTheLayout(t *testing.T) {
	isolateCLIEnv(t)
	parent := t.TempDir()
	t.Chdir(parent)
	out, _, err := run(t, "workspace", "init", "acme-dags", "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	root := filepath.Join(parent, "acme-dags")
	for _, p := range []string{workspaceFileName, ".gitignore", filepath.Join("dags", ".gitkeep")} {
		if !fileExists(filepath.Join(root, p)) {
			t.Errorf("%s was not written", p)
		}
	}
	ws, err := findWorkspace(root)
	if err != nil || ws == nil {
		t.Fatalf("the written file must be a valid workspace: %+v, %v", ws, err)
	}
	if isDirPath(filepath.Join(root, ".git")) {
		t.Error("git init happens only with --git")
	}
	for _, want := range []string{"dexaflow target add", "dexaflow new"} {
		if !strings.Contains(out, want) {
			t.Errorf("next steps lack %q:\n%s", want, out)
		}
	}
	if _, _, err := run(t, "workspace", "init", "acme-dags", "--config", emptyConfig(t)); err == nil {
		t.Error("a second init over an existing workspace must refuse")
	}
}

func TestWorkspaceInitKeepsAnExistingGitignore(t *testing.T) {
	isolateCLIEnv(t)
	root := t.TempDir()
	writeTree(t, filepath.Join(root, ".gitignore"), "secrets.env\n")
	t.Chdir(root)
	if _, _, err := run(t, "workspace", "init", "--config", emptyConfig(t)); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.Contains(string(data), "secrets.env") || !strings.Contains(string(data), "dag.json") {
		t.Errorf(".gitignore = %q, want the old line kept and dag.json added", data)
	}
}

func TestWorkspaceInitGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	isolateCLIEnv(t)
	root := filepath.Join(t.TempDir(), "ws")
	if _, _, err := run(t, "workspace", "init", root, "--git", "--config", emptyConfig(t)); err != nil {
		t.Fatalf("workspace init --git: %v", err)
	}
	if !isDirPath(filepath.Join(root, ".git")) {
		t.Error("--git must create a repository")
	}
}

func TestNewScaffoldsUnderDags(t *testing.T) {
	isolateCLIEnv(t)
	root := targetWorkspace(t, "https://dev.example", "https://prod.example")
	t.Chdir(filepath.Join(root, "dags", "orders"))
	cfg := emptyConfig(t)
	out, _, err := run(t, "new", "churn_model", "--config", cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	dir := filepath.Join(root, "dags", "churn_model")
	cfgYAML, err := os.ReadFile(filepath.Join(dir, projectConfigFile))
	if err != nil || !strings.Contains(string(cfgYAML), "dag_id: churn_model") {
		t.Fatalf("dexaflow.yaml = %q, %v", cfgYAML, err)
	}
	if !fileExists(filepath.Join(dir, "dag.py")) {
		t.Error("dag.py was not written")
	}
	if !strings.Contains(out, "dev") || !strings.Contains(out, "https://dev.example") || !strings.Contains(out, "dexaflow deploy churn_model") {
		t.Errorf("next steps must name the active target:\n%s", out)
	}
	if strings.Contains(out, "dexaflow lite churn_model") {
		t.Errorf("next steps point Lite at one folder, not the workspace:\n%s", out)
	}
}

func TestNewNeverOverwritesWithoutForce(t *testing.T) {
	isolateCLIEnv(t)
	root := targetWorkspace(t, "https://dev.example", "https://prod.example")
	t.Chdir(root)
	dag := filepath.Join(root, "dags", "orders", "dag.py")
	writeTree(t, dag, "# my work\n")
	_, _, err := run(t, "new", "orders", "--config", emptyConfig(t))
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a refusal naming --force", err)
	}
	if data, _ := os.ReadFile(dag); string(data) != "# my work\n" {
		t.Errorf("dag.py was overwritten: %q", data)
	}
	if fileExists(filepath.Join(root, "dags", "orders", projectConfigFile)) {
		t.Error("a refused scaffold must write nothing")
	}
	if _, _, err := run(t, "new", "orders", "--force", "--config", emptyConfig(t)); err != nil {
		t.Errorf("new --force: %v", err)
	}
}

func TestNewRefusesADuplicateDagID(t *testing.T) {
	isolateCLIEnv(t)
	root := targetWorkspace(t, "https://dev.example", "https://prod.example")
	writeTree(t, filepath.Join(root, "dags", "legacy", projectConfigFile), "dag_id: orders\n")
	writeTree(t, filepath.Join(root, "dags", "legacy", "dag.py"), "")
	t.Chdir(root)
	if _, _, err := run(t, "new", "orders", "--config", emptyConfig(t)); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Errorf("err = %v, want the existing project named", err)
	}
}

func TestNewOutsideAWorkspaceExplains(t *testing.T) {
	isolateCLIEnv(t)
	t.Chdir(t.TempDir())
	_, _, err := run(t, "new", "orders", "--config", emptyConfig(t))
	if err == nil || !strings.Contains(err.Error(), "workspace init") || !strings.Contains(err.Error(), "dexaflow init") {
		t.Errorf("err = %v, want both ways forward", err)
	}
}

func TestNewRejectsAnInvalidDagID(t *testing.T) {
	isolateCLIEnv(t)
	t.Chdir(targetWorkspace(t, "https://dev.example", "https://prod.example"))
	if _, _, err := run(t, "new", "../escape", "--config", emptyConfig(t)); err == nil {
		t.Error("want an error for a name that is not a dag_id")
	}
}

func TestWorkspaceInitRefusesANestedWorkspace(t *testing.T) {
	isolateCLIEnv(t)
	outer := t.TempDir()
	writeTree(t, filepath.Join(outer, workspaceFileName), workspaceFileTemplate)
	t.Chdir(outer)
	_, _, err := run(t, "workspace", "init", "inner", "--config", emptyConfig(t))
	if err == nil || !strings.Contains(err.Error(), outer) {
		t.Fatalf("err = %v, want a refusal naming the outer workspace", err)
	}
	if _, _, err := run(t, "workspace", "init", "inner", "--force", "--config", emptyConfig(t)); err != nil {
		t.Errorf("--force: %v", err)
	}
}
