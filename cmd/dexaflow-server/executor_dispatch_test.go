package main

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestExecutorDispatchEnabledForRole pins F1 (ADR 0049 pre-RC review): in the
// split api role the scheduler (which owns the executor) runs in another process,
// so the runtime podDispatch bool is always false — the /monitor/executor endpoint
// must instead report the CONFIGURED capability, or it tells an operator pod
// dispatch is off when the scheduler has it on. The all/scheduler role keeps the
// accurate runtime bool.
func TestExecutorDispatchEnabledForRole(t *testing.T) {
	k8s := &config.ServerConfig{}
	k8s.Executor.Type = "kubernetes"
	sub := &config.ServerConfig{}
	sub.Executor.Type = "subprocess"

	cases := []struct {
		name            string
		cfg             *config.ServerConfig
		servesScheduler bool
		runtime         bool
		want            bool
	}{
		{"all role uses runtime true", k8s, true, true, true},
		{"all role uses runtime false", k8s, true, false, false},
		{"api role reports configured kubernetes", k8s, false, false, true},
		{"api role reports subprocess as off", sub, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := executorDispatchEnabled(tc.cfg, tc.servesScheduler, tc.runtime); got != tc.want {
				t.Errorf("executorDispatchEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}
