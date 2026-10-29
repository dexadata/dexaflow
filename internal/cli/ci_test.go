package cli

import (
	"context"
	"errors"
	"flag"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"
)

// updateCIGolden rewrites testdata/ci from what `ci init` writes now:
// go test ./internal/cli -run TestCIInitGolden -update-ci-golden
var updateCIGolden = flag.Bool("update-ci-golden", false, "rewrite the golden files of dexaflow ci init")

// ciGoldenVersion is the CLI version the golden files pin.
const ciGoldenVersion = "v0.6.0"

// ciGoldenCommit stands for the commit of ciGoldenVersion, which the
// GitHub workflow pins the setup-dexaflow action to.
const ciGoldenCommit = "0123456789abcdef0123456789abcdef01234567"

// stubTagCommit makes the lookup of a release's commit answer
// ciGoldenCommit for ciGoldenVersion, and fail for any other tag, without
// the network, for the commands run under d.
func stubTagCommit(t *testing.T, d *cliDeps) *[]string {
	t.Helper()
	var asked []string
	d.tagCommit = func(_ context.Context, tag string) (string, error) {
		asked = append(asked, tag)
		if tag == ciGoldenVersion {
			return ciGoldenCommit, nil
		}
		return "", errors.New("no such tag")
	}
	return &asked
}

// ciGoldenImageDigest stands for the digest of the dexaflow-cli image of
// ciGoldenVersion, which the GitLab job runs in.
const ciGoldenImageDigest = "sha256:89abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567"

// stubImageDigest makes the lookup of the dexaflow-cli image answer
// ciGoldenImageDigest for any tag, without the network, for the commands
// run under d, and records the images it was asked for.
func stubImageDigest(t *testing.T, d *cliDeps) *[]string {
	t.Helper()
	var asked []string
	d.imageDigest = func(_ context.Context, image string) (string, error) {
		asked = append(asked, image)
		return ciGoldenImageDigest, nil
	}
	return &asked
}

// ciCase is one golden case: a workspace and the system it is generated for.
type ciCase struct {
	name     string
	system   string
	registry string
	// orders is the dexaflow.yaml of the orders project, beyond its dag_id.
	orders string
	// oidc points ci init at a server that offers a CI OIDC issuer for
	// every provider, so it writes the tokenless pipeline (ADR 0092).
	oidc bool
}

// ciCases are the golden cases. Each system gets the registry its users
// most often push to, so the login the template writes is the native one.
var ciCases = []ciCase{
	{name: "github", system: "github", registry: "ghcr.io/acme/dags"},
	{name: "gitlab", system: "gitlab", registry: "registry.gitlab.com/acme/dags"},
	{name: "gitlab-docker", system: "gitlab", registry: "registry.gitlab.com/acme/dags", orders: "system_packages: [libpq-dev]\n"},
	{name: "cloudbuild", system: "cloudbuild", registry: "us-central1-docker.pkg.dev/acme/dags"},
	{name: "generic", system: "generic", registry: "ghcr.io/acme/dags"},
	{name: "github-oidc", system: "github", registry: "ghcr.io/acme/dags", oidc: true},
	{name: "gitlab-oidc", system: "gitlab", registry: "registry.gitlab.com/acme/dags", oidc: true},
	{name: "cloudbuild-oidc", system: "cloudbuild", registry: "us-central1-docker.pkg.dev/acme/dags", oidc: true},
}

// ciWorkspace writes a workspace with two DAG projects, orders and billing,
// whose images go under registry, and returns its root.
func ciWorkspace(t *testing.T, registry, orders string) string {
	t.Helper()
	root := t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), "schema_version: \"1.0\"\ndefaults:\n  registry: "+registry+"\n")
	writeTree(t, filepath.Join(root, "dags", "orders", projectConfigFile), "dag_id: orders\ndependencies: [\"pandas==2.2.3\"]\n"+orders)
	writeTree(t, filepath.Join(root, "dags", "orders", "dag.py"), "# orders\n")
	writeTree(t, filepath.Join(root, "dags", "billing", projectConfigFile), "dag_id: billing\n")
	writeTree(t, filepath.Join(root, "dags", "billing", "dag.py"), "# billing\n")
	return root
}

// ciInit runs `dexaflow ci init` in root with the golden version pinned.
func ciInit(t *testing.T, root string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	d := testDeps()
	stubTagCommit(t, d)
	stubImageDigest(t, d)
	t.Chdir(root)
	full := append([]string{"ci", "init"}, args...)
	full = append(full, "--cli-version", ciGoldenVersion, "--config", emptyConfig(t))
	return runDeps(t, d, full...)
}

// ciWritten returns every file ci init added under root, by slash path,
// leaving out the fixture's own files.
func ciWritten(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p) //nolint:errcheck // p is under root
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "dags" || rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if rel == workspaceFileName {
			return nil
		}
		data, rerr := os.ReadFile(p) //nolint:gosec // a test file
		if rerr != nil {
			return rerr
		}
		out[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// ciGolden generates every golden case and returns the written files per
// case name, comparing them with testdata/ci (or rewriting it).
func ciGolden(t *testing.T) map[string]map[string]string {
	t.Helper()
	all := map[string]map[string]string{}
	for _, c := range ciCases {
		isolateCLIEnv(t)
		if c.oidc {
			t.Setenv("DEXAFLOW_SERVER_URL", newDeployDefaultsServer(t, http.StatusOK, ciOIDCOffer).URL)
		}
		root := ciWorkspace(t, c.registry, c.orders)
		if _, stderr, err := ciInit(t, root, c.system); err != nil {
			t.Fatalf("%s: ci init %s: %v\n%s", c.name, c.system, err, stderr)
		}
		all[c.name] = ciWritten(t, root)
	}
	return all
}

// TestCIInitGolden pins what `ci init` writes for each system: the files,
// their paths and every line of them.
func TestCIInitGolden(t *testing.T) {
	base, err := filepath.Abs(filepath.Join("testdata", "ci"))
	if err != nil {
		t.Fatal(err)
	}
	for name, files := range ciGolden(t) {
		dir := filepath.Join(base, name)
		if *updateCIGolden {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			for rel, content := range files {
				writeTree(t, filepath.Join(dir, filepath.FromSlash(rel)), content)
			}
			continue
		}
		want := map[string]string{}
		werr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(dir, p) //nolint:errcheck // p is under dir
			data, rerr := os.ReadFile(p)   //nolint:gosec // a golden file
			want[filepath.ToSlash(rel)] = string(data)
			return rerr
		})
		if werr != nil {
			t.Fatalf("%s: reading the golden files: %v (rewrite them with -update-ci-golden)", name, werr)
		}
		if got, exp := sortedKeys(files), sortedKeys(want); !slices.Equal(got, exp) {
			t.Errorf("%s: wrote %v, want %v", name, got, exp)
			continue
		}
		for rel, content := range files {
			if content != want[rel] {
				t.Errorf("%s: %s differs from the golden file (rewrite with -update-ci-golden after checking the change):\n%s", name, rel, content)
			}
		}
	}
}

