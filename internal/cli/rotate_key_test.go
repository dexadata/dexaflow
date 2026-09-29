package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/neochaotic/leoflow/internal/secrets"
)

// The rotation cipher must ENCRYPT with the new key and only DECRYPT with the
// old. Getting that order wrong rewrites the whole table back onto the key the
// user is trying to leave, and reports success.
func TestRotationCipherEncryptsWithTheNewKeyOnly(t *testing.T) {
	newKey := "a-brand-new-per-install-key-32b!"
	current := "the-key-being-retired-32bytes!!!"

	c, err := rotationCipher(newKey, current, "")
	if err != nil {
		t.Fatalf("rotationCipher: %v", err)
	}
	sealed, serr := c.Encrypt("hunter2")
	if serr != nil {
		t.Fatal(serr)
	}

	// Readable with the new key.
	if plain, derr := mustCLICipher(t, newKey).Decrypt(sealed); derr != nil || plain != "hunter2" {
		t.Errorf("the new key cannot read what the rotation wrote: %q %v", plain, derr)
	}
	// NOT readable with the retiring key: otherwise the rotation is cosmetic.
	if _, derr := mustCLICipher(t, current).Decrypt(sealed); derr == nil {
		t.Error("the retiring key can still read what the rotation wrote; nothing was actually rotated")
	}
	// And it still reads what the retiring key wrote, or there is nothing to
	// migrate from.
	old, _ := mustCLICipher(t, current).Encrypt("older")
	if plain, derr := c.Decrypt(old); derr != nil || plain != "older" {
		t.Errorf("the rotation cannot read the retiring key's rows: %q %v", plain, derr)
	}
}

// A recorded predecessor is carried into the read set, so an install midway
// through an earlier rotation still migrates completely.
func TestRotationCipherKeepsARecordedPredecessor(t *testing.T) {
	c, err := rotationCipher("a-brand-new-per-install-key-32b!", "the-key-being-retired-32bytes!!!", devSecretKey)
	if err != nil {
		t.Fatalf("rotationCipher: %v", err)
	}
	ancient, _ := mustCLICipher(t, devSecretKey).Encrypt("from the published key")
	if plain, derr := c.Decrypt(ancient); derr != nil || plain != "from the published key" {
		t.Errorf("a row from the published key is unreachable during rotation: %q %v", plain, derr)
	}
}

// After a successful pass nothing is left for an older key to open, so recording
// one would keep a key alive that nothing needs, and for Lite that key is the
// one published in this repository.
func TestPersistRotatedKeyDropsThePredecessor(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".leoflow")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "config.yaml")
	body := "parser_cmd: \"p\"\nworkspace: \"/w\"\nlite_executor: \"subprocess\"\nlite_port: 8088\n" +
		"admin_email: \"a@b.c\"\nadmin_password_hash: \"$2a$12$h\"\njwt_secret: \"J\"\n" +
		"secret_key: \"OLD\"\nsecret_key_previous: \"" + devSecretKey + "\"\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	sec := configFileSecrets(cfg)
	if err := persistRotatedKey(cfg, home, sec, "NEW"); err != nil {
		t.Fatalf("persistRotatedKey: %v", err)
	}

	got := configFileSecrets(cfg)
	if got.secretKey != "NEW" {
		t.Errorf("secret_key = %q, want NEW", got.secretKey)
	}
	if got.secretKeyPrevious != "" {
		t.Errorf("secret_key_previous = %q, want it gone: nothing is left under it, and for Lite it is the published key", got.secretKeyPrevious)
	}
	if got.jwtSecret != "J" {
		t.Errorf("the JWT secret was lost by the rotation: %q", got.jwtSecret)
	}
	raw, _ := os.ReadFile(cfg)
	if fi, _ := os.Stat(cfg); fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode = %v after the rotation, want 0600:\n%s", fi.Mode().Perm(), raw)
	}
}

func mustCLICipher(t *testing.T, key string) secrets.Cipher {
	t.Helper()
	k, err := secrets.ParseKey(key)
	if err != nil {
		t.Fatalf("parsing key: %v", err)
	}
	c, cerr := secrets.NewAESGCM(k)
	if cerr != nil {
		t.Fatalf("building cipher: %v", cerr)
	}
	return c
}
