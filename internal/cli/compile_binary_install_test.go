package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestDevSubprocessSetupExtractsPysrcBeforeVenv locks the ordering half of #587:
// on a binary-only install the per-DAG venv is built from the path
// resolveRuntimeSrc returns (~/.leoflow/pysrc/runtime/python), so the Lite boot
// must extract the bundled sources before it references that path — otherwise
// pip aborts with "'<home>/.leoflow/pysrc/runtime/python' does not exist".
//
// We exercise the real boot helper with an EMPTY workspace (zero projects), so
// no pip runs and the test stays fast and hermetic, and assert the boot has
// materialized the runtime source. Before the fix devSubprocessSetup never
// extracts pysrc, so this path stays absent and the test fails.
func TestDevSubprocessSetupExtractsPysrcBeforeVenv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dh, err := devHome()
	if err != nil {
		t.Fatalf("devHome: %v", err)
	}
	// The binary-only branch: no repo checkout, so resolveRuntimeSrc resolves
	// into the extracted pysrc tree.
	runtimeSrc := resolveRuntimeSrc("", dh)
	// Precondition: the runtime source does not exist yet — the exact state that
	// made the venv build fail during the soak.
	if _, statErr := os.Stat(filepath.Join(runtimeSrc, "pyproject.toml")); statErr == nil {
		t.Fatalf("precondition: %s must NOT exist before boot", runtimeSrc)
	}

	wsDir := t.TempDir()
	ws := &WorkspaceSpec{Path: wsDir} // zero projects → the boot provisions no venv

	cmd := &cobra.Command{}
	cmd.SetOut(os.Stderr)
	cmd.SetErr(os.Stderr)

	o := devOptions{host: "127.0.0.1", port: 18077, agentBin: "/bin/true"}
	if _, _, serr := devSubprocessSetup(t.Context(), cmd, ws, o, dh); serr != nil {
		t.Fatalf("devSubprocessSetup: %v", serr)
	}

	if _, statErr := os.Stat(filepath.Join(runtimeSrc, "pyproject.toml")); statErr != nil {
		t.Fatalf("boot must extract the runtime source at %s before provisioning the venv: %v", runtimeSrc, statErr)
	}
}

// TestCompileResolvesParserOnBinaryOnlyInstall is the #587 regression: on a
// fresh binary-only install (the release archive, no repo checkout) `leoflow
// compile` must find the Python parser without any PYTHONPATH set by the user.
//
// The only thing that puts `leoflow_parser` on the import path there is the
// binary's own extraction to ~/.leoflow/pysrc/parser (ensurePysrc, #239) — the
// default parser command is a bare `python3 -m leoflow_parser`, and nothing
// else wires that directory onto PYTHONPATH. CI masks the gap by exporting
// PYTHONPATH=parser from the repo root, so it only bit a real download.
//
// We reproduce the real environment faithfully: a clean HOME, the parser
// sources extracted exactly as the binary does it, the default parser_cmd (no
// config override, no --parser-cmd flag), and PYTHONPATH scrubbed. Before the
// fix this fails with ModuleNotFoundError: No module named leoflow_parser.
func TestCompileResolvesParserOnBinaryOnlyInstall(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH; resolving the real parser needs an interpreter")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)

	// Extract the bundled parser+runtime exactly as the binary does on first
	// use, so ~/.leoflow/pysrc/parser holds the importable leoflow_parser.
	if err := ensurePysrcIn(filepath.Join(home, ".leoflow", "pysrc"), nil); err != nil {
		t.Fatalf("extracting bundled parser sources: %v", err)
	}

	// A binary-only install has no repo on PYTHONPATH and the user sets none —
	// scrub any leaked from the dev shell so the only resolvable copy is the
	// extracted one under HOME.
	t.Setenv("PYTHONPATH", "")
	if err := os.Unsetenv("PYTHONPATH"); err != nil {
		t.Fatalf("unset PYTHONPATH: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatalf("init: %v", err)
	}
	out := filepath.Join(dir, "dag.json")

	// No parser_cmd in config and no --parser-cmd flag: resolveParserCommand
	// falls back to the default `python3 -m leoflow_parser`.
	if _, stderr, err := run(t, "compile", dir, "--output", out, "--image", "test:v1"); err != nil {
		t.Fatalf("compile on a binary-only install must resolve the extracted parser, got %v\nstderr=%s", err, stderr)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("dag.json missing after compile: %v", err)
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("compiled dag.json is invalid JSON: %v", err)
	}
	if err := spec.Validate(); err != nil {
		t.Errorf("compiled dag.json does not pass schema validation: %v", err)
	}
}
