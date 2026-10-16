package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// selfSignedPEM returns a valid self-signed certificate in PEM form.
func selfSignedPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "leoflow-test-ca"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNewCATrustMissingFile(t *testing.T) {
	if _, err := newCATrust(filepath.Join(t.TempDir(), "absent.pem")); err == nil {
		t.Error("a missing CA file must error, not silently trust nothing")
	}
}

func TestNewCATrustRejectsNonPEM(t *testing.T) {
	for _, junk := range [][]byte{[]byte("not a certificate"), {}, []byte("-----BEGIN CERTIFICATE-----\ngarbage\n-----END CERTIFICATE-----\n")} {
		if _, err := newCATrust(writeFile(t, "bad.pem", junk)); err == nil {
			t.Errorf("a file with no valid certificate (%q) must error", junk)
		}
	}
}

func TestNewCATrustAcceptsValidCert(t *testing.T) {
	trust, err := newCATrust(writeFile(t, "ca.pem", selfSignedPEM(t)))
	if err != nil || trust == nil || trust.current() == nil {
		t.Fatalf("a valid CA cert should produce a pool, got trust=%v err=%v", trust, err)
	}
}

// TestDialPropagatesBadCAFile: the secure path with a broken CA file must fail
// to dial rather than silently fall back to no verification.
func TestDialPropagatesBadCAFile(t *testing.T) {
	if _, _, _, err := Dial("localhost:50051", "token", false, filepath.Join(t.TempDir(), "absent.pem")); err == nil {
		t.Error("Dial with an unreadable CA file should error")
	}
}
