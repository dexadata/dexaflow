package main

import (
	"crypto/tls"
	"fmt"
	"log/slog"
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
//
// A pair that fails to load is remembered by its modification times: it is
// logged once and not parsed again until either file changes, so a broken
// rotation costs one parse, not one per handshake, and is visible in the logs
// instead of being served around silently until the old certificate expires.
type certReloader struct {
	certPath, keyPath string
	load              func(certPath, keyPath string) (tls.Certificate, error)
	log               *slog.Logger

	mu                          sync.Mutex
	cert                        *tls.Certificate
	certMod, keyMod             time.Time
	failedCertMod, failedKeyMod time.Time
}

func newCertReloader(certPath, keyPath string) (*certReloader, error) {
	r := &certReloader{certPath: certPath, keyPath: keyPath, load: tls.LoadX509KeyPair, log: slog.Default()}
	certMod, keyMod, err := r.modTimes()
	if err != nil {
		return nil, err
	}
	if err := r.reload(certMod, keyMod); err != nil {
		return nil, err
	}
	return r, nil
}

// reload reads the pair and records the modification times it was read at. On
// error the previously loaded pair is left in place.
func (r *certReloader) reload(certMod, keyMod time.Time) error {
	cert, err := r.load(r.certPath, r.keyPath)
	if err != nil {
		return fmt.Errorf("loading API TLS certificate %s and key %s: %w", r.certPath, r.keyPath, err)
	}
	r.cert, r.certMod, r.keyMod = &cert, certMod, keyMod
	r.failedCertMod, r.failedKeyMod = time.Time{}, time.Time{}
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

// getCertificate is the tls.Config hook. When the files changed since the pair
// last loaded (and since the last pair that failed) it reloads them. A reload
// that fails (a rotation caught half written, a key that does not match the
// certificate) keeps serving the last good pair, logs a warning once, and is
// not retried until either file changes again. A file that cannot be stat'ed
// counts as unchanged.
func (r *certReloader) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	certMod, keyMod, err := r.modTimes()
	if err != nil {
		return r.cert, nil //nolint:nilerr // a file briefly missing mid-rotation: keep serving the last good pair
	}
	if certMod.Equal(r.certMod) && keyMod.Equal(r.keyMod) {
		return r.cert, nil
	}
	if certMod.Equal(r.failedCertMod) && keyMod.Equal(r.failedKeyMod) {
		return r.cert, nil
	}
	if err := r.reload(certMod, keyMod); err != nil {
		r.failedCertMod, r.failedKeyMod = certMod, keyMod
		r.log.Warn("API TLS certificate reload failed; still serving the previously loaded certificate until the files change again",
			"cert", r.certPath, "key", r.keyPath, "error", err)
	}
	return r.cert, nil
}
