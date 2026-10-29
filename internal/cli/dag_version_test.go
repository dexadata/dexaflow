package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// versionProject writes a dag.py project with the given extra yaml and returns
// its directory and loaded config.
func versionProject(t *testing.T, extraYAML string) string {
	t.Helper()
	dir := t.TempDir()
	writeProjectFile(t, dir, "dag.py", "# dag\n")
	writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: etl\n"+extraYAML)
	return dir
}

func writeProjectFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// labelOf loads the project config the way compile does and derives the
// default label from it.
func labelOf(t *testing.T, dir string) string {
	t.Helper()
	cfg, err := loadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	label, err := defaultDAGVersion(context.Background(), dir, cfg, "Dockerfile", "")
	if err != nil {
		t.Fatalf("defaultDAGVersion: %v", err)
	}
	return label
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.CommandContext(context.Background(), "git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitInitTagged(t *testing.T, dir, tag string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	if tag != "" {
		gitRun(t, dir, "tag", tag)
	}
}

var labelShape = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

func TestDefaultDAGVersionIsStableForUnchangedContent(t *testing.T) {
	t.Parallel()
	dir := versionProject(t, "")
	first, second := labelOf(t, dir), labelOf(t, dir)
	if first != second {
		t.Fatalf("same content gave %q then %q", first, second)
	}
	if !labelShape.MatchString(first) {
		t.Errorf("label %q is not a valid image tag", first)
	}
}

func TestDefaultDAGVersionOutsideGitUsesFixedBase(t *testing.T) {
	t.Parallel()
	dir := versionProject(t, "")
	label := labelOf(t, dir)
	if !regexp.MustCompile(`^dev-[0-9a-f]{12}$`).MatchString(label) {
		t.Errorf("label outside git = %q, want dev-<12 hex>", label)
	}
}

func TestDefaultDAGVersionChangesWithInputs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, yaml string
		mutate     func(t *testing.T, dir string)
	}{
		{"dag source byte", "", func(t *testing.T, dir string) { writeProjectFile(t, dir, "dag.py", "# dag!\n") }},
		{"include path file", "include_paths: [lib]\n", func(t *testing.T, dir string) { writeProjectFile(t, dir, "lib/util.py", "X = 2\n") }},
		{"dependency", "dependencies: [requests==2.0]\n", func(t *testing.T, dir string) {
			writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: etl\ndependencies: [requests==2.1]\n")
		}},
		{"python version", "python_version: \"3.12\"\n", func(t *testing.T, dir string) {
			writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: etl\npython_version: \"3.13\"\n")
		}},
		{"own Dockerfile", "", func(t *testing.T, dir string) { writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := versionProject(t, c.yaml)
			writeProjectFile(t, dir, "lib/util.py", "X = 1\n")
			before := labelOf(t, dir)
			c.mutate(t, dir)
			if after := labelOf(t, dir); after == before {
				t.Errorf("label stayed %q after changing the %s", before, c.name)
			}
		})
	}
}

func TestDefaultDAGVersionIgnoresWhatDoesNotShip(t *testing.T) {
	t.Parallel()
	dir := versionProject(t, "")
	before := labelOf(t, dir)
	// Not copied by the generated Dockerfile, or rewritten by every compile.
	writeProjectFile(t, dir, "README.md", "notes\n")
	writeProjectFile(t, dir, "dag.json", `{"dag_version":"x"}`)
	writeProjectFile(t, dir, "__pycache__/dag.cpython-312.pyc", "bytecode")
	if after := labelOf(t, dir); after != before {
		t.Errorf("label moved from %q to %q on files that do not ship", before, after)
	}
}

