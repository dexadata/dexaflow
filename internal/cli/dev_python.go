package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/setup"
)

// ensureWorkspaceDagVenvs creates / refreshes the per-DAG venv for every
// project in the workspace and returns the first project's venv Python, which
// the control plane exports as LEOFLOW_PYTHON for the boot fallback path.
// Failure to provision any one project's venv stops the loop and surfaces
// which project failed: there is no point pretending the workspace is up
// when a DAG cannot import its runtime.
func ensureWorkspaceDagVenvs(ctx context.Context, cmd *cobra.Command, ws *WorkspaceSpec, home, runtimeSrc string) (bootPy string, err error) {
	for _, p := range ws.Projects {
		dagID := p.DagID
		deps := []string(nil)
		pyVersion := ""
		if p.Config != nil {
			var derr error
			if deps, derr = p.Config.EffectiveDependencies(); derr != nil {
				return "", fmt.Errorf("resolving dependencies for project %q: %w", p.Path, derr)
			}
			pyVersion = devEnforcedPythonVersion(cmd, p.Config)
		}
		py, verr := ensureDagVenv(ctx, cmd, home, dagID, runtimeSrc, pyVersion, deps)
		if verr != nil {
			return "", fmt.Errorf("provisioning venv for project %q: %w", p.Path, verr)
		}
		if bootPy == "" {
			bootPy = py
		}
	}
	if bootPy == "" {
		// No projects discovered yet, fall back to the legacy single-venv
		// location so the server can still boot with a usable LEOFLOW_PYTHON.
		// A later watcher tick will create per-DAG venvs once the user adds a
		// dag.py to the workspace.
		bootPy = venvPython(home)
	}
	return bootPy, nil
}

// venvPython returns the Python interpreter inside the dev venv under home,
// honoring the platform layout (bin on Unix, Scripts on Windows).
func venvPython(home string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "venv", "Scripts", "python.exe")
	}
	return filepath.Join(home, "venv", "bin", "python")
}

// minPythonMinor is the lowest Python 3.x the task runtime + Airflow SDK
// support; the managed relocatable CPython is pinned to this minor.
const minPythonMinor = 11

// devBasePython returns the interpreter used to CREATE a dev venv.
//
// With a declared wantVersion ("3.13") it resolves an interpreter REPORTING that
// minor, and refuses rather than substituting (see resolvePythonFor). With no
// declared version it keeps the historical precedence: the managed relocatable
// CPython (installed under ~/.dexaflow/python) when present, since it bundles
// venv + ensurepip, falling back to a python3.11 / python3 on PATH that reports
// >= 3.11. Using the managed interpreter avoids needing the system python3-venv
// package, which Debian/Ubuntu split out (the common first-run failure:
// "ensurepip is not available").
//
// `dexaflow setup` no longer downloads the managed CPython when the host has a
// Python 3.12 or 3.13 for the parser (#1700), so this is where it is fetched:
// lazily, the first time a venv needs the pinned minor (a declared 3.11, or no
// declaration and no usable host interpreter) and the host has none, or when
// the host interpreter cannot build a venv (see basePythonDeps.venvCapable).
//
// It errors when an interpreter IS present but reports an unsupported version,
// or when a declared minor other than the pinned one is missing, rather than
// returning a bare "python3" that fails opaquely later (#742).
func devBasePython(ctx context.Context, out io.Writer, home, wantVersion string) (string, error) {
	return basePythonFor(ctx, wantVersion, devBasePythonDeps(out, filepath.Dir(home)))
}

// devBasePythonDeps wires basePythonFor to the real host: PATH, `--version`,
// the venv probe, the lazy managed download into stateDir, and progress lines
// on out.
func devBasePythonDeps(out io.Writer, stateDir string) basePythonDeps {
	return basePythonDeps{
		managed:    setup.ManagedPythonPath(stateDir),
		lookPath:   exec.LookPath,
		runVersion: pythonVersion,
		ensureManaged: func(ctx context.Context) (string, error) {
			return provisionManagedPython(ctx, out, stateDir, nil)
		},
		canVenv: pythonCanVenv,
		logf: func(format string, a ...any) {
			devPrintf(out, "▸ "+format+"\n", a...)
		},
	}
}

