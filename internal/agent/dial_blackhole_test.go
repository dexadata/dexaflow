package agent

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// blackholeServer answers heartbeats and holds a log stream open until the
// client goes away.
type blackholeServer struct {
	agentv1.UnimplementedAgentServiceServer
	beats atomic.Int64
}

func (s *blackholeServer) Heartbeat(context.Context, *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	s.beats.Add(1)
	return &agentv1.HeartbeatResponse{}, nil
}

func (s *blackholeServer) StreamLogs(stream grpc.BidiStreamingServer[agentv1.LogLine, agentv1.LogAck]) error {
	<-stream.Context().Done()
	return nil
}

// freezeProxy forwards TCP between the agent and the server until frozen;
// then it keeps reading and drops every byte, so neither side sees a close.
// That is a lost node or a hung control plane as the agent sees it: the
// connection stays up and nothing ever answers.
type freezeProxy struct {
	frozen atomic.Bool
	mu     sync.Mutex
	conns  []net.Conn
}

func (p *freezeProxy) pipe(dst, src net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if err != nil {
			return
		}
		if p.frozen.Load() {
			continue
		}
		if _, err := dst.Write(buf[:n]); err != nil {
			return
		}
	}
}

// startBlackhole starts a real gRPC server with the control plane's keepalive
// enforcement policy behind a freezeProxy, and returns the proxy's address.
func startBlackhole(t *testing.T) (*blackholeServer, *freezeProxy, string) {
	t.Helper()
	var lc net.ListenConfig
	srvLn, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &blackholeServer{}
	gs := grpc.NewServer(grpc.KeepaliveEnforcementPolicy(agentrpc.KeepaliveEnforcementPolicy()))
	agentv1.RegisterAgentServiceServer(gs, hs)
	go func() { _ = gs.Serve(srvLn) }()
	t.Cleanup(gs.Stop)

	pln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &freezeProxy{}
	go func() {
		for {
			c, aerr := pln.Accept()
			if aerr != nil {
				return
			}
			var d net.Dialer
			s, derr := d.DialContext(context.Background(), "tcp", srvLn.Addr().String())
			if derr != nil {
				_ = c.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, s)
			p.mu.Unlock()
			go p.pipe(c, s)
			go p.pipe(s, c)
		}
	}()
	t.Cleanup(func() {
		_ = pln.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, c := range p.conns {
			_ = c.Close()
		}
	})
	return hs, p, pln.Addr().String()
}

// countingClient counts the heartbeats the agent attempts.
type countingClient struct {
	agentv1.AgentServiceClient
	attempts atomic.Int64
}

func (c *countingClient) Heartbeat(ctx context.Context, in *agentv1.HeartbeatRequest, opts ...grpc.CallOption) (*agentv1.HeartbeatResponse, error) {
	c.attempts.Add(1)
	return c.AgentServiceClient.Heartbeat(ctx, in, opts...)
}

// TestHeartbeatKeepsBeatingOnBlackholedControlPlane is R-13 (#946). The agent
// called Heartbeat with no deadline, so when the control plane it was
// connected to stopped answering without closing the connection, the one
// in-flight heartbeat never returned, the sequential heartbeat loop stopped,
// and the healthy task was reaped as agent_lost. With a per-call deadline each
// heartbeat fails on its own and the loop keeps trying, so it reaches a
// replacement control plane as soon as the channel reconnects.
func TestHeartbeatKeepsBeatingOnBlackholedControlPlane(t *testing.T) {
	hs, proxy, addr := startBlackhole(t)
	tuning := defaultDialTuning()
	tuning.heartbeatTimeout = 200 * time.Millisecond
	client, conn, tokens, err := dial(addr, "tok", true, "", tuning)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, werr := client.Heartbeat(context.Background(), &agentv1.HeartbeatRequest{}); werr != nil {
		t.Fatalf("warm-up heartbeat: %v", werr)
	}
	proxy.frozen.Store(true)

	counting := &countingClient{AgentServiceClient: client}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &Runner{Client: counting, HeartbeatInterval: 50 * time.Millisecond, Token: tokens}
	done := make(chan struct{})
	go func() { defer close(done); r.heartbeat(ctx, cancel) }()
	time.Sleep(2 * time.Second)
	cancel()
	<-done

	if got := counting.attempts.Load(); got < 4 {
		t.Errorf("the heartbeat loop attempted %d heartbeats in 2s against a black-holed control plane, "+
			"want several: one RPC blocked the loop (server saw %d)", got, hs.beats.Load())
	}
}

