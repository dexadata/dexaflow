package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scheduler.dispatch.tick_budget bounds how long one tick dispatches, on by
// default (#1596); max_per_tenant_per_tick is off by default.
func TestDispatchBudgetDefaults(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if got := c.Scheduler.Dispatch.TickBudget; got != 5*time.Second {
		t.Errorf("tick_budget = %v, want 5s", got)
	}
	if got := c.Scheduler.Dispatch.MaxPerTenantPerTick; got != 0 {
		t.Errorf("max_per_tenant_per_tick = %d, want 0 (no cap)", got)
	}
}

func TestDispatchBudgetEnvBindsUnderBothPrefixes(t *testing.T) {
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"SCHEDULER_DISPATCH_TICK_BUDGET", "2s")
			t.Setenv(prefix+"SCHEDULER_DISPATCH_MAX_PER_TENANT_PER_TICK", "50")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if c.Scheduler.Dispatch.TickBudget != 2*time.Second || c.Scheduler.Dispatch.MaxPerTenantPerTick != 50 {
				t.Errorf("got tick_budget %v, max_per_tenant_per_tick %d; want 2s, 50",
					c.Scheduler.Dispatch.TickBudget, c.Scheduler.Dispatch.MaxPerTenantPerTick)
			}
		})
	}
}

func TestDispatchBudgetFromConfigFile(t *testing.T) {
	for _, name := range []string{"dexaflow.yaml", "leoflow.yaml"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			body := "scheduler:\n  dispatch:\n    tick_budget: 0s\n    max_per_tenant_per_tick: 7\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := LoadServer(path, nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if c.Scheduler.Dispatch.TickBudget != 0 || c.Scheduler.Dispatch.MaxPerTenantPerTick != 7 {
				t.Errorf("got tick_budget %v, max_per_tenant_per_tick %d; want 0s, 7",
					c.Scheduler.Dispatch.TickBudget, c.Scheduler.Dispatch.MaxPerTenantPerTick)
			}
		})
	}
}

func TestDispatchBudgetRejectsNegative(t *testing.T) {
	c := validWarmConfig()
	c.Scheduler.Dispatch.TickBudget = -time.Second
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "tick_budget") {
		t.Errorf("Validate() = %v, want an error naming scheduler.dispatch.tick_budget", err)
	}
	c = validWarmConfig()
	c.Scheduler.Dispatch.MaxPerTenantPerTick = -1
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "max_per_tenant_per_tick") {
		t.Errorf("Validate() = %v, want an error naming scheduler.dispatch.max_per_tenant_per_tick", err)
	}
}
