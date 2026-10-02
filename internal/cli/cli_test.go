package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	yaml "go.yaml.in/yaml/v3"
)

func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCommand()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errb.String(), err
}

func TestVersionCommandPrintsInfo(t *testing.T) {
	out, _, err := run(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(out, "dexaflow") || !strings.Contains(out, "dev") {
		t.Errorf("version output = %q, want to contain dexaflow and dev", out)
	}
}

// TestVersionFlagMatchesSubcommand pins #598: `leoflow --version` (flag) must
// work and print exactly what `dexaflow version` (subcommand) prints — the
// companion binaries accept the --version flag, so the root CLI must too.
func TestVersionFlagMatchesSubcommand(t *testing.T) {
	flagOut, _, err := run(t, "--version")
	if err != nil {
		t.Fatalf("`leoflow --version` should be accepted, got error: %v", err)
	}
	subOut, _, err := run(t, "version")
	if err != nil {
		t.Fatalf("version subcommand: %v", err)
	}
	if strings.TrimSpace(flagOut) != strings.TrimSpace(subOut) {
		t.Errorf("--version = %q, want identical to `version` subcommand = %q", flagOut, subOut)
	}
	if !strings.Contains(flagOut, "dexaflow") {
		t.Errorf("--version output missing 'dexaflow': %q", flagOut)
	}
}

func TestVersionCommandJSON(t *testing.T) {
	out, _, err := run(t, "version", "--json")
	if err != nil {
		t.Fatalf("version --json: %v", err)
	}
	var info map[string]any
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("version --json is not valid JSON: %v (%q)", err, out)
	}
	if info["version"] != "dev" {
		t.Errorf("json version = %v, want dev", info["version"])
	}
}

func TestInitCreatesValidProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-dag")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	for _, f := range []string{"dexaflow.yaml", "dag.py"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("expected scaffolded %s: %v", f, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "dexaflow.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg domain.LeoflowConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("scaffolded dexaflow.yaml is invalid: %v", err)
	}
	if cfg.DagID != "my-dag" {
		t.Errorf("dag_id = %q, want my-dag", cfg.DagID)
	}
}

func TestValidateAcceptsScaffold(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "validate", dir); err != nil {
		t.Errorf("validate scaffold: %v", err)
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dexaflow.yaml"), []byte("dag_id: \"has spaces\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "validate", dir); err == nil {
		t.Error("validate should reject a bad dag_id")
	}
}

