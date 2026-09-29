package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Lite secret key encrypts connection passwords at rest. It used to be a
// constant compiled into this repository, identical on every install on earth,
// so anyone holding a Lite database file read every credential in it (#486).
// It is now per-install, generated the same way the JWT secret is.
func TestGenerateSecretKeyIsRandomAndWellFormed(t *testing.T) {
	a, err := generateSecretKey()
	if err != nil {
		t.Fatalf("generateSecretKey err = %v", err)
	}
	if len(a) != 64 {
		t.Errorf("key length = %d, want 64 hex chars (32 bytes of real entropy)", len(a))
	}
	for _, r := range a {
		if !isLowerHex(r) {
			t.Errorf("key %q must be lowercase hex; bad rune %q", a, r)
			break
		}
	}
	b, _ := generateSecretKey()
	if a == b {
		t.Errorf("two calls must differ, or every install would share a key again; got %q twice", a)
	}
	if a == devSecretKey {
		t.Error("a generated key must never be the published constant")
	}
}

// A configured per-install key is used alone: an install that generated its own
// key never wrote anything under the published constant, so accepting that
// constant would downgrade it from "needs my key" to "needs a key on GitHub".
func TestSecretKeyListOmitsTheConstantForAFreshInstall(t *testing.T) {
	if got := liteSecretKeyList("install-X-key", ""); got != "install-X-key" {
		t.Errorf("a fresh install must use its key alone, got %q", got)
	}
}

// A migrated install carries its predecessor so older rows stay readable, with
// the encrypting key FIRST.
func TestSecretKeyListPutsTheEncryptingKeyFirst(t *testing.T) {
	got := liteSecretKeyList("new-key", devSecretKey)
	if got != "new-key,"+devSecretKey {
		t.Errorf("list = %q; the encrypting key must lead and the predecessor follow", got)
	}
}

// If the backfill could not run, keep the install working rather than making
// every credential unreadable.
func TestSecretKeyListFallsBackWhenNothingCouldBeWritten(t *testing.T) {
	if got := liteSecretKeyList("", ""); got != devSecretKey {
		t.Errorf("with no key at all the install must still boot on the old constant, got %q", got)
	}
}

// The env must carry the key LIST, not a separate variable, and must never
// hand the server the published constant as its encrypting key.
func TestLiteServerEnvCarriesTheKeyList(t *testing.T) {
	env := sharedServerEnv(liteEnvParams{
		jwtSecret:         "jwt-x",
		secretKey:         "per-install-key",
		secretKeyPrevious: devSecretKey,
	})
	joined := strings.Join(env, "\n")

	if !strings.Contains(joined, "LEOFLOW_SECRET_KEY=per-install-key,"+devSecretKey) {
		t.Errorf("the server must receive the key list with the new key first; env was:\n%s", joined)
	}
	if strings.Contains(joined, "LEOFLOW_SECRET_KEY="+devSecretKey+",") || strings.Contains(joined, "LEOFLOW_SECRET_KEY="+devSecretKey+"\n") {
		t.Error("the published constant must never lead the list; it would keep encrypting")
	}
	if strings.Contains(joined, "LEOFLOW_SECRET_KEY_FALLBACK") {
		t.Error("the separate fallback variable is gone; a *_FALLBACK name reads as 'used when the primary is absent', which is the opposite of a decrypt-only predecessor")
	}
}

// A fresh install must not be handed the published constant at all.
func TestLiteServerEnvOnAFreshInstall(t *testing.T) {
	env := sharedServerEnv(liteEnvParams{jwtSecret: "jwt-x", secretKey: "only-mine"})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "LEOFLOW_SECRET_KEY=only-mine\n") && !strings.HasSuffix(joined, "LEOFLOW_SECRET_KEY=only-mine") {
		t.Errorf("a fresh install must get its key alone; env was:\n%s", joined)
	}
	if strings.Contains(joined, devSecretKey) {
		t.Error("the published constant reached a fresh install, which never wrote anything under it")
	}
}

// A password reset rewrites config.yaml. It must carry BOTH per-install secrets
// forward: dropping the JWT secret logs everyone out (#121), and dropping the
// secret key makes every stored connection password undecryptable (#486).
func TestWriteLiteConfigRoundTripsBothSecrets(t *testing.T) {
	home := t.TempDir()
	lc := liteSettings{Workspace: "/w", Executor: "subprocess", AdminEmail: "a@b.c", Port: 8088}
	if err := writeLiteConfig(home, "parser", lc, "$2a$12$hash", liteFileSecrets{jwtSecret: "the-jwt-secret", secretKey: "the-secret-key"}); err != nil {
		t.Fatalf("writeLiteConfig: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	got := string(raw)
	for _, want := range []string{`jwt_secret: "the-jwt-secret"`, `secret_key: "the-secret-key"`} {
		if !strings.Contains(got, want) {
			t.Errorf("config.yaml is missing %s; a rewrite that drops it orphans what it protects.\ngot:\n%s", want, got)
		}
	}
}
