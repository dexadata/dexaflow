package main

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestExecutorPolicyFromConfig: no policy is a nil policy (byte-identical
// pods), a valid one parses, and an invalid one fails startup instead of
// silently enforcing nothing (ADR 0063).
func TestExecutorPolicyFromConfig(t *testing.T) {
	cfg := &config.ServerConfig{}
	if p, err := executorPolicy(cfg); err != nil || !p.IsZero() {
		t.Errorf("no policy: %+v, %v; want a zero policy", p, err)
	}
	cfg.Executor.Policy = []byte("runtime_class_name: gvisor\n")
	if p, err := executorPolicy(cfg); err != nil || p.RuntimeClassName != "gvisor" {
		t.Errorf("valid policy: %+v, %v", p, err)
	}
	cfg.Executor.Policy = []byte("runtime_class: gvisor\n")
	if _, err := executorPolicy(cfg); err == nil {
		t.Error("an unknown policy key must fail")
	}
}

func TestValidateStartupRejectsAnInvalidExecutorPolicy(t *testing.T) {
	cfg, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Auth.JWT.Secret = "set"
	if err := validateStartup(cfg); err != nil {
		t.Fatalf("baseline config must validate: %v", err)
	}
	cfg.Executor.Policy = []byte("resources:\n  max:\n    memory: lots\n")
	if err := validateStartup(cfg); err == nil {
		t.Error("validateStartup accepted an invalid executor.policy")
	}
}

// TestValidateStartupRefusesPolicyWithWarmPools: warm pods are built outside
// dispatch and are not subject to the policy yet, so the two cannot be on
// together.
func TestValidateStartupRefusesPolicyWithWarmPools(t *testing.T) {
	cfg, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Auth.JWT.Secret = "set"
	cfg.Execution.WarmPoolsEnabled = true
	cfg.Auth.AgentTokenTransport = "exchange"
	cfg.Auth.SecretLivenessMode = "enforce"
	if err := validateStartup(cfg); err != nil {
		t.Fatalf("warm pools without a policy must validate: %v", err)
	}
	cfg.Executor.Policy = []byte("images:\n  allowed: [registry.example.com/]\n")
	if err := validateStartup(cfg); err == nil || !strings.Contains(err.Error(), "warm_pools_enabled") {
		t.Errorf("validateStartup = %v, want a refusal naming warm_pools_enabled", err)
	}
}