// sortedKeys returns the keys of m, sorted.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// TestCIInitWritesWhereEachSystemReads checks the paths: each CI system
// reads its pipeline from a fixed file, and every pipeline calls the
// script.
func TestCIInitWritesWhereEachSystemReads(t *testing.T) {
	want := map[string][]string{
		"github":          {".github/workflows/dexaflow-deploy.yaml", ciScriptPath},
		"gitlab":          {".gitlab-ci.yml", ciScriptPath},
		"gitlab-docker":   {".gitlab-ci.yml", ciScriptPath},
		"cloudbuild":      {ciScriptPath, "cloudbuild.yaml"},
		"generic":         {ciScriptPath},
		"github-oidc":     {".github/workflows/dexaflow-deploy.yaml", ciScriptPath},
		"gitlab-oidc":     {".gitlab-ci.yml", ciScriptPath},
		"cloudbuild-oidc": {ciScriptPath, "cloudbuild.yaml"},
	}
	for name, files := range ciGolden(t) {
		if got := sortedKeys(files); !slices.Equal(got, want[name]) {
			t.Errorf("%s: wrote %v, want %v", name, got, want[name])
		}
		for rel, content := range files {
			if rel != ciScriptPath && !strings.Contains(content, "sh "+ciScriptPath) {
				t.Errorf("%s: %s does not run %s:\n%s", name, rel, ciScriptPath, content)
			}
		}
	}
}

// TestCIInitYAMLParses reads each generated pipeline as its CI system
// would, down to the keys that make it run.
func TestCIInitYAMLParses(t *testing.T) {
	for name, files := range ciGolden(t) {
		for rel, content := range files {
			if !strings.HasSuffix(rel, ".yaml") && !strings.HasSuffix(rel, ".yml") {
				continue
			}
			var doc map[string]any
			if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
				t.Errorf("%s: %s does not parse: %v", name, rel, err)
				continue
			}
			var key string
			switch rel {
			case ".github/workflows/dexaflow-deploy.yaml":
				key = "jobs"
			case ".gitlab-ci.yml":
				key = "dexaflow-deploy"
			case "cloudbuild.yaml":
				key = "steps"
			}
			if doc[key] == nil {
				t.Errorf("%s: %s has no %q:\n%s", name, rel, key, content)
			}
		}
	}
}

// TestCIInitScriptIsPOSIX parses the script with sh -n, which reads the
// whole script without running it.
func TestCIInitScriptIsPOSIX(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not installed")
	}
	for name, files := range ciGolden(t) {
		p := filepath.Join(t.TempDir(), "deploy.sh")
		writeTree(t, p, files[ciScriptPath])
		if out, rerr := exec.CommandContext(t.Context(), sh, "-n", p).CombinedOutput(); rerr != nil { //nolint:gosec // a test script
			t.Errorf("%s: sh -n: %v\n%s", name, rerr, out)
		}
	}
}

// TestCIInitFlagsExist runs the docs flag checker over the generated files:
// every `dexaflow <command> --flag` they hold must exist in the command
// tree, and the checker must actually see the deploy and token commands.
func TestCIInitFlagsExist(t *testing.T) {
	root := NewRootCommand()
	for name, files := range ciGolden(t) {
		docs := map[string][]byte{}
		var seen []string
		for rel, content := range files {
			md := []byte("```\n" + content + "\n```\n")
			docs[name+"/"+rel] = md
			for _, inv := range docInvocationsIn(rel, md) {
				seen = append(seen, strings.Join(inv.args, " "))
			}
		}
		if v := docFlagViolations(root, docs); len(v) > 0 {
			t.Errorf("%s: flags the CLI does not have:\n%s", name, strings.Join(v, "\n"))
		}
		joined := strings.Join(seen, "\n")
		wants := []string{"deploy --all --changed-since", "deploy --all --yes --output json", "auth create-token --scope deploy"}
		if strings.HasSuffix(name, "-oidc") {
			wants = append(wants, "auth ci-token")
		}
		for _, want := range wants {
			if !strings.Contains(joined, want) {
				t.Errorf("%s: the checker saw no %q in:\n%s", name, want, joined)
			}
		}
	}
}

// TestCIInitActionlint runs actionlint over the GitHub workflow when it is
// installed.
func TestCIInitActionlint(t *testing.T) {
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		t.Skip("actionlint not installed")
	}
	files := ciGolden(t)["github"]
	dir := t.TempDir()
	p := filepath.Join(dir, ".github", "workflows", "dexaflow-deploy.yaml")
	writeTree(t, p, files[".github/workflows/dexaflow-deploy.yaml"])
	if out, rerr := exec.CommandContext(t.Context(), bin, p).CombinedOutput(); rerr != nil { //nolint:gosec // a test binary
		t.Errorf("actionlint: %v\n%s", rerr, out)
	}
}

