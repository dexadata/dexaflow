// Package server runs the Dexaflow control plane from another Go program
// (ADR 0099). Run with no options behaves exactly as the dexaflow-server
// binary, which is itself a thin caller of this package.
//
// Compatibility: a patch release never breaks an exported identifier of this
// package. A minor may, after the identifier was marked Deprecated in the
// previous minor, with a changelog entry that says what to use instead.
package server

import (
	"context"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/envcompat"
	"github.com/dexadata/dexaflow/internal/serverapp"
)

// runApp builds and serves the control plane; a test replaces it.
var runApp func(context.Context, *config.ServerConfig) error = serverapp.Run

// ErrDatabaseReplaced is returned by Run when the readiness probe found
// another installation's database, or none, under the running server. The
// binary exits with ExitCode(ErrDatabaseReplaced), 3, so a restart count tells
// it from a crash.
var ErrDatabaseReplaced = serverapp.ErrDatabaseReplaced

// Option changes how Run builds the server. Options to replace a component
// come in later releases (ADR 0099 §3); adding one is not a breaking change.
type Option func(*settings)

type settings struct{}

// Run builds the control plane and serves until ctx is done, then shuts down
// and returns. Call it once per process.
//
// Before anything reads the environment it mirrors the DEXAFLOW_* and
// LEOFLOW_* names onto each other, as the binary does, so either spelling
// configures the server. It reads the config file named by DEXAFLOW_CONFIG,
// then the environment. It installs no signal handler: cancel ctx to stop the
// server, for example with signal.NotifyContext.
func Run(ctx context.Context, opts ...Option) error {
	var s settings
	for _, o := range opts {
		o(&s)
	}
	serverapp.LogEnvCompat(envcompat.MirrorProcess())
	return runApp(ctx, nil)
}

// ExitCode is the process exit code for an error Run returned: 0 for nil, 3
// for ErrDatabaseReplaced, 1 for anything else.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	return serverapp.ExitCode(err)
}
