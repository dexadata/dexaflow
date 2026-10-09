package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
)

// configureAPITLS turns on HTTPS for the API and UI listener when
// server.http_tls_cert and server.http_tls_key are set (#638), so a load
// balancer can re-encrypt the hop to the pod (ALB backend-protocol HTTPS, GKE
// app-protocols HTTPS). With neither set it leaves srv untouched and serve
// keeps plain HTTP, the default. The pair is loaded here so a missing or
// invalid file is a boot error, not a listener that fails every handshake.
func configureAPITLS(srv *http.Server, s config.ServerSection) error {
	if !s.HTTPTLSEnabled() {
		return nil
	}
	r, err := newCertReloader(s.HTTPTLSCert, s.HTTPTLSKey)
	if err != nil {
		return err
	}
	srv.TLSConfig = &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: r.getCertificate,
	}
	return nil
}

// certReloader serves a certificate pair from disk and re-reads it when either
// file's modification time changes, so a rotated Secret (cert-manager renewal,
// a re-created BYO Secret) is served on the next handshake without a restart.
type certReloader struct {
	certPath, keyPath string

	mu              sync.Mutex
	cert            *tls.Certificate
	certMod, keyMod time.Time
}

func newCertReloader(certPath, keyPath string) (*certReloader, error) {
	r := &certReloader{certPath: certPath, keyPath: keyPath}
	if err := r.reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// reload reads the pair and records the modification times it was read at. On
// error the previously loaded pair is left in place.
func (r *certReloader) reload() error {
	certMod, keyMod, err := r.modTimes()
	if err != nil {
		return err
	}
	cert, err := tls.LoadX509KeyPair(r.certPath, r.keyPath)
	if err != nil {
		return fmt.Errorf("loading API TLS certificate %s and key %s: %w", r.certPath, r.keyPath, err)
	}
	r.cert, r.certMod, r.keyMod = &cert, certMod, keyMod
	return nil
}

func (r *certReloader) modTimes() (certMod, keyMod time.Time, err error) {
	ci, err := os.Stat(r.certPath)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("reading API TLS certificate %s: %w", r.certPath, err)
	}
	ki, err := os.Stat(r.keyPath)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("reading API TLS key %s: %w", r.keyPath, err)
	}
	return ci.ModTime(), ki.ModTime(), nil
}

// changed reports whether either file's modification time differs from the
// pair last loaded. A file that cannot be stat'ed counts as unchanged.
func (r *certReloader) changed() bool {
	certMod, keyMod, err := r.modTimes()
	return err == nil && (!certMod.Equal(r.certMod) || !keyMod.Equal(r.keyMod))
}

// getCertificate is the tls.Config hook. When the files changed it reloads
// them; a reload that fails (a rotation caught half written) keeps serving the
// last good pair, and the next handshake tries again.
func (r *certReloader) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.changed() {
		if err := r.reload(); err != nil {
			return r.cert, nil //nolint:nilerr // keep serving the last good pair until the rotation completes
		}
	}
	return r.cert, nil
}
