package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/setup"
)

// newDoctorCommand reports the host platform, which dependencies are present,
// and the highest achievable operating tier — the diagnostic companion to
// `leoflow setup`.
func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report host platform, dependencies, and the achievable operating tier.",
		Long: "doctor inspects the host (OS, architecture, libc), checks for Python 3.11, " +
			"Docker, k3d, and kubectl, and reports which operating tier is achievable. " +
			"It changes nothing; run `leoflow setup` to bootstrap.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := setup.Detect(setup.Probe{
				GOOS:     runtime.GOOS,
				GOARCH:   runtime.GOARCH,
				LookPath: exec.LookPath,
				Stat:     os.Stat,
				Getwd:    os.Getwd,
			})
			renderDoctor(cmd.OutOrStdout(), r)
			return nil
		},
	}
}

// renderDoctor writes a human-readable diagnostic report. It is separated from
// the command so the formatting is unit-tested.
func renderDoctor(w io.Writer, r setup.Report) {
	p := func(format string, a ...any) {
		_, _ = fmt.Fprintf(w, format, a...) //nolint:errcheck // best-effort terminal report
	}

	plat := r.OS + "/" + r.Arch
	if r.Libc != "" {
		plat += " (" + r.Libc + ")"
	}
	p("leoflow doctor\n\n")
	p("  platform      %s\n", plat)
	p("  python 3.11+  %s\n", found(r.PythonAvailable, r.PythonPath, "no python3.11/3.12/3.13 on PATH; will download a relocatable CPython 3.11 on `leoflow setup`"))
	// Said separately, because the line above answers a different question than
	// the one the reader is about to act on. It reports what can PARSE a dag.py,
	// where 3.12 and 3.13 qualify; `leoflow setup` resolves its interpreter with
	// LookPath("python3.11") and nothing else, so a host with only 3.12 sees a
	// green line and then a download it was not told about (#1224).
	if r.PythonAvailable && r.Python311Path == "" {
		p("                but `leoflow setup` needs python3.11 specifically and will download a managed CPython 3.11 (needs outbound network). Install python3.11 to avoid it.\n")
	}
	p("  docker        %s\n", found(r.Docker, "found", "not found"))
	p("  k3d           %s\n", found(r.K3d, "found", "not found (fetched on demand for the k8s tier)"))
	p("  kubectl       %s\n", found(r.Kubectl, "found", "not found (fetched on demand for the k8s tier)"))

	// Two execution paths only — no Docker executor (ADR 0015): the Docker SDK
	// carries an unfixable advisory, so Kubernetes (local k3d or prod) is the
	// sole container path and subprocess is the dev-only, unisolated escape hatch.
	p("\n  recommended executor: %s\n", r.Tier)
	p("    subprocess  always available (dev-only, no isolation)\n")
	p("    kubernetes  %s\n", availability(r.Docker))

	if r.UnderMnt {
		p("\n  WARNING: this directory is under /mnt (WSL). Keep your project in the WSL\n")
		p("  native filesystem (~/...) so `leoflow dev` hot-reload (inotify) works.\n")
	}
	p("\n  next: run `leoflow setup` to bootstrap the managed runtime.\n")
}

// found renders a present/absent line: when present it shows detail (a path or
// "found"), otherwise the absent hint.
func found(present bool, detail, absent string) string {
	if present {
		if detail == "" {
			return "found"
		}
		return "found" + ifPath(detail)
	}
	return absent
}

// ifPath wraps a filesystem path in parentheses; an empty or "found" detail
// renders nothing extra.
func ifPath(detail string) string {
	if detail == "" || detail == "found" {
		return ""
	}
	return " (" + detail + ")"
}

// availability renders whether a Docker-gated tier is reachable.
func availability(docker bool) string {
	if docker {
		return "available (Docker present; k3d/kubectl fetched on demand)"
	}
	return "needs Docker (not detected)"
}
