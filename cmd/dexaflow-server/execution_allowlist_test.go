package main

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
)

// executor.defaults.execution_allowlist reaches the dispatcher (issue #629):
// the default allows everything (no policy), a restriction becomes one.
func TestPlatformDefaultsCarryTheExecutionAllowlist(t *testing.T) {
	if pd := platformDefaults(config.PlatformDefaultsSection{
		ExecutionAllowlist: config.ExecutionAllowlistSection{Fields: []string{"*"}},
	}); pd.ExecutionPolicy != nil {
		t.Errorf("default allowlist built a policy: %+v", pd.ExecutionPolicy)
	}
	pd := platformDefaults(config.PlatformDefaultsSection{
		ExecutionAllowlist: config.ExecutionAllowlistSection{Fields: []string{"tolerations"}},
	})
	if pd.ExecutionPolicy == nil {
		t.Fatal("restricted allowlist built no policy")
	}
	task := domain.TaskSpec{TaskID: "t", Execution: &domain.Execution{Affinity: map[string]any{"a": 1}}}
	if err := pd.ExecutionPolicy.CheckTask(task); err == nil {
		t.Error("policy allowed affinity outside the allowlist")
	}
}
