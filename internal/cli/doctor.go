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
// `dexaflow setup`.
func newDoctorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Report host platform, dependencies, and the achievable operating tier.",
		Long: "doctor inspects the host (OS, architecture, libc), checks for Python 3.11+, " +
			"Docker, k3d, and kubectl, and reports which operating tier is achievable. " +
			"With a target set it also runs the checks of the deploy preflight: the control plane " +
			"answers and the token may deploy, and, in a DAG project, the registry accepts a push. " +
			"It changes nothing but a saved session it refreshes, as deploy does; run `dexaflow setup` to bootstrap.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r := setup.Detect(setup.Probe{
				GOOS:     runtime.GOOS,
				GOARCH:   runtime.GOARCH,
				LookPath: exec.LookPath,
				Stat:     os.Stat,
				Getwd:    os.Getwd,
			})
			// The same probe `dexaflow setup` runs, so doctor reports the
			// interpreter setup will actually use (#1700).
			r.ProbeSetupPython(cmd.Context(), exec.LookPath, setup.PythonVersion)
			renderDoctor(cmd.OutOrStdout(), r)
			renderProDoctor(cmd.OutOrStdout(), proDoctor(cmd))
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
	p("dexaflow doctor\n\n")
	p("  platform      %s\n", plat)
	p("  python 3.11+  %s\n", found(r.PythonAvailable, r.PythonPath, "no python3.11/3.12/3.13 on PATH; will download a relocatable CPython 3.11 on `dexaflow setup`"))
	renderDoctorSetupPython(p, r)
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
		p("  native filesystem (~/...) so `dexaflow lite` hot-reload (inotify) works.\n")
	}
	p("\n  next: run `dexaflow setup` to bootstrap the managed runtime.\n")
}

// renderDoctorSetupPython says what `dexaflow setup` will do about Python. It is
// separate from the "python 3.11+" line because that line answers what can
// PARSE a dag.py, by binary name, while this one answers what setup will use and
// whether anything will be downloaded, from the same version probe setup runs
// (Report.SetupPython). Answering both in one sentence is how a host was once
// told its Python was fine and then watched setup download (#1224).
func renderDoctorSetupPython(p func(string, ...any), r setup.Report) {
	switch {
	case r.SetupPython.Path != "":
		p("                `dexaflow setup` will use %s (Python 3.%d) for the parser; nothing to download\n", r.SetupPython.Path, r.SetupPython.Minor)
		if r.Python311Path == "" {
			p("                no host Python 3.11: a DAG declaring python_version 3.11 (the default) downloads a managed CPython 3.11 when its venv is first built (needs outbound network). Install python3.11 to avoid it.\n")
		}
	case r.PythonAvailable:
		p("                but `dexaflow setup` uses only a Python 3.11 to 3.13 and will download a managed CPython 3.11 (needs outbound network). Install python3.11, python3.12 or python3.13 to avoid it.\n")
	}
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
