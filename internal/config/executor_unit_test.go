package config

import (
	"strings"
	"testing"
)

// executor.unit (ADR 0066 §3): an optional resource unit. Unset is the default
// and changes nothing; set, both quantities must parse and be positive. It
// combines with warm pools, whose pods are then one unit.

func TestExecutorUnitDefaultsToUnset(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Executor.Unit.CPU != "" || c.Executor.Unit.Memory != "" {
		t.Errorf("executor.unit default = %+v, want unset", c.Executor.Unit)
	}
	if c.Executor.Unit.Enforce != "refuse" || c.Executor.Unit.MaxSize != 64 {
		t.Errorf("executor.unit enforce/max_size default = %q / %d, want refuse / 64", c.Executor.Unit.Enforce, c.Executor.Unit.MaxSize)
	}
}

func TestExecutorUnitEnvBinds(t *testing.T) {
	t.Setenv("DEXAFLOW_EXECUTOR_UNIT_CPU", "250m")
	t.Setenv("DEXAFLOW_EXECUTOR_UNIT_MEMORY", "512Mi")
	t.Setenv("DEXAFLOW_EXECUTOR_UNIT_ENFORCE", "warn")
	t.Setenv("DEXAFLOW_EXECUTOR_UNIT_MAX_SIZE", "16")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	want := ExecutorUnitSection{CPU: "250m", Memory: "512Mi", Enforce: "warn", MaxSize: 16}
	if c.Executor.Unit != want {
		t.Errorf("executor.unit = %+v, want %+v from env", c.Executor.Unit, want)
	}
}

func TestValidateExecutorUnit(t *testing.T) {
	cases := map[string]struct {
		unit    ExecutorUnitSection
		warm    bool
		wantErr string
	}{
		"unset":                {ExecutorUnitSection{}, false, ""},
		"valid":                {ExecutorUnitSection{CPU: "250m", Memory: "512Mi"}, false, ""},
		"valid warn":           {ExecutorUnitSection{CPU: "250m", Memory: "512Mi", Enforce: "warn", MaxSize: 8}, false, ""},
		"cpu only":             {ExecutorUnitSection{CPU: "250m"}, false, "executor.unit"},
		"bad memory":           {ExecutorUnitSection{CPU: "250m", Memory: "512MB"}, false, "executor.unit"},
		"bad enforce":          {ExecutorUnitSection{CPU: "250m", Memory: "512Mi", Enforce: "audit"}, false, "executor.unit.enforce"},
		"negative max_size":    {ExecutorUnitSection{CPU: "250m", Memory: "512Mi", MaxSize: -2}, false, "executor.unit.max_size"},
		"with warm pools":      {ExecutorUnitSection{CPU: "250m", Memory: "512Mi"}, true, ""},
		"unset and warm pools": {ExecutorUnitSection{}, true, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := validWarmConfig()
			c.Execution.WarmPoolsEnabled = tc.warm
			c.Executor.Unit = tc.unit
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
