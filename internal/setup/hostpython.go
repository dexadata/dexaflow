package setup

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// hostPythonNames lists the binary names `dexaflow setup` probes, in preference
// order, for a host interpreter to point parser_cmd at (#1700). The newest
// minor wins because a host that installed it on purpose usually keeps it
// patched; bare `python3` comes last because its minor is whatever the
// distribution ships. Every candidate is accepted only on the version it
// REPORTS, so a `python3` that is really 3.9 is never used.
var hostPythonNames = []string{"python3.13", "python3.12", "python3.11", "python3"}

// The range of host minors setup accepts for the parser. The parser shim is
// stdlib-only (ADR 0024), so any of these can parse a dag.py; the bounds are
// the minors it is tested on, which is narrower than pythonCandidates on
// purpose: setup records the interpreter in parser_cmd for good, so it only
// takes one the project has verified.
const (
	minHostPythonMinor = 11
	maxHostPythonMinor = 13
)

// HostPython is a host interpreter `dexaflow setup` can use for the parser.
type HostPython struct {
	Path  string // absolute path LookPath returned
	Minor int    // the 3.x minor the interpreter reported
}

// VersionFunc runs an interpreter and returns the major and minor it reports.
type VersionFunc func(ctx context.Context, path string) (major, minor int, err error)

// FindHostPython returns the first host interpreter, in hostPythonNames order,
// that reports a Python 3.11 to 3.13. ok is false when none qualifies, which is
// the only case where setup still has to download the managed CPython.
func FindHostPython(ctx context.Context, lookPath func(string) (string, error), runVersion VersionFunc) (HostPython, bool) {
	if lookPath == nil {
		return HostPython{}, false
	}
	if runVersion == nil {
		runVersion = PythonVersion
	}
	for _, name := range hostPythonNames {
		p, err := lookPath(name)
		if err != nil {
			continue
		}
		major, minor, verr := runVersion(ctx, p)
		if verr != nil || major != 3 || minor < minHostPythonMinor || minor > maxHostPythonMinor {
			continue
		}
		return HostPython{Path: p, Minor: minor}, true
	}
	return HostPython{}, false
}

// PythonVersion runs `<py> --version` and returns the reported major.minor.
func PythonVersion(ctx context.Context, py string) (major, minor int, err error) {
	out, err := exec.CommandContext(ctx, py, "--version").CombinedOutput() //nolint:gosec // py is a LookPath result or the managed interpreter
	if err != nil {
		return 0, 0, fmt.Errorf("running %s --version: %w", py, err)
	}
	return ParsePythonVersion(string(out))
}

// ParsePythonVersion extracts the major and minor from a `python --version`
// line such as "Python 3.11.15" (the version token may carry a patch and a
// pre-release suffix, e.g. "3.12.0rc1").
func ParsePythonVersion(s string) (major, minor int, err error) {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("unrecognized python version output %q", strings.TrimSpace(s))
	}
	return PythonMajorMinor(fields[len(fields)-1]) // "3.11.15"
}

// PythonMajorMinor is the one parser for a dotted Python version: it returns
// the numeric major and minor of "3.11", "3.11.15", "3.12.0rc1" or the
// "3.13.1.final.0" a pyvenv.cfg records. Whatever follows the minor is ignored,
// so callers that need a stricter form (the python_version field) check it
// themselves before calling.
func PythonMajorMinor(ver string) (major, minor int, err error) {
	parts := strings.SplitN(ver, ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("unrecognized python version %q", ver)
	}
	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parsing python major from %q: %w", ver, err)
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("parsing python minor from %q: %w", ver, err)
	}
	return major, minor, nil
}
