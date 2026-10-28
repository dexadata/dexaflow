package storage

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/dexadata/dexaflow/internal/config"
)

// lazyPool opens a pool that never dials: pgxpool connects on first acquire,
// so Stat reads the configured size and zero counters with no database.
func lazyPool(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	pc, err := pgxpool.ParseConfig("postgres://user@127.0.0.1:1/none")
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	pc.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(context.Background(), pc)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// scrape gathers c through a pedantic registry, which also checks that every
// series is consistent with its description, and returns each metric's value
// by pool label.
func scrape(t *testing.T, c prometheus.Collector) map[string]map[string]float64 {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(c)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]map[string]float64{}
	for _, mf := range mfs {
		byPool := map[string]float64{}
		for _, m := range mf.GetMetric() {
			byPool[poolLabel(m)] = metricValue(mf, m)
		}
		out[mf.GetName()] = byPool
	}
	return out
}

func poolLabel(m *dto.Metric) string {
	for _, l := range m.GetLabel() {
		if l.GetName() == "pool" {
			return l.GetValue()
		}
	}
	return ""
}

func metricValue(mf *dto.MetricFamily, m *dto.Metric) float64 {
	if mf.GetType() == dto.MetricType_COUNTER {
		return m.GetCounter().GetValue()
	}
	return m.GetGauge().GetValue()
}

var poolMetricNames = []string{
	"dexaflow_db_pool_acquired_conns",
	"dexaflow_db_pool_idle_conns",
	"dexaflow_db_pool_total_conns",
	"dexaflow_db_pool_max_conns",
	"dexaflow_db_pool_empty_acquires_total",
	"dexaflow_db_pool_canceled_acquires_total",
	"dexaflow_db_pool_empty_acquire_wait_seconds_total",
}

func TestPoolCollectorReportsEveryPoolByName(t *testing.T) {
	pg := &Postgres{Pool: lazyPool(t, 20), health: lazyPool(t, 2)}
	got := scrape(t, NewPoolCollector(pg))

	for _, name := range poolMetricNames {
		if len(got[name]) != 2 {
			t.Errorf("%s: series %v, want one for each of main and health", name, got[name])
		}
	}
	if want := map[string]float64{"main": 20, "health": 2}; !reflect.DeepEqual(got["dexaflow_db_pool_max_conns"], want) {
		t.Errorf("max conns = %v, want %v", got["dexaflow_db_pool_max_conns"], want)
	}
	if len(got) != len(poolMetricNames) {
		t.Errorf("families %d, want %d", len(got), len(poolMetricNames))
	}
}

func TestPoolCollectorFollowsTheSchedulerPool(t *testing.T) {
	pg := &Postgres{Pool: lazyPool(t, 20), health: lazyPool(t, 2)}
	c := NewPoolCollector(pg)

	// Unset, the scheduler shares the main pool and adds no series.
	sched, release, err := pg.ForScheduler(context.Background(), config.DatabaseSection{})
	if err != nil || sched != pg {
		t.Fatalf("unset scheduler_max_conns must share the main pool: %v", err)
	}
	release()
	if n := len(scrape(t, c)["dexaflow_db_pool_max_conns"]); n != 2 {
		t.Fatalf("a shared scheduler pool is the main pool, not a third series: got %d", n)
	}

	pg.trackSchedulerPool(lazyPool(t, 4))
	want := map[string]float64{"main": 20, "health": 2, "scheduler": 4}
	if got := scrape(t, c)["dexaflow_db_pool_max_conns"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("max conns = %v, want %v", got, want)
	}
	pg.trackSchedulerPool(nil)
	if n := len(scrape(t, c)["dexaflow_db_pool_max_conns"]); n != 2 {
		t.Fatalf("after the scheduler pool closed: %d series, want 2", n)
	}
}

func TestPoolCollectorWithoutHealthPool(t *testing.T) {
	pg := &Postgres{Pool: lazyPool(t, 5)}
	got := scrape(t, NewPoolCollector(pg))["dexaflow_db_pool_total_conns"]
	if _, ok := got["main"]; len(got) != 1 || !ok {
		t.Fatalf("got %v, want only the main pool", got)
	}
}
