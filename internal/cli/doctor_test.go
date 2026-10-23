package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/setup"
)

func TestDoctorHelpers(t *testing.T) {
	t.Parallel()
	if got := found(true, "/usr/bin/python3.11", "absent"); got != "found (/usr/bin/python3.11)" {
		t.Errorf("found(present,path) = %q", got)
	}
	if got := found(true, "found", "absent"); got != "found" {
		t.Errorf("found(present,\"found\") = %q, want \"found\"", got)
	}
	if got := found(false, "x", "not found"); got != "not found" {
		t.Errorf("found(absent) = %q, want \"not found\"", got)
	}
	if got := availability(true); !strings.Contains(got, "available") {
		t.Errorf("availability(true) = %q", got)
	}
	if got := availability(false); !strings.Contains(got, "needs Docker") {
		t.Errorf("availability(false) = %q", got)
	}
}

func TestDoctorCommand(t *testing.T) {
	cmd := newDoctorCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.ExecuteContext(testCtx(nil)); err != nil {
		t.Fatalf("doctor err = %v", err)
	}
	if !strings.Contains(out.String(), "dexaflow doctor") || !strings.Contains(out.String(), "recommended executor") {
		t.Errorf("doctor output unexpected:\n%s", out.String())
	}
}

func TestRenderDoctor(t *testing.T) {
	t.Parallel()
	t.Run("docker host recommends k8s and shows python path", func(t *testing.T) {
		var buf bytes.Buffer
		renderDoctor(&buf, setup.Report{
			OS: "linux", Arch: "amd64", Libc: "glibc",
			PythonAvailable: true, PythonPath: "/usr/bin/python3.11",
			Docker: true, Tier: setup.TierK8s,
		})
		out := buf.String()
		for _, want := range []string{
			"linux/amd64 (glibc)",
			"/usr/bin/python3.11",
			"recommended executor: k8s",
			"available (Docker present",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q\n---\n%s", want, out)
			}
		}
	})

	t.Run("no docker, musl, under /mnt shows fallbacks and WSL warning", func(t *testing.T) {
		var buf bytes.Buffer
		renderDoctor(&buf, setup.Report{
			OS: "linux", Arch: "arm64", Libc: "musl",
			PythonAvailable: false,
			Docker:          false, Tier: setup.TierSubprocess, UnderMnt: true,
		})
		out := buf.String()
		for _, want := range []string{
			"linux/arm64 (musl)",
			"will download a relocatable CPython",
			"recommended executor: subprocess",
			"needs Docker (not detected)",
			"WSL",
			"dexaflow setup",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q\n---\n%s", want, out)
			}
		}
	})
}

// TestDoctorSetupPythonLine is the regression test for #1224 and #1700.
//
// doctor's "python 3.11+" line reports what can PARSE a dag.py; the line under
// it reports what `dexaflow setup` will use and whether anything is downloaded,
// from the same probe setup runs. #1224 was a green line followed by a download
// nobody was told about. #1700 made a host 3.12 or 3.13 enough for setup, so
// the remaining download is the managed 3.11 a default DAG fetches when its
// venv is built, and doctor must say that, and not cry wolf on a host with 3.11.
func TestDoctorSetupPythonLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		r      setup.Report
		want   []string
		reject []string
	}{
		{
			name: "only python3.12: used by setup, 3.11 DAGs fetch lazily",
			r: setup.Report{PythonAvailable: true, PythonPath: "/opt/homebrew/bin/python3.12",
				SetupPython: setup.HostPython{Path: "/opt/homebrew/bin/python3.12", Minor: 12}},
			want:   []string{"will use /opt/homebrew/bin/python3.12 (Python 3.12)", "nothing to download", "python_version 3.11", "when its venv is first built", "Install python3.11"},
			reject: []string{"needs python3.11 specifically"},
		},
		{
			name: "python3.11 present: no download warning",
			r: setup.Report{PythonAvailable: true, PythonPath: "/usr/bin/python3.11", Python311Path: "/usr/bin/python3.11",
				SetupPython: setup.HostPython{Path: "/usr/bin/python3.11", Minor: 11}},
			want:   []string{"will use /usr/bin/python3.11 (Python 3.11)"},
			reject: []string{"downloads", "will download"},
		},
		{
			name:   "only an untested minor: setup downloads",
			r:      setup.Report{PythonAvailable: true, PythonPath: "/usr/bin/python3.14"},
			want:   []string{"/usr/bin/python3.14", "will download a managed CPython 3.11", "outbound network"},
			reject: []string{"nothing to download"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			tc.r.OS, tc.r.Arch, tc.r.Tier = "linux", "amd64", setup.TierSubprocess
			renderDoctor(&buf, tc.r)
			out := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("doctor output missing %q:\n%s", w, out)
				}
			}
			for _, w := range tc.reject {
				if strings.Contains(out, w) {
					t.Errorf("doctor output must not say %q:\n%s", w, out)
				}
			}
		})
	}
}
