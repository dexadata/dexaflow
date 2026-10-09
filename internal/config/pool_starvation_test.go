package config

import (
	"strings"
	"testing"
	"time"
)

// scheduler.pool_starvation_threshold (ADR 0066 §4): how long a task held only
// by its pool waits before the pool is reserved for it. 60s by default; 0
// disables reservations; negative fails boot.

func TestPoolStarvationThresholdDefault(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Scheduler.PoolStarvationThreshold != time.Minute {
		t.Errorf("default = %v, want 1m", c.Scheduler.PoolStarvationThreshold)
	}
}

func TestPoolStarvationThresholdEnvBinds(t *testing.T) {
	t.Setenv("DEXAFLOW_SCHEDULER_POOL_STARVATION_THRESHOLD", "0s")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Scheduler.PoolStarvationThreshold != 0 {
		t.Errorf("threshold = %v, want 0 from env", c.Scheduler.PoolStarvationThreshold)
	}
}

func TestPoolStarvationThresholdRejectsNegative(t *testing.T) {
	c := validWarmConfig()
	c.Scheduler.PoolStarvationThreshold = -time.Second
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "pool_starvation_threshold") {
		t.Errorf("Validate() = %v, want an error naming scheduler.pool_starvation_threshold", err)
	}
}
