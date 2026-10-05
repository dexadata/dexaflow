package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/migrations"
)

// restoreFixture builds a home with a current config.yaml and an archive whose
// config differs, and returns the home's state dir and the archive path.
func restoreFixture(t *testing.T) (dexaHome, archive string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SUDO_USER", "")
	dexaHome = filepath.Join(home, ".dexaflow")
	if err := os.MkdirAll(dexaHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dexaHome, "config.yaml"), []byte("secret_key: \"current-key\"\nworkspace: \""+home+"/ws\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "config.yaml"), []byte("workspace: \""+home+"/ws\"\n"), 0o644); err != nil { //nolint:gosec // a loose mode in the archive is the point
		t.Fatal(err)
	}
	dump := filepath.Join(t.TempDir(), "dump.sql")
	if err := os.WriteFile(dump, []byte("SELECT 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	latest, err := migrations.Latest()
	if err != nil {
		t.Fatal(err)
	}
	mb, err := marshalManifest(newBackupManifest("v0.4.0", latest, ""))
	if err != nil {
		t.Fatal(err)
	}
	archive = filepath.Join(t.TempDir(), "b.tar.gz")
	if err := writeBackupArchive(archive, src, "", dump, mb); err != nil {
		t.Fatal(err)
	}
	return dexaHome, archive
}

func restoreCmd() (*cobra.Command, *bytes.Buffer) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetContext(context.Background())
	return cmd, &out
}

// ADR 0065 section 8: a restore whose replay fails must not take the current
// key. The replay used to run AFTER config.yaml was overwritten, so a failed
// replay left the datastore under the current key and the config recording a
// different one.
func TestRestoreWithAFailedReplayKeepsTheCurrentConfig(t *testing.T) {
	dexaHome, archive := restoreFixture(t)
	before, _ := os.ReadFile(filepath.Join(dexaHome, "config.yaml"))
	orig := psqlRestore
	t.Cleanup(func() { psqlRestore = orig })
	psqlRestore = func(context.Context, string, []byte) error { return errors.New("disk full") }

	cmd, _ := restoreCmd()
	if err := runRestore(cmd, archive, true); err == nil {
		t.Fatal("a failed replay must fail the restore")
	}
	after, _ := os.ReadFile(filepath.Join(dexaHome, "config.yaml"))
	if !bytes.Equal(before, after) {
		t.Errorf("config.yaml replaced by a restore whose replay failed:\nbefore %s\nafter  %s", before, after)
	}
}

// A successful restore writes the archive's config as it is (no key added or
// removed on Lite's behalf), atomically at 0600, and keeps the config it
// replaced as config.yaml.pre-restore, printing its path.
func TestRestoreKeepsThePreviousConfigAsPreRestore(t *testing.T) {
	dexaHome, archive := restoreFixture(t)
	before, _ := os.ReadFile(filepath.Join(dexaHome, "config.yaml"))
	orig := psqlRestore
	t.Cleanup(func() { psqlRestore = orig })
	psqlRestore = func(context.Context, string, []byte) error { return nil }

	cmd, out := restoreCmd()
	if err := runRestore(cmd, archive, true); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dexaHome, "config.yaml")
	got, _ := os.ReadFile(cfg)
	if strings.Contains(string(got), "secret_key") {
		t.Errorf("restore added a key on Lite's behalf:\n%s", got)
	}
	if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o600 {
		t.Errorf("restored config mode %v, want 0600", fi.Mode().Perm())
	}
	pre := filepath.Join(dexaHome, preRestoreName)
	kept, err := os.ReadFile(pre)
	if err != nil || !bytes.Equal(kept, before) {
		t.Errorf("pre-restore copy %q (%v), want the replaced config", kept, err)
	}
	if fi, _ := os.Stat(pre); fi.Mode().Perm() != 0o600 {
		t.Errorf("pre-restore mode %v, want 0600", fi.Mode().Perm())
	}
	if !strings.Contains(out.String(), pre) {
		t.Errorf("restore did not print %s:\n%s", pre, out.String())
	}
}