func TestDefaultDAGVersionWholeContextSkipsCompileOutputs(t *testing.T) {
	t.Parallel()
	// An own Dockerfile can COPY anything, so the whole context counts; the
	// compiled dag.json and excluded paths still do not.
	dir := versionProject(t, "")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\nCOPY . /app\n")
	before := labelOf(t, dir)
	writeProjectFile(t, dir, "dag.json", `{"dag_version":"x"}`)
	writeProjectFile(t, dir, ".git/HEAD", "ref: x\n")
	if after := labelOf(t, dir); after != before {
		t.Fatalf("label moved on compile output or .git: %q -> %q", before, after)
	}
	writeProjectFile(t, dir, "README.md", "notes\n")
	if after := labelOf(t, dir); after == before {
		t.Errorf("label stayed %q after a context file changed under an own Dockerfile", before)
	}
}

func TestDefaultDAGVersionDbtIgnoresParseArtifacts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeProjectFile(t, dir, "dexaflow.yaml", "dag_id: sales\ndbt:\n  project: transform\n")
	writeProjectFile(t, dir, "transform/dbt_project.yml", "name: sales\n")
	writeProjectFile(t, dir, "transform/models/a.sql", "select 1\n")
	before := labelOf(t, dir)
	writeProjectFile(t, dir, "transform/target/manifest.json", `{"generated_at":"now"}`)
	writeProjectFile(t, dir, "transform/logs/dbt.log", "host paths")
	if after := labelOf(t, dir); after != before {
		t.Fatalf("label moved on dbt parse artifacts: %q -> %q", before, after)
	}
	writeProjectFile(t, dir, "transform/models/a.sql", "select 2\n")
	if after := labelOf(t, dir); after == before {
		t.Errorf("label stayed %q after a dbt model changed", before)
	}
}

func TestDefaultDAGVersionUsesGitOfProjectDirNotCwd(t *testing.T) {
	t.Parallel()
	// The test process runs inside this repository's checkout; the project is
	// its own repository, tagged v1.2.3. The label must come from the project.
	dir := versionProject(t, "")
	gitInitTagged(t, dir, "v1.2.3")
	label := labelOf(t, dir)
	if !regexp.MustCompile(`^v1\.2\.3-[0-9a-f]{12}$`).MatchString(label) {
		t.Errorf("label = %q, want v1.2.3-<hash> from the project's own repository", label)
	}
	rev := readGitRevision(context.Background(), dir)
	if rev.exactTag != "v1.2.3" || rev.sha == "" {
		t.Errorf("revision = %+v, want exact tag v1.2.3 and a sha", rev)
	}
}

func TestDefaultDAGVersionDirtyTree(t *testing.T) {
	t.Parallel()
	dir := versionProject(t, "")
	gitInitTagged(t, dir, "v1.0.0")
	clean := labelOf(t, dir)
	// An untracked compile output dirties the tree without changing the DAG.
	writeProjectFile(t, dir, "dag.json", "{}")
	if got := labelOf(t, dir); got != clean {
		t.Errorf("an untracked dag.json moved the label from %q to %q", clean, got)
	}
	writeProjectFile(t, dir, "dag.py", "# edited\n")
	dirty := labelOf(t, dir)
	if !strings.HasPrefix(dirty, "v1.0.0-") || dirty == clean {
		t.Errorf("dirty label = %q (clean %q), want a new v1.0.0-<hash>", dirty, clean)
	}
	writeProjectFile(t, dir, "dag.py", "# edited again\n")
	if again := labelOf(t, dir); again == dirty || again == clean {
		t.Errorf("a second edit on a dirty tree reused a label: %q", again)
	}
}

func TestReadGitRevisionOutsideGit(t *testing.T) {
	t.Parallel()
	rev := readGitRevision(context.Background(), t.TempDir())
	if rev != (gitRevision{}) {
		t.Errorf("revision outside git = %+v, want zero", rev)
	}
}

