package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An install created before per-install keys has no `secret_key:` in
// config.yaml. It must get one without a full rewrite: rewriting the file is
// how the other fields get clobbered, and one of them is the only copy of the
// JWT secret.
func TestBackfillSecretKeyAppendsWithoutTouchingTheRest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	before := "# Written by `leoflow setup` (Leoflow Lite).\nparser_cmd: \"python -m leoflow_parser\"\njwt_secret: \"KEEP-ME\"\nadmin_password_hash: \"$2a$12$KEEP\"\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}

	key, err := backfillSecretKey(path)
	if err != nil {
		t.Fatalf("backfillSecretKey: %v", err)
	}
	if key == "" || key == devSecretKey {
		t.Fatalf("must generate a real per-install key, got %q", key)
	}

	raw, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	got := string(raw)
	for _, keep := range []string{`jwt_secret: "KEEP-ME"`, `admin_password_hash: "$2a$12$KEEP"`, `parser_cmd: "python -m leoflow_parser"`} {
		if !strings.Contains(got, keep) {
			t.Errorf("backfill destroyed an existing field: %s is gone.\ngot:\n%s", keep, got)
		}
	}
	if !strings.Contains(got, `secret_key: "`+key+`"`) {
		t.Errorf("the generated key was not written.\ngot:\n%s", got)
	}
}

// Running it twice must not append a second key: the file would then have two
// `secret_key:` lines and the loader would pick one of them, silently.
func TestBackfillSecretKeyIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("jwt_secret: \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := backfillSecretKey(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := backfillSecretKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("a second backfill generated a NEW key (%q then %q); every row written under the first would be orphaned", first, second)
	}
	raw, _ := os.ReadFile(path)
	if n := strings.Count(string(raw), "secret_key:"); n != 1 {
		t.Errorf("config.yaml has %d secret_key lines, want exactly 1", n)
	}
}

// The file keeps its restrictive mode: it now holds the only copy of the key
// that decrypts every stored credential.
func TestBackfillSecretKeyKeepsThePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("jwt_secret: \"x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := backfillSecretKey(path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config.yaml mode = %v, want 0600: it holds the only copy of the encryption key", fi.Mode().Perm())
	}
}
