package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/version"
)

// resolveAndReport locates a companion binary and announces the choice, so the
// two always happen together: a resolution nobody printed is what let a
// month-old server run unnoticed (#471).
func resolveAndReport(ctx context.Context, cmd *cobra.Command, explicit, kind string) (string, error) {
	path, err := resolveCompanion(explicit, kind)
	if err != nil {
		return "", err
	}
	if werr := reportCompanionBinary(ctx, cmd, "dexaflow-"+kind, path); werr != nil {
		return "", werr
	}
	return path, nil
}

// reportCompanionBinary announces which companion binary was chosen and whether
// its version matches this CLI's.
//
// The path alone is not enough. When `dexaflow lite` silently ran a month-old
// leoflow-server, every visible signal (the banner, /readyz, the logs) looked
// correct, and the mismatch was found only by hashing the running process
// afterwards. The trio is co-versioned (ADR 0028), so a disagreement is never
// intentional and is worth interrupting for.
//
// Best-effort: a binary that will not report its version still runs. Refusing to
// start over an unreadable version string would turn a diagnostic into an
// outage, and the resolution order already makes the mismatch rare.
func reportCompanionBinary(ctx context.Context, cmd *cobra.Command, kind, path string) error {
	got := companionVersion(ctx, path)
	mine := version.Get().Version
	var werr error
	switch {
	case got == "":
		_, werr = fmt.Fprintf(cmd.ErrOrStderr(), "  %s: %s (version unavailable)\n", kind, path)
	case mine != "" && got != mine:
		_, werr = fmt.Fprintf(cmd.ErrOrStderr(),
			"  %s: %s\n  WARNING: %s reports %s but this CLI is %s. They ship together and are\n"+
				"  meant to match; a mismatch usually means an older copy was found first.\n"+
				"  Pass --%s to pin the one you want.\n",
			kind, path, kind, got, mine, kind)
	default:
		_, werr = fmt.Fprintf(cmd.ErrOrStderr(), "  %s: %s (%s)\n", kind, path, got)
	}
	return werr
}

// companionVersion asks a binary for its version, returning "" when it cannot
// be determined. Bounded so a wedged binary cannot hang startup.
//
// The version check is best-effort and must never take the CLI down with it: a
// nil parent context (which context.WithTimeout would panic on ("cannot create
// context from nil parent") is treated as no context rather than a fatal
// error.
//
//nolint:contextcheck // the nil-ctx fallback below has no parent to inherit; a fresh root is the only safe choice, and the CLI must not panic over a best-effort version probe.
func companionVersion(ctx context.Context, path string) string {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output() //nolint:gosec // path resolved by resolveBinary, not user input
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	// Output shape: "<name> <version> (commit …, built …, go…)".
	if len(fields) >= 2 {
		return fields[1]
	}
	return ""
}

// resolveBinary locates a companion binary, preferring the copy that belongs
// with the running CLI over whatever happens to be installed.
//
// Order: an explicit --flag, then the directory holding this executable, then
// the installer's ~/.dexaflow/bin, then PATH, then ./bin.
//
// PATH used to come first, which meant `dexaflow lite` ran whatever
// leoflow-server was installed earliest, however old. A validation run against
// v0.1.2-rc.1 spent its first boot exercising a v0.1.0-rc.4 server that predated
// every feature under test, and the mismatch was invisible: the banner, /readyz
// and the logs all looked correct. The trio is co-versioned (ADR 0028), so the
// binary shipped beside the running CLI is the one it was built and tested
// against; an older copy earlier on PATH is never the intended answer (#471).
//
// PATH is still consulted, so anyone relying on it keeps working; it is simply
// no longer the first answer.
func resolveBinary(explicit, name string) (string, error) {
	return resolveBinaryNamed(explicit, name)
}

// resolveCompanion finds the companion binary of the given kind ("server",
// "agent") under its current name, dexaflow-<kind>, or its pre-rename name,
// leoflow-<kind>. Location keeps its priority over name: within each place the
// new name is tried first, but a co-versioned sibling under the old name still
// beats a new-name copy that is merely on PATH.
func resolveCompanion(explicit, kind string) (string, error) {
	return resolveBinaryNamed(explicit, "dexaflow-"+kind, "leoflow-"+kind)
}

// resolveBinaryNamed searches, in order, beside this binary, the install
// directories, PATH and ./bin, trying every name at each place before moving on.
func resolveBinaryNamed(explicit string, names ...string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	for _, dir := range companionDirs() {
		for _, name := range names {
			cand := filepath.Join(dir, name)
			if isExecutableFile(cand) {
				return cand, nil
			}
		}
	}
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	for _, name := range names {
		local := filepath.Join("bin", name)
		if isExecutableFile(local) {
			// Absolute, because the subprocess executor runs the agent with a different
			// working directory; a relative path would not resolve there.
			return filepath.Abs(local)
		}
	}
	return "", fmt.Errorf("%s not found beside this binary, in ~/.dexaflow/bin or ~/.leoflow/bin, on PATH, or in ./bin; "+
		"run `make build` or pass --%s", names[0], names[0])
}

// companionDirs lists the directories that hold binaries shipped WITH this one,
// most-specific first. Both are best-effort: a failure to resolve either simply
// falls through to PATH.
func companionDirs() []string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
		dirs = append(dirs, filepath.Dir(exe))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".dexaflow", "bin"), filepath.Join(home, ".leoflow", "bin"))
	}
	return dirs
}

// isExecutableFile reports whether path is a regular file with an execute bit.
// The directory check matters: `~/.dexaflow/bin/leoflow-agent/` as a directory
// would otherwise satisfy a plain os.Stat and be handed to exec.
func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o111 != 0
}