func TestSanitizeImageTag(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("a", 200)
	cases := []struct{ in, want string }{
		{"v1.2.3", "v1.2.3"},
		{"release/v1+build.7", "release-v1-build.7"},
		{".hidden", "hidden"},
		{"--x", "x"},
		{"-.-", ""},
		{"ok_tag-1", "ok_tag-1"},
		{long, long[:128]},
	}
	for _, c := range cases {
		if got := sanitizeImageTag(c.in); got != c.want {
			t.Errorf("sanitizeImageTag(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestComposeDefaultLabelKeepsHashWithinTagLimit(t *testing.T) {
	t.Parallel()
	got := composeDefaultLabel(strings.Repeat("b/", 100), "0123456789ab")
	if len(got) > 128 || !strings.HasSuffix(got, "-0123456789ab") || !labelShape.MatchString(got) {
		t.Errorf("label = %q (len %d), want a valid tag ending in the hash", got, len(got))
	}
	if got := composeDefaultLabel("", "0123456789ab"); got != "dev-0123456789ab" {
		t.Errorf("empty base = %q, want dev-0123456789ab", got)
	}
}

// recordingParser writes a valid dag.json and records the --dag-version and
// --image it was handed, one `version<TAB>image` line per run.
func recordingParser(t *testing.T, log string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "parser.sh")
	body := "#!/usr/bin/env bash\n" +
		"out=\"\"; ver=\"\"; img=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; --dag-version) ver=\"$2\"; shift 2;; --image) img=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"printf '%s\\t%s\\n' \"$ver\" \"$img\" >> " + log + "\n" +
		"cat > \"$out\" <<JSON\n" +
		"{\"schema_version\":\"1.0\",\"dag_id\":\"etl\",\"dag_version\":\"$ver\",\"image\":\"$img\"," +
		"\"tasks\":[{\"task_id\":\"t\",\"type\":\"python\",\"entrypoint\":\"dag:t\"}]}\n" +
		"JSON\n"
	writeExecutable(t, p, body)
	return p
}

// TestCompileLabelsFromTheProjectRepository drives the real compile command on
// a project that is its own git repository while the process runs in another
// one (this checkout): the label and the git_tag image tag must come from the
// project, and a second compile of the same content must give the same label.
func TestCompileLabelsFromTheProjectRepository(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := versionProject(t, "registry:\n  url: reg.io/team\n  image_name: etl\n  tag_strategy: git_tag\n")
	gitInitTagged(t, dir, "release/2.0")
	log := filepath.Join(t.TempDir(), "argv.log")
	parser := recordingParser(t, log)

	for range 2 {
		if _, stderr, err := run(t, "compile", dir, "--parser-cmd", parser); err != nil {
			t.Fatalf("compile: %v (%s)", err, stderr)
		}
	}
	lines := nonEmptyLines(t, log)
	if len(lines) != 2 || lines[0] != lines[1] {
		t.Fatalf("two compiles of the same content recorded %q, want one label twice", lines)
	}
	f := strings.Split(lines[0], "\t")
	if !regexp.MustCompile(`^release-2\.0-[0-9a-f]{12}$`).MatchString(f[0]) {
		t.Errorf("--dag-version %q, want release-2.0-<hash> from the project's repository", f[0])
	}
	if f[1] != "reg.io/team/etl:release-2.0" {
		t.Errorf("--image %q, want the project's exact tag, sanitized", f[1])
	}
}

// TestCompileWarnsWhenTheTagStrategyFallsBack: outside git, git_sha used to
// tag `repo:` with nothing after the colon.
func TestCompileWarnsWhenTheTagStrategyFallsBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := versionProject(t, "registry:\n  url: reg.io/team\n  image_name: etl\n  tag_strategy: git_sha\n")
	log := filepath.Join(t.TempDir(), "argv.log")
	_, stderr, err := run(t, "compile", dir, "--parser-cmd", recordingParser(t, log))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	f := strings.Split(nonEmptyLines(t, log)[0], "\t")
	if f[1] != "reg.io/team/etl:"+f[0] {
		t.Errorf("--image %q, want the version label %q as the tag", f[1], f[0])
	}
	if !strings.Contains(stderr, "tag_strategy git_sha") {
		t.Errorf("stderr %q, want a warning about the git_sha fallback", stderr)
	}
}
