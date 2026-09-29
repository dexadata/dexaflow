package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// config.Load overlays LEOFLOW_* environment variables on top of the file, so
// reading the key through it and writing the result back persists whatever the
// operator happened to have exported. That turns `leoflow lite reset-password`
// into a command that destroys the only copy of the key decrypting every stored
// connection. This repo's own e2e scripts export LEOFLOW_SECRET_KEY.
//
// Anything that REWRITES config.yaml must read the secrets from the file.
func TestConfigFileSecretsIgnoreTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "jwt_secret: \"FILE-JWT\"\nsecret_key: \"FILE-KEY\"\nsecret_key_previous: \"FILE-PREV\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEOFLOW_SECRET_KEY", "ENV-KEY-FROM-SHELL")
	t.Setenv("LEOFLOW_JWT_SECRET", "ENV-JWT-FROM-SHELL")

	got := configFileSecrets(path)
	if got.secretKey != "FILE-KEY" {
		t.Errorf("secretKey = %q, want the FILE value: persisting the shell value destroys the real key", got.secretKey)
	}
	if got.jwtSecret != "FILE-JWT" {
		t.Errorf("jwtSecret = %q, want the FILE value", got.jwtSecret)
	}
	if got.secretKeyPrevious != "FILE-PREV" {
		t.Errorf("secretKeyPrevious = %q, want the FILE value", got.secretKeyPrevious)
	}
}

// A missing file yields empties rather than an error: the caller is a
// best-effort config sync, and an absent config is not a failure.
func TestConfigFileSecretsToleratesAMissingFile(t *testing.T) {
	got := configFileSecrets(filepath.Join(t.TempDir(), "nope.yaml"))
	if got.secretKey != "" || got.jwtSecret != "" {
		t.Errorf("a missing file must yield empties, got %+v", got)
	}
}

// A rewrite must carry the predecessor forward too. Dropping it re-orphans
// exactly the rows the migration exists to save, and the command that does it
// is a password reset, which no user expects to touch their credentials.
func TestWriteLiteConfigKeepsThePredecessor(t *testing.T) {
	home := t.TempDir()
	lc := liteSettings{Workspace: "/w", Executor: "subprocess", AdminEmail: "a@b.c", Port: 8088}
	sec := liteFileSecrets{jwtSecret: "j", secretKey: "k", secretKeyPrevious: "older-key"}
	if err := writeLiteConfig(home, "parser", lc, "$2a$12$hash", sec); err != nil {
		t.Fatal(err)
	}
	got := configFileSecrets(filepath.Join(home, "config.yaml"))
	if got.secretKeyPrevious != "older-key" {
		t.Errorf("secret_key_previous = %q after a rewrite, want it preserved", got.secretKeyPrevious)
	}
	if got.secretKey != "k" || got.jwtSecret != "j" {
		t.Errorf("a rewrite lost another secret: %+v", got)
	}
}

// An install with nothing left under a predecessor must not carry the published
// constant forward forever.
func TestWriteLiteConfigOmitsAnAbsentPredecessor(t *testing.T) {
	home := t.TempDir()
	lc := liteSettings{Workspace: "/w", Executor: "subprocess", AdminEmail: "a@b.c", Port: 8088}
	if err := writeLiteConfig(home, "parser", lc, "$2a$12$hash", liteFileSecrets{jwtSecret: "j", secretKey: "k"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if strings.Contains(string(raw), "secret_key_previous") {
		t.Errorf("wrote a predecessor line for an install that has none:\n%s", raw)
	}
}
