package main

import (
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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
)

// testCert is a self-signed certificate for 127.0.0.1 written to PEM files.
type testCert struct {
	certPath, keyPath string
	serial            *big.Int
	pool              *x509.CertPool
}

// writeTestCert mints a self-signed P-256 certificate for 127.0.0.1 with the
// given serial and writes it to dir/tls.crt and dir/tls.key, overwriting any
// previous pair (the shape of a Kubernetes Secret volume after rotation).
func writeTestCert(t *testing.T, dir string, serial int64) testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serial),
		Subject:               pkix.Name{CommonName: "dexaflow-api"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	c := testCert{certPath: filepath.Join(dir, "tls.crt"), keyPath: filepath.Join(dir, "tls.key"), serial: big.NewInt(serial)}
	if werr := os.WriteFile(c.certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if werr := os.WriteFile(c.keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); werr != nil {
		t.Fatal(werr)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	c.pool = x509.NewCertPool()
	c.pool.AddCert(parsed)
	return c
}

// freeAddr returns a loopback address with a port nothing listens on.
func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// startServing runs serve (the function every listener goes through) on srv
// and waits until the address accepts connections.
func startServing(t *testing.T, srv *http.Server) {
	t.Helper()
	errCh := make(chan error, 1)
	go serve(srv, errCh)
	t.Cleanup(func() { _ = srv.Close() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			t.Fatalf("serve: %v", err)
		default:
		}
		d := net.Dialer{Timeout: 100 * time.Millisecond}
		if c, err := d.DialContext(context.Background(), "tcp", srv.Addr); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never accepted connections", srv.Addr)
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
}

// TestConfigureAPITLSLeavesPlainHTTPByDefault: with no cert configured the API
// server gets no TLS config and serve keeps answering plain HTTP, today's
// behavior (#638).
func TestConfigureAPITLSLeavesPlainHTTPByDefault(t *testing.T) {
	srv := &http.Server{Addr: freeAddr(t), Handler: okHandler(), ReadHeaderTimeout: time.Second}
	if err := configureAPITLS(srv, config.ServerSection{}, nil); err != nil {
		t.Fatalf("configureAPITLS: %v", err)
	}
	if srv.TLSConfig != nil {
		t.Fatal("TLSConfig set with no cert configured, want nil (plain HTTP)")
	}
	startServing(t, srv)
	resp, err := http.Get("http://" + srv.Addr + "/") //nolint:noctx // test request
	if err != nil {
		t.Fatalf("plain HTTP GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("plain HTTP status = %d, want 200", resp.StatusCode)
	}
}

// TestConfigureAPITLSServesHTTPS: with a cert and key configured, the listener
// completes a TLS handshake a client verifying that cert accepts, refuses TLS
// below 1.2, and no longer answers plain HTTP.
func TestConfigureAPITLSServesHTTPS(t *testing.T) {
	cert := writeTestCert(t, t.TempDir(), 1)
	srv := &http.Server{Addr: freeAddr(t), Handler: okHandler(), ReadHeaderTimeout: time.Second}
	if err := configureAPITLS(srv, config.ServerSection{HTTPTLSCert: cert.certPath, HTTPTLSKey: cert.keyPath}, nil); err != nil {
		t.Fatalf("configureAPITLS: %v", err)
	}
	startServing(t, srv)

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: cert.pool, MinVersion: tls.VersionTLS12}}}
	resp, err := client.Get("https://" + srv.Addr + "/") //nolint:noctx // test request
	if err != nil {
		t.Fatalf("HTTPS GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Errorf("HTTPS GET = %d %q, want 200 ok", resp.StatusCode, body)
	}
	if resp.TLS == nil || resp.TLS.PeerCertificates[0].SerialNumber.Cmp(cert.serial) != 0 {
		t.Error("the HTTPS response was not served with the configured certificate")
	}

	old := &tls.Config{RootCAs: cert.pool, MinVersion: tls.VersionTLS11, MaxVersion: tls.VersionTLS11} //nolint:gosec // proving the server refuses it
	if conn, derr := (&tls.Dialer{Config: old}).DialContext(context.Background(), "tcp", srv.Addr); derr == nil {
		_ = conn.Close()
		t.Error("a TLS 1.1 handshake succeeded, want the server to require TLS 1.2 or later")
	}

	plain, err := http.Get("http://" + srv.Addr + "/") //nolint:noctx // test request
	if err != nil {
		t.Fatalf("plain HTTP GET: %v", err)
	}
	_ = plain.Body.Close()
	if plain.StatusCode != http.StatusBadRequest {
		t.Errorf("plain HTTP to the TLS listener = %d, want 400 (the listener speaks TLS only)", plain.StatusCode)
	}
}

// TestConfigureAPITLSFailsFastOnUnreadableFiles: a missing or invalid pair is a
// boot error naming the files, not a listener that fails every handshake.
func TestConfigureAPITLSFailsFastOnUnreadableFiles(t *testing.T) {
	dir := t.TempDir()
	missing := config.ServerSection{HTTPTLSCert: filepath.Join(dir, "nope.crt"), HTTPTLSKey: filepath.Join(dir, "nope.key")}
	err := configureAPITLS(&http.Server{}, missing, nil)
	if err == nil {
		t.Fatal("configureAPITLS with missing files = nil, want a boot error")
	}
	if !strings.Contains(err.Error(), "nope.crt") {
		t.Errorf("error %q does not name the certificate path", err)
	}

	garbage := filepath.Join(dir, "garbage.pem")
	if werr := os.WriteFile(garbage, []byte("not pem"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if err := configureAPITLS(&http.Server{}, config.ServerSection{HTTPTLSCert: garbage, HTTPTLSKey: garbage}, nil); err == nil {
		t.Error("configureAPITLS with invalid PEM = nil, want a boot error")
	}
}

// TestConfigureAPITLSPicksUpARotatedCert: cert-manager and a re-created Secret
// replace the files in place; the next handshake serves the new certificate
// without a restart.
func TestConfigureAPITLSPicksUpARotatedCert(t *testing.T) {
	dir := t.TempDir()
	first := writeTestCert(t, dir, 1)
	srv := &http.Server{Addr: freeAddr(t), Handler: okHandler(), ReadHeaderTimeout: time.Second}
	if err := configureAPITLS(srv, config.ServerSection{HTTPTLSCert: first.certPath, HTTPTLSKey: first.keyPath}, nil); err != nil {
		t.Fatalf("configureAPITLS: %v", err)
	}
	startServing(t, srv)

	if got := servedSerial(t, srv.Addr); got.Cmp(first.serial) != 0 {
		t.Fatalf("served serial = %v, want %v", got, first.serial)
	}

	second := writeTestCert(t, dir, 2)
	// Make the change visible to an mtime check even on a coarse clock.
	future := time.Now().Add(2 * time.Second)
	for _, p := range []string{second.certPath, second.keyPath} {
		if err := os.Chtimes(p, future, future); err != nil {
			t.Fatal(err)
		}
	}
	if got := servedSerial(t, srv.Addr); got.Cmp(second.serial) != 0 {
		t.Errorf("served serial after rotation = %v, want %v (the rotated certificate)", got, second.serial)
	}
}

// TestConfigureAPITLSKeepsServingWhenARotationIsHalfWritten: a reload that
// fails (the cert written, the key not yet) keeps the last good pair instead of
// failing handshakes.
func TestConfigureAPITLSKeepsServingWhenARotationIsHalfWritten(t *testing.T) {
	dir := t.TempDir()
	first := writeTestCert(t, dir, 1)
	srv := &http.Server{Addr: freeAddr(t), Handler: okHandler(), ReadHeaderTimeout: time.Second}
	if err := configureAPITLS(srv, config.ServerSection{HTTPTLSCert: first.certPath, HTTPTLSKey: first.keyPath}, nil); err != nil {
		t.Fatalf("configureAPITLS: %v", err)
	}
	startServing(t, srv)

	future := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(first.keyPath, []byte("half written"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(first.keyPath, future, future); err != nil {
		t.Fatal(err)
	}
	if got := servedSerial(t, srv.Addr); got.Cmp(first.serial) != 0 {
		t.Errorf("served serial during a broken rotation = %v, want the last good %v", got, first.serial)
	}
}

// servedSerial completes a handshake (without verification, the test only
// reads which certificate came back) and returns the leaf's serial.
func servedSerial(t *testing.T, addr string) *big.Int {
	t.Helper()
	d := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}} //nolint:gosec // reading the served cert only
	conn, err := d.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("TLS dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		t.Fatalf("dialed %T, want *tls.Conn", conn)
	}
	return tlsConn.ConnectionState().PeerCertificates[0].SerialNumber
}

// TestStartAPISideWiresHTTPTLS locks the wiring, not the leaf: the server the
// boot path builds from config carries the TLS config when the keys are set.
func TestStartAPISideWiresHTTPTLS(t *testing.T) {
	cert := writeTestCert(t, t.TempDir(), 7)
	cfg := &config.ServerConfig{}
	cfg.Auth.JWT.Secret = "secret"
	cfg.Server.HTTPAddr = "127.0.0.1:0"
	cfg.Server.HTTPTLSCert = cert.certPath
	cfg.Server.HTTPTLSKey = cert.keyPath
	reg := prometheus.NewRegistry()
	tel := &observability.Telemetry{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:  observability.NewMetrics(reg),
		Registry: reg,
		Tracer:   noop.NewTracerProvider().Tracer("test"),
	}

	srv, err := startAPISide(context.Background(), cfg, tel, nil, &storage.Postgres{}, nil, nil, nil, nil, nil, api.ExecutorInfo{}, nil, true)
	if err != nil {
		t.Fatalf("startAPISide: %v", err)
	}
	if srv.TLSConfig == nil || srv.TLSConfig.GetCertificate == nil {
		t.Fatal("startAPISide built the API server without the configured TLS listener")
	}

	cfg.Server.HTTPTLSCert, cfg.Server.HTTPTLSKey = "", ""
	plain, err := startAPISide(context.Background(), cfg, tel, nil, &storage.Postgres{}, nil, nil, nil, nil, nil, api.ExecutorInfo{}, nil, true)
	if err != nil {
		t.Fatalf("startAPISide: %v", err)
	}
	if plain.TLSConfig != nil {
		t.Error("startAPISide set a TLS config with no cert configured, want plain HTTP")
	}
}

// TestCertReloaderParsesABrokenPairOnceAndWarns: a rotation that leaves a pair
// that does not load (a key that does not match, a cert-manager misissue) is
// logged once per change and parsed once, not on every handshake, while the
// last good pair keeps being served. A later change is tried again.
func TestCertReloaderParsesABrokenPairOnceAndWarns(t *testing.T) {
	dir := t.TempDir()
	first := writeTestCert(t, dir, 1)
	r, err := newCertReloader(tlsListenerAPI, first.certPath, first.keyPath, nil)
	if err != nil {
		t.Fatalf("newCertReloader: %v", err)
	}
	var logs strings.Builder
	r.log = slog.New(slog.NewTextHandler(&logs, nil))
	loads := 0
	r.load = func(cert, key string) (tls.Certificate, error) {
		loads++
		return tls.LoadX509KeyPair(cert, key)
	}
	touch := func(offset time.Duration, paths ...string) {
		t.Helper()
		at := time.Now().Add(offset)
		for _, p := range paths {
			if err := os.Chtimes(p, at, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	served := func() *big.Int {
		t.Helper()
		c, err := r.getCertificate(nil)
		if err != nil || c == nil {
			t.Fatalf("getCertificate = %v, %v", c, err)
		}
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return leaf.SerialNumber
	}

	if err := os.WriteFile(first.keyPath, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	touch(2*time.Second, first.keyPath)
	for range 3 {
		if got := served(); got.Cmp(first.serial) != 0 {
			t.Fatalf("served serial = %v, want the last good %v", got, first.serial)
		}
	}
	if loads != 1 {
		t.Errorf("broken pair parsed %d times over 3 handshakes, want 1", loads)
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 {
		t.Errorf("logged %d warnings for one broken pair, want 1:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), first.keyPath) {
		t.Errorf("warning does not name the files:\n%s", logs.String())
	}

	second := writeTestCert(t, dir, 2)
	touch(4*time.Second, second.certPath, second.keyPath)
	if got := served(); got.Cmp(second.serial) != 0 {
		t.Errorf("served serial after a fixed rotation = %v, want %v", got, second.serial)
	}
	if loads != 2 {
		t.Errorf("loads = %d after the fixed rotation, want 2", loads)
	}
}
