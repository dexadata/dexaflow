package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// sanitizeDagIDForFs makes a dag_id safe to use as a single filesystem path
// component. Letters, digits, '_' and '-' pass through; anything else becomes
// '_'. An empty string maps to "_" so we never produce an empty component
// (which would collapse into the parent dir). Used to derive the per-DAG venv
// directory name from the user-controlled dag_id.
func sanitizeDagIDForFs(id string) string {
	if id == "" {
		return "_"
	}
	var b strings.Builder
	b.Grow(len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// dagVenvDir returns the per-DAG venv directory under the Lite dev home —
// ~/.dexaflow/dev/venvs/<sanitized-dag-id>. The "venvs" suffix (plural) is
// deliberate so it never collides with the legacy single-venv layout at
// ~/.dexaflow/dev/venv (#346).
func dagVenvDir(home, dagID string) string {
	return filepath.Join(home, "venvs", sanitizeDagIDForFs(dagID))
}

// removeDagVenv deletes a DAG's per-DAG venv (the Airflow SDK lives there, tens of
// MB) so the venv is reclaimed together with the DAG. Reports whether a venv was
// actually present and removed, so the caller can log it; a missing venv is a
// no-op, not an error (idempotent). A later reload re-creates the venv if the DAG
// comes back, via ensureWorkspaceDagVenvs.
func removeDagVenv(home, dagID string) (bool, error) {
	dir := dagVenvDir(home, dagID)
	if _, err := os.Stat(dir); err != nil {
		return false, nil //nolint:nilerr // a missing venv is nothing to remove
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, fmt.Errorf("removing venv %s: %w", dir, err)
	}
	return true, nil
}

// dagVenvPython returns the Python interpreter inside the per-DAG venv,
// honoring the platform layout (bin on Unix, Scripts on Windows).
func dagVenvPython(home, dagID string) string {
	dir := dagVenvDir(home, dagID)
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, "Scripts", "python.exe")
	}
	return filepath.Join(dir, "bin", "python")
}

// dagVenvDepsMarkerPath records which project dependencies the per-DAG venv
// currently has installed. Lives inside the venv directory so a single
// `rm -rf` of that directory wipes the freshness signal alongside the venv.
func dagVenvDepsMarkerPath(home, dagID string) string {
	return filepath.Join(dagVenvDir(home, dagID), ".leoflow-deps")
}

// dagVenvRuntimeMarkerPath records the checksum of the leoflow_runtime source
// last installed into the per-DAG venv, so a binary upgrade triggers a
// reinstall instead of leaving stale runtime in place (#239 carried over to
// the per-DAG layout).
func dagVenvRuntimeMarkerPath(home, dagID string) string {
	return filepath.Join(dagVenvDir(home, dagID), ".leoflow-runtime-checksum")
}

// installer is the Python package installer Lite uses to populate per-DAG
// venvs. uv is preferred when available (5–10× faster cold installs, #87),
// with pip as a portable fallback.
type installer string

const (
	installerPip installer = "pip"
	installerUv  installer = "uv"
)

// detectInstaller reports which installer Lite should use, preferring uv on
// PATH and falling back to pip. lookPath is the seam that lets tests force
// uv vs pip without touching the real PATH.
func detectInstaller(lookPath func(string) (string, error)) installer {
	if _, err := lookPath("uv"); err == nil {
		return installerUv
	}
	return installerPip
}

// installerCmd returns the (cmd, args) to install `packages` into the venv
// whose Python is `py`. For pip it shells the venv's own Python with
// `-m pip install`, so the install lands in that venv regardless of $PATH.
// For uv it invokes the global uv binary with `--python <py>` so uv resolves
// and writes into the target venv directly (uv does the activation itself).
//
// The argv shapes are pinned by TestInstallerCmdShapes so the wire never
// silently drifts (a missing `--python` on uv would install into a stray
// global env; a missing `-m` on pip would shell out to a system pip with
// the wrong site-packages).
func installerCmd(inst installer, py string, packages []string) (cmd string, args []string) {
	switch inst {
	case installerUv:
		args = append([]string{"pip", "install", "-q", "--python", py}, packages...)
		return "uv", args
	default:
		args = append([]string{"-m", "pip", "install", "-q"}, packages...)
		return py, args
	}
}

