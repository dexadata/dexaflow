package main

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/observability"
)

// grpcServedSerial completes a TLS handshake against the agent gRPC listener
// (offering h2, as a gRPC client does) and returns the served leaf's serial.
func grpcServedSerial(t *testing.T, addr string) *big.Int {
	t.Helper()
	d := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}}} //nolint:gosec // reading the served cert only
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

// TestStartAgentGRPCPicksUpARotatedCert drives the real agent gRPC boot path
// through a server certificate rotation (#637): cert-manager renews the Secret
// and the kubelet rewrites the mounted files; the next handshake must serve the
// renewed certificate without a restart. A rotation caught half written keeps
// the last good pair and is counted on dexaflow_tls_reload_failures_total.
func TestStartAgentGRPCPicksUpARotatedCert(t *testing.T) {
	dir := t.TempDir()
	first := writeTestCert(t, dir, 1)
	addr := freeAddr(t)
	metrics := observability.NewMetrics(prometheus.NewRegistry())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	srv, _, err := startAgentGRPC(context.Background(), addr, nil, nil, nil, nil, nil, nil, false, 0, false, "", "", 0, nil,
		first.certPath, first.keyPath, nil, agentrpc.NewInflightHandlers(), metrics, logger)
	if err != nil {
		t.Fatalf("startAgentGRPC: %v", err)
	}
	t.Cleanup(srv.Stop)

	if got := grpcServedSerial(t, addr); got.Cmp(first.serial) != 0 {
		t.Fatalf("served serial = %v, want %v", got, first.serial)
	}

	second := writeTestCert(t, dir, 2)
	touchFiles(t, 2*time.Second, second.certPath, second.keyPath)
	if got := grpcServedSerial(t, addr); got.Cmp(second.serial) != 0 {
		t.Fatalf("served serial after rotation = %v, want %v (the rotated certificate)", got, second.serial)
	}

	if err := os.WriteFile(second.keyPath, []byte("half written"), 0o600); err != nil {
		t.Fatal(err)
	}
	touchFiles(t, 4*time.Second, second.keyPath)
	for range 2 {
		if got := grpcServedSerial(t, addr); got.Cmp(second.serial) != 0 {
			t.Errorf("served serial during a broken rotation = %v, want the last good %v", got, second.serial)
		}
	}
	var m dto.Metric
	if err := metrics.TLSReloadFailures.WithLabelValues(tlsListenerAgentGRPC).Write(&m); err != nil {
		t.Fatal(err)
	}
	if got := m.GetCounter().GetValue(); got != 1 {
		t.Errorf("dexaflow_tls_reload_failures_total{listener=%q} = %v, want 1 (one broken pair, counted once)", tlsListenerAgentGRPC, got)
	}
}

// TestStartAgentGRPCFailsFastOnUnreadableCert: a missing pair is a boot error,
// as it was before reloading existed.
func TestStartAgentGRPCFailsFastOnUnreadableCert(t *testing.T) {
	_, _, err := startAgentGRPC(context.Background(), freeAddr(t), nil, nil, nil, nil, nil, nil, false, 0, false, "", "", 0, nil,
		"/nonexistent/tls.crt", "/nonexistent/tls.key", nil, agentrpc.NewInflightHandlers(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/tls.crt") {
		t.Fatalf("startAgentGRPC with a missing cert = %v, want a boot error naming the file", err)
	}
}

func touchFiles(t *testing.T, offset time.Duration, paths ...string) {
	t.Helper()
	at := time.Now().Add(offset)
	for _, p := range paths {
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
}
