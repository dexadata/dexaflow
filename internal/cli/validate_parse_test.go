package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeParser writes an executable parser stand-in and returns its path.
// body runs after the arguments are parsed into $out (the --output path).
func writeFakeParser(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-parser.sh")
	script := "#!/usr/bin/env bash\n" +
		"out=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		body
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil { //nolint:gosec // test helper script must be executable
		t.Fatal(err)
	}
	return p
}

func initProject(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	return dir
}

const goodParserBody = "echo \"Wrote $out\"\n" +
	"cat > \"$out\" <<'JSON'\n" +
	"{\"schema_version\":\"1.0\",\"dag_id\":\"proj\",\"dag_version\":\"dev\",\"image\":\"x:1\",\"tasks\":[{\"task_id\":\"hello\",\"type\":\"python\",\"entrypoint\":\"dag:hello\"}]}\n" +
	"JSON\n"

// TestValidateDefaultDoesNotImportDag pins the default: without --parse,
// validate stays the static check it has always been (#1680). A CI step that
// lints dexaflow.yaml on a runner without the DAG's dependencies keeps its exit
// code, so the parser must not even be started.
func TestValidateDefaultDoesNotImportDag(t *testing.T) {
	dir := initProject(t)
	marker := filepath.Join(t.TempDir(), "parser-ran")
	parser := writeFakeParser(t, "touch "+marker+"\nexit 1\n")

	if _, stderr, err := run(t, "validate", dir, "--parser-cmd", parser); err != nil {
		t.Fatalf("validate without --parse must not run the parser, got %v (%s)", err, stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("validate without --parse started the parser")
	}
}

// TestValidateParseRejectsDagThatFailsToImport is the #1680 report: a dag.py
// that is valid Python but raises when imported must fail validate --parse with
// the interpreter's error, not print "is valid" and exit 0.
func TestValidateParseRejectsDagThatFailsToImport(t *testing.T) {
	dir := initProject(t)
	parser := writeFakeParser(t,
		"echo \"AttributeError: 'function' object has no attribute 'override'\" >&2\nexit 1\n")

	stdout, _, err := run(t, "validate", dir, "--parse", "--parser-cmd", parser)
	if err == nil {
		t.Fatal("validate --parse must fail when dag.py fails to import")
	}
	if !strings.Contains(err.Error(), "AttributeError") {
		t.Errorf("error should carry the import failure, got: %v", err)
	}
	if strings.Contains(stdout, "is valid") {
		t.Errorf("a failed import must not print the success line, got stdout %q", stdout)
	}
}

// TestValidateParseAcceptsGoodDag checks the happy path and that stdout stays
// exactly the one success line scripts read: the parser's own "Wrote <tmp>"
// names a scratch file the user never asked for.
func TestValidateParseAcceptsGoodDag(t *testing.T) {
	dir := initProject(t)
	parser := writeFakeParser(t, goodParserBody)

	stdout, stderr, err := run(t, "validate", dir, "--parse", "--parser-cmd", parser)
	if err != nil {
		t.Fatalf("validate --parse on a good DAG: %v (%s)", err, stderr)
	}
	want := projectConfigPath(dir) + " is valid\n"
	if stdout != want {
		t.Errorf("stdout = %q, want exactly %q", stdout, want)
	}
	if _, serr := os.Stat(filepath.Join(dir, "dag.json")); serr == nil {
		t.Error("validate --parse must not leave a dag.json in the project")
	}
}

// TestValidateParseRejectsInvalidSpec: the import succeeded but what it
// declared is not a valid DAG (here a bad dag_id), which compile would reject
// on the same check.
func TestValidateParseRejectsInvalidSpec(t *testing.T) {
	dir := initProject(t)
	parser := writeFakeParser(t, "cat > \"$out\" <<'JSON'\n"+
		"{\"schema_version\":\"1.0\",\"dag_id\":\"has spaces\",\"dag_version\":\"dev\",\"image\":\"x:1\",\"tasks\":[{\"task_id\":\"hello\",\"type\":\"python\",\"entrypoint\":\"dag:hello\"}]}\n"+
		"JSON\n")

	if _, _, err := run(t, "validate", dir, "--parse", "--parser-cmd", parser); err == nil {
		t.Fatal("validate --parse must reject a parsed DAG that fails schema validation")
	}
}

// TestValidateParseWithRealParser runs a dag.py that is valid Python but
// raises at import through the bundled parser, as a binary-only install does.
// The issue's own example (.override) is tracked separately in #1681, so the
// failure here is a NameError that no shim change can make go away.
func TestValidateParseWithRealParser(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH; the real parser needs an interpreter")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := ensurePysrcIn(filepath.Join(home, ".leoflow", "pysrc"), nil); err != nil {
		t.Fatalf("extracting bundled parser sources: %v", err)
	}
	t.Setenv("PYTHONPATH", "")
	if err := os.Unsetenv("PYTHONPATH"); err != nil {
		t.Fatal(err)
	}
	dir := initProject(t)

	if _, stderr, err := run(t, "validate", dir, "--parse"); err != nil {
		t.Fatalf("validate --parse on the scaffold: %v (%s)", err, stderr)
	}

	broken := "from airflow.sdk import DAG, task\n\n" +
		"@task\ndef estimate(n: int) -> int:\n    return n\n\n" +
		"with DAG(\"broken\", schedule=None):\n    estimate(WORKERS_UNDEFINED)\n"
	if err := os.WriteFile(filepath.Join(dir, "dag.py"), []byte(broken), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if _, _, err := run(t, "validate", dir); err != nil {
		t.Fatalf("plain validate stays static and passes valid syntax: %v", err)
	}
	_, _, err := run(t, "validate", dir, "--parse")
	if err == nil {
		t.Fatal("validate --parse must reject the #1680 dag.py")
	}
	if !strings.Contains(err.Error(), "WORKERS_UNDEFINED") {
		t.Errorf("error should name the undefined name, got: %v", err)
	}
}

// TestValidateParseChecksEmbeddedDbtGroups: a dbt_group placeholder is
// expanded by compile from the dbt manifest, which validate --parse does not
// build. The group must still be declared in dexaflow.yaml, with compile's own
// message, and the rest of the graph around it is still validated.
func TestValidateParseChecksEmbeddedDbtGroups(t *testing.T) {
	dir := initProject(t)
	parser := writeFakeParser(t, "cat > \"$out\" <<'JSON'\n"+
		"{\"schema_version\":\"1.0\",\"dag_id\":\"proj\",\"dag_version\":\"dev\",\"image\":\"x:1\",\"tasks\":["+
		"{\"task_id\":\"staging\",\"type\":\"dbt_group\"},"+
		"{\"task_id\":\"report\",\"type\":\"python\",\"entrypoint\":\"dag:report\",\"depends_on\":[\"staging\"]}]}\n"+
		"JSON\n")

	_, _, err := run(t, "validate", dir, "--parse", "--parser-cmd", parser)
	if err == nil || !strings.Contains(err.Error(), "no dbt_groups.staging") {
		t.Fatalf("an undeclared dbt_group must fail with compile's message, got: %v", err)
	}

	if merr := os.MkdirAll(filepath.Join(dir, "transform"), 0o750); merr != nil {
		t.Fatal(merr)
	}
	if werr := os.WriteFile(filepath.Join(dir, "transform", "dbt_project.yml"), []byte("name: t\nprofile: t\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	cfgPath := filepath.Join(dir, "dexaflow.yaml")
	cfg, err := os.ReadFile(cfgPath) //nolint:gosec // test fixture
	if err != nil {
		t.Fatal(err)
	}
	cfg = append(cfg, []byte("dbt_groups:\n  staging:\n    project: transform\n")...)
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, err := run(t, "validate", dir, "--parse", "--parser-cmd", parser); err != nil {
		t.Fatalf("a declared dbt_group must validate without dbt on the host, got: %v (%s)", err, stderr)
	}
}

// TestValidateParseRequiresParser: a user who asked for the import check and
// has no working parser gets a failure, not a silent skip.
func TestValidateParseRequiresParser(t *testing.T) {
	dir := initProject(t)
	if _, _, err := run(t, "validate", dir, "--parse", "--parser-cmd", "/nonexistent/parser"); err == nil {
		t.Fatal("validate --parse with no parser must fail")
	}
}
