package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// withoutKubernetesClient makes the real client loader find neither an
// in-cluster config nor a kubeconfig: no service host in the environment, a
// KUBECONFIG naming a file that does not exist, and a HOME with no ~/.kube.
func withoutKubernetesClient(t *testing.T) {
	t.Helper()
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing-kubeconfig"))
	t.Setenv("HOME", t.TempDir())
}

// TestSetupDispatch_KubernetesFailsClosedWhenClientUnavailable locks the #704
// fix: on the Kubernetes executor, a Kubernetes client that cannot be built
// must fail boot, not warn and leave pod dispatch unwired while the scheduler
// reports itself healthy and fails every task as undispatchable.
func TestSetupDispatch_KubernetesFailsClosedWhenClientUnavailable(t *testing.T) {
	withoutKubernetesClient(t)
	cfg := &config.ServerConfig{}
	cfg.Executor.Type = "kubernetes"
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	on, closer, err := setupDispatch(context.Background(), cfg, nil, nil, nil, nil, nil, nil, logger, nil)
	if err == nil {
		t.Fatal("setupDispatch returned nil error on the Kubernetes executor with no Kubernetes client available; must fail closed (#704)")
	}
	if on {
		t.Error("setupDispatch reported pod dispatch enabled alongside the error; a failed build must wire nothing")
	}
	if closer != nil {
		t.Errorf("setupDispatch returned a dispatch closer (%v) alongside the error; a failed build must wire nothing", closer)
	}
}
