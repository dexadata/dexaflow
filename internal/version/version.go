// Package version exposes build metadata embedded into the binary at link time.
package version

import (
	"fmt"
	"runtime"
)

// Build metadata. These are overridden via -ldflags "-X" at build time
// (see the Makefile); the defaults apply to `go run` and untagged builds.
var (
	version   = "dev"
	gitCommit = "none"
	buildDate = "unknown"
)

// Info holds build metadata describing how the binary was produced.
type Info struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
}

// Get returns the build metadata embedded at link time, augmented with the
// Go runtime version the binary was compiled with.
func Get() Info {
	return Info{
		Version:   version,
		GitCommit: gitCommit,
		BuildDate: buildDate,
		GoVersion: runtime.Version(),
	}
}

// String returns a single-line, human-readable rendering of the build metadata.
func (i Info) String() string {
	return fmt.Sprintf("dexaflow %s (commit %s, built %s, %s)",
		i.Version, i.GitCommit, i.BuildDate, i.GoVersion)
}

// WantsVersion reports whether args ask for the version (and nothing else runs).
// It accepts `version`, `-version`, and `--version` so the long-running
// companion binaries (leoflow-server / -agent / -mcp) can answer "what version
// are you?" without a full runnable config — checked before any config load or
// network connect. Kept deliberately narrow (no `-v`, which conventionally means
// verbose) and exact (no substring match).
func WantsVersion(args []string) bool {
	for _, a := range args {
		switch a {
		case "version", "-version", "--version":
			return true
		}
	}
	return false
}

// WantsHelp reports whether args ask for usage help. Like WantsVersion, it lets
// the config-less companion daemons (leoflow-server / -agent) answer `--help`
// with a usage message and exit 0, instead of falling through to a boot attempt
// that errors on missing config. Accepts the conventional `-h`, `-help`, and
// `--help`; exact match, no substring.
func WantsHelp(args []string) bool {
	for _, a := range args {
		switch a {
		case "-h", "-help", "--help":
			return true
		}
	}
	return false
}
