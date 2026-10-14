package main

import (
	"reflect"
	"testing"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/dispatch"
)

// TestWarmPolicyIsTheDispatchers: the warm policy the API reports execution
// modes with (warmPolicy, #1707) is the one the dispatcher of the same
// configuration places with, for the pod path and the subprocess path, with
// warm pools off and on, and with every setting the decision reads.
func TestWarmPolicyIsTheDispatchers(t *testing.T) {
	configs := map[string]func(*config.ServerConfig){
		"defaults": func(*config.ServerConfig) {},
		"warm pools on": func(c *config.ServerConfig) {
			c.Execution.WarmPoolsEnabled = true
		},
		"everything set": func(c *config.ServerConfig) {
			c.Execution.WarmPoolsEnabled = true
			c.Executor.TaskServiceAccount = "dexaflow-task"
			c.Executor.Unit = config.ExecutorUnitSection{CPU: "250m", Memory: "512Mi"}
			c.Executor.Defaults.LimitsCPU, c.Executor.Defaults.LimitsMemory = "2", "4Gi"
			c.Execution.SourceMode = config.SourceModeSection{Enabled: true, Image: "rt@sha256:" + sixtyFourHex}
		},
	}
	for name, set := range configs {
		for _, executorType := range []string{"kubernetes", "subprocess"} {
			cfg := &config.ServerConfig{}
			cfg.Executor.Type = executorType
			set(cfg)
			// The registry exists exactly when warm pools are on (run()).
			var reg *agentrpc.WorkerRegistry
			if cfg.Execution.WarmPoolsEnabled {
				reg = agentrpc.NewWorkerRegistry(nil)
			}
			d := dispatch.NewDispatcher(nil, nil, nil, "", 0)
			if executorType == "subprocess" {
				configureSubprocessDispatcher(d, cfg, reg)
			} else {
				configureK8sDispatcher(d, cfg, reg, nil)
			}
			if got, want := warmPolicy(cfg), d.WarmPolicy(); !reflect.DeepEqual(got, want) {
				t.Errorf("%s, %s: API warm policy = %+v, dispatcher's = %+v", name, executorType, got, want)
			}
		}
	}
}

const sixtyFourHex = "0000000000000000000000000000000000000000000000000000000000000000"
