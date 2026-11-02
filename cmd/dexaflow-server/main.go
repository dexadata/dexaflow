// Command dexaflow-server runs the Dexaflow control plane: the HTTP API, auth,
// metrics, and (when enabled) the scheduler. The wiring lives in
// internal/serverapp (ADR 0099); this command answers its flags and maps the
// result to an exit code.
package main

import (
	"fmt"
	"os"

	"github.com/dexadata/dexaflow/internal/envcompat"
	"github.com/dexadata/dexaflow/internal/serverapp"
	"github.com/dexadata/dexaflow/internal/version"
)

// usage is printed for `--help`. leoflow-server takes no positional args; it is
// configured entirely via environment and an optional LEOFLOW_CONFIG file.
const usage = `dexaflow-server — the Dexaflow control plane (HTTP API, auth, metrics, scheduler).

Configured via environment variables and an optional config file (DEXAFLOW_CONFIG);
there are no positional arguments. See docs/configuration.md.

Flags:
  --version   print version and exit
  --help, -h  print this help and exit
`

func main() {
	// DEXAFLOW_* and the pre-rename LEOFLOW_* names are interchangeable; mirror
	// them before anything reads the environment.
	serverapp.LogEnvCompat(envcompat.MirrorProcess())

	// Answer `--version`/`--help` before loading any config, so an operator can
	// query a deployed binary without a runnable environment (#593). Without
	// this, `--help` falls through to a boot attempt that errors on missing
	// config.
	args := os.Args[1:]
	switch {
	case version.WantsVersion(args):
		fmt.Print(serverapp.VersionOutput())
		return
	case version.WantsHelp(args):
		fmt.Print(usage)
		return
	}
	if err := serverapp.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "dexaflow-server:", err)
		os.Exit(serverapp.ExitCode(err))
	}
}
