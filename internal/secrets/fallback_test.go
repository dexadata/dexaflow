package secrets_test

import (
	"testing"

	"github.com/neochaotic/leoflow/internal/secrets"
)

// The Lite fixed key was published in this repository, so every install shared
// it (#486). Rotating to a per-install key cannot orphan what the old key
// encrypted, which is what this cipher exists to prevent: it writes under the
// new key and still reads what the old one wrote.
func TestFallbackReadsLegacyAndWritesPrimary(t *testing.T) {
	legacy := newCipher(t, "legacy-insecure-key-32bytes!!!!!")
	primary := newCipher(t, "a-fresh-per-install-key-32byte!!")

	old, err := legacy.Encrypt("hunter2")
	if err != nil {
		t.Fatalf("seeding legacy ciphertext: %v", err)
	}

	c := secrets.WithFallback(primary, legacy)

	// Reads what the old key wrote, and says it came from the fallback so the
	// caller can re-encrypt it.
	plain, stale, err := c.DecryptStale(old)
	if err != nil {
		t.Fatalf("decrypting a legacy value: %v", err)
	}
	if plain != "hunter2" {
		t.Errorf("legacy plaintext = %q, want %q", plain, "hunter2")
	}
	if !stale {
		t.Error("a value encrypted under the legacy key must report stale=true, or nothing would ever be re-encrypted")
	}

	// Writes under the primary, never the legacy.
	fresh, err := c.Encrypt("hunter2")
	if err != nil {
		t.Fatalf("encrypting: %v", err)
	}
	if _, derr := legacy.Decrypt(fresh); derr == nil {
		t.Error("a value written by the fallback cipher decrypts under the LEGACY key; the rotation would be cosmetic")
	}
	if _, derr := primary.Decrypt(fresh); derr != nil {
		t.Errorf("a value written by the fallback cipher does not decrypt under the primary key: %v", derr)
	}

	// A primary value is not stale: re-encrypting it every read would be churn.
	_, stale, err = c.DecryptStale(fresh)
	if err != nil {
		t.Fatalf("decrypting a primary value: %v", err)
	}
	if stale {
		t.Error("a value already under the primary key must report stale=false")
	}
}

// Garbage must fail, not fall through to a wrong plaintext. AES-GCM is
// authenticated, which is the property that makes try-primary-then-legacy safe
// at all; this locks it.
func TestFallbackRefusesUndecryptable(t *testing.T) {
	c := secrets.WithFallback(
		newCipher(t, "a-fresh-per-install-key-32byte!!"),
		newCipher(t, "legacy-insecure-key-32bytes!!!!!"),
	)
	third := newCipher(t, "a-third-unrelated-key-32bytes!!!")
	foreign, err := third.Encrypt("secret")
	if err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if _, _, derr := c.DecryptStale(foreign); derr == nil {
		t.Error("a value under an unknown key decrypted; the fallback must refuse rather than return wrong plaintext")
	}
}

// With no legacy key the cipher must behave exactly like the primary, so Pro
// (which has no fixed key to migrate from) is unaffected.
func TestFallbackWithNoLegacyIsTransparent(t *testing.T) {
	primary := newCipher(t, "a-fresh-per-install-key-32byte!!")
	c := secrets.WithFallback(primary, nil)
	enc, err := c.Encrypt("v")
	if err != nil {
		t.Fatalf("encrypting: %v", err)
	}
	plain, stale, err := c.DecryptStale(enc)
	if err != nil || plain != "v" {
		t.Fatalf("round trip failed: %q, %v", plain, err)
	}
	if stale {
		t.Error("with no legacy key nothing can be stale")
	}
}

func newCipher(t *testing.T, key string) secrets.Cipher {
	t.Helper()
	k, err := secrets.ParseKey(key)
	if err != nil {
		t.Fatalf("parsing key %q: %v", key, err)
	}
	c, err := secrets.NewAESGCM(k)
	if err != nil {
		t.Fatalf("building cipher: %v", err)
	}
	return c
}
