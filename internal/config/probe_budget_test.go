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

// TestLivenessSchedulerStallBounds is #539: off by default, and refused when
// negative or shorter than twice the scheduler step timeout.
func TestLivenessSchedulerStallBounds(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil || c.Server.LivenessSchedulerStall != 0 {
		t.Fatalf("default server.liveness_scheduler_stall = %v (err %v), want 0", c.Server.LivenessSchedulerStall, err)
	}
	tests := []struct {
		name     string
		stall    time.Duration
		interval int
		wantErr  bool
	}{
		{name: "off", stall: 0, interval: 1000},
		{name: "negative", stall: -time.Second, interval: 1000, wantErr: true},
		{name: "under twice the step timeout", stall: 59 * time.Second, interval: 1000, wantErr: true},
		{name: "twice the step timeout", stall: time.Minute, interval: 1000},
		{name: "a slow loop needs more", stall: 2 * time.Minute, interval: 5000, wantErr: true},
		{name: "a slow loop at twice its timeout", stall: 5 * time.Minute, interval: 5000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Server.LivenessSchedulerStall = tc.stall
			c.Scheduler.LoopIntervalMS = tc.interval
			if err := c.validateLivenessSchedulerStall(); (err != nil) != tc.wantErr {
				t.Errorf("validateLivenessSchedulerStall() = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