// Every config writer takes the config lock; a restore during a migration
// refuses instead of interleaving with it.
func TestRestoreRefusesWhileTheConfigIsLocked(t *testing.T) {
	dexaHome, archive := restoreFixture(t)
	release, err := lockConfigDir(dexaHome, configLockExclusive, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	orig := psqlRestore
	t.Cleanup(func() { psqlRestore = orig })
	replayed := false
	psqlRestore = func(context.Context, string, []byte) error { replayed = true; return nil }
	cmd, _ := restoreCmd()
	if rerr := runRestore(cmd, archive, true); !errors.Is(rerr, errConfigLocked) {
		t.Errorf("restore during a config write: %v, want errConfigLocked", rerr)
	}
	if replayed {
		t.Error("restore replayed the dump without the config lock")
	}
}

// uninstall deletes config.yaml, so it takes the lock too.
func TestUninstallRefusesWhileTheConfigIsLocked(t *testing.T) {
	dexaHome, _ := restoreFixture(t)
	release, err := lockConfigDir(dexaHome, configLockExclusive, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cmd, _ := restoreCmd()
	if uerr := runUninstall(cmd, true, false); !errors.Is(uerr, errConfigLocked) {
		t.Errorf("uninstall during a config write: %v, want errConfigLocked", uerr)
	}
	if _, serr := os.Stat(filepath.Join(dexaHome, "config.yaml")); serr != nil {
		t.Errorf("uninstall removed config.yaml during a migration: %v", serr)
	}
}

// backup reads the config shared: an archive never pairs a post-commit
// datastore with a pre-migration config.
func TestBackupRefusesWhileTheConfigIsBeingRewritten(t *testing.T) {
	dexaHome, _ := restoreFixture(t)
	release, err := lockConfigDir(dexaHome, configLockExclusive, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cmd, _ := restoreCmd()
	if berr := runBackup(cmd, filepath.Join(t.TempDir(), "x.tar.gz")); !errors.Is(berr, errConfigLocked) {
		t.Errorf("backup during a config write: %v, want errConfigLocked", berr)
	}
}

// setup writes the first config.yaml under the lock, and re-checks under it
// that no config appeared meanwhile.
func TestSetupFirstConfigTakesTheLock(t *testing.T) {
	dir := t.TempDir()
	release, err := lockConfigDir(dir, configLockExclusive, false)
	if err != nil {
		t.Fatal(err)
	}
	lc := liteSettings{Workspace: "/w", Executor: "subprocess", AdminEmail: "a@b.c", Port: 8088}
	if _, werr := writeFirstLiteConfig(dir, "parser", lc); !errors.Is(werr, errConfigLocked) {
		t.Errorf("setup during a config write: %v, want errConfigLocked", werr)
	}
	if liteConfigExists(dir) {
		t.Error("setup wrote config.yaml without the lock")
	}
	release()
	pw, werr := writeFirstLiteConfig(dir, "parser", lc)
	if werr != nil || pw == "" || !liteConfigExists(dir) {
		t.Fatalf("first setup: pw=%q err=%v", pw, werr)
	}
	pw2, werr := writeFirstLiteConfig(dir, "parser", lc)
	if werr != nil || pw2 != "" {
		t.Errorf("a second setup must leave the config alone: pw=%q err=%v", pw2, werr)
	}
}

// reset-password rewrites config.yaml, so it takes the lock before anything.
func TestResetPasswordRefusesWhileTheConfigIsLocked(t *testing.T) {
	dexaHome, _ := restoreFixture(t)
	release, err := lockConfigDir(dexaHome, configLockExclusive, false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	cmd, _ := restoreCmd()
	if rerr := runResetPassword(cmd, ""); !errors.Is(rerr, errConfigLocked) {
		t.Errorf("reset-password during a config write: %v, want errConfigLocked", rerr)
	}
}