// TestCIInitRefusesAnExistingFile keeps a pipeline the user wrote: without
// --force the command names the file and writes nothing, and for GitLab,
// whose file holds every job of the project, it says to merge by hand.
func TestCIInitRefusesAnExistingFile(t *testing.T) {
	isolateCLIEnv(t)
	root := ciWorkspace(t, "registry.gitlab.com/acme/dags", "")
	writeTree(t, filepath.Join(root, ".gitlab-ci.yml"), "test:\n  script: [make test]\n")
	_, _, err := ciInit(t, root, "gitlab")
	if err == nil || !strings.Contains(err.Error(), ".gitlab-ci.yml") || !strings.Contains(err.Error(), "merge") {
		t.Fatalf("err = %v, want a refusal naming .gitlab-ci.yml and saying to merge by hand", err)
	}
	if fileExists(filepath.Join(root, ciScriptPath)) {
		t.Error("a refused init must write nothing, the script included")
	}
	if _, _, err := ciInit(t, root, "gitlab", "--force"); err != nil {
		t.Fatalf("--force: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".gitlab-ci.yml")) //nolint:errcheck // checked below
	if !strings.Contains(string(data), "dexaflow-deploy:") {
		t.Errorf("--force did not overwrite:\n%s", data)
	}
}

// TestCIInitRefusesAChangedScript keeps an edited script, and leaves one
// that is already what it would write.
func TestCIInitRefusesAChangedScript(t *testing.T) {
	isolateCLIEnv(t)
	root := ciWorkspace(t, "ghcr.io/acme/dags", "")
	if _, _, err := ciInit(t, root, "generic"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ciInit(t, root, "github"); err != nil {
		t.Fatalf("an unchanged script must not block another system: %v", err)
	}
	writeTree(t, filepath.Join(root, ciScriptPath), "#!/bin/sh\necho mine\n")
	_, _, err := ciInit(t, root, "generic")
	if err == nil || !strings.Contains(err.Error(), ciScriptPath) || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want a refusal naming %s and --force", err, ciScriptPath)
	}
}

// TestCIInitStdout prints instead of writing.
func TestCIInitStdout(t *testing.T) {
	isolateCLIEnv(t)
	root := ciWorkspace(t, "ghcr.io/acme/dags", "")
	out, _, err := ciInit(t, root, "github", "--stdout")
	if err != nil {
		t.Fatal(err)
	}
	if len(ciWritten(t, root)) != 0 {
		t.Error("--stdout must write no file")
	}
	for _, want := range []string{"# file: .github/workflows/dexaflow-deploy.yaml", "# file: " + ciScriptPath, "docker/login-action"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

// TestCIInitBranch puts --branch in the trigger.
func TestCIInitBranch(t *testing.T) {
	isolateCLIEnv(t)
	root := ciWorkspace(t, "registry.gitlab.com/acme/dags", "")
	out, _, err := ciInit(t, root, "gitlab", "--stdout", "--branch", "release")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `$CI_COMMIT_BRANCH == "release"`) || strings.Contains(out, `== "main"`) {
		t.Errorf("the rule does not follow --branch:\n%s", out)
	}
	if _, _, err := ciInit(t, root, "gitlab", "--stdout", "--branch", "main; rm -rf /"); err == nil {
		t.Error("a branch name a pipeline would run as shell must be refused")
	}
}

// TestCIInitTarget deploys to the workspace's target, whose URL is in the
// committed workspace file, so the pipeline needs no DEXAFLOW_SERVER_URL.
func TestCIInitTarget(t *testing.T) {
	isolateCLIEnv(t)
	root := targetWorkspace(t, "https://dev.example", "https://prod.example")
	writeTree(t, filepath.Join(root, "dags", "orders", "dag.py"), "# orders\n")
	out, _, err := ciInit(t, root, "github", "--stdout", "--target", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `: "${DEXAFLOW_TARGET:=${LEOFLOW_TARGET:-prod}}"`) {
		t.Errorf("the script does not select target prod:\n%s", out)
	}
	if strings.Contains(out, "vars.DEXAFLOW_SERVER_URL") {
		t.Errorf("with a target the workflow must not read DEXAFLOW_SERVER_URL:\n%s", out)
	}
	out, _, err = ciInit(t, root, "github", "--stdout")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `: "${DEXAFLOW_TARGET:=${LEOFLOW_TARGET:-dev}}"`) {
		t.Errorf("without --target the workspace's default_target applies:\n%s", out)
	}
}

// TestCIInitNeedsAReleaseVersion refuses to pin a CLI that is not a
// release: a pipeline cannot install a dev build.
func TestCIInitNeedsAReleaseVersion(t *testing.T) {
	isolateCLIEnv(t)
	root := ciWorkspace(t, "ghcr.io/acme/dags", "")
	t.Chdir(root)
	_, _, err := run(t, "ci", "init", "github", "--config", emptyConfig(t))
	if err == nil || !strings.Contains(err.Error(), "--cli-version") {
		t.Fatalf("err = %v, want a refusal that names --cli-version", err)
	}
	if _, _, err = run(t, "ci", "init", "github", "--cli-version", "latest", "--config", emptyConfig(t)); err == nil {
		t.Error("--cli-version must be a release tag")
	}
	// install.sh reads DEXAFLOW_NO_PATH and DEXAFLOW_INSTALL_DIR from v0.5.0.
	_, _, err = run(t, "ci", "init", "github", "--cli-version", "v0.4.8", "--config", emptyConfig(t))
	if err == nil || !strings.Contains(err.Error(), "v0.5.0") {
		t.Errorf("err = %v, want a refusal of a release older than v0.5.0", err)
	}
	if _, _, err = run(t, "ci", "init", "generic", "--cli-version", "v0.5.0", "--stdout", "--config", emptyConfig(t)); err != nil {
		t.Errorf("v0.5.0 must be accepted: %v", err)
	}
	// The setup-dexaflow action ships from v0.5.4.
	_, _, err = run(t, "ci", "init", "github", "--cli-version", "v0.5.3", "--stdout", "--config", emptyConfig(t))
	if err == nil || !strings.Contains(err.Error(), "v0.5.4") {
		t.Errorf("err = %v, want a refusal of a release without the setup-dexaflow action", err)
	}
}

// TestCIInitNeedsAWorkspace refuses a folder that is not in a workspace.
func TestCIInitNeedsAWorkspace(t *testing.T) {
	isolateCLIEnv(t)
	_, _, err := ciInit(t, t.TempDir(), "github")
	if err == nil || !strings.Contains(err.Error(), "dexaflow workspace init") {
		t.Fatalf("err = %v, want a refusal pointing at workspace init", err)
	}
}

// TestCIInitUnknownSystem names the systems it knows.
func TestCIInitUnknownSystem(t *testing.T) {
	isolateCLIEnv(t)
	root := ciWorkspace(t, "ghcr.io/acme/dags", "")
	_, _, err := ciInit(t, root, "jenkins")
	if err == nil || !strings.Contains(err.Error(), "github, gitlab, cloudbuild, generic") {
		t.Fatalf("err = %v, want the known systems", err)
	}
}

// TestCIInitWorkspaceInASubdirectory writes the pipeline at the top of the
// git repository, where the CI system reads it, and the script deploys
// from the workspace folder.
func TestCIInitWorkspaceInASubdirectory(t *testing.T) {
	requireGit(t)
	isolateCLIEnv(t)
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q")
	ws := ciWorkspace(t, "ghcr.io/acme/dags", "")
	sub := filepath.Join(repo, "platform", "dags-ws")
	if err := os.MkdirAll(filepath.Dir(sub), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ws, sub); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ciInit(t, filepath.Join(sub, "dags"), "github"); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(repo, ciScriptPath))
	if err != nil {
		t.Fatalf("the script is not at the top of the repository: %v", err)
	}
	if !strings.Contains(string(script), `workspace_dir="platform/dags-ws"`) {
		t.Errorf("the script does not deploy from the workspace folder:\n%s", script)
	}
	if !fileExists(filepath.Join(repo, ".github", "workflows", "dexaflow-deploy.yaml")) {
		t.Error("the workflow is not at the top of the repository")
	}
}

// requireGit skips the test without git.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// fakeDexaflow is a dexaflow on PATH that answers version as the pinned
// release and records the arguments of deploy, printing a report.
const fakeDexaflow = `#!/bin/sh
case "$1" in
version) echo "dexaflow 0.6.0 (commit none, built unknown, go)";;
deploy)
	echo "$*" >>"$FAKE_LOG"
	printf '{"server":"https://pro.example","dags":[{"dag_id":"orders","status":"registered","version":"v1-abc","image":"ghcr.io/acme/dags/orders@sha256:1"}],"removed":[{"path":"dags/old","dag_id":"old","reason":"removed"}]}\n'
	exit "${FAKE_STATUS:-0}";;
esac
`

// TestCIScriptDeploysWhatChanged runs the generated script against a fake
// CLI: the base ref selects --changed-since, an empty or all-zero one (a
// new branch) and one the clone lacks deploy everything, a missing token
// stops before any deploy, the summary lands in DEXAFLOW_SUMMARY_FILE and
// a failed deploy fails the script.
func TestCIScriptDeploysWhatChanged(t *testing.T) {
	requireGit(t)
	for _, bin := range []string{"sh", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	isolateCLIEnv(t)
	root := ciWorkspace(t, "ghcr.io/acme/dags", "")
	if _, _, err := ciInit(t, root, "generic"); err != nil {
		t.Fatal(err)
	}
	gitIn(t, root, "init", "-q")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-qm", "first")
	head := gitIn(t, root, "rev-parse", "HEAD")

	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "dexaflow"), fakeDexaflow)
	writeExecutable(t, filepath.Join(bin, "uv"), "#!/bin/sh\necho uv 0.12.24\n")
	runScript := func(base, token, status string, extra ...string) (log, summary, output string, err error) {
		dir := t.TempDir()
		logPath, sumPath := filepath.Join(dir, "log"), filepath.Join(dir, "summary.md")
		c := exec.CommandContext(t.Context(), "sh", filepath.Join(root, ciScriptPath)) //nolint:gosec // the script under test
		c.Dir = t.TempDir()
		c.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"FAKE_LOG="+logPath, "FAKE_STATUS="+status, "DEXAFLOW_BASE_REF="+base,
			"DEXAFLOW_TOKEN="+token, "DEXAFLOW_SERVER_URL=https://pro.example",
			"DEXAFLOW_SUMMARY_FILE="+sumPath, "GITHUB_STEP_SUMMARY=")
		c.Env = append(c.Env, extra...)
		out, err := c.CombinedOutput()
		l, _ := os.ReadFile(logPath) //nolint:errcheck // absent when nothing ran
		s, _ := os.ReadFile(sumPath) //nolint:errcheck // absent when nothing ran
		if err != nil {
			err = &scriptError{err: err, out: string(out)}
		}
		return strings.TrimSpace(string(l)), string(s), string(out), err
	}

	cases := []struct{ base, want, note string }{
		{head, "deploy --all --changed-since " + head + " --yes --output json", ""},
		{"", "deploy --all --yes --output json", ""},
		{"0000000000000000000000000000000000000000", "deploy --all --yes --output json", ""},
		{"1234567890abcdef1234567890abcdef12345678", "deploy --all --yes --output json", "is not in this clone"},
	}
	for _, c := range cases {
		log, summary, output, err := runScript(c.base, "dxf_x", "0")
		if err != nil {
			t.Fatalf("base %q: %v", c.base, err)
		}
		if log != c.want {
			t.Errorf("base %q: ran %q, want %q", c.base, log, c.want)
		}
		// A new branch is not a lost commit: only a ref the clone lacks
		// gets the note.
		if got := strings.Contains(output, "is not in this clone"); got != (c.note != "") {
			t.Errorf("base %q: output %q, want the missing-commit note only for a commit the clone lacks", c.base, output)
		}
		for _, want := range []string{"| orders | registered | v1-abc |", "old", "dexaflow dags delete old --deregister"} {
			if !strings.Contains(summary, want) {
				t.Errorf("base %q: summary lacks %q:\n%s", c.base, want, summary)
			}
		}
	}
	if log, _, _, err := runScript(head, "", "0"); err == nil || log != "" {
		t.Errorf("without DEXAFLOW_TOKEN the script must stop before deploying: err %v, ran %q", err, log)
	}
	_, summary, _, err := runScript(head, "dxf_x", "3")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(summary, "orders") {
		t.Errorf("a failed deploy must fail the script with its exit status (3) and still write the summary: err %v, summary %q", err, summary)
	}
	// The pre-rename names still work, as everywhere in the CLI.
	if log, _, _, err := runScript(head, "", "0", "LEOFLOW_TOKEN=dxf_old", "DEXAFLOW_SERVER_URL=", "LEOFLOW_SERVER_URL=https://old.example"); err != nil || log == "" {
		t.Errorf("LEOFLOW_TOKEN and LEOFLOW_SERVER_URL must be accepted: err %v, ran %q", err, log)
	}
	// DEXAFLOW_CLI_VERSION ends up in a URL and a shell command: only a
	// release tag is taken.
	if log, _, output, err := runScript(head, "dxf_x", "0", "DEXAFLOW_CLI_VERSION=v0.6.0;id"); err == nil || log != "" ||
		!strings.Contains(output, "is not a release tag") {
		t.Errorf("a DEXAFLOW_CLI_VERSION that is not a release tag must stop the script before anything runs: err %v, ran %q, output %q", err, log, output)
	}
}

