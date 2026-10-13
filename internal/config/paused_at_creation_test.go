package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDagsArePausedAtCreationDefaultsOn pins Airflow's default (#1705): a DAG
// registered for the first time starts paused unless the operator opts out.
func TestDagsArePausedAtCreationDefaultsOn(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if !c.DagsArePausedAtCreation {
		t.Fatal("dags_are_paused_at_creation defaults to false, want true")
	}
}

// TestDagsArePausedAtCreationBindsBothPrefixes pins the opt-out through the
// environment (the Helm path) under the current and the legacy prefix.
func TestDagsArePausedAtCreationBindsBothPrefixes(t *testing.T) {
	for _, env := range []string{"DEXAFLOW_DAGS_ARE_PAUSED_AT_CREATION", "LEOFLOW_DAGS_ARE_PAUSED_AT_CREATION"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "false")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer() error = %v", err)
			}
			if c.DagsArePausedAtCreation {
				t.Fatalf("%s=false did not turn dags_are_paused_at_creation off", env)
			}
		})
	}
}

// TestDagsArePausedAtCreationFromConfigFile: the top-level key of a config file.
func TestDagsArePausedAtCreationFromConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(path, []byte("dags_are_paused_at_creation: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServer(path, nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.DagsArePausedAtCreation {
		t.Fatal("dags_are_paused_at_creation: false in the config file was not applied")
	}
}
