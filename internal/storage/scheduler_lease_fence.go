package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/config"
)

// Leader write fencing (ADR 0106, #2329).
//
// With scheduler.lease_fencing on, the scheduler's writers (the tick, the
// dispatch workers, the reapers, the reconcilers, retention) run on a
// dedicated pool whose every session carries the lease term of the replica's
// latest leadership: dexaflow.leader_term, and an application_name of
// dexaflow-leader/<term> that a newer leader uses to end those sessions. The
// statement triggers of migration 060 refuse a write from a session whose tag
// is not the lease's current term. Only a change costs a round trip: the tag
// a session carries is remembered on the connection itself.

// leaderTermData is the key, in a connection's custom data, of the term the
// session is tagged with.
const leaderTermData = "dexaflow.leader_term"

// staleLeaderTermCode is the SQLSTATE the fence trigger raises.
const staleLeaderTermCode = "DXF01"

// defaultFencedSchedulerConns sizes the scheduler pool when fencing makes it
// dedicated and database.scheduler_max_conns is unset.
const defaultFencedSchedulerConns = 8

// IsStaleLeaderTerm reports whether err is the fence refusing a write from a
// session of an older scheduler lease term.
func IsStaleLeaderTerm(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == staleLeaderTermCode
}

// tagLeaderTerm returns the PrepareConn that makes a connection's tag match
// the lease's term before the caller gets it. An error destroys the
// connection, so one whose tag is unknown never goes back to the pool; a
// session that was ended is replaced without failing the caller.
func tagLeaderTerm(lease *SchedulerLease) func(context.Context, *pgx.Conn) (bool, error) {
	return func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		want := strconv.FormatInt(lease.Term(), 10)
		data := conn.PgConn().CustomData()
		if have, ok := data[leaderTermData].(string); ok && have == want {
			return true, nil
		}
		if _, err := conn.Exec(ctx,
			"SELECT set_config('dexaflow.leader_term', $1, false), set_config('application_name', $2, false)",
			want, "dexaflow-leader/"+want); err != nil {
			delete(data, leaderTermData)
			if conn.IsClosed() {
				// A newer leader ended this session (ADR 0106, point 4), or it
				// dropped: retry the caller's query on a new connection.
				return false, nil
			}
			return false, fmt.Errorf("tagging the scheduler session with its lease term: %w", err)
		}
		data[leaderTermData] = want
		return true, nil
	}
}

// fenceTracer counts the writes the fence refused, through onFenced.
type fenceTracer struct {
	onFenced func()
}

// TraceQueryStart implements pgx.QueryTracer.
func (t fenceTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

// TraceQueryEnd implements pgx.QueryTracer.
func (t fenceTracer) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if t.onFenced != nil && IsStaleLeaderTerm(data.Err) {
		t.onFenced()
	}
}

// fencedSchedulerPoolConfig builds the scheduler pool of a fenced leader: the
// scheduler pool's config, dedicated whether or not its size is set, with
// every session tagged by the lease's term.
func fencedSchedulerPoolConfig(cfg config.DatabaseSection, lease *SchedulerLease, onFenced func()) (*pgxpool.Config, error) {
	n := cfg.SchedulerMaxConns
	if n <= 0 {
		n = defaultFencedSchedulerConns
	}
	if n > math.MaxInt32 {
		return nil, fmt.Errorf("database.scheduler_max_conns must be between 1 and %d, got %d", math.MaxInt32, n)
	}
	pc, err := smallPoolConfig(cfg, int32(n))
	if err != nil {
		return nil, err
	}
	pc.PrepareConn = tagLeaderTerm(lease)
	pc.ConnConfig.Tracer = fenceTracer{onFenced: onFenced}
	return pc, nil
}

// ForFencedScheduler is ForScheduler for a leader with
// scheduler.lease_fencing on: always a dedicated pool, of
// database.scheduler_max_conns connections or 8 when it is unset, whose
// sessions carry lease's term. onFenced, when set, is called for each write
// the fence refuses.
func (p *Postgres) ForFencedScheduler(ctx context.Context, cfg config.DatabaseSection, lease *SchedulerLease, onFenced func()) (*Postgres, func(), error) {
	pc, err := fencedSchedulerPoolConfig(cfg, lease, onFenced)
	if err != nil {
		return nil, nil, err
	}
	return p.dedicated(ctx, pc, "scheduler", &p.schedulerPool)
}
