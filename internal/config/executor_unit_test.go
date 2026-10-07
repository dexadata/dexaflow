package config

import (
	"strings"
	"testing"
)

// executor.unit (ADR 0066 §3): an optional resource unit. Unset is the default
// and changes nothing; set, both quantities must parse and be positive, and it
// cannot be combined with warm pools, whose pods are sized once for every task.

func TestExecutorUnitDefaultsToUnset(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Executor.Unit.CPU != "" || c.Executor.Unit.Memory != "" {
		t.Errorf("executor.unit default = %+v, want unset", c.Executor.Unit)
	}
}

func TestExecutorUnitEnvBinds(t *testing.T) {
	t.Setenv("LEOFLOW_EXECUTOR_UNIT_CPU", "250m")
	t.Setenv("LEOFLOW_EXECUTOR_UNIT_MEMORY", "512Mi")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Executor.Unit.CPU != "250m" || c.Executor.Unit.Memory != "512Mi" {
		t.Errorf("executor.unit = %+v, want 250m / 512Mi from env", c.Executor.Unit)
	}
}

func TestValidateExecutorUnit(t *testing.T) {
	cases := map[string]struct {
		cpu, memory string
		warm        bool
		wantErr     string
	}{
		"unset":                {"", "", false, ""},
		"valid":                {"250m", "512Mi", false, ""},
		"cpu only":             {"250m", "", false, "executor.unit"},
		"bad memory":           {"250m", "512MB", false, "executor.unit"},
		"with warm pools":      {"250m", "512Mi", true, "warm_pools_enabled"},
		"unset and warm pools": {"", "", true, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := validWarmConfig()
			c.Execution.WarmPoolsEnabled = tc.warm
			c.Executor.Unit.CPU, c.Executor.Unit.Memory = tc.cpu, tc.memory
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("Validate() = %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}