// ensureDagVenv creates the per-DAG venv under home/venvs/<dag_id>/ if absent
// and installs the task runtime + Airflow SDK + the project's declared
// dependencies into it (skipping the install when the runtime is already
// present AND its source checksum has not drifted, and skipping deps when the
// dep marker matches). Returns the venv's Python path — what the subprocess
// executor exports as LEOFLOW_PYTHON for tasks of that DAG.
//
// The freshness gates mirror the single-venv ensureDevVenv (deps signature +
// runtime checksum) but are scoped to one DAG, so editing one project's
// `dependencies:` never re-runs pip for every other project in the workspace.
func ensureDagVenv(ctx context.Context, cmd *cobra.Command, home, dagID, runtimeSrc, pythonVersion string, deps []string) (string, error) {
	py := dagVenvPython(home, dagID)
	dir := dagVenvDir(home, dagID)
	inst := detectInstaller(exec.LookPath)

	stale, staleMinor, serr := dagVenvMinorMismatch(dir, pythonVersion)
	if serr != nil {
		return "", serr
	}
	_, statErr := os.Stat(py)
	if stale || statErr != nil {
		// Resolve the base interpreter BEFORE discarding anything. A declared
		// minor the host does not have aborts the command, and the reload path
		// keeps serving whatever was last registered — so the venv that boot was
		// built on has to survive the failure. Discarding first left the author
		// who typed a version they do not have with no venv at all, and a full
		// dependency reinstall even after reverting the edit.
		base, berr := devBasePython(ctx, home, pythonVersion)
		if berr != nil {
			return "", berr
		}
		if stale {
			devPrintf(cmd.OutOrStdout(), "▸ %q declares python_version %s but its venv was built on 3.%d — rebuilding it (this reinstalls the runtime and every declared dependency)\n", dagID, pythonVersion, staleMinor)
			if rerr := os.RemoveAll(dir); rerr != nil {
				return "", fmt.Errorf("removing the %q venv built on Python 3.%d: %w", dagID, staleMinor, rerr)
			}
		}
		devPrintf(cmd.OutOrStdout(), "▸ creating isolated dev venv for %q …\n", dagID)
		mk := exec.CommandContext(ctx, base, "-m", "venv", dir) //nolint:gosec // base is the managed CPython or a resolved python3 >= 3.11
		mk.Stdout, mk.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
		if e := mk.Run(); e != nil {
			return "", fmt.Errorf("creating dev venv for %q with %s (the managed CPython bundles venv; a system python3 may need its python3-venv package): %w", dagID, base, e)
		}
		// A marker says "THIS interpreter has X", so it cannot outlive the
		// interpreter. The stale branch above removes the whole directory and
		// takes the markers with it, but the interpreter-missing branch does not:
		// a partial delete or a python symlink into an upgraded-away build leaves
		// a valid-looking pyvenv.cfg, `venv` runs over the directory with no
		// --clear, and the surviving marker asserts dependencies that the new
		// interpreter has never been given (#1096).
		discardDagVenvMarkers(home, dagID)
	}

	if err := ensureDagVenvRuntime(ctx, cmd, home, dagID, runtimeSrc, py, inst); err != nil {
		return "", err
	}

	// Project deps: (re)install when the project's `dependencies:` change.
	// Skipped entirely when the project declares none — most quick-demo DAGs
	// only need runtime + SDK and pay no install cost after the first boot.
	if len(deps) > 0 && !dagVenvDepsUpToDate(home, dagID, deps) {
		devPrintf(cmd.OutOrStdout(), "▸ installing %d dep(s) into the %q venv (using %s) …\n", len(deps), dagID, inst)
		runCmd, runArgs := installerCmd(inst, py, deps)
		install := exec.CommandContext(ctx, runCmd, runArgs...) //nolint:gosec // runCmd is the venv's py or "uv" — both vetted
		install.Stdout, install.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
		if e := install.Run(); e != nil {
			return "", fmt.Errorf("installing deps into venv %q: %w", dagID, e)
		}
		if e := os.WriteFile(dagVenvDepsMarkerPath(home, dagID), []byte(devDepsSignature(deps)), 0o600); e != nil {
			return "", fmt.Errorf("recording deps marker for %q: %w", dagID, e)
		}
	}
	return py, nil
}

