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

// TestWarmWorkersAbsent: the register response reports no warm worker where
// none runs although warm pools are on: the subprocess executor starts no warm
// pool reconciler, and neither does a server whose pod dispatch is disabled
// (no Kubernetes client). The pod path with dispatch on runs them.
func TestWarmWorkersAbsent(t *testing.T) {
	cases := []struct {
		name         string
		warm         bool
		executorType string
		podDispatch  bool
		want         string
	}{
		{"warm pools off", false, "subprocess", true, ""},
		{"kubernetes, dispatch on", true, "kubernetes", true, ""},
		{"kubernetes, dispatch disabled", true, "kubernetes", false, "pod dispatch is disabled on this server"},
		{"subprocess", true, "subprocess", true, "the subprocess executor runs no warm workers"},
	}
	for _, c := range cases {
		cfg := &config.ServerConfig{}
		cfg.Execution.WarmPoolsEnabled = c.warm
		cfg.Executor.Type = c.executorType
		if got := warmWorkersAbsent(cfg, c.podDispatch); got != c.want {
			t.Errorf("%s: warmWorkersAbsent = %q, want %q", c.name, got, c.want)
		}
	}
}
