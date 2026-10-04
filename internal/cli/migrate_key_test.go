package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// No config.yaml: the command refuses and names `dexaflow setup` first. It
// does not invent a config, which is where attempt 3 lost keys.
func TestMigrateKeyRefusesWithoutAConfig(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	err := (&migrateKeyRun{out: &out, stateDir: dir, yes: true}).run(context.Background())
	if err == nil || !strings.Contains(err.Error()+out.String(), "dexaflow setup") {
		t.Fatalf("want a refusal naming `dexaflow setup`, got %v\n%s", err, out.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a refused run wrote %v", entries)
	}
}

// An unparseable config is a refusal, never "empty": nothing is written.
func TestMigrateKeyRefusesAnUnparseableConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	body := "secret_key: [oops\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := (&migrateKeyRun{out: &out, stateDir: dir, yes: true}).run(context.Background()); err == nil {
		t.Fatalf("an unparseable config must be refused:\n%s", out.String())
	}
	raw, _ := os.ReadFile(cfg)
	if string(raw) != body {
		t.Errorf("config changed: %q", raw)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 { // config.yaml and the lock file
		t.Errorf("a refused run left %d entries", len(entries))
	}
}

// Declining the prompt changes nothing and says so truthfully.
func TestMigrateKeyDeclinedPromptWritesNothing(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("workspace: /w\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	r := &migrateKeyRun{out: &out, stateDir: dir, confirm: func() bool { return false }}
	if err := r.run(context.Background()); err == nil {
		t.Fatal("a declined migration must not exit 0: the install is not migrated")
	}
	raw, _ := os.ReadFile(cfg)
	if string(raw) != "workspace: /w\n" {
		t.Errorf("config changed after a declined prompt: %q", raw)
	}
}

// The datastores migrate-key brought up are stopped BEFORE the config lock is
// released (ADR 0065 section 3, "leave the cluster as found"): a `dexaflow
// lite` waiting on that lock must not go on against a cluster that is being
// stopped under it.
func TestMigrateKeyLeavesItsDatastoresBeforeReleasingTheConfigLock(t *testing.T) {
	dir := t.TempDir()
	key := strings.Repeat("ab", 32)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("secret_key: "+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var left bool
	var lockErr error
	r := &migrateKeyRun{stateDir: dir, yes: true, leave: func() {
		left = true
		release, err := lockConfigDir(dir, configLockShared, false)
		if err == nil {
			release()
		}
		lockErr = err
	}}
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("an already migrated install must exit 0: %v", err)
	}
	if !left {
		t.Fatal("run did not leave its datastores")
	}
	if !errors.Is(lockErr, errConfigLocked) {
		t.Errorf("the config lock was free while the datastores were being left (err=%v)", lockErr)
	}
}

// Owner decision on ADR 0065 section 8: a migrate-key that finishes clean has
// scanned every datastore on disk under the config's key alone, so it removes
// config.yaml.pre-restore. A dry run, or a run that does not finish, keeps it.
func TestMigrateKeyRemovesPreRestoreOnlyAfterACleanFinish(t *testing.T) {
	key := strings.Repeat("cd", 32)
	setup := func(t *testing.T) (dir, pre string) {
		t.Helper()
		dir = t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("secret_key: "+key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		pre = filepath.Join(dir, preRestoreName)
		if err := os.WriteFile(pre, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir, pre
	}
	t.Run("clean finish removes it", func(t *testing.T) {
		dir, pre := setup(t)
		var out bytes.Buffer
		if err := (&migrateKeyRun{out: &out, stateDir: dir, yes: true}).run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(pre); !os.IsNotExist(err) {
			t.Errorf("%s survived a clean migrate-key", pre)
		}
		if !strings.Contains(out.String(), pre) {
			t.Errorf("the removal was not reported: %q", out.String())
		}
	})
	t.Run("dry run keeps it", func(t *testing.T) {
		dir, pre := setup(t)
		if err := (&migrateKeyRun{stateDir: dir, dryRun: true}).run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(pre); err != nil {
			t.Errorf("a dry run removed %s: %v", pre, err)
		}
	})
	t.Run("declined run keeps it", func(t *testing.T) {
		dir, pre := setup(t)
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("workspace: /w\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = (&migrateKeyRun{stateDir: dir, confirm: func() bool { return false }}).run(context.Background())
		if _, err := os.Stat(pre); err != nil {
			t.Errorf("a declined run removed %s: %v", pre, err)
		}
	})
}
