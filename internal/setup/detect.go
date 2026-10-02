// Package setup implements host detection and bootstrap for `leoflow setup`
// and `leoflow doctor`: it determines the platform, which dependencies are
// present, and which operating tier is achievable, preferring relocatable
// downloads into ~/.dexaflow over system package managers.
package setup

import (
	"io/fs"
	"strings"
)

// Tier is an operating mode, ordered by the infrastructure it needs.
type Tier int

const (
	// TierSubprocess runs the agent directly on the host with no isolation — the
	// dev-only escape hatch (ADR 0015). Fastest loop; no Docker or Kubernetes.
	TierSubprocess Tier = iota
	// TierK8s runs each task in an ephemeral pod via client-go, on a local
	// single-node cluster (k3d) or a real cluster. The sole container path
	// (ADR 0015); Docker is only the engine that hosts the local cluster, never
	// an executor itself.
	TierK8s
)

// String returns the tier's short name.
func (t Tier) String() string {
	switch t {
	case TierSubprocess:
		return "subprocess"
	case TierK8s:
		return "k8s"
	default:
		return "unknown"
	}
}

// Probe carries the host facts Detect needs. The function fields are injected so
// detection is testable without touching the real filesystem or PATH.
type Probe struct {
	GOOS     string
	GOARCH   string
	LookPath func(string) (string, error) // os/exec.LookPath
	Stat     func(string) (fs.FileInfo, error)
	Getwd    func() (string, error)
}

// Report is the outcome of Detect: the platform, which tools are present, and
// the highest achievable tier.
type Report struct {
	OS              string
	Arch            string
	Libc            string // "glibc" or "musl" on linux; empty on darwin
	PythonAvailable bool   // a usable Python 3.11+ interpreter is on PATH
	PythonPath      string // path to the chosen interpreter; empty when none is on PATH
	// Python311Path is a host python3.11 SPECIFICALLY, empty when there is none.
	//
	// It is separate from PythonPath because the two answer different questions
	// and the answers disagree on a very common machine. PythonPath answers
	// "what can parse a dag.py", and 3.12 or 3.13 can, because the Lite parser
	// shim is stdlib-only (ADR 0024). This field answers "will `leoflow setup`
	// have to download a managed CPython", and only a 3.11 avoids that, because
	// setup.EnsurePython resolves the host interpreter with LookPath("python3.11")
	// and nothing else qualifies.
	//
	// Reporting only the first made `doctor` and `setup --dry-run` tell a host
	// with just 3.12 that its system Python would be used and nothing
	// downloaded, and then setup downloaded (#1224). Neither component was
	// wrong on its own; they were answering different questions in the same
	// sentence.
	Python311Path string
	Docker        bool
	K3d           bool
	Kubectl       bool
	UnderMnt      bool // cwd under /mnt (WSL 9p mount): inotify hot-reload is unreliable
	Tier          Tier
}

// pythonCandidates lists the interpreter binary names Detect probes for, in
// priority order. The Lite parser shim is stdlib-only (ADR 0024) so any
// 3.11+ interpreter works; we trust the binary-name convention rather than
// invoking `--version` on every candidate (issue #D4). 3.11 wins when
// present because it's the version the managed CPython matches, keeping
// dev/prod parity for users on the documented path.
//
// This is NOT the list of Python lines Leoflow publishes a task base image for,
// and it deliberately differs from it. That list is the python_version enum in
// internal/domain/schemas/leoflow-yaml-schema.json (see
// domain.SupportedPythonVersions), and it answers a different question: which
// interpreter runs INSIDE the DAG image, on a machine that is not this one.
// This list answers which interpreter on THIS host can parse a dag.py. #1031
// read the two as one list that had drifted; they are two lists that agree
// about nothing in particular, and the overlap is a coincidence of both being
// Python.
//
// The forward-looking range (3.14–3.18) covers minors that haven't shipped
// yet so users with a future system Python still skip the managed-CPython
// download. Bump the upper bound when a new minor lands AND the parser
// shim has been verified against it — the list is the deliberate gate
// for "we know this version works", not a free-for-all (see follow-up
// issue tracking the more general fallback via `python3 --version` exec).
//
// The `// lite-only` marker is load-bearing, not decoration:
// scripts/check-python-host-interpreters.sh requires every candidate to be
// either in the published matrix or marked, so the difference between these
// two lists is always a recorded decision rather than the unexplained gap
// #1031 read as drift. Move an entry across the marker in the same change that
// starts or stops publishing a base image for it.
var pythonCandidates = []string{
	"python3.11", "python3.12", "python3.13",
	"python3.14", "python3.15", "python3.16", "python3.17", "python3.18", // lite-only
}

// muslLoaders are the dynamic-loader paths that mark a musl-based distro (Alpine).
var muslLoaders = []string{
	"/lib/ld-musl-x86_64.so.1",
	"/lib/ld-musl-aarch64.so.1",
	"/lib/ld-musl-armhf.so.1",
}

// Detect inspects the host and reports the achievable tier. Docker presence
// makes the Kubernetes tier achievable (k3d and kubectl are fetched on demand);
// without Docker the host falls back to the subprocess tier.
func Detect(p Probe) Report {
	has := func(name string) bool {
		if p.LookPath == nil {
			return false
		}
		_, err := p.LookPath(name)
		return err == nil
	}
	r := Report{
		OS:      p.GOOS,
		Arch:    p.GOARCH,
		Libc:    detectLibc(p),
		Docker:  has("docker"),
		K3d:     has("k3d"),
		Kubectl: has("kubectl"),
	}
	if p.LookPath != nil {
		for _, name := range pythonCandidates {
			if path, err := p.LookPath(name); err == nil {
				r.PythonAvailable = true
				r.PythonPath = path
				break
			}
		}
		// Probed separately rather than inferred from PythonPath's basename: the
		// candidate order means a host with 3.11 reports it here anyway, and a
		// host without one must report empty even though PythonPath is set.
		if path, err := p.LookPath("python3.11"); err == nil {
			r.Python311Path = path
		}
	}
	if p.Getwd != nil {
		if wd, err := p.Getwd(); err == nil && strings.HasPrefix(wd, "/mnt/") {
			r.UnderMnt = true
		}
	}
	if r.Docker {
		r.Tier = TierK8s
	} else {
		r.Tier = TierSubprocess
	}
	return r
}

// detectLibc returns "musl" or "glibc" on linux (based on the dynamic loader)
// and an empty string on other platforms.
func detectLibc(p Probe) string {
	if p.GOOS != "linux" {
		return ""
	}
	if p.Stat != nil {
		for _, loader := range muslLoaders {
			if _, err := p.Stat(loader); err == nil {
				return "musl"
			}
		}
	}
	return "glibc"
}

// PythonCandidates returns the interpreter binary names Detect probes for, in
// preference order. It exists so `leoflow dev` and `leoflow validate` resolve an
// interpreter from the SAME list `leoflow setup` and `leoflow doctor` report on
// (#1092): the two used to disagree, so doctor could name a python3.13 that dev
// never probed for. The slice is copied because callers are in another package
// and a shared backing array would let one of them reorder the gate's literal.
func PythonCandidates() []string {
	return append([]string(nil), pythonCandidates...)
}
