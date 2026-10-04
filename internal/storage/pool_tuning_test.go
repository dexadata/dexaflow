package storage

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/config"
)

const tuningTestURL = "postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable"

// With no tuning keys set every pool is configured exactly as before.
func TestPoolTuningOffKeepsTodaysPools(t *testing.T) {
	cfg := config.DatabaseSection{URL: tuningTestURL, MaxOpenConns: 25, MaxIdleConns: 5}
	pc, err := mainPoolConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pc.ConnConfig.RuntimeParams["statement_timeout"]; ok {
		t.Errorf("main pool sets statement_timeout %q with the key unset", pc.ConnConfig.RuntimeParams["statement_timeout"])
	}
	if pc.AfterConnect != nil {
		t.Error("main pool runs an AfterConnect hook with the key unset")
	}
	if pc.MaxConnLifetimeJitter != 0 {
		t.Errorf("main pool MaxConnLifetimeJitter = %s with the key unset, want 0", pc.MaxConnLifetimeJitter)
	}
}

// statement_timeout bounds API statements on the main pool. It is set with
// SET once the connection is up, not sent as a startup parameter: PgBouncer
// refuses a startup parameter it does not know with "unsupported startup
// parameter", so a startup parameter would stop the server from connecting
// through it at all. The integration test checks the value Postgres reports.
func TestMainPoolAppliesStatementTimeout(t *testing.T) {
	pc, err := mainPoolConfig(config.DatabaseSection{URL: tuningTestURL, StatementTimeoutMS: 30000})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := pc.ConnConfig.RuntimeParams["statement_timeout"]; ok {
		t.Errorf("main pool sends statement_timeout %q as a startup parameter", got)
	}
	if pc.AfterConnect == nil {
		t.Error("main pool has no AfterConnect hook to set statement_timeout")
	}
}

// The timeout never reaches the leader election pool (its advisory lock
// session must not be cut), the health probes, or the scheduler's own pool.
func TestStatementTimeoutStaysOffTheOtherPools(t *testing.T) {
	cfg := config.DatabaseSection{URL: tuningTestURL, StatementTimeoutMS: 30000, SchedulerMaxConns: 4}
	leader, err := leaderPoolConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	health, err := smallPoolConfig(cfg, healthPoolConns)
	if err != nil {
		t.Fatal(err)
	}
	sched, err := schedulerPoolConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for name, params := range map[string]map[string]string{
		"leader": leader.ConnConfig.RuntimeParams, "health": health.ConnConfig.RuntimeParams, "scheduler": sched.ConnConfig.RuntimeParams,
	} {
		if v, ok := params["statement_timeout"]; ok {
			t.Errorf("%s pool sets statement_timeout %q; only the main pool may", name, v)
		}
	}
	for name, pc := range map[string]*pgxpool.Config{"leader": leader, "health": health, "scheduler": sched} {
		if pc.AfterConnect != nil {
			t.Errorf("%s pool runs an AfterConnect hook; only the main pool sets statement_timeout", name)
		}
	}
}

// Lifetime jitter spreads reconnects of the main, scheduler and health pools;
// the leader pool keeps its one session for good.
func TestConnLifetimeJitter(t *testing.T) {
	cfg := config.DatabaseSection{URL: tuningTestURL, ConnMaxLifetimeJitterMS: 60000, SchedulerMaxConns: 4}
	main, err := mainPoolConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sched, err := schedulerPoolConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	leader, err := leaderPoolConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if main.MaxConnLifetimeJitter != time.Minute || sched.MaxConnLifetimeJitter != time.Minute {
		t.Errorf("jitter main %s, scheduler %s; want 1m both", main.MaxConnLifetimeJitter, sched.MaxConnLifetimeJitter)
	}
	if leader.MaxConnLifetimeJitter != 0 || leader.MaxConnLifetime != leaderConnLifetime {
		t.Errorf("leader pool lifetime %s jitter %s; want %s and no jitter", leader.MaxConnLifetime, leader.MaxConnLifetimeJitter, leaderConnLifetime)
	}
}

// The scheduler pool is sized by its own key and keeps no idle floor, like
// the other small pools.
func TestSchedulerPoolConfigSize(t *testing.T) {
	pc, err := schedulerPoolConfig(config.DatabaseSection{URL: tuningTestURL, MaxOpenConns: 25, MaxIdleConns: 5, SchedulerMaxConns: 4})
	if err != nil {
		t.Fatal(err)
	}
	if pc.MaxConns != 4 || pc.MinConns != 0 {
		t.Errorf("scheduler pool MaxConns %d MinConns %d, want 4 and 0", pc.MaxConns, pc.MinConns)
	}
}
