// Package version exposes build metadata embedded into the binary at link time.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// modulePath is this module's path, looked up in the build info of a program
// that imports it (pkg/server) and so sets no ldflags of its own.
const modulePath = "github.com/dexadata/dexaflow"

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
// Go runtime version the binary was compiled with. A program that imports
// this module (through pkg/server) sets no ldflags, so its version is the
// module version its go.mod requires, read from the build info.
func Get() Info {
	v := version
	if v == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			v = dependencyVersion(bi, v)
		}
	}
	return Info{
		Version:   v,
		GitCommit: gitCommit,
		BuildDate: buildDate,
		GoVersion: runtime.Version(),
	}
}

// dependencyVersion is the version of this module as a dependency of the
// program bi describes, or fallback when the program is this module itself
// (its own builds stay "dev" without ldflags) or does not list it.
func dependencyVersion(bi *debug.BuildInfo, fallback string) string {
	if bi.Main.Path == modulePath {
		return fallback
	}
	for _, d := range bi.Deps {
		if d.Path != modulePath {
			continue
		}
		if d.Replace != nil && d.Replace.Version != "" {
			return d.Replace.Version
		}
		if d.Version != "" {
			return d.Version
		}
	}
	return fallback
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
