package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeServerConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLoadServerReadsTheExecutorPolicyVerbatim: executor.policy carries keys
// with dots and slashes (node labels such as kubernetes.io/os) that viper's key
// delimiter would split, so it is kept as the raw YAML subtree for the
// executor to parse (ADR 0063).
func TestLoadServerReadsTheExecutorPolicyVerbatim(t *testing.T) {
	c, err := LoadServer(writeServerConfig(t, `executor:
  task_namespace: tasks
  policy:
    runtime_class_name: gvisor
    placement:
      node_selector:
        kubernetes.io/os: linux
`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Executor.TaskNamespace != "tasks" {
		t.Errorf("task_namespace = %q; the policy subtree must not disturb its siblings", c.Executor.TaskNamespace)
	}
	got := string(c.Executor.Policy)
	for _, want := range []string{"runtime_class_name: gvisor", "kubernetes.io/os: linux"} {
		if !strings.Contains(got, want) {
			t.Errorf("policy YAML %q is missing %q", got, want)
		}
	}
}

func TestLoadServerWithoutAPolicyHasNone(t *testing.T) {
	c, err := LoadServer(writeServerConfig(t, "executor:\n  task_namespace: tasks\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Executor.Policy) != 0 {
		t.Errorf("policy = %q, want none", c.Executor.Policy)
	}
	if c, err = LoadServer("", nil); err != nil || len(c.Executor.Policy) != 0 {
		t.Errorf("no config file: policy = %q, err = %v", c.Executor.Policy, err)
	}
}

// TestLoadServerResolvesAnchorsInThePolicy: an anchor defined outside the
// policy subtree, and a merge key, resolve instead of failing startup.
func TestLoadServerResolvesAnchorsInThePolicy(t *testing.T) {
	c, err := LoadServer(writeServerConfig(t, `pools: &sel
  kubernetes.io/os: linux
base: &base
  runtime_class_name: gvisor
executor:
  policy:
    <<: *base
    placement:
      node_selector: *sel
`), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := string(c.Executor.Policy)
	for _, want := range []string{"runtime_class_name: gvisor", "kubernetes.io/os: linux"} {
		if !strings.Contains(got, want) {
			t.Errorf("policy YAML %q is missing %q", got, want)
		}
	}
}

func TestLoadServerTreatsANullOrEmptyPolicyAsNone(t *testing.T) {
	for _, v := range []string{"null", `""`, "~"} {
		c, err := LoadServer(writeServerConfig(t, "executor:\n  policy: "+v+"\n"), nil)
		if err != nil || len(c.Executor.Policy) != 0 {
			t.Errorf("policy: %s gave %q, %v; want no policy", v, c.Executor.Policy, err)
		}
	}
}

// TestLoadServerRefusesAPolicyInANonYAMLConfig: the policy is only decoded from
// YAML or JSON, so one written in another format fails startup instead of
// being silently ignored.
func TestLoadServerRefusesAPolicyInANonYAMLConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("[executor.policy]\nruntime_class_name = \"gvisor\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadServer(p, nil); err == nil || !strings.Contains(err.Error(), "executor.policy") {
		t.Errorf("LoadServer = %v, want a refusal naming executor.policy", err)
	}
}
