package main

import (
	"errors"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"k8s.io/client-go/kubernetes"
)

// failingK8sClient stands in for buildK8sClient when neither in-cluster config
// nor a kubeconfig is available (the #700 reproduction: exchange selected, no
// client reachable).
func failingK8sClient() (kubernetes.Interface, error) {
	return nil, errors.New("no in-cluster config or kubeconfig")
}

// TestBuildTokenExchange_FailsClosedWhenClientUnavailable locks the #700 fix:
// with the exchange transport explicitly selected on a Kubernetes executor, a
// Kubernetes client that cannot be built must FAIL BOOT — not warn-and-continue
// with the exchange unwired, which turns every task pod's bootstrap into an
// Unimplemented ExchangeToken while /readyz stays green.
func TestBuildTokenExchange_FailsClosedWhenClientUnavailable(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Auth.AgentTokenTransport = config.AgentTokenTransportExchange
	cfg.Executor.Type = "kubernetes"

	xchg, err := buildTokenExchange(cfg, failingK8sClient)
	if err == nil {
		t.Fatal("buildTokenExchange returned nil error when the exchange transport is selected but no Kubernetes client is available; must fail closed (#700)")
	}
	if xchg != nil {
		t.Errorf("buildTokenExchange returned a non-nil exchange (%v) alongside the error; a failed build must wire nothing", xchg)
	}
}

// TestBuildTokenExchange_EnvVarTransportNeedsNoClient locks the preserved
// early-return: the default env-var transport needs no Kubernetes client, so a
// failing builder must never be consulted and boot must proceed with a nil
// exchange and no error.
func TestBuildTokenExchange_EnvVarTransportNeedsNoClient(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Auth.AgentTokenTransport = config.AgentTokenTransportEnvVar
	cfg.Executor.Type = "kubernetes"

	xchg, err := buildTokenExchange(cfg, failingK8sClient)
	if err != nil {
		t.Fatalf("buildTokenExchange returned error %v for the env-var transport; that transport needs no Kubernetes client", err)
	}
	if xchg != nil {
		t.Errorf("buildTokenExchange wired an exchange (%v) for the env-var transport; want nil", xchg)
	}
}

// TestBuildTokenExchange_NonKubernetesExecutorNeedsNoClient locks the other
// half of the early-return: even with the exchange transport selected, a
// non-Kubernetes executor (e.g. subprocess) has no pod/SA/TokenReview, so no
// client is needed and boot proceeds with a nil exchange and no error.
func TestBuildTokenExchange_NonKubernetesExecutorNeedsNoClient(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Auth.AgentTokenTransport = config.AgentTokenTransportExchange
	cfg.Executor.Type = "subprocess"

	xchg, err := buildTokenExchange(cfg, failingK8sClient)
	if err != nil {
		t.Fatalf("buildTokenExchange returned error %v for a non-Kubernetes executor; no client is needed there", err)
	}
	if xchg != nil {
		t.Errorf("buildTokenExchange wired an exchange (%v) for a non-Kubernetes executor; want nil", xchg)
	}
}
