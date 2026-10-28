package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// lookPathUV finds the uv binary of the daemonless dependency layer (ADR
// 0078) on PATH. Commands reach it as cliDeps.lookUV, so the tests of this
// package choose whether uv is there.
func lookPathUV() (string, error) { return exec.LookPath("uv") }

// uvKeptEnv are the variables uv runs with, from the CLI's environment:
// where to find programs, the home and cache directories, and how to reach
// the index (proxies and certificate authorities, UV_NATIVE_TLS and
// UV_SYSTEM_CERTS among them: they choose which authorities are trusted,
// not what is installed). Every other variable is dropped, every other UV_*
// and PIP_* one included, and uv runs with --no-config, so neither the
// host's uv nor its pip configuration changes what ships.
var uvKeptEnv = []string{
	"PATH", "HOME", "USERPROFILE", "TMPDIR", "TEMP", "TMP", "SYSTEMROOT", "LOCALAPPDATA", "APPDATA",
	"XDG_CACHE_HOME", "UV_CACHE_DIR",
	"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "UV_NATIVE_TLS", "UV_SYSTEM_CERTS",
}

// uvTool runs uv for the daemonless dependency layer.
type uvTool struct {
	// path is the uv binary.
	path string
	// version is what `uv --version` printed, "uv 0.11.32".
	version string
	// index is the package index URL, credentials included, uv installs
	// from in place of PyPI; "" is PyPI.
	index string
}

// findUV returns uv when it is on PATH and answers --version, or nil and
// why not.
func findUV(ctx context.Context) (u *uvTool, why string) {
	p, err := depsFrom(ctx).lookUV()
	if err != nil {
		return nil, "uv is not on PATH"
	}
	u = &uvTool{path: p}
	out, err := u.run(ctx, "", "--version")
	if err != nil {
		return nil, fmt.Sprintf("uv --version failed (%v)", err)
	}
	fields := strings.Fields(out)
	if len(fields) < 2 || fields[0] != "uv" {
		return nil, fmt.Sprintf("uv --version printed %q, not a uv version", strings.TrimSpace(out))
	}
	u.version = fields[0] + " " + fields[1]
	return u, ""
}

// uvEnv is the environment uv runs with (uvKeptEnv), with Python downloads
// off: uv uses a Python already on this machine and never fetches one. A
// package index goes in UV_DEFAULT_INDEX, and in UV_INDEX_URL for the uv
// releases that predate UV_DEFAULT_INDEX and would otherwise resolve from
// PyPI; never in uv's argv, where ps would show its credentials.
func uvEnv(index string) []string {
	env := []string{"UV_PYTHON_DOWNLOADS=never"}
	if index != "" {
		env = append(env, "UV_DEFAULT_INDEX="+index, "UV_INDEX_URL="+index)
	}
	for _, k := range uvKeptEnv {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// findPython checks that uv finds a Python on this machine. uv needs one to
// resolve and install, even for another Python version and platform, and
// never downloads one here (uvEnv). It runs with --no-config in an empty
// directory, as the resolution and the install do, so no .python-version
// or uv.toml where the CLI runs changes the answer.
func (u *uvTool) findPython(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "dexaflow-uv-")
	if err != nil {
		return fmt.Errorf("making the directory of the Python lookup: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // best-effort cleanup of a temporary directory
	_, err = u.run(ctx, dir, "python", "find", "--no-config")
	return err
}

// run runs uv with args in dir (the current directory when empty) and
// returns its standard output; a failure carries the first useful line of
// what uv printed on standard error.
func (u *uvTool) run(ctx context.Context, dir string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, u.path, args...) //nolint:gosec // G204: the uv binary found on PATH, with arguments built here.
	c.Dir, c.Env = dir, uvEnv(u.index)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		if msg := uvMessage(stderr.String()); msg != "" {
			return "", errors.New(u.scrub(msg))
		}
		return "", fmt.Errorf("running uv %s: %w", args[0], err)
	}
	return stdout.String(), nil
}

// scrub removes the package index credentials from msg, in the URL as uv
// prints it and in their decoded form.
func (u *uvTool) scrub(msg string) string {
	parsed, err := url.Parse(u.index)
	if u.index == "" || err != nil || parsed.User == nil {
		return msg
	}
	msg = strings.ReplaceAll(msg, u.index, parsed.Redacted())
	if pass, ok := parsed.User.Password(); ok && pass != "" {
		msg = strings.ReplaceAll(msg, pass, "xxxxx")
		if raw := strings.TrimPrefix(parsed.User.String(), parsed.User.Username()+":"); raw != pass {
			msg = strings.ReplaceAll(msg, raw, "xxxxx")
		}
	}
	return msg
}

// uvMessage trims what uv printed on a failure to its first useful
// sentence: the reason under "No solution found when resolving
// dependencies", or the first line, without the tree drawing.
func uvMessage(stderr string) string {
	var parts []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "×╰─▶│ "))
		switch {
		case line == "" && len(parts) > 0:
			return strings.Join(parts, " ")
		case line == "", strings.HasPrefix(line, "No solution found"):
			continue
		case strings.HasPrefix(line, "hint:"):
			return strings.Join(parts, " ")
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, " ")
}