// basePythonDeps carries what basePythonFor touches, so the lazy download is
// testable against a fake PATH without the network.
type basePythonDeps struct {
	managed       string
	lookPath      func(string) (string, error)
	runVersion    func(context.Context, string) (int, int, error)
	ensureManaged func(context.Context) (string, error)
	// canVenv reports whether an interpreter can build a venv; nil skips the
	// check.
	canVenv func(context.Context, string) bool
	// logf prints a progress line; nil prints nothing.
	logf func(string, ...any)
}

// pythonCanVenv reports whether py can build a venv with pip in it. Debian
// and Ubuntu ship ensurepip in the separate python3-venv package, so a system
// interpreter there imports venv but cannot run `-m venv`.
func pythonCanVenv(ctx context.Context, py string) bool {
	return exec.CommandContext(ctx, py, "-c", "import venv, ensurepip").Run() == nil //nolint:gosec // py is a resolved interpreter path (LookPath result)
}

// venvCapable returns the host interpreter p, or the managed CPython when p
// cannot build a venv and the managed build is the minor the DAG needs (want is
// the declared minor, 0 when none is declared). Before #1700 setup had always
// fetched the managed build, which bundles ensurepip, so a Debian or Ubuntu
// host without python3-venv never met this; now the host interpreter is found
// first. A declared minor Dexaflow does not pin is never substituted: p is
// returned and the venv step's error names the missing package.
func (d basePythonDeps) venvCapable(ctx context.Context, want int, p string) (string, error) {
	if p == d.managed || d.canVenv == nil || d.canVenv(ctx, p) {
		return p, nil
	}
	if want != 0 && want != minPythonMinor {
		return p, nil
	}
	if d.logf != nil {
		d.logf("%s cannot create a venv (its venv or ensurepip module is missing; on Debian and Ubuntu "+
			"that is the python3-venv package), so the managed CPython 3.%d is used instead", p, minPythonMinor)
	}
	return d.ensureManaged(ctx)
}

// basePythonFor is devBasePython with its dependencies injected.
func basePythonFor(ctx context.Context, wantVersion string, d basePythonDeps) (string, error) {
	// A declared python_version is the authoring surface's statement about which
	// interpreter the task runs on, and the cluster honors it through the task
	// base image. Honor it here too, or `dexaflow lite` validates the DAG on an
	// interpreter the deployment will never use (#1092).
	if wantVersion != "" {
		want, verr := parsePythonMinor(wantVersion)
		if verr != nil {
			return "", verr
		}
		p, err := resolvePythonFor(ctx, want, d.managed, d.lookPath, d.runVersion)
		if err != nil {
			if want != minPythonMinor {
				return p, err
			}
			// The pinned minor is the one Dexaflow can provide itself.
			return d.ensureManaged(ctx)
		}
		return d.venvCapable(ctx, want, p)
	}
	p, err := resolvePython3(ctx, d.managed, d.lookPath, d.runVersion)
	if err != nil {
		return "", err
	}
	if p == "" {
		return d.ensureManaged(ctx)
	}
	return d.venvCapable(ctx, 0, p)
}

// provisionManagedPython downloads and verifies the pinned managed CPython into
// stateDir/python, the same build `dexaflow setup` used to fetch up front, and
// turns a failed download into a message that names the way out. A nil client
// uses the setup package's default, as `dexaflow setup` does, which honors
// HTTPS_PROXY.
func provisionManagedPython(ctx context.Context, out io.Writer, stateDir string, client *http.Client) (string, error) {
	p, err := setup.EnsureManagedPython(ctx, setup.EnsureOpts{
		Home: stateDir, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Libc: detectLibc(),
		Stat: os.Stat, Client: client,
		Logf: func(format string, a ...any) {
			devPrintf(out, "▸ "+format+"\n", a...)
		},
	})
	if err != nil {
		return "", fmt.Errorf("no host interpreter reports Python 3.%d, and fetching the managed CPython 3.%d failed "+
			"(it needs outbound network; check your proxy settings, or install python3.%d and retry): %w",
			minPythonMinor, minPythonMinor, minPythonMinor, err)
	}
	return p, nil
}

