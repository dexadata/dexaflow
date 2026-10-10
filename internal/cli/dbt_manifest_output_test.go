package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// A dbt parse failure must carry the cause dbt printed — not just "exit status N".
// dbt logs parse errors to STDOUT, which the old code dropped, blinding CI (#836).
func TestLoadDbtManifestParseFailureCarriesCause(t *testing.T) {
	binDir := t.TempDir()
	// A fake dbt that logs the real cause to STDOUT (as dbt-core does) and exits
	// non-zero — the exact shape of the reported failure (missing `dbt deps`).
	script := "#!/bin/sh\n" +
		"echo 'Compilation Error: dbt found 1 package(s) missing, run dbt deps'\n" +
		"exit 2\n"
	if err := os.WriteFile(filepath.Join(binDir, "dbt"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "dbt_project.yml"), []byte("profile: shop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	_, err := loadDbtManifest(cmd, proj, &domain.DbtConfig{}, false, "dag")
	if err == nil {
		t.Fatal("expected a dbt parse error")
	}
	if !strings.Contains(err.Error(), "run dbt deps") {
		t.Errorf("error must carry the dbt stdout cause; got: %v", err)
	}
}
