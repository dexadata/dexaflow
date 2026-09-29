package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSanitizeDagIDForFs(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"sales_etl", "sales_etl"},
		{"my-dag", "my-dag"},
		{"with.dots", "with_dots"},
		{"weird/slash", "weird_slash"},
		{"../escape", "___escape"},
		{"name with spaces", "name_with_spaces"},
		{"", "_"},
		{"...", "___"},
	}
	for _, c := range cases {
		if got := sanitizeDagIDForFs(c.in); got != c.want {
			t.Errorf("sanitizeDagIDForFs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDagVenvPathLayout(t *testing.T) {
	home := filepath.FromSlash("/h/.leoflow/dev")
	dir := dagVenvDir(home, "sales_etl")
	wantDir := filepath.Join(home, "venvs", "sales_etl")
	if dir != wantDir {
		t.Errorf("dagVenvDir = %q, want %q", dir, wantDir)
	}
	py := dagVenvPython(home, "sales_etl")
	wantPy := filepath.Join(wantDir, "bin", "python")
	if runtime.GOOS == "windows" {
		wantPy = filepath.Join(wantDir, "Scripts", "python.exe")
	}
	if py != wantPy {
		t.Errorf("dagVenvPython = %q, want %q", py, wantPy)
	}
	// Markers live inside the per-DAG venv directory so a single rm -rf wipes
	// every freshness/lock signal alongside the venv itself — the
	// single-shared-venv layout had to manage them at the root.
	if got := dagVenvDepsMarkerPath(home, "sales_etl"); !strings.HasPrefix(got, wantDir) {
		t.Errorf("dagVenvDepsMarkerPath = %q, want a path under %q", got, wantDir)
	}
	if got := dagVenvRuntimeMarkerPath(home, "sales_etl"); !strings.HasPrefix(got, wantDir) {
		t.Errorf("dagVenvRuntimeMarkerPath = %q, want a path under %q", got, wantDir)
	}
}

func TestDetectInstallerPrefersUv(t *testing.T) {
	uvFound := func(name string) (string, error) {
		if name == "uv" {
			return "/opt/homebrew/bin/uv", nil
		}
		return "", errors.New("not found")
	}
	if got := detectInstaller(uvFound); got != installerUv {
		t.Errorf("detectInstaller(uv on PATH) = %q, want uv", got)
	}
	noUv := func(_ string) (string, error) { return "", errors.New("not found") }
	if got := detectInstaller(noUv); got != installerPip {
		t.Errorf("detectInstaller(no uv) = %q, want pip", got)
	}
}

func TestInstallerCmdShapes(t *testing.T) {
	// pip: run the venv's Python as the installer host. Tests rely on
	// argv[0]==<py> so the venv-relative install path is preserved even when
	// the operator's $PATH points elsewhere.
	cmd, args := installerCmd(installerPip, "/v/py", []string{"pandas", "duckdb==1"})
	if cmd != "/v/py" {
		t.Errorf("pip cmd = %q, want /v/py", cmd)
	}
	joined := strings.Join(args, " ")
	for _, must := range []string{"-m", "pip", "install", "pandas", "duckdb==1"} {
		if !strings.Contains(joined, must) {
			t.Errorf("pip args %q missing %q", joined, must)
		}
	}
	// uv: invoke `uv pip install --python <py> ...` so uv resolves and
	// installs into the target venv directly — no need to activate it.
	cmd, args = installerCmd(installerUv, "/v/py", []string{"pandas"})
	if cmd != "uv" {
		t.Errorf("uv cmd = %q, want uv", cmd)
	}
	joined = strings.Join(args, " ")
	for _, must := range []string{"pip", "install", "--python", "/v/py", "pandas"} {
		if !strings.Contains(joined, must) {
			t.Errorf("uv args %q missing %q", joined, must)
		}
	}
	// Empty package list is a no-op shape — callers should not invoke it,
	// but if they do, the argv must still be valid (no trailing space, no
	// dangling separator).
	cmd, args = installerCmd(installerUv, "/v/py", nil)
	if cmd == "" || len(args) == 0 {
		t.Errorf("installerCmd(uv, nil) returned empty cmd/args: %q %v", cmd, args)
	}
}

// TestEnsureDagVenvSkipsAllGatesWhenAlreadyFresh exercises the happy
// short-circuit: the venv exists with the runtime importable AND the marker
// matches AND the deps marker matches → no install runs, no error returned.
// Uses a sh stub for "python" so we never depend on a real interpreter or
// network, but the function still walks every gate (venv-exists, import-OK,
// checksum-match, deps-match) — the regression class being protected against
// is a startup-time pip storm that hits every Lite reload when the gates
// accidentally invert (#346's user pain).
func TestEnsureDagVenvSkipsAllGatesWhenAlreadyFresh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh stub is POSIX-only; the per-DAG layout is exercised by TestDagVenvPathLayout on Windows")
	}
	home := t.TempDir()
	dagID := "etl"
	venvDir := dagVenvDir(home, dagID)
	binDir := filepath.Join(venvDir, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// A python stub that always succeeds — used for the importability gate.
	// It records its args so the gate's payload can be asserted below: the
	// runner re-imports the user's dag.py per task, so a venv that has
	// leoflow_runtime but not leoflow looks healthy here and then fails every
	// python task in Lite (#17).
	pyStub := filepath.Join(binDir, "python")
	argvLog := filepath.Join(home, "py-argv")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvLog + "\nexit 0\n"
	if err := os.WriteFile(pyStub, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	// A pysrc fixture so runtimeSrcChecksum returns a stable value; then
	// stamp the matching marker so the binary-upgrade gate skips.
	runtimeRoot := writeRuntimeFixture(t, t.TempDir(), map[string]string{
		"runner.py": "def run(): pass\n",
	})
	want, cerr := runtimeSrcChecksum(runtimeRoot)
	if cerr != nil {
		t.Fatalf("runtimeSrcChecksum: %v", cerr)
	}
	if err := os.WriteFile(dagVenvRuntimeMarkerPath(home, dagID), []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	deps := []string{"requests==2.31.0"}
	if err := os.WriteFile(dagVenvDepsMarkerPath(home, dagID), []byte(devDepsSignature(deps)), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ensureDagVenv(context.Background(), devTestCmd(), home, dagID, runtimeRoot, "", deps)
	if err != nil {
		t.Fatalf("ensureDagVenv: %v", err)
	}
	if got != pyStub {
		t.Errorf("ensureDagVenv returned %q, want %q (the stub)", got, pyStub)
	}

	// The freshness gate must probe BOTH packages. leoflow carries the
	// authoring names a dag.py imports at its top (dbt_group, ADR 0043), and
	// runner.py re-imports that module for every python task — so a venv
	// holding only leoflow_runtime passes this gate and then fails every
	// python task in the pod. Gating on leoflow_runtime alone left a venv
	// built before that package existed looking fresh whenever the checksum
	// signal is empty.
	argv, rerr := os.ReadFile(argvLog)
	if rerr != nil {
		t.Fatalf("the python stub recorded no args: %v", rerr)
	}
	if !strings.Contains(string(argv), "import leoflow_runtime, leoflow") {
		t.Errorf("importability gate probed %q, want it to import leoflow_runtime AND leoflow", strings.TrimSpace(string(argv)))
	}
}

// TestEnsureWorkspaceDagVenvsEmptyFallback pins the no-projects boot path:
// when the workspace has no DAGs yet (the first user dropping a dag.py into
// a fresh workspace), the boot venv falls back to the legacy single-venv
// location so the server still has a usable LEOFLOW_PYTHON. A regression
// here would be a control plane that won't start until at least one DAG is
// registered — the exact onboarding hole this branch prevents.
func TestEnsureWorkspaceDagVenvsEmptyFallback(t *testing.T) {
	home := t.TempDir()
	ws := &WorkspaceSpec{Path: "ws", Projects: nil}
	got, err := ensureWorkspaceDagVenvs(context.Background(), devTestCmd(), ws, home, "runtime/python")
	if err != nil {
		t.Fatalf("ensureWorkspaceDagVenvs(empty): %v", err)
	}
	if want := venvPython(home); got != want {
		t.Errorf("empty workspace must fall back to legacy venv %q, got %q", want, got)
	}
}

func TestLiteVenvsRootEnvWiringFromSubprocessServerEnv(t *testing.T) {
	// The subprocess executor reads LEOFLOW_LITE_VENVS_ROOT to look up the
	// per-DAG interpreter for each task. Without this env, the per-DAG
	// install on the watcher side would create venvs the executor never
	// finds — a silent regression that would only surface as
	// ModuleNotFoundError on the first task. Pin the wire so the
	// regression is impossible.
	env := strings.Join(subprocessServerEnv(liteEnvParams{host: "127.0.0.1", port: 8088, adminHash: "", adminEmail: "", jwtSecret: ""}, "/bin/agent", "/proj", "/venv/py", "/h/venvs"), "\n")
	for _, must := range []string{
		"LEOFLOW_LITE_VENVS_ROOT=/h/venvs",
		// And the legacy LEOFLOW_PYTHON remains as the boot/fallback.
		"LEOFLOW_PYTHON=/venv/py",
	} {
		if !strings.Contains(env, must) {
			t.Errorf("subprocessServerEnv missing %q\nfull env:\n%s", must, env)
		}
	}
}