// leoflowManagedPython returns the path to the managed relocatable CPython that
// `dexaflow setup` installs under ~/.dexaflow/python, or "" if the home directory
// cannot be resolved.
func leoflowManagedPython() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return setup.ManagedPythonPath(stateDirIn(h))
}

// resolvePython3 returns a usable Python >= 3.11 with a single, unified
// precedence shared by `dexaflow lite` and `dexaflow validate` (#742):
//
//   - the managed pinned build (when present) wins: it is the checksum-verified
//     CPython `dexaflow setup` provisioned at exactly the pinned version, so it is
//     trusted without being re-executed;
//   - otherwise the first host python3.11/python3 that reports >= 3.11 is used;
//   - a host interpreter that IS present but reports an unsupported version is
//     rejected with an actionable `dexaflow setup` hint, so a half-provisioned
//     host never silently runs an unsupported interpreter that fails later at an
//     unrelated component;
//   - when no interpreter exists at all, it returns ("", nil) and lets the
//     caller decide whether that is fatal.
//
// managed is the managed interpreter path (may be ""); lookPath and runVersion
// are injectable for tests.
func resolvePython3(ctx context.Context, managed string, lookPath func(string) (string, error), runVersion func(context.Context, string) (int, int, error)) (string, error) {
	if managed != "" {
		if _, err := os.Stat(managed); err == nil {
			return managed, nil
		}
	}
	sawUnsupported := false
	for _, name := range []string{"python3.11", "python3"} {
		p, err := lookPath(name)
		if err != nil {
			continue
		}
		major, minor, verr := runVersion(ctx, p)
		if verr != nil {
			continue // cannot determine version; try the next candidate
		}
		if major > 3 || (major == 3 && minor >= minPythonMinor) {
			return p, nil
		}
		sawUnsupported = true
	}
	if sawUnsupported {
		return "", fmt.Errorf("found a Python interpreter but it is older than 3.%d; run `dexaflow setup` to provision a managed CPython", minPythonMinor)
	}
	return "", nil
}

// pythonVersion runs `<py> --version` and returns the reported major.minor.
func pythonVersion(ctx context.Context, py string) (major, minor int, err error) {
	return setup.PythonVersion(ctx, py)
}

// resolveRuntimeSrc returns the leoflow_runtime package source to pip-install
// into the dev venv. An explicit --runtime-src wins; otherwise the repo path
// (source checkout) is used when present; otherwise the copy `dexaflow setup`
// extracted under ~/.dexaflow/pysrc (a binary-only install has no repo, so the
// repo-relative "runtime/python" does not exist there).
func resolveRuntimeSrc(flagValue, home string) string {
	if flagValue != "" && flagValue != "runtime/python" {
		return flagValue
	}
	if _, err := os.Stat(filepath.Join("runtime", "python", "pyproject.toml")); err == nil {
		return "runtime/python"
	}
	return filepath.Join(filepath.Dir(home), "pysrc", "runtime", "python")
}

// devDepsSignature is an order-independent canonical form of a dependency list, so
// reordering `dependencies:` does not force a needless reinstall.
func devDepsSignature(deps []string) string {
	s := append([]string(nil), deps...)
	sort.Strings(s)
	return strings.Join(s, "\n")
}

