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
	"net/http"

	"github.com/dexadata/dexaflow/internal/envcompat"
	"github.com/dexadata/dexaflow/internal/serverapp"
	"github.com/dexadata/dexaflow/pkg/admission"
	"github.com/dexadata/dexaflow/pkg/authn"
	"github.com/dexadata/dexaflow/pkg/logsink"
	"github.com/dexadata/dexaflow/pkg/xcomstore"
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

// WithXComBackend stores XCom values in backend instead of Redis or, with no
// Redis configured, the embedded Postgres store; boot logs that the backend
// came from an option. The server still checks the size and schema of each
// value and records its metadata in Postgres, and Redis, when configured,
// still serves the live log tail. A backend that is also an
// xcomstore.BatchBackend serves a task's bulk pulls in one call. The caller
// owns it: Run never closes it. A nil backend keeps the configured one; with
// several options the last one wins.
func WithXComBackend(backend xcomstore.Backend) Option {
	return func(s *settings) {
		if backend != nil {
			s.app.XComBackend = backend
		}
	}
}

// UIServer serves a web UI in place of the embedded one. The server mounts
// StaticHandler under /static/, with that prefix stripped from the request
// path, and calls Index for every other GET that matches no API or /ui/
// route, with the base path the UI is served under. Index is reached only
// with a valid session: a visitor without one is redirected to the server's
// own sign-in page first, so a UI never renders for a signed-out visitor.
type UIServer interface {
	StaticHandler() http.Handler
	Index(w http.ResponseWriter, basePath string)
}

// WithUIServer serves ui in place of the embedded single page app; the ui.*
// keys that configure the embedded one (banners, instance name, home link,
// favicon, stylesheets) are then ignored, and boot logs that the UI came
// from an option. The API, /ui/config included, is unchanged. A nil ui keeps
// the embedded one; with several options the last one wins.
func WithUIServer(ui UIServer) Option {
	return func(s *settings) {
		if ui != nil {
			s.app.UIServer = ui
		}
	}
}

// WithAuthenticator checks API requests and password sign-in with a, an
// authenticator of your own, such as one backed by a company directory. A
// request token goes to the server's own authenticator first, so the
// sessions an SSO login, a trusted-issuer handoff or a renewal opens keep
// working, and to a only when the server refuses it as invalid. Password
// sign-in (POST /api/v2/auth/token) goes to a alone. API tokens and CI
// sessions are still checked in front of both, and agent tokens never reach
// a.
//
// The server authorizes the User a returns by its roles and permissions, and
// clears its Scoped, Scopes and DeployAudience fields first: only the server
// grants those (ADR 0099 §6). A nil a keeps the server's own sign-in; with
// several options the last one wins.
func WithAuthenticator(a authn.Authenticator) Option {
	return func(s *settings) {
		if a != nil {
			s.app.Authenticator = a
		}
	}
}

// WithTrustedIssuer verifies the trusted-issuer handoff (POST
// /api/v2/auth/session) with an issuer of your own, accepting a handoff only
// when the browser posts it from one of allowedOrigins (scheme://host[:port],
// one or more). A verified identity opens a UI session for the active user
// linked to the issuer under its subject, in its tenant; it never creates a
// user and never brings roles. Run refuses to start when allowedOrigins is
// empty or invalid, or when auth.trusted_issuer is configured too.
//
// The bearer mode, the deploy audience and the host pattern of
// auth.trusted_issuer need the built-in verifier and stay off with an issuer
// from options. A nil ti keeps the configured issuer; with several options
// the last one wins.
func WithTrustedIssuer(ti authn.TrustedIssuer, allowedOrigins ...string) Option {
	return func(s *settings) {
		if ti != nil {
			s.app.TrustedIssuer = ti
			s.app.TrustedIssuerOrigins = allowedOrigins
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
