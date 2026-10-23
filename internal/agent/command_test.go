package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildCommandPython(t *testing.T) {
	argv, err := BuildCommand("python", "tasks.extract:run", "")
	if err != nil {
		t.Fatal(err)
	}
	// -u forces unbuffered stdout/stderr so print() bytes are not lost on
	// SIGKILL/OOM before Python's atexit can flush. Companion env var
	// PYTHONUNBUFFERED=1 (see runner.go buildEnv) covers re-execed children.
	want := []string{"python", "-u", "-m", "leoflow_runtime", "tasks.extract:run"}
	if len(argv) != len(want) {
		t.Fatalf("unexpected argv: %v", argv)
	}
	for i, w := range want {
		if argv[i] != w {
			t.Errorf("argv[%d] = %q, want %q", i, argv[i], w)
		}
	}
}

func TestBuildCommandPythonHonorsInterpreterEnv(t *testing.T) {
	t.Setenv("LEOFLOW_PYTHON", "python3")
	argv, err := BuildCommand("python", "dag:extract", "")
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "python3" {
		t.Errorf("argv[0] = %q, want python3 (LEOFLOW_PYTHON)", argv[0])
	}
}

func TestBuildCommandPythonBadEntrypoint(t *testing.T) {
	for _, ep := range []string{"", "noseparator", ":run", "mod:"} {
		if _, err := BuildCommand("python", ep, ""); err == nil {
			t.Errorf("entrypoint %q should be rejected", ep)
		}
	}
}

func TestBuildCommandBash(t *testing.T) {
	argv, err := BuildCommand("bash", "echo hi", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 3 || argv[0] != "bash" || argv[1] != "-c" || argv[2] != "echo hi" {
		t.Errorf("unexpected argv: %v", argv)
	}
	if _, err := BuildCommand("bash", "", ""); err == nil {
		t.Error("empty bash command should be rejected")
	}
}

func TestBuildCommandBashTemplatedRoutesThroughRuntime(t *testing.T) {
	// A bash command with {{ }} is rendered with the run context by the runtime (#382);
	// plain bash stays a direct `bash -c` (TestBuildCommandBash) so bash-only images
	// need no Python.
	argv, err := BuildCommand("bash", "echo {{ ds }}", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{pythonInterpreter(), "-u", "-m", "leoflow_runtime", "--bash", "echo {{ ds }}"}
	if len(argv) != len(want) {
		t.Fatalf("argv = %v, want %v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, argv[i], want[i])
		}
	}
}

func TestBuildCommandRejectsUnsupported(t *testing.T) {
	// http_api was removed (ADR 0047/0048, #512); like any unknown operator the
	// agent has no command for it and rejects it.
	if _, err := BuildCommand("http_api", "x", ""); err == nil {
		t.Error("removed http_api operator should be rejected")
	}
	if _, err := BuildCommand("ruby", "x", ""); err == nil {
		t.Error("unknown operator should be rejected")
	}
}

func TestXComEnvVar(t *testing.T) {
	if got := XComEnvVar("raw", []byte(`{"rows":3}`)); got != `LEOFLOW_XCOM_RAW={"rows":3}` {
		t.Errorf("XComEnvVar = %q", got)
	}
}

func TestReadReturnValue(t *testing.T) {
	if _, ok, err := ReadReturnValue(filepath.Join(t.TempDir(), "missing.json")); ok || err != nil {
		t.Errorf("missing file: ok=%v err=%v, want false,nil", ok, err)
	}
	p := filepath.Join(t.TempDir(), "rv.json")
	if err := os.WriteFile(p, []byte(`42`), 0o644); err != nil {
		t.Fatal(err)
	}
	v, ok, err := ReadReturnValue(p)
	if err != nil || !ok || string(v) != "42" {
		t.Errorf("present file: v=%q ok=%v err=%v", v, ok, err)
	}
}

// TestBuildCommandAirflowOperator verifies a captured provider operator dispatches
// to the runtime's --operator mode with the dotted class (ADR 0040). entrypoint is
// empty for operators; the class carries the import path.
func TestBuildCommandAirflowOperator(t *testing.T) {
	cls := "airflow.providers.standard.operators.bash.BashOperator"
	argv, err := BuildCommand("airflow_operator", "", cls)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"python", "-u", "-m", "leoflow_runtime", "--operator", cls}
	if len(argv) != len(want) {
		t.Fatalf("unexpected argv: %v", argv)
	}
	for i, w := range want {
		if argv[i] != w {
			t.Errorf("argv[%d] = %q, want %q", i, argv[i], w)
		}
	}
}

// TestBuildCommandAirflowOperatorRequiresClass verifies an airflow_operator task
// with no operator class is a loud error, not a silently-broken command.
func TestBuildCommandAirflowOperatorRequiresClass(t *testing.T) {
	if _, err := BuildCommand("airflow_operator", "", ""); err == nil {
		t.Fatal("expected an error for airflow_operator with no class, got nil")
	}
}
