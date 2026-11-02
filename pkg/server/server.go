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

	"github.com/dexadata/dexaflow/internal/envcompat"
	"github.com/dexadata/dexaflow/internal/serverapp"
	"github.com/dexadata/dexaflow/pkg/admission"
	"github.com/dexadata/dexaflow/pkg/logsink"
)

// runApp builds and serves the control plane; a test replaces it.
var runApp = serverapp.Run

// ErrDatabaseReplaced is returned by Run when the readiness probe found
// another installation's database, or none, under the running server. The
// binary exits with ExitCode(ErrDatabaseReplaced), 3, so a restart count tells
// it from a crash.
var ErrDatabaseReplaced = serverapp.ErrDatabaseReplaced

// Option changes how Run builds the server. More options come in later
// releases (ADR 0099 §3); adding one is not a breaking change.
type Option func(*settings)

type settings struct {
	app serverapp.Options
}

// WithAdmission registers a dispatch admission hook (ADR 0100). Every task
// attempt is offered to it after Dexaflow's own checks and before the
// attempt's agent token is minted. Hooks run in the order they are
// registered, before the webhook execution.admission.webhook.url configures;
// the first refusal wins and each later hook sees the earlier changes.
// execution.admission.timeout and fail_open apply to every hook. A nil hook
// is ignored.
func WithAdmission(h admission.Hook) Option {
	return func(s *settings) {
		if h != nil {
			s.app.AdmissionHooks = append(s.app.AdmissionHooks, h)
		}
	}
}

// WithLogSink stores task logs in sink instead of the backend logs.backend
// configures, whose keys are then ignored; boot logs that the sink came from
// an option. The sink serves every read of a task log too, so the API, the UI
// and the CLI show what it stored. The caller owns it: Run never closes it.
// The optional interfaces of package logsink apply, and a sink without
// logsink.Resumer refuses an agent's reconnect of a log stream. A nil sink
// keeps the configured one; with several options the last one wins.
func WithLogSink(sink logsink.Sink) Option {
	return func(s *settings) {
		if sink != nil {
			s.app.LogSink = sink
		}
	}
}

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
	return runApp(ctx, s.app)
}

// ExitCode is the process exit code for an error Run returned: 0 for nil, 3
// for ErrDatabaseReplaced, 1 for anything else.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	return serverapp.ExitCode(err)
}
