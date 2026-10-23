package config

import (
	"testing"
	"time"
)

// TestLoadServerProbeBudget pins #1050: server.probe_budget defaults to the 2s
// /readyz always used, binds from both prefixes (the chart sets
// LEOFLOW_SERVER_PROBE_BUDGET), and a negative value fails boot.
func TestLoadServerProbeBudget(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil || c.Server.ProbeBudget != 2*time.Second {
		t.Fatalf("default probe budget = %s (%v), want 2s", c.Server.ProbeBudget, err)
	}
	t.Setenv("LEOFLOW_SERVER_PROBE_BUDGET", "9s")
	if c, err = LoadServer("", nil); err != nil || c.Server.ProbeBudget != 9*time.Second {
		t.Fatalf("LEOFLOW_ probe budget = %s (%v), want 9s", c.Server.ProbeBudget, err)
	}
	t.Setenv("DEXAFLOW_SERVER_PROBE_BUDGET", "4s")
	if c, err = LoadServer("", nil); err != nil || c.Server.ProbeBudget != 4*time.Second {
		t.Fatalf("DEXAFLOW_ probe budget = %s (%v), want 4s (new prefix wins)", c.Server.ProbeBudget, err)
	}
	c.Auth.JWT.Secret = "set"
	c.Server.ProbeBudget = -time.Second
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil with server.probe_budget -1s, want error")
	}
}
