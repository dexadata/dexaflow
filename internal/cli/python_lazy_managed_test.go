package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/setup"
)

// stubPythons writes stub interpreters printing the given `--version` lines and
// makes their directory the whole PATH.
func stubPythons(t *testing.T, stubs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, version := range stubs {
		script := fmt.Sprintf("#!/bin/sh\necho %q\n", version)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil { //nolint:gosec // test stub must be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

// TestBasePythonForFetchesManagedLazily is the build-time half of #1700. Setup
// now keeps a host 3.12 for the parser and downloads nothing, so the managed
// CPython 3.11 is fetched when a DAG venv first needs a 3.11 the host lacks,
// and only then: a declared minor the host has, or one Dexaflow does not pin,
// never triggers the download.
func TestBasePythonForFetchesManagedLazily(t *testing.T) {
	const fetched = "/state/python/bin/python3.11"
	cases := []struct {
		name        string
		declared    string // python_version; "" for none
		stubs       map[string]string
		want        string // "fetched", a stub name, or "" for an error
		wantFetched bool
	}{
		{"declared 3.11 on a 3.12-only host fetches", "3.11", map[string]string{"python3.12": "Python 3.12.4"}, "fetched", true},
		{"declared 3.11 on a 3.13 and python3 3.12 host fetches", "3.11", map[string]string{"python3.13": "Python 3.13.1", "python3": "Python 3.12.4"}, "fetched", true},
		{"declared 3.11 with a host python3.11 uses it", "3.11", map[string]string{"python3.11": "Python 3.11.9", "python3.12": "Python 3.12.4"}, "python3.11", false},
		{"declared 3.11 with python3 reporting 3.11 uses it", "3.11", map[string]string{"python3": "Python 3.11.9"}, "python3", false},
		{"declared 3.12 with a host 3.12 uses it", "3.12", map[string]string{"python3.12": "Python 3.12.4"}, "python3.12", false},
		{"declared 3.13 missing errors, never fetches", "3.13", map[string]string{"python3.12": "Python 3.12.4"}, "", false},
		{"no declaration and no host python fetches", "", map[string]string{}, "fetched", true},
		{"no declaration uses a host python3", "", map[string]string{"python3": "Python 3.12.4"}, "python3", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := stubPythons(t, tc.stubs)
			calls := 0
			got, err := basePythonFor(context.Background(), tc.declared, basePythonDeps{
				managed:    filepath.Join(t.TempDir(), "absent", "python3.11"),
				lookPath:   exec.LookPath,
				runVersion: pythonVersion,
				ensureManaged: func(context.Context) (string, error) {
					calls++
					return fetched, nil
				},
			})
			if (calls > 0) != tc.wantFetched {
				t.Errorf("managed fetch calls = %d, want fetched = %v", calls, tc.wantFetched)
			}
			switch tc.want {
			case "":
				if err == nil {
					t.Errorf("basePythonFor = %q, want an error", got)
				}
			case "fetched":
				if err != nil || got != fetched {
					t.Errorf("basePythonFor = %q, %v; want the fetched managed build", got, err)
				}
			default:
				if want := filepath.Join(dir, tc.want); err != nil || got != want {
					t.Errorf("basePythonFor = %q, %v; want %q", got, err, want)
				}
			}
		})
	}
}

// failingTransport stands in for a host with no route to the CPython mirror.
type failingTransport struct{ called *bool }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	*f.called = true
	return nil, errors.New("dial tcp: connect: connection refused")
}

// TestProvisionManagedPythonOfflineMessage locks the actionable error for the
// lazy fetch: behind a proxy or offline it must say why a download happened and
// name the two ways out, not just print the dial error.
func TestProvisionManagedPythonOfflineMessage(t *testing.T) {
	t.Parallel()
	called := false
	_, err := provisionManagedPython(context.Background(), io.Discard, t.TempDir(),
		&http.Client{Transport: failingTransport{called: &called}})
	if err == nil {
		t.Fatal("provisionManagedPython err = nil with no network")
	}
	if !called {
		t.Skip("platform has no pinned CPython build; nothing was downloaded")
	}
	for _, want := range []string{"Python 3.11", "outbound network", "proxy", "install python3.11", "connection refused"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

// TestSetupPythonPlan locks what `setup --dry-run` says about Python against
// what setup.EnsurePython does (#1700): a host 3.12 is used and nothing is
// downloaded now, but the reader is told a default 3.11 DAG fetches CPython 3.11
// when its venv is built.
func TestSetupPythonPlan(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		r       setup.Report
		managed bool
		want    []string
		reject  []string
	}{
		{
			name: "host 3.12 only",
			r: setup.Report{PythonAvailable: true, PythonPath: "/usr/bin/python3.12",
				SetupPython: setup.HostPython{Path: "/usr/bin/python3.12", Minor: 12}},
			want:   []string{"using system /usr/bin/python3.12 (Python 3.12)", "python_version 3.11", "when its venv is first built"},
			reject: []string{"will download", "will install"},
		},
		{
			name: "host 3.11",
			r: setup.Report{PythonAvailable: true, PythonPath: "/usr/bin/python3.11", Python311Path: "/usr/bin/python3.11",
				SetupPython: setup.HostPython{Path: "/usr/bin/python3.11", Minor: 11}},
			want:   []string{"using system /usr/bin/python3.11"},
			reject: []string{"download"},
		},
		{
			name:    "managed install wins",
			r:       setup.Report{SetupPython: setup.HostPython{Path: "/usr/bin/python3.13", Minor: 13}},
			managed: true,
			want:    []string{"managed CPython"},
			reject:  []string{"download", "/usr/bin/python3.13"},
		},
		{
			name: "only an untested minor",
			r:    setup.Report{PythonAvailable: true, PythonPath: "/usr/bin/python3.14"},
			want: []string{"/usr/bin/python3.14", "will download a managed CPython 3.11"},
		},
		{
			name: "nothing",
			r:    setup.Report{},
			want: []string{"will install a relocatable CPython 3.11", "needs network"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			printSetupPythonPlan(&buf, tc.r, tc.managed)
			out := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("plan missing %q:\n%s", w, out)
				}
			}
			for _, w := range tc.reject {
				if strings.Contains(out, w) {
					t.Errorf("plan must not say %q:\n%s", w, out)
				}
			}
		})
	}
}

