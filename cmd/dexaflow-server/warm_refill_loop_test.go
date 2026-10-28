package main

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestWarmPoolEventRefillDefaultAndEnv locks the gate: off by default (today's
// polling refill), reachable from both env prefixes.
func TestWarmPoolEventRefillDefaultAndEnv(t *testing.T) {
	c, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Execution.WarmPoolEventRefill {
		t.Error("execution.warm_pool_event_refill default = true, want false")
	}
	for _, name := range []string{"DEXAFLOW_EXECUTION_WARM_POOL_EVENT_REFILL", "LEOFLOW_EXECUTION_WARM_POOL_EVENT_REFILL"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "true")
			c, err := config.LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if !c.Execution.WarmPoolEventRefill {
				t.Errorf("%s=true did not bind", name)
			}
		})
	}
}