// resolvedDist is one line of a resolution: a distribution pinned to one
// version, with the hashes of its files.
type resolvedDist struct {
	// name is the distribution name, normalized.
	name string
	// version is the exact version.
	version string
	// hashes are the --hash values, sorted.
	hashes []string
}

// pin is name==version.
func (d resolvedDist) pin() string { return d.name + "==" + d.version }

// line is the requirement line of d with its hashes, as --require-hashes
// reads it.
func (d resolvedDist) line() string {
	var b strings.Builder
	b.WriteString(d.pin())
	for _, h := range d.hashes {
		b.WriteString(" --hash=" + h)
	}
	return b.String()
}

// compile resolves requirements for one wheel platform tag (x86_64-
// manylinux_2_36) and Python version, wheels only, with every distribution
// of the base pinned as a constraint, and returns the resolution sorted by
// name.
func (u *uvTool) compile(ctx context.Context, tag, python string, constraints, requirements []string) ([]resolvedDist, error) {
	dir, err := os.MkdirTemp("", "dexaflow-uv-")
	if err != nil {
		return nil, fmt.Errorf("making the directory of the resolution: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // best-effort cleanup of a temporary directory
	files := map[string][]string{"base-constraints.txt": constraints, "requirements.in": requirements}
	for name, lines := range files {
		if werr := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o600); werr != nil {
			return nil, fmt.Errorf("writing %s: %w", name, werr)
		}
	}
	if _, rerr := u.run(ctx, dir, "pip", "compile", "--no-config", "--quiet", "--python-platform", tag, "--python-version", python,
		"--only-binary", ":all:", "--generate-hashes", "--no-header", "--no-annotate",
		"-c", "base-constraints.txt", "-o", "resolved.txt", "requirements.in"); rerr != nil {
		return nil, rerr
	}
	data, err := os.ReadFile(filepath.Join(dir, "resolved.txt")) //nolint:gosec // G304: the file uv wrote in our directory.
	if err != nil {
		return nil, fmt.Errorf("reading the resolution: %w", err)
	}
	return parseResolved(string(data))
}

// parseResolved reads the output of `uv pip compile --generate-hashes`:
// one name==version per requirement, its --hash options on continuation
// lines.
func parseResolved(text string) ([]resolvedDist, error) {
	joined := strings.ReplaceAll(text, "\\\n", " ")
	var out []resolvedDist
	for _, line := range strings.Split(joined, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name, version, ok := strings.Cut(fields[0], "==")
		if !ok || name == "" || version == "" || strings.ContainsAny(version, ";,<>=!~*") {
			return nil, fmt.Errorf("uv resolved %q, which is not one version of one distribution", strings.TrimSpace(line))
		}
		d := resolvedDist{name: normalizedName(name), version: version}
		for _, f := range fields[1:] {
			h, isHash := strings.CutPrefix(f, "--hash=")
			if !isHash {
				return nil, fmt.Errorf("uv resolved %q, with an option other than --hash", strings.TrimSpace(line))
			}
			d.hashes = append(d.hashes, h)
		}
		if len(d.hashes) == 0 {
			return nil, fmt.Errorf("uv resolved %s without a hash", d.pin())
		}
		slices.Sort(d.hashes)
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b resolvedDist) int { return strings.Compare(a.name, b.name) })
	return out, nil
}

// install installs the hash-pinned requirement lines into target as
// `pip install --target` lays them out, for one wheel platform tag and
// Python version, without their dependencies (the lines are the whole
// resolution).
func (u *uvTool) install(ctx context.Context, tag, python string, lines []string, target string) error {
	dir, err := os.MkdirTemp("", "dexaflow-uv-")
	if err != nil {
		return fmt.Errorf("making the directory of the install: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck // best-effort cleanup of a temporary directory
	reqs := filepath.Join(dir, "resolved-minus-base.txt")
	if werr := os.WriteFile(reqs, []byte(strings.Join(lines, "\n")+"\n"), 0o600); werr != nil {
		return fmt.Errorf("writing the requirements to install: %w", werr)
	}
	_, err = u.run(ctx, dir, "pip", "install", "--no-config", "--quiet", "--target", target, "--python-platform", tag,
		"--python-version", python, "--only-binary", ":all:", "--require-hashes", "--no-deps", "-r", reqs)
	return err
}
