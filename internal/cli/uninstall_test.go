package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestRemoveBinariesIn covers the uninstall fix: the leoflow binaries (which
// install.sh puts on a PATH dir like /usr/local/bin, not under ~/.leoflow) must be
// removed, while unrelated files in the same dir are left untouched.
func TestRemoveBinariesIn(t *testing.T) {
	dir := t.TempDir()
	all := []string{
		"dexaflow", "dexaflow-server", "dexaflow-agent", "dexaflow-mcp",
		"leoflow", "leoflow-server", "leoflow-agent", "leoflow-mcp",
	}
	for _, n := range append(all, "other-tool") {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removeBinariesIn(&bytes.Buffer{}, dir)
	for _, n := range all {
		if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", n)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "other-tool")); err != nil {
		t.Error("unrelated binaries must be left untouched")
	}
	removeBinariesIn(&bytes.Buffer{}, "") // empty dir is a no-op, must not panic
}

// TestResolveLiteProjectRejectsNonProjectArg covers the CLI clarity fix: an
// explicit `dexaflow lite <arg>` that is not a project (e.g. the `dexaflow lite
// uninstall` typo) fails with an actionable message, not a cryptic dexaflow.yaml error.
func TestResolveLiteProjectRejectsNonProjectArg(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)

	if _, err := resolveLiteProject(cmd, []string{"uninstall"}); err == nil {
		t.Fatal("a non-project argument should error")
	} else if !strings.Contains(err.Error(), "workspace path") || !strings.Contains(err.Error(), "dexaflow uninstall") {
		// The error must name the typo (so the user sees what was misparsed)
		// and hint at the actual `dexaflow uninstall` command so they recover
		// quickly. Post-Phase-3 wording shifted from "no Dexaflow project" to
		// "workspace path" since the canonical model is a workspace, not a
		// single project.
		t.Errorf("error should be actionable, got: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dexaflow.yaml"), []byte("dag_id: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveLiteProject(cmd, []string{dir}); err != nil || got != dir {
		t.Errorf("existing project: got (%q, %v), want (%q, nil)", got, err, dir)
	}
}

// uninstallCmd builds a command with the given stdin and captured output.
func uninstallCmd(stdin string) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetIn(strings.NewReader(stdin))
	return cmd, out
}

func seedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".leoflow", "bin")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "leoflow"), []byte("bin"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestUninstallRemovesHomeWithYes(t *testing.T) {
	home := seedHome(t)
	cmd, _ := uninstallCmd("")
	if err := runUninstall(cmd, true, false); err != nil {
		t.Fatalf("uninstall --yes: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".leoflow")); !os.IsNotExist(err) {
		t.Errorf("~/.leoflow should be removed, err=%v", err)
	}
}

func TestUninstallAbortsWithoutConfirmation(t *testing.T) {
	home := seedHome(t)
	cmd, out := uninstallCmd("no\n") // user declines
	if err := runUninstall(cmd, false, false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".leoflow")); err != nil {
		t.Errorf("~/.leoflow must NOT be removed when the user declines, err=%v", err)
	}
	if !strings.Contains(out.String(), "aborted") {
		t.Errorf("should report aborted, got %q", out.String())
	}
}

func TestUninstallConfirmedByTypingYes(t *testing.T) {
	home := seedHome(t)
	cmd, _ := uninstallCmd("yes\n")
	if err := runUninstall(cmd, false, false); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".leoflow")); !os.IsNotExist(err) {
		t.Errorf("typing 'yes' should remove ~/.leoflow, err=%v", err)
	}
}

func TestUninstallNothingToRemove(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.leoflow
	cmd, out := uninstallCmd("")
	if err := runUninstall(cmd, true, false); err != nil {
		t.Fatalf("uninstall on a clean home: %v", err)
	}
	if !strings.Contains(out.String(), "Nothing to remove") {
		t.Errorf("should report nothing to remove, got %q", out.String())
	}
}

func TestUninstallWired(t *testing.T) {
	found := false
	for _, c := range NewRootCommand().Commands() {
		if c.Name() == "uninstall" {
			found = true
		}
	}
	if !found {
		t.Error("`dexaflow uninstall` should be registered on the root command")
	}
}

// TestRemoveHomeExceptPreservesDatastore: a plain uninstall keeps the datastore
// (pgdata) for a reinstall while removing everything else under the home.
func TestRemoveHomeExceptPreservesDatastore(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "pgdata", "PG_VERSION"), "16")
	mustWrite(t, filepath.Join(root, "postgres", "bin", "postgres"), "x")
	mustWrite(t, filepath.Join(root, "config.yaml"), "admin: x")
	if err := removeHomeExcept(root, "pgdata"); err != nil {
		t.Fatalf("removeHomeExcept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "pgdata", "PG_VERSION")); err != nil {
		t.Errorf("pgdata must be preserved on a plain uninstall: %v", err)
	}
	for _, gone := range []string{"config.yaml", "postgres"} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s must be removed, stat err = %v", gone, err)
		}
	}
}

// TestRemoveHomeExceptRemovesRootWhenNoDatastore: a Docker-only install (no
// managed pgdata) has its home removed entirely — its data lives in the Docker
// volume, which a plain uninstall also leaves intact (it does not run down -v).
func TestRemoveHomeExceptRemovesRootWhenNoDatastore(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".leoflow")
	mustWrite(t, filepath.Join(root, "postgres", "bin", "postgres"), "x")
	if err := removeHomeExcept(root, "pgdata"); err != nil {
		t.Fatalf("removeHomeExcept: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("home must be removed entirely when there is no datastore to keep, stat err = %v", err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// An install from before the rename has its state in ~/.leoflow behind a
// ~/.dexaflow link (config.HomeDirIn). Removing it must remove the data, not
// just the link, and leave no link pointing at nothing.
func TestRemoveLeoflowHomeThroughTheLegacyLink(t *testing.T) {
	for _, purge := range []bool{true, false} {
		home := t.TempDir()
		legacy := filepath.Join(home, ".leoflow")
		if err := os.MkdirAll(filepath.Join(legacy, "python"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacy, "config.yaml"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(home, ".dexaflow")
		if err := os.Symlink(".leoflow", link); err != nil {
			t.Fatal(err)
		}
		cmd := &cobra.Command{}
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		if err := removeLeoflowHome(cmd, link, purge); err != nil {
			t.Fatalf("purge=%v: %v", purge, err)
		}
		if _, err := os.Lstat(legacy); !os.IsNotExist(err) {
			t.Errorf("purge=%v: ~/.leoflow still exists (err=%v)", purge, err)
		}
		if _, err := os.Lstat(link); !os.IsNotExist(err) {
			t.Errorf("purge=%v: the ~/.dexaflow link was left behind (err=%v)", purge, err)
		}
	}
}
