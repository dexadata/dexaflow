package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestPrintSetupSummaryHighlightsCredentials: a fresh install's credentials are
// rendered as a high-contrast block — horizontal dividers, an uppercased title,
// and the password unmissable on its own line — so users stop losing the
// generated password in the install log (#122). Plain-text (no color) here so
// the assertion is stable on non-TTY (the buffer is not a *os.File).
func TestPrintSetupSummaryHighlightsCredentials(t *testing.T) {
	var buf bytes.Buffer
	lc := liteSettings{AdminEmail: "admin@leoflow.local", Workspace: "/tmp/x", Port: 8088}
	printSetupSummary(&buf, lc, "blueturtle42")
	out := buf.String()
	for _, want := range []string{
		"═",                            // strong visual divider above + below
		"LEOFLOW LITE ADMIN",           // uppercased, unmissable title
		"SAVE NOW",                     // tells the user to save it
		"admin@leoflow.local",          // user line
		"blueturtle42",                 // the password value, verbatim
		"http://localhost:8088",        // open URL with the configured port
		"dexaflow lite reset-password", // recovery hint
	} {
		if !strings.Contains(out, want) {
			t.Errorf("setup summary must contain %q so the user can find it; got:\n%s", want, out)
		}
	}
	if c := strings.Count(out, "═══"); c < 2 {
		t.Errorf("setup summary should have at least two divider lines, got %d:\n%s", c, out)
	}
}

// TestPrintSetupSummaryOnRerun: when the admin already exists (no generated
// password), the summary points at reset-password instead of showing the block.
func TestPrintSetupSummaryOnRerun(t *testing.T) {
	var buf bytes.Buffer
	printSetupSummary(&buf, liteSettings{AdminEmail: "admin@leoflow.local", Port: 8088}, "")
	out := buf.String()
	if strings.Contains(out, "LEOFLOW LITE ADMIN") {
		t.Errorf("re-run must not flash the credentials block, got:\n%s", out)
	}
	if !strings.Contains(out, "reset-password") {
		t.Errorf("re-run must point at `dexaflow lite reset-password`, got:\n%s", out)
	}
}
