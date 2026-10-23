package setup

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakePath builds a directory of stub interpreters and makes it the whole PATH.
// Each entry maps a binary name to what its `--version` prints; an empty value
// makes the stub exit 1, like a broken symlink or a shim with no interpreter.
// It returns the directory so a test can name the expected path.
func fakePath(t *testing.T, stubs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, version := range stubs {
		script := "#!/bin/sh\nexit 1\n"
		if version != "" {
			script = fmt.Sprintf("#!/bin/sh\necho %q\n", version)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil { //nolint:gosec // test stub must be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// TestFindHostPython is the probe `dexaflow setup` runs for the parser
// interpreter (#1700). It drives the real exec.LookPath and the real
// `--version` probe against stub executables, because the bug was a probe that
// only ever asked for one binary name.
func TestFindHostPython(t *testing.T) {
	cases := []struct {
		name      string
		stubs     map[string]string
		wantName  string // "" means no usable host interpreter
		wantMinor int
	}{
		{"only python3.12", map[string]string{"python3.12": "Python 3.12.4"}, "python3.12", 12},
		{"only python3.13", map[string]string{"python3.13": "Python 3.13.1"}, "python3.13", 13},
		{"only python3.11", map[string]string{"python3.11": "Python 3.11.9"}, "python3.11", 11},
		{"newest minor wins", map[string]string{
			"python3.11": "Python 3.11.9", "python3.12": "Python 3.12.4", "python3.13": "Python 3.13.1",
		}, "python3.13", 13},
		{"bare python3 reporting 3.12", map[string]string{"python3": "Python 3.12.4"}, "python3", 12},
		{"named minors outrank bare python3", map[string]string{
			"python3": "Python 3.13.1", "python3.12": "Python 3.12.4",
		}, "python3.12", 12},
		{"python3 too old", map[string]string{"python3": "Python 3.9.18"}, "", 0},
		{"python3 too new", map[string]string{"python3": "Python 3.14.0"}, "", 0},
		{"only an untested minor", map[string]string{"python3.14": "Python 3.14.0"}, "", 0},
		{"broken python3.13 falls through", map[string]string{
			"python3.13": "", "python3.12": "Python 3.12.4",
		}, "python3.12", 12},
		{"a name is not trusted over its version", map[string]string{
			"python3.13": "Python 3.10.2", "python3": "Python 3.11.9",
		}, "python3", 11},
		{"pre-release version", map[string]string{"python3.13": "Python 3.13.0rc2"}, "python3.13", 13},
		{"nothing on PATH", map[string]string{}, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := fakePath(t, tc.stubs)
			got, ok := FindHostPython(context.Background(), exec.LookPath, nil)
			if tc.wantName == "" {
				if ok {
					t.Fatalf("FindHostPython = %+v, want none", got)
				}
				return
			}
			if !ok {
				t.Fatalf("FindHostPython found nothing, want %s", tc.wantName)
			}
			if want := filepath.Join(dir, tc.wantName); got.Path != want || got.Minor != tc.wantMinor {
				t.Errorf("FindHostPython = %+v, want {%s %d}", got, want, tc.wantMinor)
			}
		})
	}
}

// TestEnsurePythonUsesAnySupportedHostPython is the regression for #1700:
// setup resolved its interpreter with LookPath("python3.11") alone, so a host
// with only 3.12 or 3.13 downloaded a 50 MB CPython it did not need, and failed
// outright behind a proxy or offline. The client fails the test on any request,
// so "no download" is asserted, not inferred.
func TestEnsurePythonUsesAnySupportedHostPython(t *testing.T) {
	cases := []struct {
		name         string
		stubs        map[string]string
		wantName     string // "" means the managed download must be attempted
		wantDownload bool
	}{
		{"python3.12 only, offline", map[string]string{"python3.12": "Python 3.12.4"}, "python3.12", false},
		{"python3.13 only, offline", map[string]string{"python3.13": "Python 3.13.1"}, "python3.13", false},
		{"python3.11 only", map[string]string{"python3.11": "Python 3.11.9"}, "python3.11", false},
		{"bare python3 reporting 3.12", map[string]string{"python3": "Python 3.12.4"}, "python3", false},
		{"python3 reporting 3.9 downloads", map[string]string{"python3": "Python 3.9.18"}, "", true},
		{"no python at all downloads", map[string]string{}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := fakePath(t, tc.stubs)
			var client *http.Client
			rec := &recordingRoundTripper{}
			if tc.wantDownload {
				client = &http.Client{Transport: rec}
			} else {
				client = &http.Client{Transport: errRoundTripper{t}}
			}
			got, err := EnsurePython(context.Background(), EnsureOpts{
				Home: t.TempDir(), GOOS: "linux", GOARCH: "amd64",
				LookPath: exec.LookPath, Stat: os.Stat, Client: client,
			})
			if tc.wantDownload {
				if !rec.called || err == nil {
					t.Fatalf("EnsurePython = %q, %v; want a download attempt that fails offline", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("EnsurePython err = %v, want the host interpreter", err)
			}
			if want := filepath.Join(dir, tc.wantName); got != want {
				t.Errorf("EnsurePython = %q, want %q", got, want)
			}
		})
	}
}

// writeManagedPython lays out a complete managed install at the pinned version
// under home and returns its interpreter path.
func writeManagedPython(t *testing.T, home string) string {
	t.Helper()
	managed := ManagedPythonPath(home)
	if err := os.MkdirAll(filepath.Dir(managed), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("#!/bin/sh\necho 'Python "+pyVersion+"'\n"), 0o700); err != nil { //nolint:gosec // test stub must be executable
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "python", pyVersionFile), []byte(pyVersion), 0o600); err != nil {
		t.Fatal(err)
	}
	return managed
}

// TestEnsurePythonManagedStillWinsOverNewerHost keeps the #742 precedence: a
// current managed install outranks every host interpreter, newer ones included.
func TestEnsurePythonManagedStillWinsOverNewerHost(t *testing.T) {
	fakePath(t, map[string]string{"python3.13": "Python 3.13.1"})
	home := t.TempDir()
	managed := writeManagedPython(t, home)
	got, err := EnsurePython(context.Background(), EnsureOpts{
		Home: home, GOOS: "linux", GOARCH: "amd64",
		LookPath: exec.LookPath, Stat: os.Stat,
		Client: &http.Client{Transport: errRoundTripper{t}},
	})
	if err != nil || got != managed {
		t.Fatalf("EnsurePython = %q, %v; want the managed %q", got, err, managed)
	}
	if !ManagedPythonInstalled(home) {
		t.Error("ManagedPythonInstalled = false for a complete pinned install")
	}
}

// TestEnsureManagedPythonIgnoresHost is the lazy half of #1700: when a DAG
// needs the pinned minor, a host interpreter of another minor is no substitute,
// so EnsureManagedPython must go to the network even with a host Python
// present, and must not when the managed build is already installed.
func TestEnsureManagedPythonIgnoresHost(t *testing.T) {
	fakePath(t, map[string]string{"python3.12": "Python 3.12.4", "python3.11": "Python 3.11.9"})

	rec := &recordingRoundTripper{}
	_, err := EnsureManagedPython(context.Background(), EnsureOpts{
		Home: t.TempDir(), GOOS: "linux", GOARCH: "amd64",
		LookPath: exec.LookPath, Stat: os.Stat, Client: &http.Client{Transport: rec},
	})
	if !rec.called || err == nil {
		t.Fatalf("EnsureManagedPython err = %v, called = %v; want a download attempt", err, rec.called)
	}
	if ManagedPythonInstalled(t.TempDir()) {
		t.Error("ManagedPythonInstalled = true for an empty home")
	}

	home := t.TempDir()
	managed := writeManagedPython(t, home)
	got, err := EnsureManagedPython(context.Background(), EnsureOpts{
		Home: home, GOOS: "linux", GOARCH: "amd64", Stat: os.Stat,
		Client: &http.Client{Transport: errRoundTripper{t}},
	})
	if err != nil || got != managed {
		t.Fatalf("EnsureManagedPython = %q, %v; want the installed %q", got, err, managed)
	}
}

// TestDetectSetupPython locks that `doctor` and `setup --dry-run` read the same
// probe the real setup runs, and that a bare python3 reporting 3.11 counts as
// a host 3.11 (no lazy download for a default DAG).
func TestDetectSetupPython(t *testing.T) {
	cases := []struct {
		name       string
		stubs      map[string]string
		wantSetup  string
		want311    string
		withRunVer bool
	}{
		{"3.12 only: setup uses it, no 3.11", map[string]string{"python3.12": "Python 3.12.4"}, "python3.12", "", true},
		{"python3 reporting 3.11", map[string]string{"python3": "Python 3.11.9"}, "python3", "python3", true},
		{"3.13 plus python3.11", map[string]string{"python3.13": "Python 3.13.1", "python3.11": "Python 3.11.9"}, "python3.13", "python3.11", true},
		{"Detect alone runs nothing", map[string]string{"python3.12": "Python 3.12.4"}, "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := fakePath(t, tc.stubs)
			r := Detect(Probe{GOOS: "linux", GOARCH: "amd64", LookPath: exec.LookPath, Stat: statNone()})
			if tc.withRunVer {
				r.ProbeSetupPython(context.Background(), exec.LookPath, PythonVersion)
			}
			wantPath := func(name string) string {
				if name == "" {
					return ""
				}
				return filepath.Join(dir, name)
			}
			if r.SetupPython.Path != wantPath(tc.wantSetup) {
				t.Errorf("SetupPython = %+v, want %q", r.SetupPython, wantPath(tc.wantSetup))
			}
			if r.Python311Path != wantPath(tc.want311) {
				t.Errorf("Python311Path = %q, want %q", r.Python311Path, wantPath(tc.want311))
			}
		})
	}
}

// TestPythonMajorMinor covers the one dotted-version parser every caller goes
// through: `--version` output (via ParsePythonVersion), a pyvenv.cfg version
// and the python_version field. Anything past the minor is ignored; a missing
// or non-numeric major or minor is an error.
func TestPythonMajorMinor(t *testing.T) {
	for _, tc := range []struct {
		in           string
		major, minor int
		ok           bool
	}{
		{"3.11", 3, 11, true},
		{"3.11.15", 3, 11, true},
		{"3.12.0rc1", 3, 12, true},
		{"3.13.1.final.0", 3, 13, true},
		{"3.9.18", 3, 9, true},
		{"4.0", 4, 0, true},
		{"3", 0, 0, false},
		{"", 0, 0, false},
		{"3.", 0, 0, false},
		{".12", 0, 0, false},
		{"x.12", 0, 0, false},
		{"3.x", 0, 0, false},
		{"3.12rc1", 0, 0, false},
		{"py3.13", 0, 0, false},
	} {
		major, minor, err := PythonMajorMinor(tc.in)
		if !tc.ok {
			if err == nil {
				t.Errorf("PythonMajorMinor(%q) = (%d, %d), want an error", tc.in, major, minor)
			}
			continue
		}
		if err != nil || major != tc.major || minor != tc.minor {
			t.Errorf("PythonMajorMinor(%q) = (%d, %d, %v), want (%d, %d, nil)", tc.in, major, minor, err, tc.major, tc.minor)
		}
	}
}

// TestParsePythonVersion covers extraction of major/minor from `--version`
// output: the last field is the version token PythonMajorMinor parses.
func TestParsePythonVersion(t *testing.T) {
	for _, tc := range []struct {
		in           string
		major, minor int
		ok           bool
	}{
		{"Python 3.11.15\n", 3, 11, true},
		{"Python 3.9.18", 3, 9, true},
		{"Python 3.12.0rc1", 3, 12, true},
		{"garbage", 0, 0, false},
		{"", 0, 0, false},
		{"Python 3", 0, 0, false},
	} {
		major, minor, err := ParsePythonVersion(tc.in)
		if !tc.ok {
			if err == nil {
				t.Errorf("ParsePythonVersion(%q) = (%d, %d), want an error", tc.in, major, minor)
			}
			continue
		}
		if err != nil || major != tc.major || minor != tc.minor {
			t.Errorf("ParsePythonVersion(%q) = (%d, %d, %v), want (%d, %d, nil)", tc.in, major, minor, err, tc.major, tc.minor)
		}
	}
}
