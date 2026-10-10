package config_test

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// The task-pod hardening defaults are cluster-operator policy, so they must be
// reachable the way an operator actually configures the control plane: an env
// var on the deployment. viper's AutomaticEnv only binds keys it has already
// seen via SetDefault, so a new field added to the struct without a matching
// entry in serverDefaults is silently unreachable — the struct compiles, the
// env var is ignored, and the escape hatch does not exist. This pins both keys
// against that.
func TestPodSecurityDefaultsBindFromEnv(t *testing.T) {
	// Each key is flipped away from its own default so a value that binds proves
	// the env var reached the field rather than merely matching the default:
	// run_tasks_as_non_root defaults on, so drive it off; read_only defaults off,
	// so drive it on.
	t.Setenv("LEOFLOW_EXECUTOR_DEFAULTS_RUN_TASKS_AS_NON_ROOT", "false")
	t.Setenv("LEOFLOW_EXECUTOR_DEFAULTS_READ_ONLY_TASK_ROOT_FILESYSTEM", "true")
	c, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Executor.Defaults.RunTasksAsNonRoot {
		t.Error("LEOFLOW_EXECUTOR_DEFAULTS_RUN_TASKS_AS_NON_ROOT did not bind: the non-root opt-out would be unreachable")
	}
	if !c.Executor.Defaults.ReadOnlyTaskRootFilesystem {
		t.Error("LEOFLOW_EXECUTOR_DEFAULTS_READ_ONLY_TASK_ROOT_FILESYSTEM did not bind")
	}
}

// The secure posture must be what an operator gets without configuring anything:
// runAsNonRoot on (the shipped images carry numeric non-root UIDs), and
// readOnlyRootFilesystem off (restricted does not require it and it breaks
// ordinary Python tasks).
func TestPodSecurityDefaultsAreSecureWhenUnset(t *testing.T) {
	c, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if !c.Executor.Defaults.RunTasksAsNonRoot {
		t.Error("RunTasksAsNonRoot must default to true now that the shipped images carry numeric non-root UIDs")
	}
	if c.Executor.Defaults.ReadOnlyTaskRootFilesystem {
		t.Error("ReadOnlyTaskRootFilesystem must default to false (restricted does not require it)")
	}
}
