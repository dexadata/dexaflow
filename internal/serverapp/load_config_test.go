package serverapp

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestLoadConfigOrder pins where Run's configuration comes from: a config
// passed in wins, then the file the option names, then the one
// LEOFLOW_CONFIG (mirrored from DEXAFLOW_CONFIG) names; the environment
// applies on top of a file.
func TestLoadConfigOrder(t *testing.T) {
	dir := t.TempDir()
	write := func(name, addr string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("server:\n  http_addr: \""+addr+"\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	fromEnv := write("env.yaml", ":7001")
	fromOption := write("option.yaml", ":7002")
	t.Setenv("LEOFLOW_CONFIG", fromEnv)

	given := &config.ServerConfig{}
	if got, err := loadConfig(Options{Config: given, ConfigFile: fromOption}); err != nil || got != given {
		t.Fatalf("loadConfig with a config = %p, %v; want the given config", got, err)
	}
	got, err := loadConfig(Options{ConfigFile: fromOption})
	if err != nil || got.Server.HTTPAddr != ":7002" {
		t.Fatalf("loadConfig with a file = %q, %v; want the option's file", got.Server.HTTPAddr, err)
	}
	got, err = loadConfig(Options{})
	if err != nil || got.Server.HTTPAddr != ":7001" {
		t.Fatalf("loadConfig with nothing = %q, %v; want LEOFLOW_CONFIG's file", got.Server.HTTPAddr, err)
	}
	t.Setenv("DEXAFLOW_SERVER_HTTP_ADDR", ":7003")
	got, err = loadConfig(Options{ConfigFile: fromOption})
	if err != nil || got.Server.HTTPAddr != ":7003" {
		t.Fatalf("loadConfig with a file and the environment = %q, %v; want the environment on top", got.Server.HTTPAddr, err)
	}
	if _, err := loadConfig(Options{ConfigFile: filepath.Join(dir, "missing.yaml")}); err == nil {
		t.Fatal("a missing config file must fail, not fall back to defaults")
	}
}

// TestApplyLegacyPathsLeavesAGivenConfigAlone keeps an embedder's own
// configuration as given: only a configuration the server loaded itself falls
// back to the leoflow paths (#2155).
func TestApplyLegacyPathsLeavesAGivenConfigAlone(t *testing.T) {
	given := &config.ServerConfig{}
	given.Executor.Type = "subprocess"
	given.Executor.AgentPath = config.DefaultAgentPath
	given.Logs.Dir = config.DefaultLogsDir
	t.Setenv("PATH", t.TempDir())
	applyLegacyPaths(Options{Config: given}, given, slog.New(slog.DiscardHandler))
	if given.Logs.Dir != config.DefaultLogsDir || given.Executor.AgentPath != config.DefaultAgentPath {
		t.Fatalf("a given config was changed: logs.dir %q, agent_path %q", given.Logs.Dir, given.Executor.AgentPath)
	}
}

// TestApplyLegacyPathsResolvesALoadedConfig drives the loaded path: with only
// leoflow-agent on PATH, the subprocess executor keeps running it.
func TestApplyLegacyPathsResolvesALoadedConfig(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, config.LegacyAgentPath), []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // an executable fixture
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("DEXAFLOW_EXECUTOR_TYPE", "subprocess")
	cfg, err := loadConfig(Options{})
	if err != nil {
		t.Fatal(err)
	}
	applyLegacyPaths(Options{}, cfg, slog.New(slog.DiscardHandler))
	if cfg.Executor.AgentPath != config.LegacyAgentPath {
		t.Fatalf("agent_path = %q, want %q", cfg.Executor.AgentPath, config.LegacyAgentPath)
	}
}

// TestTaskSecretAliasOnlyForTheDefaultPath: the task Secret is mounted at the
// leoflow path too only while the mount path is the dexaflow default (#2155).
func TestTaskSecretAliasOnlyForTheDefaultPath(t *testing.T) {
	if got := taskSecretAlias(config.DefaultTaskSecretMountPath); got != config.LegacyTaskSecretMountPath {
		t.Errorf("alias for the default path = %q, want %q", got, config.LegacyTaskSecretMountPath)
	}
	if got := taskSecretAlias("/var/secrets/gcp"); got != "" {
		t.Errorf("alias for an operator's own path = %q, want none", got)
	}
}
