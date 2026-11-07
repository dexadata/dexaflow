package serverapp

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

func policyConfig(doc string) *config.ServerConfig {
	cfg := &config.ServerConfig{}
	cfg.Executor.Policy = []byte(doc)
	cfg.Executor.TaskServiceAccount = "dexaflow-task"
	return cfg
}

func TestForcePolicyNoneIsNil(t *testing.T) {
	p, err := forcePolicy(policyConfig(""))
	if err != nil || p != nil {
		t.Fatalf("forcePolicy = %v, %v; want nil, nil", p, err)
	}
}

func TestForcePolicyInvalidFailsBoot(t *testing.T) {
	if _, err := forcePolicy(policyConfig("force:\n  runtime_class: gvisor\n")); err == nil {
		t.Fatal("a misspelled rule must fail boot")
	}
}

func TestForcePolicyRefusesWarmPools(t *testing.T) {
	cfg := policyConfig("force:\n  runtime_class_name: gvisor\n")
	cfg.Execution.WarmPoolsEnabled = true
	_, err := forcePolicy(cfg)
	if err == nil || !strings.Contains(err.Error(), "execution.warm_pools_enabled") {
		t.Fatalf("err = %v; warm pods would run outside the policy", err)
	}
}

func TestForcePolicyServiceAccountMustBeAllowed(t *testing.T) {
	cfg := policyConfig("force:\n  service_account: sandboxed\n")
	cfg.Executor.Defaults.ExecutionAllowlist.ServiceAccounts = []string{"etl"}
	if _, err := forcePolicy(cfg); err == nil || !strings.Contains(err.Error(), "execution_allowlist.service_accounts") {
		t.Fatalf("err = %v; a forced account outside the allowlist would refuse every task", err)
	}
	cfg.Executor.Defaults.ExecutionAllowlist.ServiceAccounts = []string{"etl", "sandboxed"}
	if _, err := forcePolicy(cfg); err != nil {
		t.Fatalf("an allowed account: %v", err)
	}
	// The default task account is always allowed, as in the executor.
	cfg = policyConfig("force:\n  service_account: dexaflow-task\n")
	cfg.Executor.Defaults.ExecutionAllowlist.ServiceAccounts = []string{"etl"}
	if _, err := forcePolicy(cfg); err != nil {
		t.Fatalf("the default task account: %v", err)
	}
}