// scriptError carries the output of a failed script run.
type scriptError struct {
	err error
	out string
}

func (e *scriptError) Error() string { return e.err.Error() + "\n" + e.out }

// Unwrap returns the error of the run, an *exec.ExitError for a failed script.
func (e *scriptError) Unwrap() error { return e.err }

// ciDocBlockRe finds a fenced block of the docs marked as the output of a
// golden case: an HTML comment naming the case, then a block titled with
// the file path.
var ciDocBlockRe = regexp.MustCompile("(?s)<!-- ci-init: ([a-z-]+) -->\n```(?:yaml|sh) title=\"([^\"]+)\"\n(.*?)```")

// TestCIRecipesMatchGenerated keeps the recipes of the CI/CD page equal to
// what `ci init` writes: each block marked <!-- ci-init: <case> --> must be
// the golden file of that case, with the version as vX.Y.Z.
func TestCIRecipesMatchGenerated(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "website", "content", "operate", "cicd-deploy.md"))
	if err != nil {
		t.Fatal(err)
	}
	golden := ciGolden(t)
	blocks := ciDocBlockRe.FindAllStringSubmatch(string(page), -1)
	seen := map[string]bool{}
	for _, b := range blocks {
		name, path, body := b[1], b[2], b[3]
		files, ok := golden[name]
		if !ok {
			t.Errorf("block %s names an unknown case %q", path, name)
			continue
		}
		want := strings.NewReplacer(ciGoldenVersion, "vX.Y.Z", ciGoldenCommit, "<sha>", ciGoldenImageDigest, "sha256:<digest>").Replace(files[path])
		if want == "" {
			t.Errorf("case %s writes no %s", name, path)
			continue
		}
		if body != want {
			t.Errorf("the recipe %s (%s) in cicd-deploy.md is not what ci init writes; paste testdata/ci/%s/%s with %s as vX.Y.Z, %s as <sha> and %s as sha256:<digest>:\n%s",
				path, name, name, path, ciGoldenVersion, ciGoldenCommit, ciGoldenImageDigest, body)
		}
		seen[name+"/"+path] = true
	}
	for _, want := range []string{"github/.github/workflows/dexaflow-deploy.yaml", "gitlab/.gitlab-ci.yml", "cloudbuild/cloudbuild.yaml", "generic/" + ciScriptPath} {
		if !seen[want] {
			t.Errorf("cicd-deploy.md shows no generated %s", want)
		}
	}
}

