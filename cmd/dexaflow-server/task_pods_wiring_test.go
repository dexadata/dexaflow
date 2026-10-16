package main

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestTaskPodSettings: the register dry run reports task pods as the
// dispatcher starts them (#1889): only when pod dispatch is on, with
// executor.defaults.run_tasks_as_non_root and executor.task_service_account.
func TestTaskPodSettings(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Executor.Type = "kubernetes"
	cfg.Executor.TaskServiceAccount = "dexaflow-task"
	cfg.Executor.Defaults.RunTasksAsNonRoot = true

	got := taskPodSettings(cfg, true)
	if !got.Enabled || !got.RunAsNonRoot || got.DefaultServiceAccount != "dexaflow-task" {
		t.Errorf("taskPodSettings = %+v, want enabled, non-root, default dexaflow-task", got)
	}
	if got.ServiceAccounts == nil && got.Unread == "" {
		t.Errorf("taskPodSettings reads no ServiceAccount and says not why: %+v", got)
	}

	cfg.Executor.Defaults.RunTasksAsNonRoot = false
	if got := taskPodSettings(cfg, true); got.RunAsNonRoot {
		t.Errorf("run_tasks_as_non_root false: RunAsNonRoot = true")
	}
	if got := taskPodSettings(cfg, false); got.Enabled {
		t.Errorf("pod dispatch off: Enabled = true")
	}
}
