package agentrpc

import (
	"errors"
	"log/slog"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// detailInternal is the phrase an agent receives in place of a control-plane
// failure's own text.
//
// The task pod is a trust boundary. It runs the tenant's own image and
// entrypoint, so every gRPC status message is text handed to authenticated,
// tenant-controlled code that can call TaskSpec, GetVariables or GetXCom in a
// loop. A repository error normally wraps a driver error, and pgconn renders a
// *pgconn.PgError as "severity: message (SQLSTATE code)" with the constraint,
// table and column names of our schema inside the message; a *pgconn.ConnectError
// renders the database user and name. Echoing either into the pod is schema
// disclosure (CWE-209). RecoveryUnaryInterceptor already collapses a panic to a
// constant for exactly this audience; this is the same rule applied to the
// failures the handlers themselves see.
const detailInternal = "the request could not be completed; see the control-plane logs"

// safeMessage returns the phrase Leoflow composed for this failure, or fallback
// when the error carries no such phrase.
//
// The default is deny. Only a domain.SafeError — an error someone deliberately
// wrote as client-facing — contributes text to a status message; everything
// else, including every fmt.Errorf wrap of a driver error, is replaced. A newly
// introduced storage error therefore cannot leak by omission: it leaks only if
// someone rewrites it as a SafeError, the one construct whose GoDoc says its
// message is shown verbatim.
func safeMessage(err error, fallback string) string {
	var safe *domain.SafeError
	if errors.As(err, &safe) && safe.ClientMessage() != "" {
		return safe.ClientMessage()
	}
	return fallback
}

// internalStatus is the single sanctioned way to turn a failure the agent
// cannot act on into the status it receives. It returns codes.Internal with op
// plus a client-safe phrase, and hands the real error to the control-plane log
// under a cause field, alongside attrs.
//
// op is a constant written at the call site, never derived from an error, so
// the agent's own log — the one an operator reads in the task's output — still
// says WHICH step failed. Over-redaction is a real regression: a task that
// fails with no usable reason costs the person debugging the DAG more than the
// disclosure costs the tenant.
//
// The parameter is named cause, not err, because the value is destined for the
// log and never for the wire; the package's source guard trips on any
// error-named value reaching a status constructor.
func internalStatus(op string, cause error, attrs ...any) error {
	slog.Error(op, causeArgs(cause, attrs)...)
	return redactedStatus(op, cause)
}

// peerStatus is internalStatus for a failure the PEER caused rather than the
// control plane: a stream that broke because the pod went away. The redaction
// is identical — a transport error is not a database error today, but a status
// built from any error value is the shape this package no longer allows — while
// the log stays at warn, so an ordinary pod kill does not manufacture an error
// line for an operator to chase.
func peerStatus(op string, cause error, attrs ...any) error {
	slog.Warn(op, causeArgs(cause, attrs)...)
	return redactedStatus(op, cause)
}

// redactedStatus is the message both build: the operation, then either the
// phrase Leoflow composed for this failure or the constant.
func redactedStatus(op string, cause error) error {
	return status.Error(codes.Internal, op+": "+safeMessage(cause, detailInternal))
}

// causeArgs puts the real error first on the log line, ahead of the caller's
// own fields, under the same cause key the HTTP request log uses (#961).
func causeArgs(cause error, attrs []any) []any {
	// Every call site today is inside an `if err != nil`, so this cannot fire —
	// but this helper is documented as the single sanctioned way to build one of
	// these statuses, and a future site outside that guard would panic into the
	// recovery interceptor rather than log.
	if cause == nil {
		return attrs
	}
	args := make([]any, 0, len(attrs)+2)
	args = append(args, "cause", cause.Error())
	return append(args, attrs...)
}

// attemptAttrs are the log fields every redacted agent failure carries. The
// agent has no request id to echo back, so the attempt identity is the join key
// between the phrase the task log shows and the cause recorded here.
func attemptAttrs(id *auth.AgentIdentity) []any {
	if id == nil {
		return nil
	}
	return []any{
		"ti", id.TaskInstanceID, "tenant", id.TenantID, "dag", id.DagID,
		"run", id.RunID, "task", id.TaskID, "try", id.TryNumber,
	}
}