// TestCIInitGitHubRegistryLogins writes the native login of each registry
// the projects push to: workload identity for Artifact Registry, an IAM role
// per ECR region, a stored credential for any other registry, and no
// packages permission when nothing goes to ghcr.io.
func TestCIInitGitHubRegistryLogins(t *testing.T) {
	isolateCLIEnv(t)
	root := ciWorkspace(t, "us-docker.pkg.dev/acme/dags", "")
	writeTree(t, filepath.Join(root, "dags", "billing", projectConfigFile),
		"dag_id: billing\nregistry:\n  url: 123456789012.dkr.ecr.eu-west-1.amazonaws.com/dags\n")
	writeTree(t, filepath.Join(root, "dags", "ledger", projectConfigFile),
		"dag_id: ledger\nregistry:\n  url: harbor.example.com/dags\n")
	writeTree(t, filepath.Join(root, "dags", "ledger", "dag.py"), "# ledger\n")
	out, _, err := ciInit(t, root, "github", "--stdout")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"google-github-actions/auth@", "aws-region: eu-west-1",
		"registry: harbor.example.com", "secrets.DEXAFLOW_REGISTRY_PASSWORD", "id-token: write"} {
		if !strings.Contains(out, want) {
			t.Errorf("the workflow lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "packages: write") || strings.Contains(out, "registry: ghcr.io") {
		t.Errorf("no project pushes to ghcr.io:\n%s", out)
	}
	workflow, _, _ := strings.Cut(strings.TrimPrefix(out, "# file: .github/workflows/dexaflow-deploy.yaml\n"), "\n# file: ")
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(workflow), &doc); err != nil {
		t.Errorf("the workflow does not parse: %v\n%s", err, workflow)
	}
}

