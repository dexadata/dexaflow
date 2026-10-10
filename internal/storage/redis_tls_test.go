package storage

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
)

// freshCAPEM mints a self-signed CA cert and returns it as PEM. We do this at
// test time rather than ship a static PEM constant so the test is robust to
// editor mangling (whitespace, line endings) and immune to clock-related
// "not after" surprises if a hard-coded fixture ages out.
func freshCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "leoflow-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestRedisOptionsLoadsCAFromFile pins the #312 contract: when RedisSection
// has CAFile set, NewRedis (via redisOptions) overrides
// opts.TLSConfig.RootCAs with the bundle parsed from disk, so the client
// trusts the per-instance CA managed Redis (Memorystore SERVER_AUTHENTICATION,
// ElastiCache in-transit, Azure Cache) presents. Without this knob the only
// reachable TLS posture is "system roots" — empty against any managed
// provider.
func TestRedisOptionsLoadsCAFromFile(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, freshCAPEM(t), 0o600); err != nil {
		t.Fatalf("seed CA file: %v", err)
	}
	opts, err := redisOptions("rediss://example.invalid:6380/0", caPath)
	if err != nil {
		t.Fatalf("redisOptions: %v", err)
	}
	if opts.TLSConfig == nil {
		t.Fatal("expected TLSConfig to be initialized")
	}
	if opts.TLSConfig.RootCAs == nil {
		t.Error("expected RootCAs to be populated from the CA file")
	}
	// Defensive minimum: the override MUST keep TLS 1.2 as the floor —
	// implicitly via the redis.ParseURL path or explicitly when we
	// initialize TLSConfig from scratch.
	if opts.TLSConfig.MinVersion != 0 && opts.TLSConfig.MinVersion < tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want >= TLS 1.2", opts.TLSConfig.MinVersion)
	}
}

// TestRedisOptionsCAFileMissingFailsLoud covers the misconfig shape: an
// operator typo'd the path or the ConfigMap was not mounted. We want a clear
// error at boot, not a silent fallback to system roots (which would then
// fail the Ping with a confusing x509 error from the *real* CA mismatch).
func TestRedisOptionsCAFileMissingFailsLoud(t *testing.T) {
	_, err := redisOptions("rediss://example.invalid:6380/0", "/nonexistent/ca.crt")
	if err == nil {
		t.Fatal("expected an error for a missing CA file, got nil")
	}
	if !strings.Contains(err.Error(), "ca") && !strings.Contains(err.Error(), "CA") {
		t.Errorf("error should name the CA file as the cause; got %q", err.Error())
	}
}

// TestRedisOptionsCAFileMalformedFailsLoud covers the second misconfig: the
// file exists but is not PEM (operator pointed at the wrong file, the
// ConfigMap held a binary cert, etc.). Same posture: clear error at boot.
func TestRedisOptionsCAFileMalformedFailsLoud(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(caPath, []byte("not a PEM file at all"), 0o600); err != nil {
		t.Fatalf("seed bad CA file: %v", err)
	}
	_, err := redisOptions("rediss://example.invalid:6380/0", caPath)
	if err == nil {
		t.Fatal("expected an error for malformed PEM, got nil")
	}
}

// TestRedisOptionsEmptyCAFilePassesThrough: the default path (no CA file
// configured) must behave exactly like before this PR — system roots only,
// no TLSConfig surgery. This protects every Lite + non-managed Redis install
// from a regression caused by the new branch.
func TestRedisOptionsEmptyCAFilePassesThrough(t *testing.T) {
	// Use a `rediss://` URL so ParseURL builds a TLSConfig; we then assert
	// our code did NOT replace its RootCAs.
	opts, err := redisOptions("rediss://example.invalid:6380/0", "")
	if err != nil {
		t.Fatalf("redisOptions: %v", err)
	}
	if opts.TLSConfig == nil {
		t.Fatal("rediss:// URL must yield a TLSConfig (system roots)")
	}
	if opts.TLSConfig.RootCAs != nil {
		t.Error("with no CAFile, RootCAs should be nil (i.e. system roots), not an explicit pool")
	}
}

// Probe the config wiring so the env var → RedisSection.CAFile mapping is
// observable at compile time even if no test exercises ServerConfig
// directly here. A compile failure here flags a rename of the mapstructure
// tag or the field.
var _ = config.RedisSection{CAFile: ""}