// TestUnaryCallsHaveADeadline: every unary RPC the agent makes carries a
// deadline, so no call (GetTaskSpec, ReportState, an XCom push) can hang a task
// on a control plane that stopped answering.
func TestUnaryCallsHaveADeadline(t *testing.T) {
	_, proxy, addr := startBlackhole(t)
	tuning := defaultDialTuning()
	tuning.callTimeout = 300 * time.Millisecond
	client, conn, _, err := dial(addr, "tok", true, "", tuning)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, werr := client.Heartbeat(context.Background(), &agentv1.HeartbeatRequest{}); werr != nil {
		t.Fatalf("warm-up heartbeat: %v", werr)
	}
	proxy.frozen.Store(true)

	start := time.Now()
	_, err = client.GetTaskSpec(context.Background(), &agentv1.GetTaskSpecRequest{})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("GetTaskSpec on a black-holed control plane = %v, want DeadlineExceeded", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("GetTaskSpec took %v to give up, want about the %v deadline", took, tuning.callTimeout)
	}
}

// TestCallerDeadlineIsKept: a caller's own, shorter deadline still wins.
func TestCallerDeadlineIsKept(t *testing.T) {
	_, proxy, addr := startBlackhole(t)
	client, conn, _, err := dial(addr, "tok", true, "", defaultDialTuning())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, werr := client.Heartbeat(context.Background(), &agentv1.HeartbeatRequest{}); werr != nil {
		t.Fatalf("warm-up heartbeat: %v", werr)
	}
	proxy.frozen.Store(true)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = client.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("GetTaskSpec = %v, want DeadlineExceeded", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("GetTaskSpec took %v, want the caller's 100ms deadline", took)
	}
}

// TestKeepaliveDetectsABlackholedStream: a stream with no traffic (a warm
// worker waiting on AwaitAssignment, here a log stream) on a connection whose
// peer vanished used to stay blocked until the kernel gave up on the socket,
// minutes later. Client keepalive pings the idle connection and closes it when
// the ping goes unanswered, so the stream fails with Unavailable. gRPC clamps
// the ping interval to at least 10s, so this test takes about 11s.
func TestKeepaliveDetectsABlackholedStream(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for gRPC's 10s minimum keepalive interval")
	}
	_, proxy, addr := startBlackhole(t)
	tuning := defaultDialTuning()
	tuning.keepalive = keepalive.ClientParameters{Time: 10 * time.Second, Timeout: time.Second, PermitWithoutStream: true}
	client, conn, _, err := dial(addr, "tok", true, "", tuning)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	stream, err := client.StreamLogs(context.Background(), grpc.WaitForReady(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&agentv1.LogLine{Message: "hello"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	proxy.frozen.Store(true)

	recvErr := make(chan error, 1)
	go func() {
		_, rerr := stream.Recv()
		recvErr <- rerr
	}()
	select {
	case rerr := <-recvErr:
		if status.Code(rerr) != codes.Unavailable {
			t.Errorf("stream ended with %v, want Unavailable from the keepalive", rerr)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the stream stayed blocked 20s on a black-holed connection: no keepalive")
	}
}

// TestKeepaliveAgreesWithServerEnforcement: the control plane closes a
// connection whose client pings more often than its enforcement policy's
// MinTime ("too_many_pings"), or pings with no active stream when that is not
// permitted. The agent's production keepalive must sit inside that policy, or
// enabling it would get every agent's connection torn down. It must also
// detect a dead peer well before the heartbeat reaper (90s by default) acts.
func TestKeepaliveAgreesWithServerEnforcement(t *testing.T) {
	client := defaultDialTuning().keepalive
	server := agentrpc.KeepaliveEnforcementPolicy()
	if client.Time < server.MinTime {
		t.Errorf("client keepalive time %v is below the server's MinTime %v", client.Time, server.MinTime)
	}
	if client.PermitWithoutStream && !server.PermitWithoutStream {
		t.Error("the client pings without an active stream, which the server does not permit")
	}
	if !client.PermitWithoutStream {
		t.Error("the client must ping an idle connection, or an idle assignment stream is never probed")
	}
	if detect := client.Time + client.Timeout; detect > 45*time.Second {
		t.Errorf("a dead peer is detected after %v, want well inside the 90s reap threshold", detect)
	}
	tuning := defaultDialTuning()
	if tuning.heartbeatTimeout >= DefaultHeartbeatInterval {
		t.Errorf("heartbeat deadline %v must be shorter than the %v heartbeat interval", tuning.heartbeatTimeout, DefaultHeartbeatInterval)
	}
	if tuning.callTimeout > 45*time.Second {
		t.Errorf("unary call deadline %v must be well under the 90s reap threshold", tuning.callTimeout)
	}
}