// TestCIInitShellcheck runs shellcheck over the script when it is
// installed.
func TestCIInitShellcheck(t *testing.T) {
	bin, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck not installed")
	}
	p := filepath.Join(t.TempDir(), "deploy.sh")
	writeTree(t, p, ciGolden(t)["generic"][ciScriptPath])
	if out, rerr := exec.CommandContext(t.Context(), bin, "-s", "sh", p).CombinedOutput(); rerr != nil { //nolint:gosec // a test binary
		t.Errorf("shellcheck: %v\n%s", rerr, out)
	}
}

// ciUsesRe matches a `uses:` line pinned by commit with its version.
var ciUsesRe = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s+[\w.-]+/[\w./-]+@[0-9a-f]{40} # v\d`)

// TestCIInitPinsActions requires every action of the generated workflows
// to be pinned by commit SHA with its version in a comment: a tag can be
// moved to other code, a SHA cannot.
func TestCIInitPinsActions(t *testing.T) {
	isolateCLIEnv(t)
	root := ciWorkspace(t, "us-docker.pkg.dev/acme/dags", "")
	writeTree(t, filepath.Join(root, "dags", "billing", projectConfigFile),
		"dag_id: billing\nregistry:\n  url: 123456789012.dkr.ecr.eu-west-1.amazonaws.com/dags\n")
	writeTree(t, filepath.Join(root, "dags", "ledger", projectConfigFile), "dag_id: ledger\nregistry:\n  url: ghcr.io/acme\n")
	writeTree(t, filepath.Join(root, "dags", "ledger", "dag.py"), "# ledger\n")
	writeTree(t, filepath.Join(root, "dags", "audit", projectConfigFile), "dag_id: audit\nregistry:\n  url: harbor.example.com/dags\n")
	writeTree(t, filepath.Join(root, "dags", "audit", "dag.py"), "# audit\n")
	out, _, err := ciInit(t, root, "github", "--stdout")
	if err != nil {
		t.Fatal(err)
	}
	workflows := []string{out, ciGolden(t)["github"][".github/workflows/dexaflow-deploy.yaml"]}
	seen := 0
	for _, w := range workflows {
		for _, line := range strings.Split(w, "\n") {
			if !strings.Contains(line, "uses:") || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			seen++
			if !ciUsesRe.MatchString(line) {
				t.Errorf("not pinned by SHA with a version comment: %q", line)
			}
		}
	}
	if seen < 9 {
		t.Errorf("saw %d uses: lines, want every action of both workflows", seen)
	}
}

// TestCIInitGitHubGuards keeps a manual run to the deploy branch, runs
// the job in a GitHub environment (so DEXAFLOW_TOKEN can be an environment
// secret), keeps the checkout token out of the clone, and lets the job read
// the runs of the workflow.
func TestCIInitGitHubGuards(t *testing.T) {
	isolateCLIEnv(t)
	root := ciWorkspace(t, "ghcr.io/acme/dags", "")
	out, _, err := ciInit(t, root, "github", "--stdout", "--branch", "release", "--environment", "dags-prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"if: github.ref == 'refs/heads/release'", "environment: dags-prod",
		"persist-credentials: false", "actions: read", "steps.base.outputs.sha"} {
		if !strings.Contains(out, want) {
			t.Errorf("the workflow lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "github.event.before") {
		t.Errorf("the base must be the last successful deploy, not the previous push:\n%s", out)
	}
	if _, _, err := ciInit(t, root, "github", "--stdout", "--environment", "prod; x"); err == nil {
		t.Error("an environment name a workflow would have to quote must be refused")
	}
	golden := ciGolden(t)["github"][".github/workflows/dexaflow-deploy.yaml"]
	if !strings.Contains(golden, "environment: production") {
		t.Errorf("the default environment is production:\n%s", golden)
	}
}

// TestCIInitGitLabDefaultBranch follows the project's default branch
// unless --branch names one.
func TestCIInitGitLabDefaultBranch(t *testing.T) {
	golden := ciGolden(t)["gitlab"][".gitlab-ci.yml"]
	if !strings.Contains(golden, "$CI_COMMIT_BRANCH == $CI_DEFAULT_BRANCH") {
		t.Errorf("without --branch the rule follows $CI_DEFAULT_BRANCH:\n%s", golden)
	}
}

// fakeTool writes an executable named name into a new directory and
// returns the directory.
func fakeTool(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, name), script)
	return dir
}

// TestCIGitHubBaseIsTheLastSuccessfulDeploy runs the workflow's base step
// with a fake gh: it outputs the head SHA of the last successful run of the
// workflow on the branch, and nothing (deploy everything) when gh fails.
func TestCIGitHubBaseIsTheLastSuccessfulDeploy(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not installed")
	}
	var doc struct {
		Jobs map[string]struct {
			Steps []struct {
				ID  string            `yaml:"id"`
				Env map[string]string `yaml:"env"`
				Run string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(ciGolden(t)["github"][".github/workflows/dexaflow-deploy.yaml"]), &doc); err != nil {
		t.Fatal(err)
	}
	var run string
	var env map[string]string
	for _, s := range doc.Jobs["deploy"].Steps {
		if s.ID == "base" {
			run, env = s.Run, s.Env
		}
	}
	if run == "" || env["BRANCH"] != "main" {
		t.Fatalf("no base step with BRANCH=main in the workflow: %q %v", run, env)
	}
	for _, c := range []struct{ gh, want string }{
		{"#!/bin/sh\necho \"$*\" >>\"$GH_LOG\"\necho abc123\n", "sha=abc123"},
		{"#!/bin/sh\nexit 1\n", "sha="},
	} {
		dir := t.TempDir()
		out, log := filepath.Join(dir, "out"), filepath.Join(dir, "log")
		bin := fakeTool(t, "gh", c.gh)
		cmd := exec.CommandContext(t.Context(), "sh", "-e", "-c", run) //nolint:gosec // the step under test
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"GITHUB_OUTPUT="+out, "GITHUB_REPOSITORY=acme/dags", "BRANCH=main", "GH_LOG="+log)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("the base step failed: %v\n%s", err, b)
		}
		got, _ := os.ReadFile(out) //nolint:errcheck // checked below
		if strings.TrimSpace(string(got)) != c.want {
			t.Errorf("GITHUB_OUTPUT = %q, want %q", got, c.want)
		}
		if args, _ := os.ReadFile(log); len(args) > 0 { //nolint:errcheck // absent when gh failed
			for _, want := range []string{"run list", "--workflow dexaflow-deploy.yaml", "--branch main", "--status success", "--repo acme/dags"} {
				if !strings.Contains(string(args), want) {
					t.Errorf("gh %s lacks %q", args, want)
				}
			}
		}
	}
}

