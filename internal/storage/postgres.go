// Package storage wraps the Postgres and Redis connections used by the control
// plane, exposing the sqlc-generated query set and health checks.
package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// pgStartupBudget caps how long NewPostgres waits for the first successful
// ping. Lite hits this when docker compose marks Postgres Healthy (pg_isready)
// before the server accepts client TCP — the first ping gets `connection reset
// by peer`. Pro hits it during external-PG failover, network blips, or restart
// of the upstream cluster. 30s is comfortably above docker cold-start on a
// busy laptop but still surfaces a real misconfig (wrong DSN, blocked auth)
// before the operator gives up.
const pgStartupBudget = 30 * time.Second

// pgStartupBackoff is the first delay between failed pings; the loop doubles
// it up to a 3s cap. Small enough that a healthy Postgres still feels instant;
// large enough that real misconfigs don't get hammered with reconnect storms.
const pgStartupBackoff = 100 * time.Millisecond

// Postgres holds a pgx connection pool and the generated query set.
type Postgres struct {
	Pool    *pgxpool.Pool
	Queries *queries.Queries
	// specs memoizes parsed DAG specs by (immutable) dag_version_id, shared by
	// the scheduler read path and the dispatch/agent path so a spec is fetched
	// and decoded once per version, not once per active run per tick.
	specs *specCache
	// dirtyAheadLogged latches the readiness probe's "a migration is in flight"
	// log line so it is written once per episode rather than once per probe
	// period for the whole duration of a migration. SchemaReady clears it when
	// the condition clears, so a later upgrade is reported again.
	dirtyAheadLogged atomic.Bool
	// health is a dedicated one-connection pool used by Ping and the schema
	// read, so a probe never queues behind application traffic. See
	// newHealthPool for why that matters. Nil only in tests that construct a
	// Postgres literal; probePool falls back to Pool in that case.
	health *pgxpool.Pool
}

// probePool returns the pool health checks must use: the dedicated one when it
// exists, the main pool otherwise. Every probe read goes through here so no
// single call site can quietly put a probe back on the request path's pool.
func (p *Postgres) probePool() *pgxpool.Pool {
	if p.health != nil {
		return p.health
	}
	return p.Pool
}

