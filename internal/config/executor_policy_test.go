package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadPolicyConfig(t *testing.T, name, body string) (*ServerConfig, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadServer(path, nil)
}

func TestExecutorPolicyIsReadVerbatimFromYAML(t *testing.T) {
	c, err := loadPolicyConfig(t, "server.yaml", `
pool_labels: &os
  kubernetes.io/os: linux
executor:
  policy:
    force:
      runtime_class_name: gvisor
      node_selector:
        <<: *os
        node.example.com/pool: sandbox
`)
	if err != nil {
		t.Fatal(err)
	}
	got := string(c.Executor.Policy)
	for _, want := range []string{"runtime_class_name: gvisor", "kubernetes.io/os: linux", "node.example.com/pool: sandbox"} {
		if !strings.Contains(got, want) {
			t.Errorf("policy %q lacks %q (dotted keys must survive, anchors resolve)", got, want)
		}
	}
}

func TestExecutorPolicyFromJSON(t *testing.T) {
	c, err := loadPolicyConfig(t, "server.json", `{"executor": {"policy": {"force": {"service_account": "sandboxed"}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(c.Executor.Policy), "service_account: sandboxed") {
		t.Fatalf("policy = %q", c.Executor.Policy)
	}
}

func TestExecutorPolicyAbsentNullOrEmptyIsNone(t *testing.T) {
	for name, body := range map[string]string{
		"absent": "executor:\n  task_namespace: tasks\n",
		"null":   "executor:\n  policy: null\n",
		"empty":  "executor:\n  policy: \"\"\n",
	} {
		c, err := loadPolicyConfig(t, "server.yaml", body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(c.Executor.Policy) != 0 {
			t.Errorf("%s: policy = %q, want none", name, c.Executor.Policy)
		}
	}
}

func TestExecutorPolicyInAnotherFormatFailsBoot(t *testing.T) {
	_, err := loadPolicyConfig(t, "server.toml", "[executor.policy.force]\nruntime_class_name = \"gvisor\"\n")
	if err == nil || !strings.Contains(err.Error(), "executor.policy is only read from a YAML or JSON config file") {
		t.Fatalf("err = %v; a policy that would go unenforced must fail boot", err)
	}
}
