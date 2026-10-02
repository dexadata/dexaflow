package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestNewHTTPServerAppliesConfiguredTimeouts pins the wiring of
// server.read_timeout and server.idle_timeout onto the API listener. The
// header timeout is always on; WriteTimeout stays off, since a write deadline
// would cut live log tails and long downloads.
func TestNewHTTPServerAppliesConfiguredTimeouts(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Server.HTTPAddr = ":0"
	cfg.Server.ReadTimeout = 2 * time.Minute
	cfg.Server.IdleTimeout = 90 * time.Second
	srv := newHTTPServer(cfg.Server.HTTPAddr, http.NotFoundHandler(), cfg)
	if srv.ReadTimeout != 2*time.Minute || srv.IdleTimeout != 90*time.Second {
		t.Errorf("ReadTimeout=%v IdleTimeout=%v, want 2m0s and 1m30s", srv.ReadTimeout, srv.IdleTimeout)
	}
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (streams must not be cut)", srv.WriteTimeout)
	}
}

// TestNewHTTPServerDefaultsMatchToday pins that an unset config builds the
// listener exactly as before: only the header timeout.
func TestNewHTTPServerDefaultsMatchToday(t *testing.T) {
	srv := newHTTPServer(":0", http.NotFoundHandler(), &config.ServerConfig{})
	if srv.ReadTimeout != 0 || srv.IdleTimeout != 0 || srv.WriteTimeout != 0 || srv.ReadHeaderTimeout != 10*time.Second {
		t.Errorf("defaults = read %v idle %v write %v header %v", srv.ReadTimeout, srv.IdleTimeout, srv.WriteTimeout, srv.ReadHeaderTimeout)
	}
}