// runtimeSrcChecksum returns a stable SHA-256 hex digest covering every `.py`
// file under runtimeSrc, walked in deterministic path order. Used by
// ensureDagVenv to decide whether the venv's installed leoflow_runtime has
// drifted from the bundled pysrc after a binary upgrade (#239).
//
// Non-Python files (READMEs, __pycache__/*.pyc, etc.) are ignored: a stray
// .pyc generated by a previous run would otherwise force perpetual
// reinstalls.
func runtimeSrcChecksum(runtimeSrc string) (string, error) {
	var files []string
	err := filepath.WalkDir(runtimeSrc, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".py") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walking runtime source: %w", err)
	}
	sort.Strings(files)

	h := sha256.New()
	for _, p := range files {
		rel, rerr := filepath.Rel(runtimeSrc, p)
		if rerr != nil {
			return "", fmt.Errorf("relativizing %s: %w", p, rerr)
		}
		// Hash the path + a separator + the content + a separator so two files
		// can't collide by content concatenation (e.g. moving lines between files).
		h.Write([]byte(rel))
		h.Write([]byte{0})
		b, rerr := os.ReadFile(p) //nolint:gosec // path is under the bundled pysrc tree
		if rerr != nil {
			return "", fmt.Errorf("reading %s: %w", p, rerr)
		}
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// parsePythonMinor turns a `python_version` field ("3.13") into its minor. The
// field is an authoring-surface value that decides which interpreter runs the
// task, so anything that is not exactly `3.<minor>` is refused rather than
// coerced: a coerced version is how dev and the cluster end up disagreeing
// without anyone being told (#1092).
func parsePythonMinor(v string) (int, error) {
	major, minor, ok := strings.Cut(v, ".")
	if !ok || major != "3" || minor == "" || strings.Contains(minor, ".") {
		return 0, fmt.Errorf("python_version %q is not of the form 3.<minor> (e.g. \"3.13\")", v)
	}
	_, n, err := setup.PythonMajorMinor(v)
	if err != nil {
		// Past the check above only the minor can fail; wrap its strconv error
		// alone so the message names nothing but the field.
		var numErr *strconv.NumError
		if errors.As(err, &numErr) {
			err = numErr
		}
		return 0, fmt.Errorf("python_version %q is not of the form 3.<minor> (e.g. \"3.13\"): %w", v, err)
	}
	return n, nil
}

// resolvePythonFor resolves the interpreter for a DECLARED python_version, and
// differs from resolvePython3 on the one point that caused #1092: the managed
// build is trusted by path when nothing was requested, and is version-CHECKED
// when something was. Trusting the path is what silently gave a project pinned
// to 3.13 a 3.11 venv, because the managed build's path ends in `python3.11`.
//
// Order: the managed build when it reports the requested minor, then
// `python3.<minor>`, then the shared candidate list, then bare `python3`, each
// accepted only on the version it REPORTS, never on its name. When nothing
// matches it returns an error naming the version asked for and the versions
// found, because "install 3.13" is the only useful next step and the caller
// cannot phrase it.
func resolvePythonFor(ctx context.Context, want int, managed string,
	lookPath func(string) (string, error),
	runVersion func(context.Context, string) (int, int, error),
) (string, error) {
	matches := func(p string) bool {
		major, minor, err := runVersion(ctx, p)
		return err == nil && major == 3 && minor == want
	}
	if managed != "" {
		if _, err := os.Stat(managed); err == nil && matches(managed) {
			return managed, nil
		}
	}
	// `python3.<want>` first: on a host with several minors it is the only name
	// that cannot resolve to the wrong one.
	names := append([]string{fmt.Sprintf("python3.%d", want)}, setup.PythonCandidates()...)
	names = append(names, "python3")
	seen := map[string]bool{}
	var found []string
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		p, err := lookPath(name)
		if err != nil {
			continue
		}
		major, minor, verr := runVersion(ctx, p)
		if verr != nil {
			continue
		}
		if major == 3 && minor == want {
			return p, nil
		}
		found = append(found, fmt.Sprintf("%d.%d", major, minor))
	}
	if len(found) > 0 {
		return "", fmt.Errorf("this project declares python_version 3.%d, but the only interpreters on this host report %s; %s, or change python_version to a version you have",
			want, strings.Join(dedupe(found), ", "), installHint(want))
	}
	return "", fmt.Errorf("this project declares python_version 3.%d and no Python interpreter was found; %s", want, installHint(want))
}

