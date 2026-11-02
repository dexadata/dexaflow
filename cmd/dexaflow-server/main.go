// Command dexaflow-server runs the Dexaflow control plane: the HTTP API, auth,
// metrics, and (when enabled) the scheduler. It is the first caller of
// pkg/server (ADR 0099): it answers its flags, stops the server on SIGINT or
// SIGTERM and maps the result to an exit code.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dexadata/dexaflow/internal/serverapp"
	"github.com/dexadata/dexaflow/internal/version"
	"github.com/dexadata/dexaflow/pkg/server"
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
	// server.Run mirrors the DEXAFLOW_* and LEOFLOW_* names before it reads
	// the environment; the flags above read none of it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "dexaflow-server:", err)
		os.Exit(server.ExitCode(err))
	}
}
