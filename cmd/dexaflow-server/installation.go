package main

import (
	"context"
	"errors"
)

// exitDatabaseReplaced is the exit code of a process whose database was
// replaced under it (ADR 0090 decision 5), so an operator and the restart
// count tell it from a crash. 1 stays every other failure.
const exitDatabaseReplaced = 3

// errDatabaseReplaced stops a running server whose readiness probe found
// another installation's database, or none, under it.
var errDatabaseReplaced = errors.New("the database was replaced under this process (another installation id, none, or no schema); " +
	"exiting so no cache outlives the old database: the restart waits, not ready, until the right database is back or this one is accepted with installation.id")

// replacedNotifier is told when readiness finds the database replaced;
// *storage.Postgres implements it.
type replacedNotifier interface {
	SetOnReplaced(func())
}

// stopOnReplaced returns the context the server runs under, canceled with
// errDatabaseReplaced the first time readiness finds the database replaced.
// Exiting is the only flush that is complete: it drops the tenant id cache
// (#1614), the spec cache and every other cache without listing them.
func stopOnReplaced(ctx context.Context, n replacedNotifier) (runCtx context.Context, release func()) {
	c, cancel := context.WithCancelCause(ctx)
	n.SetOnReplaced(func() { cancel(errDatabaseReplaced) })
	return c, func() { cancel(nil) }
}

// serveExit turns a shutdown the server caused itself (a lost key-migration
// lock, a replaced database) into a non-zero exit, so neither the
// supervising `dexaflow lite`, the kubelet nor an operator mistakes it for a
// clean stop. A serve error wins.
func serveExit(ctx context.Context, serveErr error) error {
	if serveErr != nil {
		return serveErr
	}
	cause := context.Cause(ctx)
	if errors.Is(cause, errKeyLockLost) || errors.Is(cause, errDatabaseReplaced) {
		return cause
	}
	return nil
}

// exitCode is the process exit code for what run returned.
func exitCode(err error) int {
	if errors.Is(err, errDatabaseReplaced) {
		return exitDatabaseReplaced
	}
	return 1
}