// TestRunSetupOnHostWithOnlyPython312 is the acceptance case of #1700, driven
// through the real `dexaflow setup` command: a host whose only Python is 3.12
// completes setup with no CPython download, and parser_cmd names that 3.12.
func TestRunSetupOnHostWithOnlyPython312(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := stubPythons(t, map[string]string{"python3.12": "Python 3.12.4"})

	cmd := newSetupCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--workspace", filepath.Join(home, "ws")})
	if err := cmd.ExecuteContext(testCtx(nil)); err != nil {
		t.Fatalf("setup err = %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "downloading relocatable CPython") {
		t.Errorf("setup downloaded a CPython on a host with python3.12:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".dexaflow", "python")); !os.IsNotExist(err) {
		t.Errorf("a managed CPython tree was created: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(home, ".dexaflow", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "python3.12") + " -m leoflow_parser"; !strings.Contains(string(cfg), want) {
		t.Errorf("parser_cmd does not use the host python3.12 (%q):\n%s", want, cfg)
	}
}

// TestBasePythonForFallsBackWhenHostCannotVenv covers the Debian and Ubuntu
// host whose system Python has no venv or ensurepip module (the python3-venv
// package is split out). Before #1700 setup had already fetched the managed
// CPython and the venv was built on it; now the host interpreter is found
// first, so a host that cannot build a venv must fall back to the managed
// build, with a line saying why, whenever that build is the minor the DAG
// needs. A declared minor Dexaflow does not pin is never substituted.
func TestBasePythonForFallsBackWhenHostCannotVenv(t *testing.T) {
	const fetched = "/state/python/bin/python3.11"
	cases := []struct {
		name     string
		declared string
		stubs    map[string]string
		want     string // "fetched" or a stub name
	}{
		{"no declaration, host 3.12 without venv", "", map[string]string{"python3": "Python 3.12.4"}, "fetched"},
		{"declared 3.11, host 3.11 without venv", "3.11", map[string]string{"python3.11": "Python 3.11.9"}, "fetched"},
		{"declared 3.12, host 3.12 without venv is kept", "3.12", map[string]string{"python3.12": "Python 3.12.4"}, "python3.12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := stubPythons(t, tc.stubs)
			var logged bytes.Buffer
			calls := 0
			got, err := basePythonFor(context.Background(), tc.declared, basePythonDeps{
				managed:    filepath.Join(t.TempDir(), "absent", "python3.11"),
				lookPath:   exec.LookPath,
				runVersion: pythonVersion,
				canVenv:    func(context.Context, string) bool { return false },
				logf: func(format string, a ...any) {
					fmt.Fprintf(&logged, format, a...)
				},
				ensureManaged: func(context.Context) (string, error) {
					calls++
					return fetched, nil
				},
			})
			if err != nil {
				t.Fatalf("basePythonFor err = %v", err)
			}
			if tc.want == "fetched" {
				if got != fetched || calls != 1 {
					t.Errorf("basePythonFor = %q after %d fetch(es); want the managed build", got, calls)
				}
				for _, w := range []string{"cannot create a venv", "python3-venv", "managed CPython 3.11"} {
					if !strings.Contains(logged.String(), w) {
						t.Errorf("log %q does not say %q", logged.String(), w)
					}
				}
				return
			}
			if want := filepath.Join(dir, tc.want); got != want || calls != 0 {
				t.Errorf("basePythonFor = %q after %d fetch(es); want %q and no fetch", got, calls, want)
			}
		})
	}
}

// TestPythonCanVenv drives the real probe: an interpreter that can import venv
// and ensurepip passes, one that cannot (the stub exits 1 on -c) fails.
func TestPythonCanVenv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil { //nolint:gosec // test stub must be executable
			t.Fatal(err)
		}
		return p
	}
	ok := write("ok", `[ "$1" = "-c" ] && case "$2" in *ensurepip*) exit 0;; esac; exit 1`)
	broken := write("broken", `exit 1`)
	if !pythonCanVenv(context.Background(), ok) {
		t.Error("pythonCanVenv(ok) = false, want true")
	}
	if pythonCanVenv(context.Background(), broken) {
		t.Error("pythonCanVenv(broken) = true, want false")
	}
}

// TestDevBasePythonWiresTheVenvFallback locks the wiring: the deps
// devBasePython builds probe the host for venv support and print the fallback
// line to the command's output, so the fallback is not left to a test double.
func TestDevBasePythonWiresTheVenvFallback(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	d := devBasePythonDeps(&out, t.TempDir())
	if d.canVenv == nil || d.logf == nil || d.ensureManaged == nil {
		t.Fatalf("devBasePythonDeps left a hook nil: canVenv=%v logf=%v ensureManaged=%v", d.canVenv != nil, d.logf != nil, d.ensureManaged != nil)
	}
	broken := filepath.Join(t.TempDir(), "python3")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil { //nolint:gosec // test stub must be executable
		t.Fatal(err)
	}
	if d.canVenv(context.Background(), broken) {
		t.Error("the wired venv probe accepts an interpreter that cannot import ensurepip")
	}
	d.logf("hello %d", 1)
	if !strings.Contains(out.String(), "hello 1\n") {
		t.Errorf("the wired logf wrote %q to the command output, want the line", out.String())
	}
}
