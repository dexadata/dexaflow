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

// A configured per-install key is returned verbatim: that is the whole point.
func TestResolveLiteSecretKeyPrefersConfig(t *testing.T) {
	if got := resolveLiteSecretKey("install-X-key"); got != "install-X-key" {
		t.Errorf("resolveLiteSecretKey with a configured key must return it, got %q", got)
	}
}

// A legacy install has no secret_key in config.yaml. Falling back to the
// constant keeps its existing connections readable; refusing would break an
// upgrade for someone who did nothing wrong.
func TestResolveLiteSecretKeyFallsBackForLegacyInstalls(t *testing.T) {
	if got := resolveLiteSecretKey(""); got != devSecretKey {
		t.Errorf("an empty key must fall back to the legacy constant so an existing install keeps working, got %q", got)
	}
}

// The env handed to the server must carry the RESOLVED key, and must name the
// legacy constant separately so the server can still read rows written under
// it. Without the fallback var, rotating the key orphans every existing
// credential, which is worse than the published key it replaces.
func TestLiteServerEnvCarriesResolvedKeyAndLegacyFallback(t *testing.T) {
	env := sharedServerEnv(liteEnvParams{
		jwtSecret: "jwt-x",
		secretKey: "per-install-key",
	})
	joined := strings.Join(env, "\n")

	if !strings.Contains(joined, "LEOFLOW_SECRET_KEY=per-install-key") {
		t.Errorf("the server must receive the per-install key; env was:\n%s", joined)
	}
	if strings.Contains(joined, "LEOFLOW_SECRET_KEY="+devSecretKey) {
		t.Error("the server must not receive the published constant as its PRIMARY key; the rotation would be cosmetic")
	}
	if !strings.Contains(joined, "LEOFLOW_SECRET_KEY_FALLBACK="+devSecretKey) {
		t.Errorf("without the legacy key as a fallback, existing connections become undecryptable; env was:\n%s", joined)
	}
}

// A legacy install, with no key configured, must still boot: primary and
// fallback both resolve to the constant, so nothing is orphaned and nothing
// pretends to be rotated.
func TestLiteServerEnvOnALegacyInstall(t *testing.T) {
	env := sharedServerEnv(liteEnvParams{jwtSecret: "jwt-x", secretKey: ""})
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "LEOFLOW_SECRET_KEY="+devSecretKey) {
		t.Errorf("a legacy install must still get a usable key; env was:\n%s", joined)
	}
}

// A password reset rewrites config.yaml. It must carry BOTH per-install secrets
// forward: dropping the JWT secret logs everyone out (#121), and dropping the
// secret key makes every stored connection password undecryptable, because the
// next boot falls back to the published constant while the rows are encrypted
// under the real key (#486).
func TestWriteLiteConfigRoundTripsBothSecrets(t *testing.T) {
	home := t.TempDir()
	lc := liteSettings{Workspace: "/w", Executor: "subprocess", AdminEmail: "a@b.c", Port: 8088}
	if err := writeLiteConfig(home, "parser", lc, "$2a$12$hash", "the-jwt-secret", "the-secret-key"); err != nil {
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
