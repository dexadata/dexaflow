package serverapp

import (
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
