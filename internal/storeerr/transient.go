// Package storeerr classifies the errors the control plane's stores (Postgres
// and Redis) return, so a caller can tell an outage that a retry outlasts from
// a failure that repeats on every attempt.
package storeerr

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
)

// transientSQLStates are the Postgres error codes a failover, a restart, a full
// connection budget or a lock conflict produce. Class 08 (connection exception)
// is matched by prefix in pgTransient.
var transientSQLStates = map[string]bool{
	"57P01": true, // admin_shutdown: the server was told to stop (failover, restart)
	"57P02": true, // crash_shutdown
	"57P03": true, // cannot_connect_now: the server is starting up
	"53300": true, // too_many_connections
	"57014": true, // query_canceled: statement_timeout, or a cancel during failover
	"25006": true, // read_only_sql_transaction: connected to a demoted primary
	"40001": true, // serialization_failure
	"40P01": true, // deadlock_detected
}

// redisTransientPrefixes are the reply prefixes Redis uses while it loads its
// dataset, after a failover demoted the node, or while a cluster reshards.
var redisTransientPrefixes = []string{"LOADING", "READONLY", "MASTERDOWN", "TRYAGAIN", "CLUSTERDOWN"}

// Transient reports whether err is a store failure that a later retry of the
// same request can succeed past: the database or Redis was unreachable,
// restarting, failing over, out of connections, or timed out. It reads only the
// error's identity and codes, never its text, except for the reply prefixes
// Redis defines as its error codes.
//
// A logical failure (a constraint violation, a missing key, bad input) is not
// transient, and neither is context.Canceled: a canceled request was abandoned
// by its caller, not failed by the store.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if pgTransient(err) || redisTransient(err) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func pgTransient(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return transientSQLStates[pgErr.Code] || strings.HasPrefix(pgErr.Code, "08")
	}
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return true
	}
	return pgconn.SafeToRetry(err) || pgconn.Timeout(err)
}

func redisTransient(err error) bool {
	if errors.Is(err, redis.ErrPoolTimeout) || errors.Is(err, redis.ErrClosed) {
		return true
	}
	var replyErr redis.Error
	if !errors.As(err, &replyErr) {
		return false
	}
	for _, prefix := range redisTransientPrefixes {
		if redis.HasErrorPrefix(replyErr, prefix) {
			return true
		}
	}
	return false
}
