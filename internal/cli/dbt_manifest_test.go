package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestLiteDbtBin: a Lite compile parses the manifest with the per-DAG venv's dbt
// (~/.dexaflow/dev/venvs/<dag>/bin/dbt) — the same dbt the task runs — not a system
// dbt the user may not have (L1). It returns "" when the venv dbt is absent so the
// caller falls back to PATH.
func TestLiteDbtBin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := liteDbtBinAt(home, "sales"); got != "" {
		t.Errorf("no venv yet: want empty, got %q", got)
	}

	bin := filepath.Join(home, ".dexaflow", "dev", "venvs", "sales", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	dbtPath := filepath.Join(bin, "dbt")
	if err := os.WriteFile(dbtPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := liteDbtBinAt(home, "sales"); got != dbtPath {
		t.Errorf("with venv dbt: got %q, want %q", got, dbtPath)
	}
	if got := liteDbtBinAt(home, ""); got != "" {
		t.Errorf("empty dag id: want empty, got %q", got)
	}
}

// TestDbtProjectHasProfiles gates the L4 default duckdb: a project that ships its own
// profiles.yml must report true so the compiler never overrides the user's warehouse.
func TestDbtProjectHasProfiles(t *testing.T) {
	dir := t.TempDir()
	if dbtProjectHasProfiles(dir) {
		t.Error("no profiles.yml: want false")
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles.yml"), []byte("x: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !dbtProjectHasProfiles(dir) {
		t.Error("with profiles.yml: want true (L4 must not override a user-configured warehouse)")
	}
}

// TestWriteParseDuckdbProfile: the compile-time half of L4 respects a project's own
// profiles.yml, and otherwise writes a temporary default-duckdb profile so `dbt parse`
// succeeds zero-config (in a temp dir, never the user's ~/.dbt).
func TestWriteParseDuckdbProfile(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "shop")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "dbt_project.yml"), []byte("profile: shop\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &domain.DbtConfig{Project: "shop"}

	// Project ships its own profiles.yml -> respected (no temp profile generated).
	if err := os.WriteFile(filepath.Join(proj, "profiles.yml"), []byte("shop: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := writeParseDuckdbProfile(dir, c); got != "" {
		t.Errorf("project has profiles.yml: want empty (respected), got %q", got)
	}

	// No project profiles.yml -> a temp dir carrying a generated duckdb profile.
	if err := os.Remove(filepath.Join(proj, "profiles.yml")); err != nil {
		t.Fatal(err)
	}
	got := writeParseDuckdbProfile(dir, c)
	if got == "" {
		t.Fatal("no profiles.yml: want a temp profiles dir, got empty")
	}
	defer func() { _ = os.RemoveAll(got) }()
	data, err := os.ReadFile(filepath.Join(got, "profiles.yml"))
	if err != nil {
		t.Fatalf("temp profiles.yml missing: %v", err)
	}
	if !strings.Contains(string(data), "duckdb") || !strings.Contains(string(data), "shop") {
		t.Errorf("temp profile = %s, want a duckdb profile under 'shop'", data)
	}
}

// TestLoadDbtManifestBaked covers the pre-built-manifest path (Pro/CI) and its error;
// the fresh `dbt parse` path is an external-binary orchestration, gated end-to-end by
// the e2e-dbt job (ADR 0011), not unit-tested.
func TestLoadDbtManifestBaked(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "shop")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "m.json"), []byte(`{"nodes":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	// A pinned manifest is read as-is (baked path), regardless of local.
	data, err := loadDbtManifest(cmd, dir, &domain.DbtConfig{Project: "shop", Manifest: "m.json"}, false, "d")
	if err != nil {
		t.Fatalf("baked manifest: %v", err)
	}
	if string(data) != `{"nodes":{}}` {
		t.Errorf("baked manifest = %q, want the file's bytes", data)
	}

	// A missing pinned manifest is a loud error.
	if _, err := loadDbtManifest(cmd, dir, &domain.DbtConfig{Project: "shop", Manifest: "nope.json"}, false, "d"); err == nil {
		t.Error("missing manifest: want an error")
	}
}

// TestDbtConnectionProfile: the profile name is read for both a managed connection and
// the no-connection case (so L4 can name its default duckdb), and it fails loudly only
// when a connection is set but the project's dbt_project.yml is unreadable.
func TestDbtConnectionProfile(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "dbt_project.yml"), []byte("profile: shop\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if conn, prof, err := dbtConnectionProfile(dir, &domain.DbtConfig{Project: "p", Connection: "wh"}); err != nil || conn != "wh" || prof != "shop" {
		t.Errorf("with conn = (%q, %q, %v), want (wh, shop, nil)", conn, prof, err)
	}
	if conn, prof, err := dbtConnectionProfile(dir, &domain.DbtConfig{Project: "p"}); err != nil || conn != "" || prof != "shop" {
		t.Errorf("no conn = (%q, %q, %v), want ('', shop, nil) — the profile name feeds L4", conn, prof, err)
	}
	if conn, prof, err := dbtConnectionProfile(dir, &domain.DbtConfig{Project: "missing"}); err != nil || conn != "" || prof != "" {
		t.Errorf("no project, no conn = (%q, %q, %v), want ('', '', nil) — graceful fallback", conn, prof, err)
	}
	if _, _, err := dbtConnectionProfile(dir, &domain.DbtConfig{Project: "missing", Connection: "wh"}); err == nil {
		t.Error("conn set but no dbt_project.yml: want a loud error")
	}
}
