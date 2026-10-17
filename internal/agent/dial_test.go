package agent

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
)

func TestDialRequiresAddressAndToken(t *testing.T) {
	if _, _, _, err := Dial("", "token", true, ""); err == nil {
		t.Error("missing address should error")
	}
	if _, _, _, err := Dial("localhost:50051", "", true, ""); err == nil {
		t.Error("missing token should error")
	}
}

func TestDialBuildsClient(t *testing.T) {
	for _, insecure := range []bool{true, false} {
		client, conn, tokens, err := Dial("localhost:50051", "token", insecure, "")
		if err != nil {
			t.Fatalf("Dial(insecure=%v): %v", insecure, err)
		}
		if client == nil || conn == nil || tokens == nil {
			t.Fatalf("Dial(insecure=%v) returned nil client/conn/tokens", insecure)
		}
		if tokens.Token() != "token" {
			t.Errorf("Dial seeded token = %q, want %q", tokens.Token(), "token")
		}
		if cerr := conn.Close(); cerr != nil {
			t.Errorf("closing conn: %v", cerr)
		}
	}
}

// TestDialReconnectBackoffIsCappedAtReportRetryDelay pins the reconnect backoff
// Dial gives the channel (#899). Without it grpc-go's default applies (MaxDelay
// 120s), and a fail-fast ReportState on a TRANSIENT_FAILURE channel does not
// trigger a connect, so after a long outage the channel could stay down for up
// to two minutes after the control plane is back, however often the report
// loop retried.
func TestDialReconnectBackoffIsCappedAtReportRetryDelay(t *testing.T) {
	bo := defaultDialTuning().reconnect
	if bo.MaxDelay <= 0 || bo.MaxDelay > reportRetryMaxDelay {
		t.Errorf("reconnect MaxDelay = %v, want in (0, %v]", bo.MaxDelay, reportRetryMaxDelay)
	}
	if bo.BaseDelay <= 0 || bo.BaseDelay > bo.MaxDelay || bo.Multiplier < 1 || bo.Jitter <= 0 {
		t.Errorf("reconnect backoff %+v is not a jittered exponential ramp up to MaxDelay", bo)
	}
}

// TestDialChannelFollowsItsReconnectBackoff pins the wiring behind #899: the
// channel dial builds redials on the backoff it is given, not on grpc-go's
// default. It is given a one-hour backoff, fails its first connection attempt
// (connection refused), and must then stay down for the whole observation
// window even though the server is back: its next attempt is an hour away.
// Were the backoff ignored, the channel would redial within about a second.
// The assertion holds however slow the machine is; only catching that mutation
// depends on the window. TestDialReconnectBackoffIsCappedAtReportRetryDelay
// pins the value Dial passes.
func TestDialChannelFollowsItsReconnectBackoff(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close() // the control plane is down: connections are refused

	slow := defaultDialTuning()
	slow.reconnect = backoff.Config{BaseDelay: time.Hour, Multiplier: 1.6, Jitter: 0.2, MaxDelay: time.Hour}
	_, conn, _, err := dial(addr, "token", true, "", slow)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	conn.Connect()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for st := conn.GetState(); st != connectivity.TransientFailure; st = conn.GetState() {
		if !conn.WaitForStateChange(ctx, st) {
			t.Fatalf("the first connection attempt never failed (last state %v)", st)
		}
	}

	lis, err = lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		t.Skipf("could not re-listen on %s: %v", addr, err)
	}
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	window, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	for st := conn.GetState(); conn.WaitForStateChange(window, st); st = conn.GetState() {
		if now := conn.GetState(); now == connectivity.Connecting || now == connectivity.Ready {
			t.Fatalf("channel redialed (%v) during its one-hour backoff: dial did not apply the backoff it was given", now)
		}
	}
}