// TestCIGitLabBase runs the job's base snippet against a fake GitLab API:
// with DEXAFLOW_GITLAB_API_TOKEN the base is the last successful push
// pipeline, without it the previous commit of the push, and a failed API
// call deploys everything. The snippet asks with python3, which every image
// the job runs in has, since the dexaflow-cli image has no curl.
func TestCIGitLabBase(t *testing.T) {
	for _, bin := range []string{"sh", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " not installed")
		}
	}
	var doc map[string]struct {
		Script []string `yaml:"script"`
	}
	if err := yaml.Unmarshal([]byte(ciGolden(t)["gitlab"][".gitlab-ci.yml"]), &doc); err != nil {
		t.Fatal(err)
	}
	var snippet string
	for _, s := range doc["dexaflow-deploy"].Script {
		if strings.Contains(s, "DEXAFLOW_GITLAB_API_TOKEN") {
			snippet = s
		}
	}
	if snippet == "" {
		t.Fatal("no base snippet in the job")
	}
	for _, c := range []struct {
		token, body string
		status      int
		want        string
	}{
		{"glpat-x", `[{"sha":"feed01"}]`, http.StatusOK, "feed01"},
		{"", `[{"sha":"feed01"}]`, http.StatusOK, "before01"},
		{"glpat-x", `{"message":"401 Unauthorized"}`, http.StatusUnauthorized, ""},
		{"glpat-x", `[]`, http.StatusOK, ""},
	} {
		var asked []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asked = append(asked, r.URL.RequestURI()+" "+r.Header.Get("PRIVATE-TOKEN"))
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body)) //nolint:errcheck // a test server
		}))
		// A curl that fails loudly comes first on PATH: the snippet must not use it.
		bin := fakeTool(t, "curl", "#!/bin/sh\necho curl called >&2\nexit 9\n")
		cmd := exec.CommandContext(t.Context(), "sh", "-e", "-c", snippet+"\nprintf '%s' \"$DEXAFLOW_BASE_REF\"") //nolint:gosec // the snippet under test
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"DEXAFLOW_GITLAB_API_TOKEN="+c.token, "CI_COMMIT_BEFORE_SHA=before01",
			"CI_API_V4_URL="+srv.URL+"/api/v4", "CI_PROJECT_ID=7", "CI_COMMIT_BRANCH=main")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		b, err := cmd.Output()
		srv.Close()
		if err != nil {
			t.Fatalf("token %q: the snippet failed: %v\n%s", c.token, err, stderr.String())
		}
		if string(b) != c.want {
			t.Errorf("token %q, answer %d %s: DEXAFLOW_BASE_REF = %q, want %q\n%s", c.token, c.status, c.body, b, c.want, stderr.String())
		}
		if strings.Contains(stderr.String(), "curl called") {
			t.Errorf("token %q: the snippet ran curl, which the dexaflow-cli image does not have", c.token)
		}
		if c.token == "" {
			if len(asked) > 0 {
				t.Errorf("without a token the API must not be called: %v", asked)
			}
			continue
		}
		want := "/api/v4/projects/7/pipelines?ref=main&source=push&status=success&per_page=1 glpat-x"
		if !slices.Equal(asked, []string{want}) {
			t.Errorf("token %q: asked %v, want [%s]", c.token, asked, want)
		}
	}
}

