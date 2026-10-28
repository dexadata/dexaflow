package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// testCA is a CA that can sign server leaves for 127.0.0.1.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, name string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// leaf signs a server certificate for 127.0.0.1.
func (ca testCA) leaf(t *testing.T) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "dexaflow-scheduler"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// writeCA overwrites the bundle file the way a ConfigMap volume update does.
func writeCA(t *testing.T, path string, bundles ...[]byte) {
	t.Helper()
	if err := os.WriteFile(path, bytes.Join(bundles, nil), 0o600); err != nil {
		t.Fatal(err)
	}
}

// droppingListener records the connections it accepted so a test can sever
// them all and make the client reconnect (a scheduler restart, a network blip).
type droppingListener struct {
	net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func (l *droppingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.mu.Lock()
		l.conns = append(l.conns, c)
		l.mu.Unlock()
	}
	return c, err
}

func (l *droppingListener) dropAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		_ = c.Close()
	}
	l.conns = nil
}

type unimplementedAgentServer struct {
	agentv1.UnimplementedAgentServiceServer
}

// TestDialTrustsARotatedCABundleOnReconnect drives the agent's real Dial
// through a CA rotation done the documented way (#637): the CA bundle first
// gains the new CA next to the old one, the server then switches to a leaf of
// the new CA, a broken bundle write is ignored, and the old CA is finally
// removed. The agent keeps one ClientConn for the whole rotation, as a long
// running task does, and every reconnect must still verify the server.
func TestDialTrustsARotatedCABundleOnReconnect(t *testing.T) {
	oldCA, newCA := newTestCA(t, "old-ca"), newTestCA(t, "new-ca")
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	writeCA(t, caFile, oldCA.pem)

	var served atomic.Pointer[tls.Certificate]
	served.Store(oldCA.leaf(t))
	srvCreds := credentials.NewTLS(&tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return served.Load(), nil },
	})
	var lc net.ListenConfig
	inner, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lis := &droppingListener{Listener: inner}
	srv := grpc.NewServer(grpc.Creds(srvCreds))
	agentv1.RegisterAgentServiceServer(srv, unimplementedAgentServer{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	client, conn, _, err := Dial(inner.Addr().String(), "token", false, caFile)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// reachesWithin reports whether an RPC gets through the TLS handshake within
	// d: the test server implements nothing, so a verified connection answers
	// Unimplemented. It retries because an RPC racing the dropped connection
	// fails with Unavailable before the client reconnects.
	reachesWithin := func(d time.Duration) error {
		deadline := time.Now().Add(d)
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			_, err := client.Heartbeat(ctx, &agentv1.HeartbeatRequest{}, grpc.WaitForReady(true))
			cancel()
			if status.Code(err) == codes.Unimplemented {
				return nil
			}
			if time.Now().After(deadline) {
				return err
			}
		}
	}
	reaches := func(step string) {
		t.Helper()
		if err := reachesWithin(5 * time.Second); err != nil {
			t.Fatalf("%s: Heartbeat = %v, want the server reached (Unimplemented)", step, err)
		}
	}

	reaches("old CA, old leaf")

	writeCA(t, caFile, oldCA.pem, newCA.pem)
	served.Store(newCA.leaf(t))
	lis.dropAll()
	reaches("union bundle, new leaf, after reconnect")

	writeCA(t, caFile, []byte("-----BEGIN CERTIFICATE-----\nhalf written"))
	lis.dropAll()
	reaches("broken bundle write, after reconnect (last good bundle kept)")

	writeCA(t, caFile, newCA.pem)
	lis.dropAll()
	reaches("new CA only, after reconnect")

	served.Store(oldCA.leaf(t))
	lis.dropAll()
	if err := reachesWithin(2 * time.Second); err == nil {
		t.Fatal("an old-CA leaf was accepted after the old CA left the bundle, want verification to fail")
	}
}

// TestCATrustUnionBundleVerifiesBothCAs: a bundle holding two CAs trusts a
// leaf of either, the state every agent must be in while a CA rotates.
func TestCATrustUnionBundleVerifiesBothCAs(t *testing.T) {
	oldCA, newCA := newTestCA(t, "old-ca"), newTestCA(t, "new-ca")
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	writeCA(t, caFile, oldCA.pem, newCA.pem)
	trust, err := newCATrust(caFile)
	if err != nil {
		t.Fatalf("newCATrust: %v", err)
	}
	for name, ca := range map[string]testCA{"old": oldCA, "new": newCA} {
		leaf, perr := x509.ParseCertificate(ca.leaf(t).Certificate[0])
		if perr != nil {
			t.Fatal(perr)
		}
		if _, verr := leaf.Verify(x509.VerifyOptions{Roots: trust.current()}); verr != nil {
			t.Errorf("%s CA leaf rejected by the union bundle: %v", name, verr)
		}
	}
}

// TestCATrustKeepsLastGoodBundleAndWarnsOnce: a bundle that stops parsing (or
// disappears) leaves the last good pool in place and is logged once per broken
// content, not on every handshake; a later good bundle is picked up.
func TestCATrustKeepsLastGoodBundleAndWarnsOnce(t *testing.T) {
	first, second := newTestCA(t, "first"), newTestCA(t, "second")
	caFile := filepath.Join(t.TempDir(), "ca.crt")
	writeCA(t, caFile, first.pem)
	trust, err := newCATrust(caFile)
	if err != nil {
		t.Fatalf("newCATrust: %v", err)
	}
	var logs strings.Builder
	trust.log = slogTo(&logs)
	good := trust.current()

	writeCA(t, caFile, []byte("not a certificate"))
	for range 3 {
		if got := trust.current(); got != good {
			t.Fatal("a broken bundle replaced the last good pool")
		}
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Errorf("logged %d warnings for one broken bundle over 3 handshakes, want 1:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), caFile) {
		t.Errorf("warning does not name the CA file:\n%s", logs.String())
	}

	if err := os.Remove(caFile); err != nil {
		t.Fatal(err)
	}
	if got := trust.current(); got != good {
		t.Fatal("a missing bundle replaced the last good pool")
	}

	writeCA(t, caFile, second.pem)
	if got := trust.current(); got == good {
		t.Error("a valid new bundle was not picked up")
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 2 {
		t.Errorf("logged %d warnings, want 2 (the broken bundle, then the missing file):\n%s", n, logs.String())
	}
}

func slogTo(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }
