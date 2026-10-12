package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
)

// The execution allowlist (issue #629) defaults to allowing every field, so an
// existing install sees no change.
func TestExecutionAllowlistDefaultsToAllowAll(t *testing.T) {
	c, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	al := c.Executor.Defaults.ExecutionAllowlist
	if !slices.Equal(al.Fields, []string{"*"}) {
		t.Errorf("fields = %v, want [*]", al.Fields)
	}
	if len(al.PriorityClasses) != 0 {
		t.Errorf("priority_classes = %v, want empty", al.PriorityClasses)
	}
}

// Both env prefixes bind, each list from one comma-separated variable.
func TestExecutionAllowlistBindsFromEnv(t *testing.T) {
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_FIELDS", "node_selector,tolerations")
			t.Setenv(prefix+"EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_PRIORITY_CLASSES", "etl-low")
			c, err := config.LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			al := c.Executor.Defaults.ExecutionAllowlist
			if !slices.Equal(al.Fields, []string{"node_selector", "tolerations"}) {
				t.Errorf("fields = %v", al.Fields)
			}
			if !slices.Equal(al.PriorityClasses, []string{"etl-low"}) {
				t.Errorf("priority_classes = %v", al.PriorityClasses)
			}
		})
	}
}

func TestExecutionAllowlistBindsFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dexaflow.yaml")
	body := "executor:\n  defaults:\n    execution_allowlist:\n      fields: [affinity]\n      priority_classes: [a, b]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadServer(path, nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	al := c.Executor.Defaults.ExecutionAllowlist
	if !slices.Equal(al.Fields, []string{"affinity"}) || !slices.Equal(al.PriorityClasses, []string{"a", "b"}) {
		t.Errorf("allowlist = %+v", al)
	}
}

// The service account and image allowlists (issue #537 C2) default to empty
// (allow any) and bind from both env prefixes.
func TestExecutionAllowlistIdentityKeysBind(t *testing.T) {
	c, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	al := c.Executor.Defaults.ExecutionAllowlist
	if len(al.ServiceAccounts) != 0 || len(al.Images) != 0 {
		t.Errorf("defaults = %+v, want empty service_accounts and images", al)
	}
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_SERVICE_ACCOUNTS", "etl,reports")
			t.Setenv(prefix+"EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_IMAGES", "registry.example.com/team-a/*")
			c, err := config.LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			al := c.Executor.Defaults.ExecutionAllowlist
			if !slices.Equal(al.ServiceAccounts, []string{"etl", "reports"}) {
				t.Errorf("service_accounts = %v", al.ServiceAccounts)
			}
			if !slices.Equal(al.Images, []string{"registry.example.com/team-a/*"}) {
				t.Errorf("images = %v", al.Images)
			}
		})
	}
}

// A typo in the allowlist fails boot instead of silently allowing or
// refusing the wrong field.
func TestExecutionAllowlistValidatesAtBoot(t *testing.T) {
	t.Setenv("DEXAFLOW_EXECUTOR_DEFAULTS_EXECUTION_ALLOWLIST_FIELDS", "nodeSelector")
	c, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "execution_allowlist") {
		t.Fatalf("Validate = %v, want an execution_allowlist error", err)
	}
}

// An explicitly empty fields list in the config file allows no field, the same
// as "none" and as an empty list in Helm. Before, it parsed as "allow all", so
// an operator locking placement down got the opposite with no warning.
func TestExecutionAllowlistEmptyListInFileAllowsNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dexaflow.yaml")
	body := "executor:\n  defaults:\n    execution_allowlist:\n      fields: []\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := config.LoadServer(path, nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if al := c.Executor.Defaults.ExecutionAllowlist.Fields; al == nil || len(al) != 0 {
		t.Fatalf("fields = %#v, want an explicit empty list", al)
	}
	p, err := domain.ParseExecutionPolicy(c.Executor.Defaults.ExecutionAllowlist.ExecutionPolicyConfig())
	if err != nil {
		t.Fatalf("ParseExecutionPolicy: %v", err)
	}
	if p == nil {
		t.Fatal("policy is nil (allow all), want one that allows no field")
	}
	task := domain.TaskSpec{TaskID: "t", Execution: &domain.Execution{NodeSelector: map[string]string{"pool": "a"}}}
	if err := p.CheckTask(task); err == nil {
		t.Fatal("CheckTask = nil, want node_selector refused")
	}
}