// TestCIInitGitLabRunsInTheCLIImage runs the GitLab job in the published
// dexaflow-cli image of the pinned release, pinned by the digest its
// registry answers, with GitLab's shell in place of the image's entrypoint
// and nothing installed in the job. A release without the image keeps the
// Python image, a project that needs a daemon keeps Docker in Docker, and a
// digest that cannot be read is a refusal, not an unpinned image.
func TestCIInitGitLabRunsInTheCLIImage(t *testing.T) {
	var doc map[string]struct {
		Image struct {
			Name       string   `yaml:"name"`
			Entrypoint []string `yaml:"entrypoint"`
		} `yaml:"image"`
		BeforeScript []string `yaml:"before_script"`
		Script       []string `yaml:"script"`
	}
	golden := ciGolden(t)["gitlab"][".gitlab-ci.yml"]
	if err := yaml.Unmarshal([]byte(golden), &doc); err != nil {
		t.Fatal(err)
	}
	job := doc["dexaflow-deploy"]
	if want := ciCLIImageRepo + ":" + ciGoldenVersion + "@" + ciGoldenImageDigest; job.Image.Name != want {
		t.Errorf("image = %q, want %q", job.Image.Name, want)
	}
	if !slices.Equal(job.Image.Entrypoint, []string{""}) {
		t.Errorf("entrypoint = %q, want [\"\"] so GitLab runs the script in a shell", job.Image.Entrypoint)
	}
	if len(job.BeforeScript) > 0 {
		t.Errorf("the image runs as a non-root user and carries what the job needs; nothing may be installed: %q", job.BeforeScript)
	}
	if strings.Contains(strings.Join(job.Script, "\n"), "curl") {
		t.Errorf("the image has no curl:\n%s", golden)
	}

	isolateCLIEnv(t)
	root := ciWorkspace(t, "registry.gitlab.com/acme/dags", "")
	d := testDeps()
	stubTagCommit(t, d)
	asked := stubImageDigest(t, d)
	t.Chdir(root)
	if _, _, err := runDeps(t, d, "ci", "init", "gitlab", "--cli-version", "v0.7.0", "--stdout", "--config", emptyConfig(t)); err != nil {
		t.Fatal(err)
	}
	if want := []string{ciCLIImageRepo + ":v0.7.0"}; !slices.Equal(*asked, want) {
		t.Errorf("looked up %v, want %v", *asked, want)
	}
	*asked = nil
	out, _, err := runDeps(t, d, "ci", "init", "gitlab", "--cli-version", "v0.5.3", "--stdout", "--config", emptyConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(*asked) > 0 || !strings.Contains(out, "image: "+ciPythonImage) || strings.Contains(out, ciCLIImageRepo) {
		t.Errorf("v0.5.3 ships no dexaflow-cli image, so the job keeps %s: looked up %v\n%s", ciPythonImage, *asked, out)
	}
	writeTree(t, filepath.Join(root, "dags", "orders", projectConfigFile), "dag_id: orders\nsystem_packages: [libpq-dev]\n")
	out, _, err = runDeps(t, d, "ci", "init", "gitlab", "--cli-version", "v0.7.0", "--stdout", "--config", emptyConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(*asked) > 0 || !strings.Contains(out, "image: "+ciDockerImage) || strings.Contains(out, ciCLIImageRepo) {
		t.Errorf("a project that needs a daemon keeps Docker in Docker, with no lookup: looked up %v\n%s", *asked, out)
	}
	writeTree(t, filepath.Join(root, "dags", "orders", projectConfigFile), "dag_id: orders\n")

	d.imageDigest = func(context.Context, string) (string, error) { return "latest", nil }
	if _, _, err = runDeps(t, d, "ci", "init", "gitlab", "--cli-version", "v0.7.0", "--stdout", "--config", emptyConfig(t)); err == nil ||
		!strings.Contains(err.Error(), "not a sha256 digest") {
		t.Errorf("err = %v, want a refusal of an answer that is not a digest", err)
	}

	d.imageDigest = func(context.Context, string) (string, error) { return "", errors.New("unauthorized") }
	_, _, err = runDeps(t, d, "ci", "init", "gitlab", "--cli-version", "v0.7.0", "--stdout", "--config", emptyConfig(t))
	if err == nil || !strings.Contains(err.Error(), ciCLIImageRepo+":v0.7.0") || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("err = %v, want a refusal naming the image whose digest could not be read, and why", err)
	}
}

// TestCIInitGitHubInstallsWithTheAction installs the CLI on GitHub with
// the setup-dexaflow action of the same release, pinned by the commit of
// its tag, and refuses when that commit cannot be found.
func TestCIInitGitHubInstallsWithTheAction(t *testing.T) {
	workflow := ciGolden(t)["github"][".github/workflows/dexaflow-deploy.yaml"]
	for _, want := range []string{
		"uses: dexadata/dexaflow/.github/actions/setup-dexaflow@" + ciGoldenCommit + " # " + ciGoldenVersion,
		"version: " + ciGoldenVersion,
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("the workflow lacks %q:\n%s", want, workflow)
		}
	}
	if strings.Contains(workflow, ciScriptPath+" install") {
		t.Errorf("the workflow still installs with the script:\n%s", workflow)
	}
	isolateCLIEnv(t)
	root := ciWorkspace(t, "ghcr.io/acme/dags", "")
	d := testDeps()
	asked := stubTagCommit(t, d)
	stubImageDigest(t, d)
	t.Chdir(root)
	_, _, err := runDeps(t, d, "ci", "init", "github", "--cli-version", "v0.7.0", "--stdout", "--config", emptyConfig(t))
	if err == nil || !strings.Contains(err.Error(), "v0.7.0") {
		t.Errorf("err = %v, want a refusal naming the release whose commit is unknown", err)
	}
	if !slices.Equal(*asked, []string{"v0.7.0"}) {
		t.Errorf("looked up %v, want the commit of v0.7.0", *asked)
	}
	*asked = nil
	if _, _, err := runDeps(t, d, "ci", "init", "gitlab", "--cli-version", "v0.7.0", "--stdout", "--config", emptyConfig(t)); err != nil || len(*asked) > 0 {
		t.Errorf("only the GitHub workflow pins a commit: err %v, looked up %v", err, *asked)
	}
}

// TestTagCommitIn takes the commit an annotated tag points to, not the tag
// object, and a lightweight tag's own entry.
func TestTagCommitIn(t *testing.T) {
	t.Parallel()
	tagObj, commit := strings.Repeat("a", 40), strings.Repeat("b", 40)
	annotated := tagObj + "\trefs/tags/v0.6.0\n" + commit + "\trefs/tags/v0.6.0^{}\n"
	if got, err := tagCommitIn(annotated, "v0.6.0"); err != nil || got != commit {
		t.Errorf("annotated: %q, %v; want the peeled commit %s", got, err, commit)
	}
	if got, err := tagCommitIn(commit+"\trefs/tags/v0.6.0\n", "v0.6.0"); err != nil || got != commit {
		t.Errorf("lightweight: %q, %v", got, err)
	}
	if _, err := tagCommitIn(commit+"\trefs/tags/v0.6.1\n", "v0.6.0"); err == nil {
		t.Error("another tag's entry must not be taken")
	}
	if _, err := tagCommitIn("", "v0.6.0"); err == nil {
		t.Error("no entry must be an error")
	}
}
