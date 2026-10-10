package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/setup"
)

func TestDoctorHelpers(t *testing.T) {
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
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor err = %v", err)
	}
	if !strings.Contains(out.String(), "dexaflow doctor") || !strings.Contains(out.String(), "recommended executor") {
		t.Errorf("doctor output unexpected:\n%s", out.String())
	}
}

func TestRenderDoctor(t *testing.T) {
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

// TestDoctorWarnsWhenOnlyANewerPythonIsPresent is the regression test for the
// first-run gap in #1224.
//
// The two components disagree on purpose and both are right. Detect accepts
// 3.11, 3.12 or 3.13 because the Lite parser shim is stdlib-only, so any of
// them can parse a dag.py. setup.EnsurePython resolves the host interpreter
// with LookPath("python3.11") and nothing else, because the runtime is pinned.
//
// The bug was the SENTENCE, not either rule: doctor reported the 3.12 as found
// and said nothing more, so a machine with only 3.12 was told its system Python
// was fine and then watched setup download a CPython it had not been warned
// about. Behind a proxy that is a hard failure at a step doctor called green.
func TestDoctorWarnsWhenOnlyANewerPythonIsPresent(t *testing.T) {
	var buf bytes.Buffer
	renderDoctor(&buf, setup.Report{
		OS: "darwin", Arch: "arm64",
		PythonAvailable: true,
		PythonPath:      "/opt/homebrew/bin/python3.12",
		// No python3.11 on this host: this is the whole fixture.
		Python311Path: "",
		Tier:          setup.TierSubprocess,
	})
	out := buf.String()

	if !strings.Contains(out, "python3.12") {
		t.Errorf("doctor no longer names the interpreter that can parse:\n%s", out)
	}
	if !strings.Contains(out, "download") {
		t.Errorf("doctor reported a green Python and never mentioned the download setup will do:\n%s", out)
	}
	if !strings.Contains(out, "python3.11") {
		t.Errorf("doctor does not say which interpreter would avoid the download:\n%s", out)
	}

	// And it must NOT cry wolf on a host that has 3.11: a warning that fires
	// when nothing will be downloaded trains the reader to ignore it, which is
	// the failure mode of the line it replaces.
	buf.Reset()
	renderDoctor(&buf, setup.Report{
		OS: "linux", Arch: "amd64",
		PythonAvailable: true,
		PythonPath:      "/usr/bin/python3.11",
		Python311Path:   "/usr/bin/python3.11",
		Tier:            setup.TierSubprocess,
	})
	if strings.Contains(buf.String(), "will download") {
		t.Errorf("doctor warned about a download on a host that has python3.11:\n%s", buf.String())
	}
}
