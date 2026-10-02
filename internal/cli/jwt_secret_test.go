package cli

import (
	"strings"
	"testing"
)

// isLowerHex reports whether r is a lowercase hex digit (0-9, a-f). Used in the
// secret-shape assertion below; extracted so the loop body is a single positive
// predicate (staticcheck QF1001).
func isLowerHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
}

// TestGenerateJWTSecretLengthAndHex: the secret is 32 random bytes hex-encoded
// (64 chars, all lowercase hex), and each call returns a new value — so
// `dexaflow setup` rotates it on every fresh install (#121).
func TestGenerateJWTSecretLengthAndHex(t *testing.T) {
	a, err := generateJWTSecret()
	if err != nil {
		t.Fatalf("generateJWTSecret err = %v", err)
	}
	if len(a) != 64 {
		t.Errorf("secret length = %d, want 64 hex chars (32 bytes)", len(a))
	}
	for _, r := range a {
		if !isLowerHex(r) {
			t.Errorf("secret %q must be lowercase hex; bad rune %q", a, r)
			break
		}
	}
	b, _ := generateJWTSecret()
	if a == b {
		t.Errorf("two calls must produce different secrets (rotation property), got %q twice", a)
	}
}

// TestResolveLiteJWTSecretPrefersConfig: a configured per-install secret is
// returned verbatim — it is what makes a reinstall invalidate prior tokens.
func TestResolveLiteJWTSecretPrefersConfig(t *testing.T) {
	got := resolveLiteJWTSecret("install-X-secret")
	if got != "install-X-secret" {
		t.Errorf("resolveLiteJWTSecret with a configured secret must return it, got %q", got)
	}
}

// TestResolveLiteJWTSecretLegacyFallback: an empty config (legacy install with
// no `jwt_secret` field) falls back to the dev-only constant — so the upgrade
// does not break existing setups; the user is told to run `dexaflow setup` to
// rotate the secret per install.
func TestResolveLiteJWTSecretLegacyFallback(t *testing.T) {
	if got := resolveLiteJWTSecret(""); got != devJWTSecret {
		t.Errorf("empty config must fall back to the dev constant, got %q", got)
	}
	if !strings.HasPrefix(devJWTSecret, "dev-") {
		t.Errorf("the legacy fallback must be clearly a dev-only value, got %q", devJWTSecret)
	}
}
