package cli

import (
	"flag"
	"io/fs"
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

// ciCase is one golden case: a workspace and the system it is generated for.
type ciCase struct {
	name     string
	system   string
	registry string
	// orders is the dexaflow.yaml of the orders project, beyond its dag_id.
	orders string
}

// ciCases are the golden cases. Each system gets the registry its users
// most often push to, so the login the template writes is the native one.
var ciCases = []ciCase{
	{name: "github", system: "github", registry: "ghcr.io/acme/dags"},
	{name: "gitlab", system: "gitlab", registry: "registry.gitlab.com/acme/dags"},
	{name: "gitlab-docker", system: "gitlab", registry: "registry.gitlab.com/acme/dags", orders: "system_packages: [libpq-dev]\n"},
	{name: "cloudbuild", system: "cloudbuild", registry: "us-central1-docker.pkg.dev/acme/dags"},
	{name: "generic", system: "generic", registry: "ghcr.io/acme/dags"},
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
	t.Chdir(root)
	full := append([]string{"ci", "init"}, args...)
	full = append(full, "--cli-version", ciGoldenVersion, "--config", emptyConfig(t))
	return run(t, full...)
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
		"github":        {".github/workflows/dexaflow-deploy.yaml", ciScriptPath},
		"gitlab":        {".gitlab-ci.yml", ciScriptPath},
		"gitlab-docker": {".gitlab-ci.yml", ciScriptPath},
		"cloudbuild":    {ciScriptPath, "cloudbuild.yaml"},
		"generic":       {ciScriptPath},
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
		for _, want := range []string{"deploy --all --changed-since", "deploy --all --yes --output json", "auth create-token --scope deploy"} {
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
	if !strings.Contains(out, `: "${DEXAFLOW_TARGET:=prod}"`) {
		t.Errorf("the script does not select target prod:\n%s", out)
	}
	if strings.Contains(out, "vars.DEXAFLOW_SERVER_URL") {
		t.Errorf("with a target the workflow must not read DEXAFLOW_SERVER_URL:\n%s", out)
	}
	out, _, err = ciInit(t, root, "github", "--stdout")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `: "${DEXAFLOW_TARGET:=dev}"`) {
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
	if _, _, err := run(t, "ci", "init", "github", "--cli-version", "latest", "--config", emptyConfig(t)); err == nil {
		t.Error("--cli-version must be a release tag")
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
	writeTree(t, filepath.Join(bin, "dexaflow"), fakeDexaflow)
	writeTree(t, filepath.Join(bin, "uv"), "#!/bin/sh\necho uv 0.12.24\n")
	for _, f := range []string{"dexaflow", "uv"} {
		if err := os.Chmod(filepath.Join(bin, f), 0o700); err != nil { //nolint:gosec // a test executable
			t.Fatal(err)
		}
	}
	runScript := func(base, token, status string) (log, summary, output string, err error) {
		dir := t.TempDir()
		logPath, sumPath := filepath.Join(dir, "log"), filepath.Join(dir, "summary.md")
		c := exec.CommandContext(t.Context(), "sh", filepath.Join(root, ciScriptPath)) //nolint:gosec // the script under test
		c.Dir = t.TempDir()
		c.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"FAKE_LOG="+logPath, "FAKE_STATUS="+status, "DEXAFLOW_BASE_REF="+base,
			"DEXAFLOW_TOKEN="+token, "DEXAFLOW_SERVER_URL=https://pro.example",
			"DEXAFLOW_SUMMARY_FILE="+sumPath, "GITHUB_STEP_SUMMARY=")
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
		for _, want := range []string{"| orders | registered | v1-abc |", "old", "dexaflow admin dags pause old"} {
			if !strings.Contains(summary, want) {
				t.Errorf("base %q: summary lacks %q:\n%s", c.base, want, summary)
			}
		}
	}
	if log, _, _, err := runScript(head, "", "0"); err == nil || log != "" {
		t.Errorf("without DEXAFLOW_TOKEN the script must stop before deploying: err %v, ran %q", err, log)
	}
	if _, summary, _, err := runScript(head, "dxf_x", "3"); err == nil || !strings.Contains(summary, "orders") {
		t.Errorf("a failed deploy must fail the script and still write the summary: err %v, summary %q", err, summary)
	}
}

// scriptError carries the output of a failed script run.
type scriptError struct {
	err error
	out string
}

func (e *scriptError) Error() string { return e.err.Error() + "\n" + e.out }

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
		want := strings.ReplaceAll(files[path], ciGoldenVersion, "vX.Y.Z")
		if want == "" {
			t.Errorf("case %s writes no %s", name, path)
			continue
		}
		if body != want {
			t.Errorf("the recipe %s (%s) in cicd-deploy.md is not what ci init writes; paste testdata/ci/%s/%s with %s as vX.Y.Z:\n%s",
				path, name, name, path, ciGoldenVersion, body)
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
	for _, want := range []string{"google-github-actions/auth@v2", "aws-region: eu-west-1",
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
