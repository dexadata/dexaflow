package storeerr_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"

	"github.com/dexadata/dexaflow/internal/storeerr"
)

// redisReplyErr is how go-redis surfaces a server error reply: a string error
// whose text starts with the reply's prefix.
type redisReplyErr string

func (e redisReplyErr) Error() string { return string(e) }
func (redisReplyErr) RedisError()     {}

// TestTransientRecognizesStoreOutages pins the failures a control plane sees
// while its database or Redis fails over, restarts or runs out of
// connections (R-01). Each is wrapped the way the storage layer wraps errors,
// so the classification has to reach through %w.
func TestTransientRecognizesStoreOutages(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	cases := map[string]error{
		"postgres admin shutdown (failover, restart)": &pgconn.PgError{Code: "57P01"},
		"postgres crash shutdown":                     &pgconn.PgError{Code: "57P02"},
		"postgres starting up":                        &pgconn.PgError{Code: "57P03"},
		"postgres connection exception class":         &pgconn.PgError{Code: "08006"},
		"postgres too many connections":               &pgconn.PgError{Code: "53300"},
		"postgres statement timeout":                  &pgconn.PgError{Code: "57014"},
		"postgres read-only after a failover":         &pgconn.PgError{Code: "25006"},
		"postgres serialization failure":              &pgconn.PgError{Code: "40001"},
		"postgres deadlock":                           &pgconn.PgError{Code: "40P01"},
		"pool acquire timed out":                      context.DeadlineExceeded,
		"connection refused":                          refused,
		"connection dropped mid-read":                 io.ErrUnexpectedEOF,
		"connection closed":                           io.EOF,
		"redis pool timeout":                          redis.ErrPoolTimeout,
		"redis client closed":                         redis.ErrClosed,
		"redis loading":                               redisReplyErr("LOADING Redis is loading the dataset in memory"),
		"redis read-only replica":                     redisReplyErr("READONLY You can't write against a read only replica."),
		"redis master down":                           redisReplyErr("MASTERDOWN Link with MASTER is down"),
		"redis try again":                             redisReplyErr("TRYAGAIN Multiple keys request during rehashing of slot"),
		"redis cluster down":                          redisReplyErr("CLUSTERDOWN The cluster is down"),
	}
	for name, cause := range cases {
		t.Run(name, func(t *testing.T) {
			wrapped := fmt.Errorf("loading task spec: %w", cause)
			if !storeerr.Transient(wrapped) {
				t.Errorf("Transient(%v) = false, want true", wrapped)
			}
		})
	}
}

// TestTransientRejectsLogicalFailures: an error that will fail the same way on
// every retry stays non-transient, so the agent is told Internal and does not
// spend its retry budget on it.
func TestTransientRejectsLogicalFailures(t *testing.T) {
	cases := map[string]error{
		"nil":                         nil,
		"plain error":                 errors.New("decoding spec: unexpected token"),
		"unique violation":            &pgconn.PgError{Code: "23505"},
		"foreign key violation":       &pgconn.PgError{Code: "23503"},
		"undefined column":            &pgconn.PgError{Code: "42703"},
		"redis wrong type":            redisReplyErr("WRONGTYPE Operation against a key holding the wrong kind of value"),
		"redis key not found":         redis.Nil,
		"caller canceled the request": context.Canceled,
	}
	for name, cause := range cases {
		t.Run(name, func(t *testing.T) {
			if storeerr.Transient(cause) {
				t.Errorf("Transient(%v) = true, want false", cause)
			}
		})
	}
}
