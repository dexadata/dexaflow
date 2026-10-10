package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestRunParserSetsProjectConfigEnv pins the migration contract: when the Go
// CLI invokes the parser it MUST hand over the resolved project config as
// JSON in LEOFLOW_PROJECT_CONFIG_JSON. Without this env var the parser
// raises a clear error pointing at the missing handshake (see
// parser/tests/test_config_env_var.py). Future contributors who replumb the
// parser invocation and forget to set the env var fail this test, not the
// next user's `dexaflow compile`.
//
// We run a tiny shell-script "parser" that prints its env vars to stdout —
// no actual Python needed — so this test stays fast and hermetic.
func TestRunParserSetsProjectConfigEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script parser stub assumes a POSIX env-dumping shell")
	}
	dir := t.TempDir()
	stubPath := filepath.Join(dir, "stub_parser.sh")
	// Stub prints the env var and exits 0 — the assertion is that we
	// observe it set to the marshaled JSON.
	stub := "#!/bin/sh\nprintf '%s' \"$LEOFLOW_PROJECT_CONFIG_JSON\"\nexit 0\n"
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write stub: %v", err)
	}

	cfg := &domain.LeoflowConfig{
		DagID: "carry_via_env",
		Owner: "marker-from-go",
		Tags:  []string{"alpha-cut"},
	}

	cmd := &cobra.Command{}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := runParser(cmd, stubPath, parserArgs{
		source:        "/dev/null",
		config:        "/dev/null",
		output:        filepath.Join(dir, "dag.json"),
		image:         "stub:v1",
		dagVersion:    "v1",
		projectConfig: cfg,
	})
	if err != nil {
		t.Fatalf("runParser: %v\nstderr=%s", err, stderr.String())
	}

	captured := stdout.String()
	if captured == "" {
		t.Fatal("parser stub printed nothing — LEOFLOW_PROJECT_CONFIG_JSON was empty")
	}

	var roundTrip domain.LeoflowConfig
	if jerr := json.Unmarshal([]byte(captured), &roundTrip); jerr != nil {
		t.Fatalf("captured env var is not valid JSON: %v\n---\n%s", jerr, captured)
	}
	if roundTrip.DagID != cfg.DagID {
		t.Errorf("DagID round-trip = %q, want %q", roundTrip.DagID, cfg.DagID)
	}
	if roundTrip.Owner != cfg.Owner {
		t.Errorf("Owner round-trip = %q, want %q", roundTrip.Owner, cfg.Owner)
	}
	if len(roundTrip.Tags) == 0 || roundTrip.Tags[0] != "alpha-cut" {
		t.Errorf("Tags round-trip = %v, want [alpha-cut]", roundTrip.Tags)
	}
}

// TestRunParserOmitsEnvWhenConfigNil covers the back-compat branch: callers
// that legitimately have no project config (none today, but the path is
// reachable in tests) must not get an env var set to "null" or similar —
// the parser would refuse such an invocation, and a silent partial config
// is worse than an explicit miss.
func TestRunParserOmitsEnvWhenConfigNil(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script parser stub assumes a POSIX env-dumping shell")
	}
	dir := t.TempDir()
	stubPath := filepath.Join(dir, "stub_no_env.sh")
	stub := "#!/bin/sh\n" +
		"if [ -n \"${LEOFLOW_PROJECT_CONFIG_JSON+set}\" ]; then\n" +
		"  echo \"SET=$LEOFLOW_PROJECT_CONFIG_JSON\"\n" +
		"else\n" +
		"  echo UNSET\n" +
		"fi\nexit 0\n"
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write stub: %v", err)
	}

	cmd := &cobra.Command{}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	// Defensive: scrub any leaked env from the dev shell.
	t.Setenv("LEOFLOW_PROJECT_CONFIG_JSON", "")
	if err := os.Unsetenv("LEOFLOW_PROJECT_CONFIG_JSON"); err != nil {
		t.Fatalf("unset: %v", err)
	}

	err := runParser(cmd, stubPath, parserArgs{
		source:     "/dev/null",
		config:     "/dev/null",
		output:     filepath.Join(dir, "dag.json"),
		image:      "stub:v1",
		dagVersion: "v1",
		// projectConfig left nil on purpose.
	})
	if err != nil {
		t.Fatalf("runParser: %v\nstderr=%s", err, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != "UNSET" {
		t.Errorf("expected stub to report UNSET when projectConfig is nil; got %q", got)
	}
}
