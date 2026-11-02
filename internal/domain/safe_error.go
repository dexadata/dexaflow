package domain

import (
	"errors"
	"fmt"
)

// SafeError carries a message Dexaflow composed itself, attached to one of the
// sentinel classes above. It exists so the API boundary can tell "a phrase we
// wrote for the caller" apart from "whatever text the failure happened to
// carry".
//
// The distinction is a security boundary, not a style choice. A repository
// error usually wraps a driver error, and a *pgconn.PgError renders itself as
// "severity: message (SQLSTATE code)" with the constraint, table and column
// names of our schema inside the message. Echoing that to an authenticated
// tenant of a multi-tenant control plane is schema disclosure (CWE-209). The
// API therefore returns Message verbatim and replaces everything else with a
// constant phrase, logging the real error instead.
//
// That inverts the safe/unsafe default: a new error is opaque to clients until
// someone deliberately writes it as a SafeError, so a newly wrapped driver
// error cannot leak by omission.
//
// Message is returned to API clients verbatim. Never interpolate a driver,
// filesystem, or network error into it — pass the caller-facing facts (a role
// name, a declared variable, a configured cap) and let the underlying error
// reach the log through the wrap chain.
type SafeError struct {
	// Message is the client-facing phrase, without the class's own wording.
	Message string
	// Class is the sentinel this failure belongs to (ErrValidation,
	// ErrConflict, ErrNotFound, ...). errors.Is sees it through Unwrap.
	Class error
}

// Error renders "<message>: <class>", identical to what
// fmt.Errorf("<message>: %w", class) produced before, so logs and error strings
// are unchanged.
func (e *SafeError) Error() string {
	if e.Message == "" {
		return e.Class.Error()
	}
	return e.Message + ": " + e.Class.Error()
}

// Unwrap returns the sentinel class so errors.Is keeps working.
func (e *SafeError) Unwrap() error { return e.Class }

// ClientMessage returns the phrase that may be shown to an API client.
func (e *SafeError) ClientMessage() string { return e.Message }

// Safef builds a SafeError in class with the formatted message. The message is
// shown to API clients verbatim, so it must contain only facts the caller
// supplied or Dexaflow chose — never a driver, filesystem, or network error.
func Safef(class error, format string, a ...any) error {
	return &SafeError{Message: fmt.Sprintf(format, a...), Class: class}
}

// ClientMessageOr returns the phrase Dexaflow composed for err, or fallback
// when err carries no such phrase. It is the deny-by-default rule every client
// boundary applies: the HTTP API (safeDetail) and the agent gRPC service
// (safeMessage) both read it from here, so the rule has one copy (#1078). Only
// a SafeError, an error someone deliberately wrote as client-facing,
// contributes text; everything else, including every fmt.Errorf wrap of a
// driver error, is replaced, so a newly introduced storage error cannot leak by
// omission.
func ClientMessageOr(err error, fallback string) string {
	var safe *SafeError
	if errors.As(err, &safe) && safe.ClientMessage() != "" {
		return safe.ClientMessage()
	}
	return fallback
}