// TestValidateRejectsBrokenDagPython covers issue #D8: today validate only
// checks dexaflow.yaml + that dag.py exists, so a syntactically broken dag.py
// would pass validation and only blow up at compile/run time. validate must
// catch Python-syntax errors so a user gets a real go/no-go before push.
//
// The check is best-effort: it requires a Python interpreter (managed or
// system). When neither is available it surfaces a clear warning rather than
// silently skipping — covered by TestValidateGracefulWithoutPython.
func TestValidateRejectsBrokenDagPython(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH; the strict check needs an interpreter")
	}
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	// Overwrite dag.py with invalid Python.
	if err := os.WriteFile(filepath.Join(dir, "dag.py"), []byte("this is not valid python\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := run(t, "validate", dir)
	if err == nil {
		t.Fatal("validate must reject a syntactically broken dag.py")
	}
	combined := err.Error() + "\n" + stderr
	if !strings.Contains(combined, "dag.py") || !strings.Contains(strings.ToLower(combined), "syntax") {
		t.Errorf("validate error should name dag.py and mention a syntax problem, got: %s", combined)
	}
}

// TestValidateGracefulWithoutPython covers the fresh-install path: when no
// Python interpreter is reachable (managed not yet installed via
// `dexaflow setup`, system python3 not on PATH), validate must still succeed
// on a well-formed scaffold and surface a warning telling the user how to
// enable the strict check — never silently passing without explanation.
func TestValidateGracefulWithoutPython(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	// Hide python3 from PATH; point HOME at an empty dir so the managed
	// interpreter is also absent.
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("HOME", t.TempDir())

	_, stderr, err := run(t, "validate", dir)
	if err != nil {
		t.Fatalf("validate with no python should still succeed on a good scaffold, got %v (%s)", err, stderr)
	}
	if !strings.Contains(stderr, "skipping dag.py syntax check") || !strings.Contains(stderr, "dexaflow setup") {
		t.Errorf("expected a warning naming the skip and pointing to `dexaflow setup`, got: %q", stderr)
	}
}

// TestCompileRecoversAfterConfigUpdate covers the EXACT user scenario from
// the BYO-Python docs review: run `dexaflow compile` before `dexaflow setup`
// (so the default parser_cmd has no module to load), then "fix the state"
// by writing a working parser_cmd into ~/.leoflow/config.yaml (which is
// what `dexaflow setup` does in real life), then re-run compile WITHOUT any
// flag override and confirm it picks up the new config and succeeds.
//
// This is the file-state version of TestCompileFailThenRecoverIsClean
// below — that one swaps the --parser-cmd flag between runs, which proves
// flag overrides recover cleanly but does NOT prove that the config-file
// read path picks up a fresh value after a previous failed invocation.
func TestCompileRecoversAfterConfigUpdate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	leoflowDir := filepath.Join(home, ".leoflow")
	if err := os.MkdirAll(leoflowDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(leoflowDir, "config.yaml")

	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "dag.json")

	brokenParser := filepath.Join(t.TempDir(), "broken-parser.sh")
	if err := os.WriteFile(brokenParser, []byte("#!/usr/bin/env bash\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("parser_cmd: \""+brokenParser+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "compile", dir, "--output", out, "--image", "test:v1"); err == nil {
		t.Fatal("compile should fail when config's parser_cmd is broken")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatalf("compile fail left a stale %s — recovery via config update would be poisoned", out)
	}

	// Simulate `dexaflow setup` having just landed: overwrite the config with
	// a working parser_cmd. The next compile must pick up the new value
	// (no in-process caching of the file content from the previous run).
	goodParser := filepath.Join(t.TempDir(), "good-parser.sh")
	goodScript := "#!/usr/bin/env bash\n" +
		"out=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"cat > \"$out\" <<'JSON'\n" +
		"{\"schema_version\":\"1.0\",\"dag_id\":\"proj\",\"dag_version\":\"dev\",\"image\":\"test:v1\",\"tasks\":[{\"task_id\":\"hello\",\"type\":\"python\",\"entrypoint\":\"dag:hello\"}]}\n" +
		"JSON\n"
	if err := os.WriteFile(goodParser, []byte(goodScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("parser_cmd: \""+goodParser+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "compile", dir, "--output", out, "--image", "test:v1"); err != nil {
		t.Fatalf("compile must recover after the config's parser_cmd is fixed, got %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("dag.json missing after config-update recovery: %v", err)
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("recovered dag.json is invalid JSON: %v", err)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("recovered dag.json does not pass schema validation: %v", err)
	}
}

// TestCompileFailThenRecoverIsClean covers the out-of-order scenario surfaced
// during the BYO-Python deploy-docs review: a CI user who runs
// `dexaflow compile` before `dexaflow setup` (or before a working parser_cmd
// is in scope) sees a clean failure, and a subsequent run with the parser
// correctly wired succeeds — no partial dag.json, no stale state from the
// failed attempt that would poison the recovery.
//
// The contract here is "fail early, leave nothing behind". We do not test
// `dexaflow setup` end-to-end (that path provisions a managed Python on disk,
// which a unit test cannot afford); we simulate "fixed parser_cmd" by
// swapping the failing parser command for a working fake on the second run.
func TestCompileFailThenRecoverIsClean(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "dag.json")

	// Step 1 — broken parser command. compile must fail.
	brokenParser := filepath.Join(t.TempDir(), "broken-parser.sh")
	brokenScript := "#!/usr/bin/env bash\necho 'No module named leoflow_parser' >&2\nexit 1\n"
	if err := os.WriteFile(brokenParser, []byte(brokenScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "compile", dir, "--output", out, "--image", "test:v1", "--parser-cmd", brokenParser); err == nil {
		t.Fatal("compile with a broken parser-cmd should fail, but it succeeded")
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatalf("compile fail must NOT have written %s — found stale output that would mask the next run", out)
	}

	// Step 2 — supply a working parser_cmd (the recovery the user would do by
	// running `dexaflow setup` in real life). compile must succeed cleanly.
	goodParser := filepath.Join(t.TempDir(), "good-parser.sh")
	goodScript := "#!/usr/bin/env bash\n" +
		"out=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"cat > \"$out\" <<'JSON'\n" +
		"{\"schema_version\":\"1.0\",\"dag_id\":\"proj\",\"dag_version\":\"dev\",\"image\":\"test:v1\",\"tasks\":[{\"task_id\":\"hello\",\"type\":\"python\",\"entrypoint\":\"dag:hello\"}]}\n" +
		"JSON\n"
	if err := os.WriteFile(goodParser, []byte(goodScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "compile", dir, "--output", out, "--image", "test:v1", "--parser-cmd", goodParser); err != nil {
		t.Fatalf("compile with a working parser-cmd should recover after a prior failure, got %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("dag.json missing after recovery: %v", err)
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("dag.json from recovery is invalid: %v", err)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("recovered dag.json does not pass schema validation: %v", err)
	}
}

func TestCompileProducesValidDAGJSON(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	parser := filepath.Join(t.TempDir(), "fake-parser.sh")
	script := "#!/usr/bin/env bash\n" +
		"out=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"cat > \"$out\" <<'JSON'\n" +
		"{\"schema_version\":\"1.0\",\"dag_id\":\"proj\",\"dag_version\":\"dev\",\"image\":\"test:v1\",\"tasks\":[{\"task_id\":\"hello\",\"type\":\"python\",\"entrypoint\":\"dag:hello\"}]}\n" +
		"JSON\n"
	if err := os.WriteFile(parser, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "dag.json")
	if _, _, err := run(t, "compile", dir, "--output", out, "--image", "test:v1", "--parser-cmd", parser); err != nil {
		t.Fatalf("compile: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading dag.json: %v", err)
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("compiled dag.json is invalid: %v", err)
	}
}

func TestCompileBuildsImageWhenRequested(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	parser := filepath.Join(t.TempDir(), "fake-parser.sh")
	pscript := "#!/usr/bin/env bash\n" +
		"out=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"cat > \"$out\" <<'JSON'\n" +
		"{\"schema_version\":\"1.0\",\"dag_id\":\"proj\",\"dag_version\":\"dev\",\"image\":\"test:v1\",\"tasks\":[{\"task_id\":\"hello\",\"type\":\"python\",\"entrypoint\":\"dag:hello\"}]}\n" +
		"JSON\n"
	if err := os.WriteFile(parser, []byte(pscript), 0o755); err != nil {
		t.Fatal(err)
	}
	// Fake builder records its argv so we can assert the docker build invocation.
	marker := filepath.Join(t.TempDir(), "built.txt")
	builder := filepath.Join(t.TempDir(), "fake-docker.sh")
	if err := os.WriteFile(builder, []byte("#!/usr/bin/env bash\necho \"$@\" > "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "dag.json")
	if _, _, err := run(t, "compile", dir, "--output", out, "--image", "test:v1",
		"--parser-cmd", parser, "--build", "--builder", builder); err != nil {
		t.Fatalf("compile --build: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("builder was not invoked: %v", err)
	}
	if !strings.Contains(string(got), "build") || !strings.Contains(string(got), "-t test:v1") {
		t.Errorf("builder argv = %q, want a 'build -t test:v1' invocation", got)
	}
}

func TestCompilePushesImage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	parser := filepath.Join(t.TempDir(), "fake-parser.sh")
	pscript := "#!/usr/bin/env bash\n" +
		"out=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"cat > \"$out\" <<'JSON'\n" +
		"{\"schema_version\":\"1.0\",\"dag_id\":\"proj\",\"dag_version\":\"dev\",\"image\":\"test:v1\",\"tasks\":[{\"task_id\":\"hello\",\"type\":\"python\",\"entrypoint\":\"dag:hello\"}]}\n" +
		"JSON\n"
	if err := os.WriteFile(parser, []byte(pscript), 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "calls.txt")
	builder := filepath.Join(t.TempDir(), "fake-docker.sh")
	if err := os.WriteFile(builder, []byte("#!/usr/bin/env bash\necho \"$@\" >> "+marker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "dag.json")
	if _, _, err := run(t, "compile", dir, "--output", out, "--image", "test:v1",
		"--parser-cmd", parser, "--build", "--push", "--builder", builder); err != nil {
		t.Fatalf("compile --build --push: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("builder not invoked: %v", err)
	}
	if !strings.Contains(string(got), "build") || !strings.Contains(string(got), "push test:v1") {
		t.Errorf("builder calls = %q, want both build and push test:v1", got)
	}
}

func TestCompilePushRequiresBuild(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "compile", dir, "--push", "--image", "x:1", "--parser-cmd", "true"); err == nil {
		t.Error("--push without --build should error")
	}
}

func TestCompileBuildRequiresImage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "compile", dir, "--build", "--parser-cmd", "true"); err == nil {
		t.Error("--build without --image should error")
	}
}

func TestRunsTrigger(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"dag_run_id":"run-123","state":"queued"}`))
	}))
	defer srv.Close()

	out, _, err := run(t, "runs", "trigger", "etl", "--server", srv.URL, "--token", "t")
	if err != nil {
		t.Fatalf("runs trigger: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v2/dags/etl/dagRuns" {
		t.Errorf("hit %s %s, want POST /api/v2/dags/etl/dagRuns", gotMethod, gotPath)
	}
	if !strings.Contains(out, "run-123") || !strings.Contains(out, "queued") {
		t.Errorf("output = %q, want run id and state", out)
	}
}

func TestRunsStatusLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"dag_runs":[{"dag_run_id":"run-9","state":"success"}],"total_entries":1}`))
	}))
	defer srv.Close()

	out, _, err := run(t, "runs", "status", "etl", "--server", srv.URL, "--token", "t")
	if err != nil {
		t.Fatalf("runs status: %v", err)
	}
	if !strings.Contains(out, "run-9") || !strings.Contains(out, "success") {
		t.Errorf("output = %q, want latest run id and state", out)
	}
}

func TestRunsStatusByRunID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"dag_run_id":"run-7","state":"running"}`))
	}))
	defer srv.Close()

	out, _, err := run(t, "runs", "status", "etl", "--run", "run-7", "--server", srv.URL, "--token", "t")
	if err != nil {
		t.Fatalf("runs status --run: %v", err)
	}
	if gotPath != "/api/v2/dags/etl/dagRuns/run-7" {
		t.Errorf("hit %s, want the specific run endpoint", gotPath)
	}
	if !strings.Contains(out, "run-7") || !strings.Contains(out, "running") {
		t.Errorf("output = %q, want the run id and state", out)
	}
}

func TestRunsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	if _, _, err := run(t, "runs", "status", "etl", "--server", srv.URL); err == nil {
		t.Error("a non-2xx status should error")
	}
}

func TestServerCommandPointsToBinary(t *testing.T) {
	out, _, err := run(t, "server")
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	if !strings.Contains(out, "dexaflow-server") {
		t.Errorf("server output = %q, want mention of dexaflow-server", out)
	}
}
