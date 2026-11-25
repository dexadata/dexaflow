package config

import (
	"errors"
	"strings"
	"testing"
)

// fakeHost answers the two questions ResolveLegacyPaths asks of the host.
type fakeHost struct {
	dirs    map[string]bool
	onPath  map[string]bool
	lookups []string
}

func (h *fakeHost) dirExists(p string) bool { return h.dirs[p] }

func (h *fakeHost) lookPath(name string) (string, error) {
	h.lookups = append(h.lookups, name)
	if h.onPath[name] {
		return "/usr/local/bin/" + name, nil
	}
	return "", errors.New("not found")
}

func defaultsConfig(t *testing.T) *ServerConfig {
	t.Helper()
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestLegacyLogsDirKeptWhenOnlyItExists covers an install upgraded from a
// release whose default was /var/log/leoflow: its logs stay where they are,
// with one warning naming the setting to pin (#2155).
func TestLegacyLogsDirKeptWhenOnlyItExists(t *testing.T) {
	c := defaultsConfig(t)
	h := &fakeHost{dirs: map[string]bool{LegacyLogsDir: true}}
	warnings := c.resolveLegacyPaths(h.dirExists, h.lookPath)
	if c.Logs.Dir != LegacyLogsDir {
		t.Fatalf("logs.dir = %q, want the existing %q", c.Logs.Dir, LegacyLogsDir)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "logs.dir") || !strings.Contains(warnings[0], "DEXAFLOW_LOGS_DIR") {
		t.Fatalf("warnings = %q, want one naming logs.dir and DEXAFLOW_LOGS_DIR", warnings)
	}
}

func TestNewLogsDirWinsWhenBothExist(t *testing.T) {
	c := defaultsConfig(t)
	h := &fakeHost{dirs: map[string]bool{LegacyLogsDir: true, DefaultLogsDir: true}}
	if w := c.resolveLegacyPaths(h.dirExists, h.lookPath); len(w) != 0 {
		t.Fatalf("warnings = %q, want none", w)
	}
	if c.Logs.Dir != DefaultLogsDir {
		t.Fatalf("logs.dir = %q, want %q", c.Logs.Dir, DefaultLogsDir)
	}
}

func TestExplicitLogsDirIsNeverReplaced(t *testing.T) {
	t.Setenv("DEXAFLOW_LOGS_DIR", "/data/logs")
	c := defaultsConfig(t)
	h := &fakeHost{dirs: map[string]bool{LegacyLogsDir: true}}
	if w := c.resolveLegacyPaths(h.dirExists, h.lookPath); len(w) != 0 {
		t.Fatalf("warnings = %q, want none", w)
	}
	if c.Logs.Dir != "/data/logs" {
		t.Fatalf("logs.dir = %q, want the explicit /data/logs", c.Logs.Dir)
	}
}

// TestLegacyAgentBinaryKeptWhenOnlyItIsOnPath covers a host that still has the
// leoflow-agent binary of an older install and no dexaflow-agent.
func TestLegacyAgentBinaryKeptWhenOnlyItIsOnPath(t *testing.T) {
	t.Setenv("DEXAFLOW_EXECUTOR_TYPE", "subprocess")
	c := defaultsConfig(t)
	h := &fakeHost{onPath: map[string]bool{LegacyAgentPath: true}}
	warnings := c.resolveLegacyPaths(h.dirExists, h.lookPath)
	if c.Executor.AgentPath != LegacyAgentPath {
		t.Fatalf("agent_path = %q, want %q", c.Executor.AgentPath, LegacyAgentPath)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "executor.agent_path") {
		t.Fatalf("warnings = %q, want one naming executor.agent_path", warnings)
	}
}

func TestNewAgentBinaryWinsWhenOnPath(t *testing.T) {
	t.Setenv("DEXAFLOW_EXECUTOR_TYPE", "subprocess")
	c := defaultsConfig(t)
	h := &fakeHost{onPath: map[string]bool{LegacyAgentPath: true, DefaultAgentPath: true}}
	if w := c.resolveLegacyPaths(h.dirExists, h.lookPath); len(w) != 0 {
		t.Fatalf("warnings = %q, want none", w)
	}
	if c.Executor.AgentPath != DefaultAgentPath {
		t.Fatalf("agent_path = %q, want %q", c.Executor.AgentPath, DefaultAgentPath)
	}
}

// TestAgentPathIgnoredOffSubprocess keeps the Kubernetes executor, which never
// runs a local agent, from looking for one.
func TestAgentPathIgnoredOffSubprocess(t *testing.T) {
	c := defaultsConfig(t)
	h := &fakeHost{onPath: map[string]bool{LegacyAgentPath: true}}
	c.resolveLegacyPaths(h.dirExists, h.lookPath)
	if len(h.lookups) != 0 || c.Executor.AgentPath != DefaultAgentPath {
		t.Fatalf("lookups = %q, agent_path = %q: the kubernetes executor must not look", h.lookups, c.Executor.AgentPath)
	}
}