// installHint names the action that actually produces the requested minor.
// It never offers `dexaflow setup`: setup provisions no venv interpreter, and
// since #1700 it does not even fetch the managed build when the host has a
// Python 3.12 or 3.13 for the parser, so offering it sends the user around a
// loop that cannot end: setup succeeds, the version is still missing, and the
// same error comes back. The pinned minor is the one Dexaflow can fetch itself.
func installHint(want int) string {
	if want == minPythonMinor {
		return fmt.Sprintf("install Python 3.%d (with network access, `dexaflow lite` fetches a managed CPython 3.%d itself)", want, want)
	}
	return fmt.Sprintf("install Python 3.%d", want)
}

// dedupe keeps the first occurrence of each entry, so an error that lists the
// versions found does not repeat "3.11" once per alias pointing at it.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// pyvenvMinor reports the Python minor a venv was BUILT with, read from its
// pyvenv.cfg. ensureDagVenv short-circuits on the interpreter file existing, so
// without this a venv created before the project changed `python_version` keeps
// serving the old interpreter forever and the declared version would apply only
// to venvs that never existed.
//
// A venv whose version cannot be determined reports ok=false rather than 0, so
// an unreadable cfg leaves the venv alone instead of rebuilding it every boot.
func pyvenvMinor(dir string) (int, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "pyvenv.cfg")) //nolint:gosec // dir is a leoflow-managed venv path
	if err != nil {
		return 0, false
	}
	// Only the minor is read: the major of a pyvenv.cfg version has never been
	// checked, so it is replaced before parsing and a bad one cannot hide the
	// minor.
	minorOf := func(v string) (int, error) {
		_, rest, ok := strings.Cut(v, ".")
		if !ok {
			return 0, fmt.Errorf("unrecognized version %q", v)
		}
		_, m, err := setup.PythonMajorMinor("3." + rest)
		return m, err
	}
	var fallback string
	for _, line := range strings.Split(string(b), "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "version":
			if m, err := minorOf(strings.TrimSpace(val)); err == nil {
				return m, true
			}
		case "version_info":
			fallback = strings.TrimSpace(val)
		}
	}
	if fallback != "" {
		if m, err := minorOf(fallback); err == nil {
			return m, true
		}
	}
	return 0, false
}

// devEnforcedPythonVersion reports the python_version a dev venv must be built
// on, or "" to keep the historical precedence (managed build, else any host
// python3 >= 3.11).
//
// Enforcing this field is right only when it says something. It says nothing in
// three cases, and refusing to boot in any of them would break a working dev
// loop over a choice the user never made:
//
//   - ApplyDefaults fills it with "3.11" for every config that omits it, and
//     discovery defaults EVERY project, including directories with no
//     dexaflow.yaml at all. Enforcing there turns "no python3.11 on this host"
//     from a silent, working fallback into a hard stop, on a machine where
//     nothing is actually inconsistent: the image is py3.11 because the same
//     default chose it, so dev and the cluster already agree.
//   - An explicit build.base_image wins over python_version when the image is
//     resolved (resolveBaseImage), so the field has no bearing on what the
//     cluster runs and cannot be skewed against it.
//   - A deprecated version is one we are withdrawing. Refusing tells the author
//     to install an interpreter that is on its way out, to keep using a version
//     that is on its way out. Warn and fall back, which is the nudge that
//     matches the direction of travel.
func devEnforcedPythonVersion(cmd *cobra.Command, cfg *domain.LeoflowConfig) string {
	if cfg == nil || cfg.PythonVersionDefaulted || cfg.BaseImage != "" {
		return ""
	}
	if _, deprecated := domain.DeprecatedPythonVersion(cfg.PythonVersion); deprecated {
		devPrintf(cmd.OutOrStdout(), "▸ python_version %s is deprecated; building this venv on the default interpreter instead; migrate to a supported version\n", cfg.PythonVersion)
		return ""
	}
	return cfg.PythonVersion
}
