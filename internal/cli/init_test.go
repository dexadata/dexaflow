package cli

import (
	"bytes"
	"os"
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
	t.Parallel()
	target := filepath.Join(t.TempDir(), "myproj")
	cmd := newInitCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{target})
	if err := cmd.ExecuteContext(testCtx(nil)); err != nil {
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

// TestScaffoldedFilesHaveNoEmDash locks the text `init` and `new` write into a
// user's project: the dag.py and dexaflow.yaml they scaffold carry no em dash,
// the house style of every text the project ships (#1709).
func TestScaffoldedFilesHaveNoEmDash(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "orders_report")
	if _, err := scaffoldProject(dir); err != nil {
		t.Fatalf("scaffoldProject: %v", err)
	}
	for _, name := range []string{"dag.py", "dexaflow.yaml"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.ContainsRune(string(b), '—') {
			t.Errorf("%s has an em dash:\n%s", name, b)
		}
	}
}
