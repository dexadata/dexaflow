package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Gap 7: a key exported in the operator's shell must not reach the Lite server.
// The server prefers DEXAFLOW_* over LEOFLOW_*, so an inherited
// DEXAFLOW_SECRET_KEY would have silently beaten the LEOFLOW_SECRET_KEY Lite
// builds from config.yaml, and new rows would land under a key the file does
// not record.
func TestLiteServerEnvironIgnoresAnExportedKey(t *testing.T) {
	base := []string{
		"PATH=/bin",
		"DEXAFLOW_SECRET_KEY=from-the-shell",
		"LEOFLOW_SECRET_KEY=also-from-the-shell",
		"DEXAFLOW_SECRET_KEY_REENCRYPT_ON_BOOT=true",
		"LEOFLOW_SECRET_KEY_MIGRATION_LOCK=false",
	}
	env := liteServerEnviron(base, sharedServerEnv(liteEnvParams{secretKey: "from-the-file"}))
	joined := strings.Join(env, "\n")
	for _, bad := range []string{"from-the-shell", "SECRET_KEY_REENCRYPT_ON_BOOT=true", "SECRET_KEY_MIGRATION_LOCK=false"} {
		if strings.Contains(joined, bad) {
			t.Errorf("inherited %q reached the server:\n%s", bad, joined)
		}
	}
	for _, want := range []string{"PATH=/bin", "LEOFLOW_SECRET_KEY=from-the-file", "LEOFLOW_SECRET_KEY_REENCRYPT_ON_BOOT=false", "LEOFLOW_SECRET_KEY_MIGRATION_LOCK=true"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q:\n%s", want, joined)
		}
	}
}

// One line says the exported key was ignored, so nobody believes it was used.
func TestEnvKeyNoteSaysAnExportedKeyIsIgnored(t *testing.T) {
	env := map[string]string{"LEOFLOW_SECRET_KEY": "envkey"}
	getenv := func(k string) string { return env[k] }
	if note := envKeyNote("filekey", getenv); !strings.Contains(note, "LEOFLOW_SECRET_KEY") || !strings.Contains(note, "ignored") {
		t.Errorf("note %q must name the variable and say it is ignored", note)
	}
	if note := envKeyNote("envkey", getenv); note != "" {
		t.Errorf("an exported key equal to the file's needs no note, got %q", note)
	}
	if note := envKeyNote("filekey", func(string) string { return "" }); note != "" {
		t.Errorf("no variable, no note; got %q", note)
	}
}

// Degenerate config: a predecessor with no secret_key is Legacy with an extra
// predecessor, so the server must read with both (ADR 0065 section 6).
func TestSecretKeyListLegacyWithAHandSetPredecessor(t *testing.T) {
	if got := liteSecretKeyList("", "hand-set"); got != devSecretKey+",hand-set" {
		t.Errorf("got %q, want the constant then the hand-set key", got)
	}
}

func fakeServer(t *testing.T, versionOut string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dexaflow-server")
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then printf '" + versionOut + "'; exit 0; fi\necho booted >&2; exit 3\n"
	if err := os.WriteFile(p, []byte(script), 0o700); err != nil { //nolint:gosec // test executable
		t.Fatal(err)
	}
	return p
}

// "No lock, no boot": `dexaflow lite` refuses a server binary that does not
// advertise the key-migration lock (an older binary found on PATH or passed
// with --server-bin would ignore the setting and run unprotected).
func TestCheckServerKeyLock(t *testing.T) {
	ok := fakeServer(t, `dexaflow-server v0.5.1 (commit x)\ncapabilities: lite-key-lock\n`)
	if err := checkServerKeyLock(context.Background(), ok); err != nil {
		t.Errorf("a server advertising the lock was refused: %v", err)
	}
	old := fakeServer(t, `dexaflow-server v0.5.0 (commit y)\n`)
	err := checkServerKeyLock(context.Background(), old)
	if err == nil || !strings.Contains(err.Error(), old) || !strings.Contains(err.Error(), "--server-bin") {
		t.Errorf("an old server must be refused, naming it and --server-bin; got %v", err)
	}
}

// The owner's rule for restore: config.yaml.pre-restore is kept until the next
// successful boot, then removed, and the boot says so.
func TestRemovePreRestoreAfterABoot(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, preRestoreName)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	removePreRestore(&out, dir)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("%s survived a successful boot", p)
	}
	if !strings.Contains(out.String(), p) {
		t.Errorf("the boot did not say what it removed: %q", out.String())
	}
	out.Reset()
	removePreRestore(&out, dir)
	if out.Len() != 0 {
		t.Errorf("nothing to remove must print nothing, got %q", out.String())
	}
}

// A `dexaflow lite` that did not start the managed cluster must not stop it on
// the way out: the cluster may belong to a running migration (ADR 0065 section
// 3, "leave the cluster as found").
func TestManagedCleanupOnlyStopsWhatItStarted(t *testing.T) {
	stopped := false
	stop := func() { stopped = true }
	managedCleanup(false, stop)()
	if stopped {
		t.Error("stopped a cluster this run found already running")
	}
	managedCleanup(true, stop)()
	if !stopped {
		t.Error("did not stop the cluster this run started")
	}
}
