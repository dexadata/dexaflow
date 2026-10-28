package agent

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"

	"google.golang.org/grpc/credentials"
)

// caTrust holds the CA bundle the agent verifies the control plane against and
// re-reads it from disk on every TLS handshake, so a connection made after the
// mounted ca.crt changed (a reconnect, a new channel) trusts the new bundle
// without restarting the agent (#637). The bundle may hold several CAs at once:
// during a CA rotation it carries the old and the new CA, so the agent accepts a
// server certificate from either.
//
// A bundle that cannot be read or holds no certificate (a rotation caught half
// written, a mistaken ConfigMap) never replaces the last good one: the agent
// keeps verifying against the previous pool and logs a warning once per broken
// content, not once per handshake.
type caTrust struct {
	path string
	read func(path string) ([]byte, error)
	log  *slog.Logger

	mu          sync.Mutex
	pool        *x509.CertPool
	goodSum     [sha256.Size]byte
	lastFailure string
}

// newCATrust loads the bundle at path. A missing or empty bundle is an error
// here, at dial time, so a broken mount fails fast instead of every handshake
// failing later.
func newCATrust(path string) (*caTrust, error) {
	t := &caTrust{path: path, read: readCAFile, log: slog.Default()}
	data, err := t.read(path)
	if err != nil {
		return nil, fmt.Errorf("reading CA file %q: %w", path, err)
	}
	pool, err := poolFromPEM(path, data)
	if err != nil {
		return nil, err
	}
	t.pool, t.goodSum = pool, sha256.Sum256(data)
	return t, nil
}

func readCAFile(path string) ([]byte, error) {
	return os.ReadFile(path) //nolint:gosec // G304: CA path is operator-supplied config.
}

// poolFromPEM parses every certificate of a PEM bundle into a pool.
func poolFromPEM(path string, data []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("no certificates found in CA file %q", path)
	}
	return pool, nil
}

// current re-reads the bundle and returns the pool to verify the next handshake
// with: the new bundle when it changed and parses, the last good one otherwise.
func (t *caTrust) current() *x509.CertPool {
	data, rerr := t.read(t.path)
	t.mu.Lock()
	defer t.mu.Unlock()
	if rerr != nil {
		t.warnOnce("read:"+rerr.Error(), rerr)
		return t.pool
	}
	sum := sha256.Sum256(data)
	if sum == t.goodSum {
		t.lastFailure = ""
		return t.pool
	}
	key := "parse:" + hex.EncodeToString(sum[:])
	if key == t.lastFailure {
		return t.pool
	}
	pool, perr := poolFromPEM(t.path, data)
	if perr != nil {
		t.warnOnce(key, perr)
		return t.pool
	}
	t.pool, t.goodSum, t.lastFailure = pool, sum, ""
	t.log.Info("agent TLS CA bundle reloaded", "ca_file", t.path)
	return t.pool
}

// warnOnce logs a reload failure the first time a given failure is seen. The
// caller holds t.mu.
func (t *caTrust) warnOnce(key string, err error) {
	if key == t.lastFailure {
		return
	}
	t.lastFailure = key
	t.log.Warn("agent TLS CA bundle reload failed; still verifying the control plane against the previously loaded bundle",
		"ca_file", t.path, "error", err)
}

// reloadingTLSCreds is the agent's gRPC transport credential when a CA bundle
// is configured. Each client handshake builds its TLS config from the bundle's
// current content, so a reconnect picks up a rotated or extended bundle.
type reloadingTLSCreds struct {
	trust      *caTrust
	serverName string
}

var _ credentials.TransportCredentials = (*reloadingTLSCreds)(nil)

func (c *reloadingTLSCreds) tlsConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: c.trust.current(), ServerName: c.serverName}
}

// ClientHandshake verifies the server against the bundle as it is on disk now.
func (c *reloadingTLSCreds) ClientHandshake(ctx context.Context, authority string, rawConn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return credentials.NewTLS(c.tlsConfig()).ClientHandshake(ctx, authority, rawConn)
}

// ServerHandshake is not supported: the credential is for the agent's client
// side only.
func (c *reloadingTLSCreds) ServerHandshake(net.Conn) (net.Conn, credentials.AuthInfo, error) {
	return nil, nil, errors.New("agent TLS credentials are client-only")
}

// Info reports the TLS protocol information.
func (c *reloadingTLSCreds) Info() credentials.ProtocolInfo {
	return credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.serverName}).Info()
}

// Clone returns a copy that shares the reloading bundle.
func (c *reloadingTLSCreds) Clone() credentials.TransportCredentials {
	return &reloadingTLSCreds{trust: c.trust, serverName: c.serverName}
}

// OverrideServerName sets the name verified against the server certificate.
//
// Deprecated: kept to satisfy credentials.TransportCredentials.
func (c *reloadingTLSCreds) OverrideServerName(name string) error {
	c.serverName = name
	return nil
}
