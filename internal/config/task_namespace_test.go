package config

import "testing"

// TestExecutorTaskNamespace pins the fix for #480: the namespace the server
// creates task pods + staging PVCs in must be CONFIGURABLE (and bind from the
// LEOFLOW_EXECUTOR_TASK_NAMESPACE env the chart sets from .Values.taskNamespace),
// not a hardcoded constant that silently disagrees with the RBAC the chart grants.
func TestExecutorTaskNamespace(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	// "dexaflow" since 0.6.0 (#2155); it was "leoflow" before.
	if c.Executor.TaskNamespace != "dexaflow" {
		t.Errorf("default executor.task_namespace = %q, want %q", c.Executor.TaskNamespace, "dexaflow")
	}
	t.Setenv("LEOFLOW_EXECUTOR_TASK_NAMESPACE", "data-platform")
	c, err = LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.TaskNamespace != "data-platform" {
		t.Errorf("executor.task_namespace from env = %q, want data-platform", c.Executor.TaskNamespace)
	}
}
