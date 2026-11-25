// Package version exposes build metadata embedded into the binary at link time.
package version

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"runtime/debug"
)

// ReleaseVersionEnv names the release an rc image was promoted to (ADR 0109).
// A GA whose images are the last rc's carries it in the image configuration,
// so the rc binaries in it report the GA version. Promoted honors it only for
// the rc's own release.
const ReleaseVersionEnv = "DEXAFLOW_RELEASE_VERSION"

// rcVersion matches a release candidate version, with or without the leading
// "v" (GoReleaser's .Version has none), and captures its release.
var rcVersion = regexp.MustCompile(`^v?([0-9]+\.[0-9]+\.[0-9]+)-rc\.[0-9]+$`)

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
	// BuiltAs is the version the binary was linked as when it reports a
	// promoted release version instead (ADR 0109); empty otherwise.
	BuiltAs string `json:"built_as,omitempty"`
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
	info := Info{
		Version:   v,
		GitCommit: gitCommit,
		BuildDate: buildDate,
		GoVersion: runtime.Version(),
	}
	if p := Promoted(v); p != v {
		info.Version, info.BuiltAs = p, v
	}
	return info
}

// Promoted returns the version a binary linked as linked reports. It is the
// release named by ReleaseVersionEnv when linked is a release candidate of
// exactly that release (v0.6.0-rc.2 with v0.6.0), and linked otherwise, so the
// variable can promote an rc to its own GA and cannot make any other build
// claim a version. The result keeps linked's form, with or without the "v".
func Promoted(linked string) string {
	return promoted(linked, os.Getenv(ReleaseVersionEnv))
}

func promoted(linked, release string) string {
	m := rcVersion.FindStringSubmatch(linked)
	if m == nil {
		return linked
	}
	base := m[1]
	if release != base && release != "v"+base {
		return linked
	}
	if linked[0] == 'v' {
		return "v" + base
	}
	return base
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
	if i.BuiltAs != "" {
		return fmt.Sprintf("dexaflow %s (built as %s, commit %s, built %s, %s)",
			i.Version, i.BuiltAs, i.GitCommit, i.BuildDate, i.GoVersion)
	}
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
