package main

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/config"
)

// fakeClient returns a client builder that counts its calls and gives a
// fake clientset, or err.
func fakeClient(calls *int, err error) func() (kubernetes.Interface, error) {
	return func() (kubernetes.Interface, error) {
		*calls++
		if err != nil {
			return nil, err
		}
		return fake.NewClientset(), nil
	}
}

// TestTaskPodSettings: the register dry run reports task pods as the
// dispatcher starts them (#1889): only when pod dispatch is on, with
// executor.defaults.run_tasks_as_non_root and executor.task_service_account,
// and with a ServiceAccount reader on a process that holds the executor's
// Role (the all role, ADR 0085).
func TestTaskPodSettings(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Executor.Type = "kubernetes"
	cfg.Executor.TaskServiceAccount = "dexaflow-task"
	cfg.Executor.Defaults.RunTasksAsNonRoot = true
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	calls := 0
	got := taskPodSettings(cfg, true, fakeClient(&calls, nil), logger)
	if !got.Enabled || !got.RunAsNonRoot || got.DefaultServiceAccount != "dexaflow-task" {
		t.Errorf("taskPodSettings = %+v, want enabled, non-root, default dexaflow-task", got)
	}
	if got.ServiceAccounts == nil || got.Unread != "" || calls != 1 {
		t.Errorf("all role: reader %v, unread %q, %d clients; want a reader from one client", got.ServiceAccounts, got.Unread, calls)
	}

	cfg.Executor.Defaults.RunTasksAsNonRoot = false
	if s := taskPodSettings(cfg, true, fakeClient(&calls, nil), logger); s.RunAsNonRoot {
		t.Errorf("run_tasks_as_non_root false: RunAsNonRoot = true")
	}

	calls = 0
	if s := taskPodSettings(cfg, false, fakeClient(&calls, nil), logger); s.Enabled || s.ServiceAccounts != nil || calls != 0 {
		t.Errorf("pod dispatch off: %+v, %d clients; want nothing enabled and no client", s, calls)
	}

	got = taskPodSettings(cfg, true, fakeClient(&calls, errors.New("no kubeconfig")), logger)
	if got.ServiceAccounts != nil || !strings.Contains(got.Unread, "no Kubernetes client") {
		t.Errorf("client error: reader %v, unread %q", got.ServiceAccounts, got.Unread)
	}

	split := &config.ServerConfig{}
	split.Executor.Type = "kubernetes"
	split.Server.Role = config.RoleAPI
	calls = 0
	got = taskPodSettings(split, true, fakeClient(&calls, nil), logger)
	if got.ServiceAccounts != nil || calls != 0 || !strings.Contains(got.Unread, "ADR 0049") {
		t.Errorf("split api role: reader %v, unread %q, %d clients; want no client and the reason", got.ServiceAccounts, got.Unread, calls)
	}
}
