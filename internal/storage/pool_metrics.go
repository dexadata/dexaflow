package storage

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Pool label values of the dexaflow_db_pool_* metrics. The leader pool is left
// out: it holds one session for the advisory lock and is never contended.
const (
	poolMain      = "main"
	poolHealth    = "health"
	poolScheduler = "scheduler"
)

// PoolCollector exports pgxpool statistics of the control plane's database
// pools, read at scrape time (#1049).
//
// The health pool (#1046) keeps readiness green while the request pool is fully
// lent out, which is the right readiness semantic and removes the last symptom
// of main-pool exhaustion. These series are the signal that replaces it:
// dexaflow_db_pool_empty_acquires_total counts callers that found no idle
// connection and had to wait, and dexaflow_db_pool_canceled_acquires_total the
// ones that gave up waiting.
type PoolCollector struct {
	pg *Postgres

	acquired, idle, total, maxConns *prometheus.Desc
	emptyAcquires, canceled, waited *prometheus.Desc
}

// NewPoolCollector returns a collector for pg's main and health pools, and for
// its dedicated scheduler pool while one is open (ForScheduler).
func NewPoolCollector(pg *Postgres) *PoolCollector {
	desc := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc("dexaflow_db_pool_"+name, help+", by pool (main, health, scheduler).", []string{"pool"}, nil)
	}
	return &PoolCollector{
		pg:            pg,
		acquired:      desc("acquired_conns", "Connections currently lent out by the database pool"),
		idle:          desc("idle_conns", "Idle connections in the database pool"),
		total:         desc("total_conns", "Open connections in the database pool, idle, lent out and being opened"),
		maxConns:      desc("max_conns", "Maximum connections the database pool may open"),
		emptyAcquires: desc("empty_acquires_total", "Acquires that found no idle connection and had to wait for one"),
		canceled:      desc("canceled_acquires_total", "Acquires that gave up waiting for a connection (their context ended)"),
		waited:        desc("empty_acquire_wait_seconds_total", "Time acquires spent waiting for a connection, in seconds"),
	}
}

// Describe implements prometheus.Collector.
func (c *PoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.acquired, c.idle, c.total, c.maxConns, c.emptyAcquires, c.canceled, c.waited} {
		ch <- d
	}
}

// Collect implements prometheus.Collector.
func (c *PoolCollector) Collect(ch chan<- prometheus.Metric) {
	c.collect(ch, poolMain, c.pg.Pool)
	c.collect(ch, poolHealth, c.pg.health)
	c.collect(ch, poolScheduler, c.pg.schedulerPool.Load())
}

func (c *PoolCollector) collect(ch chan<- prometheus.Metric, name string, pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	s := pool.Stat()
	gauge := func(d *prometheus.Desc, v int32) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, float64(v), name)
	}
	counter := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v, name)
	}
	gauge(c.acquired, s.AcquiredConns())
	gauge(c.idle, s.IdleConns())
	gauge(c.total, s.TotalConns())
	gauge(c.maxConns, s.MaxConns())
	counter(c.emptyAcquires, float64(s.EmptyAcquireCount()))
	counter(c.canceled, float64(s.CanceledAcquireCount()))
	counter(c.waited, s.EmptyAcquireWaitTime().Seconds())
}

// trackSchedulerPool records the dedicated scheduler pool for PoolCollector,
// or forgets it with nil once the pool is closed.
func (p *Postgres) trackSchedulerPool(pool *pgxpool.Pool) {
	p.schedulerPool.Store(pool)
}
