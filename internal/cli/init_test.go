package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitCommandPrintsNextStep covers #D6 from the Lite dogfood audit (#212):
// after scaffolding a project, the user has no idea what command to run next.
// The success message must surface at least one concrete next-step hint
// (typically `dexaflow lite <path>` or `dexaflow validate <path>`) so the
// onboarding loop terminates at a useful instruction rather than dead air.
func TestInitCommandPrintsNextStep(t *testing.T) {
	target := filepath.Join(t.TempDir(), "myproj")
	cmd := newInitCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{target})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("init: %v", err)
	}

	got := stdout.String()
	if !strings.Contains(got, "Initialized Dexaflow project") {
		t.Errorf("missing success line; got: %q", got)
	}
	if !strings.Contains(got, "dexaflow lite") && !strings.Contains(got, "dexaflow validate") {
		t.Errorf("D6: expected a next-step suggestion (dexaflow lite / dexaflow validate); got: %q", got)
	}
}
