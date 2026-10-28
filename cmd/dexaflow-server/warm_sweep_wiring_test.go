package main

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/config"
)

// TestRequireWarmSweepFollowsConfig locks the #1678 wiring: with
// execution.warm_read_only_root_filesystem off the registry ignores the sweep;
// on, it warns about a worker that does not confirm it, and refuses one only
// when execution.warm_require_attempt_home_sweep is on too.
func TestRequireWarmSweepFollowsConfig(t *testing.T) {
	cases := []struct {
		readOnlyRoot, require bool
		want                  agentrpc.SweepPolicy
	}{
		{false, false, agentrpc.SweepIgnore},
		{false, true, agentrpc.SweepIgnore},
		{true, false, agentrpc.SweepWarn},
		{true, true, agentrpc.SweepRefuse},
	}
	for _, c := range cases {
		cfg := &config.ServerConfig{}
		cfg.Execution.WarmReadOnlyRootFilesystem = c.readOnlyRoot
		cfg.Execution.WarmRequireAttemptHomeSweep = c.require
		reg := agentrpc.NewWorkerRegistry(nil)
		requireWarmSweep(cfg, reg, nil)
		if got := reg.SweepPolicy(); got != c.want {
			t.Errorf("read_only_root=%v require=%v: sweep policy = %v, want %v", c.readOnlyRoot, c.require, got, c.want)
		}
	}
}