// dagVenvRuntimeChecksum returns the checksum recorded by the most recent
// runtime install into the per-DAG venv (empty when no marker yet).
func dagVenvRuntimeChecksum(home, dagID string) string {
	b, err := os.ReadFile(dagVenvRuntimeMarkerPath(home, dagID)) //nolint:gosec // path derived from the per-user dev home + sanitized dag_id
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// dagVenvDepsUpToDate reports whether the per-DAG venv already has exactly
// these deps installed, by signature comparison.
func dagVenvDepsUpToDate(home, dagID string, deps []string) bool {
	b, err := os.ReadFile(dagVenvDepsMarkerPath(home, dagID)) //nolint:gosec // path derived from the per-user dev home + sanitized dag_id
	if err != nil {
		return false
	}
	return string(b) == devDepsSignature(deps)
}

// dagVenvMinorMismatch reports whether a per-DAG venv was built on a different
// Python minor than the project now declares, along with the minor it was
// actually built on.
//
// ensureDagVenv short-circuits on the venv's interpreter file existing, so
// without this an author who edits `python_version` on a project they have
// already run keeps the old interpreter indefinitely and the declared value
// applies only to venvs that never existed (#1092).
//
// A venv whose minor cannot be read is reported as matching rather than stale:
// an unreadable pyvenv.cfg would otherwise mean a full reinstall on every boot,
// which is a worse failure than the skew this guards against.
//
// Deciding is separated from discarding on purpose — the caller resolves the
// interpreter for the declared minor first, so a version the host does not have
// leaves the existing venv intact instead of destroying it on the way to an
// error.
func dagVenvMinorMismatch(dir, pythonVersion string) (stale bool, builtOn int, err error) {
	if pythonVersion == "" {
		return false, 0, nil
	}
	want, perr := parsePythonMinor(pythonVersion)
	if perr != nil {
		return false, 0, perr
	}
	have, ok := pyvenvMinor(dir)
	if !ok || have == want {
		return false, 0, nil
	}
	return true, have, nil
}

// ensureDagVenvRuntime installs the task runtime + Airflow SDK into a per-DAG
// venv when either (a) the runtime is not importable (fresh venv) or (b) the
// installed runtime's checksum drifts from the bundled pysrc — the
// binary-upgrade case (#239). An empty checksum makes the import gate the sole
// signal.
//
// All three packages are probed: leoflow_runtime runs the task, and dexaflow
// (and leoflow, its pre-rename re-export) carry the authoring names a dag.py
// imports at its top, which the runner re-imports
// per task, so a venv missing it fails every python task (#17). Gating on
// leoflow_runtime alone would leave a venv built before that package existed
// looking healthy whenever the checksum signal is empty.
func ensureDagVenvRuntime(ctx context.Context, cmd *cobra.Command, home, dagID, runtimeSrc, py string, inst installer) error {
	check := exec.CommandContext(ctx, py, "-c", "import leoflow_runtime, leoflow, dexaflow") //nolint:gosec // py is a managed per-DAG venv interpreter
	importOK := check.Run() == nil
	want, cerr := runtimeSrcChecksum(runtimeSrc)
	need := !importOK
	if cerr == nil && want != "" && dagVenvRuntimeChecksum(home, dagID) != want {
		need = true
	}
	if !need {
		return nil
	}
	devPrintf(cmd.OutOrStdout(), "▸ installing task runtime + Airflow SDK into the %q venv (using %s) …\n", dagID, inst)
	runCmd, runArgs := installerCmd(inst, py, []string{runtimeSrc, taskSDKVersion})
	install := exec.CommandContext(ctx, runCmd, runArgs...) //nolint:gosec // runCmd is the venv's py or "uv" — both vetted
	install.Stdout, install.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	if e := install.Run(); e != nil {
		return fmt.Errorf("installing runtime into venv %q: %w", dagID, e)
	}
	if want != "" {
		if e := os.WriteFile(dagVenvRuntimeMarkerPath(home, dagID), []byte(want+"\n"), 0o600); e != nil {
			// Best-effort marker write: a failure means the next startup
			// reinstalls (slow, not broken) — better than leaving a stale
			// marker that masks the upgrade.
			devPrintf(cmd.OutOrStdout(), "  (warning) recording runtime checksum for %q: %v\n", dagID, e)
		}
	}
	return nil
}

// discardDagVenvMarkers drops the per-DAG freshness markers after the venv's
// interpreter has been created.
//
// Best effort and silent on absence: a fresh venv has no markers, and failing a
// boot because a file that should not exist could not be removed would trade a
// real problem for an imaginary one. What matters is that a marker never
// survives the interpreter it describes; if the removal fails, the worst case is
// exactly the behavior that exists today.
func discardDagVenvMarkers(home, dagID string) {
	_ = os.Remove(dagVenvDepsMarkerPath(home, dagID))    //nolint:errcheck // best effort; absence is the desired state
	_ = os.Remove(dagVenvRuntimeMarkerPath(home, dagID)) //nolint:errcheck // best effort; absence is the desired state
}
