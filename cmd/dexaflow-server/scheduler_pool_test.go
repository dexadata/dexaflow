package main

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// The dedicated scheduler pool is opened only by a process that runs the
// scheduler, as the docs say: with scheduler.enabled=false the janitors keep
// to the main pool and no extra connections are held.
func TestSchedulerPoolOnlyWhenTheSchedulerRuns(t *testing.T) {
	for _, tc := range []struct {
		enabled bool
		conns   int
		want    int
	}{
		{enabled: true, conns: 4, want: 4},
		{enabled: false, conns: 4, want: 0},
		{enabled: true, conns: 0, want: 0},
	} {
		cfg := &config.ServerConfig{}
		cfg.Scheduler.Enabled = tc.enabled
		cfg.Database.SchedulerMaxConns = tc.conns
		cfg.Database.StatementTimeoutMS = 30000
		got := schedulerDatabase(cfg)
		if got.SchedulerMaxConns != tc.want {
			t.Errorf("scheduler.enabled=%v scheduler_max_conns=%d: pool size %d, want %d", tc.enabled, tc.conns, got.SchedulerMaxConns, tc.want)
		}
		if got.StatementTimeoutMS != 30000 {
			t.Errorf("schedulerDatabase changed statement_timeout_ms to %d", got.StatementTimeoutMS)
		}
	}
}
