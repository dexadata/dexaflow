package main

import (
	"bufio"
	"context"
	"io"
	"net"
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

// TestNewHTTPServerIdleZeroKeepsIdleConnectionsWithReadTimeout pins that
// server.idle_timeout 0 keeps today's behavior even when server.read_timeout
// is set. net/http falls back to ReadTimeout for a zero IdleTimeout, which
// would silently close idle keep-alive connections after the read timeout.
func TestNewHTTPServerIdleZeroKeepsIdleConnectionsWithReadTimeout(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Server.ReadTimeout = 150 * time.Millisecond
	srv := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}), cfg)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	var d net.Dialer
	conn, err := d.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	br := bufio.NewReader(conn)
	get := func() error {
		if _, werr := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); werr != nil {
			return werr
		}
		resp, rerr := http.ReadResponse(br, nil)
		if rerr != nil {
			return rerr
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.Body.Close()
	}
	if err := get(); err != nil {
		t.Fatalf("first request: %v", err)
	}
	time.Sleep(400 * time.Millisecond) // well past the read timeout, idle.
	if err := get(); err != nil {
		t.Errorf("idle keep-alive connection closed after the read timeout: %v", err)
	}
}

// TestNewHTTPServerHeaderTimeoutNeverExceedsReadTimeout pins that a
// server.read_timeout below the fixed 10s header timeout also bounds reading
// the headers, so the shorter setting the operator chose is the one that holds.
func TestNewHTTPServerHeaderTimeoutNeverExceedsReadTimeout(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Server.ReadTimeout = 3 * time.Second
	srv := newHTTPServer(":0", http.NotFoundHandler(), cfg)
	if srv.ReadHeaderTimeout != 3*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 3s (capped by read_timeout)", srv.ReadHeaderTimeout)
	}
}
