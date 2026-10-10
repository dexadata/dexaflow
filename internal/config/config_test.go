package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func TestLoadAppliesDefaults(t *testing.T) {
	c, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.ServerURL != defaultServerURL {
		t.Errorf("ServerURL = %q, want %q", c.ServerURL, defaultServerURL)
	}
	if c.LogLevel != defaultLogLevel {
		t.Errorf("LogLevel = %q, want %q", c.LogLevel, defaultLogLevel)
	}
	if c.ParserCmd != defaultParserCmd {
		t.Errorf("ParserCmd = %q, want %q", c.ParserCmd, defaultParserCmd)
	}
}

func TestLoadEnvOverridesDefault(t *testing.T) {
	t.Setenv("LEOFLOW_LOG_LEVEL", "debug")
	c, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q", c.LogLevel, "debug")
	}
}

func TestLoadFileOverridesDefault(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("log_level: warn\nserver_url: http://example:9000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p, nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want %q", c.LogLevel, "warn")
	}
	if c.ServerURL != "http://example:9000" {
		t.Errorf("ServerURL = %q, want %q", c.ServerURL, "http://example:9000")
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("log_level: warn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEOFLOW_LOG_LEVEL", "error")
	c, err := Load(p, nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.LogLevel != "error" {
		t.Errorf("LogLevel = %q, want %q (env should override file)", c.LogLevel, "error")
	}
}

func TestLoadFlagOverridesEnv(t *testing.T) {
	t.Setenv("LEOFLOW_LOG_LEVEL", "error")
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("log-level", "", "log level")
	if err := fs.Parse([]string{"--log-level=trace"}); err != nil {
		t.Fatal(err)
	}
	c, err := Load("", fs)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.LogLevel != "trace" {
		t.Errorf("LogLevel = %q, want %q (flag should override env)", c.LogLevel, "trace")
	}
}

func TestDefaultConfigFile(t *testing.T) {
	p, err := DefaultConfigFile()
	if err != nil {
		t.Fatalf("DefaultConfigFile() error = %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(p), ".dexaflow/config.yaml") {
		t.Errorf("DefaultConfigFile() = %q, want suffix .dexaflow/config.yaml", p)
	}
}
