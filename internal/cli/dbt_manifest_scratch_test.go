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

// Issue #1060: leoflow's own dbt parse must write to a scratch directory,
// redirecting DBT_TARGET_PATH and DBT_LOG_PATH so it does not mutate or pollute
// the author's working tree with target/ or logs/.
func TestLoadDbtManifestParsesInScratchDir(t *testing.T) {
	binDir := t.TempDir()
	// Fake dbt that records the target and log paths passed in environment,
	// writes manifest.json into $DBT_TARGET_PATH, and writes a log into $DBT_LOG_PATH.
	script := "#!/bin/sh\n" +
		"if [ -z \"$DBT_TARGET_PATH\" ] || [ -z \"$DBT_LOG_PATH\" ]; then\n" +
		"  echo 'DBT_TARGET_PATH or DBT_LOG_PATH not set' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"mkdir -p \"$DBT_TARGET_PATH\" \"$DBT_LOG_PATH\"\n" +
		"echo '{\"metadata\":{\"dbt_version\":\"1.8.0\"}}' > \"$DBT_TARGET_PATH/manifest.json\"\n" +
		"echo 'fake dbt log' > \"$DBT_LOG_PATH/dbt.log\"\n" +
		"echo 'Done.'\n"
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

	data, err := loadDbtManifest(cmd, proj, &domain.DbtConfig{}, false, "dag")
	if err != nil {
		t.Fatalf("loadDbtManifest failed: %v", err)
	}
	if !strings.Contains(string(data), "dbt_version") {
		t.Errorf("manifest data unexpected: %s", data)
	}

	// Working tree must NOT contain target/ or logs/
	if _, serr := os.Stat(filepath.Join(proj, "target")); !os.IsNotExist(serr) {
		t.Errorf("working tree contains target/ dir; dbt parse must not write into project tree")
	}
	if _, serr := os.Stat(filepath.Join(proj, "logs")); !os.IsNotExist(serr) {
		t.Errorf("working tree contains logs/ dir; dbt parse must not write into project tree")
	}
}