// poolConfig builds a pgxpool.Config from the database section.
func poolConfig(cfg config.DatabaseSection) (*pgxpool.Config, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing database url: %w", err)
	}
	if cfg.MaxOpenConns > 0 && cfg.MaxOpenConns <= math.MaxInt32 {
		pc.MaxConns = int32(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 && cfg.MaxIdleConns <= math.MaxInt32 {
		pc.MinConns = int32(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetimeJitterMS > 0 {
		pc.MaxConnLifetimeJitter = time.Duration(cfg.ConnMaxLifetimeJitterMS) * time.Millisecond
	}
	return pc, nil
}

// mainPoolConfig builds the config of the main pool, the one the API serves
// from. It is poolConfig plus database.statement_timeout_ms, which belongs on
// this pool alone: the leader pool's session holds the scheduler advisory lock,
// the health pool must answer probes, and the scheduler pool runs maintenance
// that is allowed to take its time. Set as a startup parameter, the timeout is
// in force from the first statement of every connection and survives RESET ALL.
func mainPoolConfig(cfg config.DatabaseSection) (*pgxpool.Config, error) {
	pc, err := poolConfig(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.StatementTimeoutMS > 0 {
		pc.ConnConfig.RuntimeParams["statement_timeout"] = strconv.Itoa(cfg.StatementTimeoutMS)
	}
	return pc, nil
}

// schedulerPoolConfig builds the config of the scheduler's dedicated pool,
// sized by database.scheduler_max_conns. Like the other small pools it keeps
// no idle floor, and it never carries the API statement timeout.
func schedulerPoolConfig(cfg config.DatabaseSection) (*pgxpool.Config, error) {
	n := cfg.SchedulerMaxConns
	if n <= 0 || n > math.MaxInt32 {
		return nil, fmt.Errorf("database.scheduler_max_conns must be between 1 and %d, got %d", math.MaxInt32, n)
	}
	return smallPoolConfig(cfg, int32(n))
}

// NewPostgres opens a connection pool and verifies connectivity, retrying
// transient failures during boot for up to pgStartupBudget. Pre-2026-06,
// the first failed ping fatal-ed the server, so a docker compose race or
// any Pro failover blip became a hard crash. The retry loop keeps Lite
// boot ergonomic and Pro startup resilient under realistic upstream-PG
// dynamics. A truly broken setup (wrong DSN, bad auth) still surfaces
// quickly because the underlying error is wrapped into the final error.
func NewPostgres(ctx context.Context, cfg config.DatabaseSection) (*Postgres, error) {
	pc, err := mainPoolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}
	if err := connectWithRetry(ctx, pool.Ping, pgStartupBudget, pgStartupBackoff); err != nil {
		pool.Close()
		return nil, err
	}
	// Opened here rather than wired by the caller: a probe that shares the
	// request pool is the failure mode this exists to prevent (#1042), and a
	// caller that forgets to attach it reintroduces that silently — the checks
	// map still satisfies HealthChecker and every test still passes. pgxpool
	// connects lazily, so this costs no boot latency.
	health, herr := newHealthPool(ctx, cfg)
	if herr != nil {
		pool.Close()
		return nil, herr
	}
	// The health pool gets the same startup budget as the main one. Without it
	// the retry loop above has a hole exactly where it was written for: the boot
	// schema gate reads through this pool, so a compose race or a failover blip
	// landing in the milliseconds between the main pool's successful ping and
	// this pool's first dial would fail boot outright.
	if err := connectWithRetry(ctx, health.Ping, pgStartupBudget, pgStartupBackoff); err != nil {
		health.Close()
		pool.Close()
		return nil, err
	}
	return &Postgres{Pool: pool, Queries: queries.New(pool), specs: newSpecCache(), health: health}, nil
}

// newHealthPool opens the dedicated pool behind Ping and the readiness schema
// read.
//
// Readiness takes two pool acquires per probe — Ping borrows a connection, the
// schema SELECT borrows another — and both used to come from the pool serving
// requests. Under saturation, with that pool fully lent out, the probe blocks
// waiting for a connection and fails on its own deadline. Every replica shares
// the condition, because load is what caused it, so they fail readiness in the
// same window and the Service can empty: a load-induced outage reported as a
// database problem, and one that feeds itself as the pods left in rotation
// absorb the load of the ones that left.
//
// Two connections, not one. Both probe reads are single-row SELECTs issued back
// to back, so the kubelet alone would be served fine by one — its prober is
// serial per container, so a pod never has two /readyz in flight. But three
// callers reach these checks, and the third is not a probe:
// /api/v2/monitor/health is the UI's database widget, authenticated but
// otherwise ungated, so any logged-in viewer can drive it. A second connection
// keeps a dashboard refresh off the kubelet's path. Same reasoning as
// NewLeaderPool, one layer over.
func newHealthPool(ctx context.Context, cfg config.DatabaseSection) (*pgxpool.Pool, error) {
	pc, err := smallPoolConfig(cfg, healthPoolConns)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("creating health pool: %w", err)
	}
	return pool, nil
}

// healthPoolConns is the health pool's size: one for the kubelet's serial
// prober, one so the UI's database widget cannot make it wait.
const healthPoolConns = 2

// smallPoolConfig builds a pool config capped at n connections, for the two
// pools that must stay tiny: the scheduler's advisory-lock session and the
// health checks.
//
// It clamps MinConns as well, which poolConfig sets from database.maxIdleConns
// (chart default 5). pgxpool does not reject MinConns > MaxConns and nothing
// breaks visibly, so this is a wart rather than a bug — but it is two warts,
// and the second is the one worth naming:
//
//   - Every health-check period, checkMinConns computes minConns-total and asks
//     puddle for the difference. Nothing reaches the network: puddle returns
//     ErrNotAvailable once the pool is at maxSize, BEFORE calling the
//     constructor, and pgxpool swallows that error. The cost is a few goroutines
//     a minute, not connection attempts against Postgres.
//   - checkConnsHealth guards BOTH of its destroy branches with
//     totalConns >= minConns, so with MinConns above MaxConns the pool was
//     exempt from background lifetime and idle recycling entirely. Clamping
//     re-enables both, which is the correct behavior.
//
// The second bullet used to end "and, for the leader pool, a no-op in practice:
// Acquire enforces MaxConnLifetime unconditionally, and watchLeadership
// acquires every 5s, so an expired connection was already being replaced within
// a tick of its deadline." Every clause of that is true and the conclusion was
// wrong. Replacing the connection is exactly the harm for the leader pool,
// because the advisory lock is session-scoped and dies with it (#1199).
// NewLeaderPool therefore sets its own lifetimes; see leaderConnLifetime.
func smallPoolConfig(cfg config.DatabaseSection, n int32) (*pgxpool.Config, error) {
	pc, err := poolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pc.MaxConns = n
	pc.MinConns = 0
	pc.MinIdleConns = 0
	return pc, nil
}

// connectWithRetry calls pingFn until it returns nil, the budget elapses, or
// the context is canceled. Backoff doubles each iteration up to 3s. The final
// error wraps the LAST underlying error so the operator can distinguish
// "connection reset" (transient race) from "auth failed" or "no such host"
// (real misconfig that will never recover). Extracted as a top-level helper
// so it is testable without a live Postgres — see postgres_retry_test.go.
func connectWithRetry(ctx context.Context, pingFn func(context.Context) error, budget, initialBackoff time.Duration) error {
	deadline := time.Now().Add(budget)
	backoff := initialBackoff
	const maxBackoff = 3 * time.Second
	var lastErr error
	for {
		lastErr = pingFn(ctx)
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("postgres unreachable after %s: %w", budget, lastErr)
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.Canceled) {
				return context.Canceled
			}
			return ctx.Err()
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// leaderConnLifetime is how long the leader pool's one connection may live.
//
// It is a very large finite duration and NOT zero, which is the whole point.
// pgxpool computes a connection's deadline as time.Now().Add(MaxConnLifetime),
// so zero expires it immediately and every Acquire opens a NEW session.
// Measured against a real Postgres: at zero, 0 of 6 acquires kept the same
// backend and 0 of 6 still held the advisory lock, which would turn an hourly
// step-down into one per watch tick. Same arithmetic, same trap, for
// MaxConnIdleTime.
//
// A century is "never" for a process that is restarted by every deploy, without
// asking time.Time to represent something it cannot.
const leaderConnLifetime = 100 * 365 * 24 * time.Hour

// NewLeaderPool opens a dedicated single-connection pool for the scheduler
// advisory lock, so the session holding the lock is stable (ADR 0009).
//
// "Stable" has to include not being recycled on a timer, and it did not.
// pg_advisory_lock is SESSION-scoped: the lock lives and dies with the
// connection. pgxpool.ParseConfig applies a default MaxConnLifetime of one hour
// when the DSN carries no pool_max_conn_lifetime, and leoflow's DSNs do not, so
// this pool's connection was replaced every hour and the leadership went with
// it. HoldsLock then correctly reported the lock gone and watchLeadership
// correctly stepped down: every layer did its job and the scheduler still gave
// up leadership once an hour for no reason at all.
//
// It is not theoretical. A 15.7-hour soak recorded 15 step-downs, one per hour,
// in a single-process deployment where nothing was contending for the lock
// (#1199). On one replica the cost is a pause; on several it moves leadership,
// and everything the leader was in the middle of, on a timer nobody chose.
//
// Recycling buys this pool nothing anyway. watchLeadership queries every 5s, so
// a connection that really dies is caught within a tick and the step-down that
// follows is a true one.
func NewLeaderPool(ctx context.Context, cfg config.DatabaseSection) (*pgxpool.Pool, error) {
	pc, err := leaderPoolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("creating leader pool: %w", err)
	}
	return pool, nil
}

// leaderPoolConfig builds the leader pool's config: one connection that is
// never recycled (see NewLeaderPool), with no lifetime jitter, and none of the
// main pool's statement timeout, which would cut the lock holder's queries.
func leaderPoolConfig(cfg config.DatabaseSection) (*pgxpool.Config, error) {
	pc, err := smallPoolConfig(cfg, 1)
	if err != nil {
		return nil, err
	}
	pc.MaxConnLifetime = leaderConnLifetime
	pc.MaxConnIdleTime = leaderConnLifetime
	pc.MaxConnLifetimeJitter = 0
	return pc, nil
}

// ForScheduler returns the handle the scheduler side should use, and a func
// that releases it.
//
// With database.scheduler_max_conns unset it is p itself and the release func
// does nothing: the scheduler shares the main pool, as it always has. With it
// set, it is a dedicated pool of that size, without the API statement timeout,
// so a burst of API traffic holding every main pool connection cannot stall a
// scheduler tick or a reaper behind it. The dedicated handle shares p's spec
// cache, so the scheduler and the agent path still decode each version once.
func (p *Postgres) ForScheduler(ctx context.Context, cfg config.DatabaseSection) (*Postgres, func(), error) {
	if cfg.SchedulerMaxConns <= 0 {
		return p, func() {}, nil
	}
	pc, err := schedulerPoolConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, nil, fmt.Errorf("creating scheduler pool: %w", err)
	}
	if err := connectWithRetry(ctx, pool.Ping, pgStartupBudget, pgStartupBackoff); err != nil {
		pool.Close()
		return nil, nil, err
	}
	sched := &Postgres{Pool: pool, Queries: queries.New(pool), specs: sharedSpecCache(p)}
	return sched, pool.Close, nil
}

// Ping checks database connectivity (used by /readyz). It goes through
// probePool, not Pool: a connectivity check that has to wait for the request
// path to free a connection is reporting load, not connectivity.
func (p *Postgres) Ping(ctx context.Context) error {
	return p.probePool().Ping(ctx)
}

// Close releases the connection pools.
func (p *Postgres) Close() {
	if p.health != nil {
		p.health.Close()
	}
	p.Pool.Close()
}
