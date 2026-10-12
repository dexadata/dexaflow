package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// Dial connects to the control plane's AgentService, attaching the bearer token
// to every RPC. When allowInsecure is true (local development against a cluster
// without TLS) the transport is unencrypted; otherwise TLS 1.2+ is required. When
// caFile is set, the server certificate is verified against that CA bundle (a
// self-signed / cluster CA); otherwise the system roots are used. The bundle is
// read here, so a missing or empty file fails the dial, and read again on every
// later handshake, so a reconnect trusts a rotated bundle (one that holds the
// old and new CA during a CA rotation) without restarting the agent (#637).
//
// The channel pings the control plane when idle and every unary call carries a
// deadline (see defaultDialTuning), so a control plane that stops answering is
// noticed in seconds rather than when the kernel gives up on the socket.
//
// It also returns the *TokenSource backing the per-RPC credential: the heartbeat
// loop swaps a renewed token into it (ADR 0055 Fix #4) and the interceptor picks
// the new bearer up on the next call.
func Dial(addr, token string, allowInsecure bool, caFile string) (agentv1.AgentServiceClient, *grpc.ClientConn, *TokenSource, error) {
	return dial(addr, token, allowInsecure, caFile, defaultDialTuning())
}

// The agent channel's liveness settings (#946, R-13). Without them a control
// plane that stops answering without closing the connection (a lost node, a
// hung process, a network partition) blocked the agent until the kernel gave
// up on the socket, minutes later: an in-flight heartbeat never returned, the
// heartbeat loop stopped, and the healthy task was reaped as agent_lost; a warm
// worker waiting on its assignment stream held its pod and slot all along.
const (
	// agentKeepaliveTime is how long the channel may sit with no traffic before
	// the agent pings it. It must stay at or above the server's enforcement
	// MinTime (agentrpc.KeepaliveEnforcementPolicy, 10s), or the server closes
	// the connection for pinging too often; twice that leaves room for timer
	// skew.
	agentKeepaliveTime = 20 * time.Second
	// agentKeepaliveTimeout is how long a ping may go unanswered before the
	// channel is closed and every call and stream on it fails with Unavailable.
	// With agentKeepaliveTime, a dead peer is detected within 30s, well inside
	// the heartbeat reaper's 90s.
	agentKeepaliveTimeout = 10 * time.Second
	// agentCallTimeout bounds every unary RPC but Heartbeat (GetTaskSpec,
	// ReportState, PushXCom, FetchXCom, the secrets reads, Register,
	// ExchangeToken). It is generous for a healthy control plane under load
	// and still far under the reap threshold; the callers that retry (the
	// state report) try again after it.
	agentCallTimeout = 30 * time.Second
	// agentHeartbeatTimeout bounds one heartbeat. It is shorter than the
	// heartbeat interval, so a heartbeat that cannot be answered is over
	// before the next one is due and the loop never stalls.
	agentHeartbeatTimeout = 10 * time.Second
)

// dialTuning holds the channel's liveness settings, so tests can shorten them.
type dialTuning struct {
	keepalive        keepalive.ClientParameters
	callTimeout      time.Duration
	heartbeatTimeout time.Duration
}

// defaultDialTuning is the production tuning.
func defaultDialTuning() dialTuning {
	return dialTuning{
		keepalive: keepalive.ClientParameters{
			Time:    agentKeepaliveTime,
			Timeout: agentKeepaliveTimeout,
			// Probe an idle connection too: a warm worker's assignment stream
			// carries nothing for as long as the worker waits for work.
			PermitWithoutStream: true,
		},
		callTimeout:      agentCallTimeout,
		heartbeatTimeout: agentHeartbeatTimeout,
	}
}

// callDeadline returns a unary interceptor that gives every call a deadline:
// tuning.heartbeatTimeout for Heartbeat and tuning.callTimeout for the rest. A
// caller's own earlier deadline still applies, since a derived context never
// outlives its parent. Streams (StreamLogs, AwaitAssignment) are long-lived by
// design and rely on the keepalive instead.
func callDeadline(tuning dialTuning) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		timeout := tuning.callTimeout
		if method == agentv1.AgentService_Heartbeat_FullMethodName {
			timeout = tuning.heartbeatTimeout
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func dial(addr, token string, allowInsecure bool, caFile string, tuning dialTuning) (agentv1.AgentServiceClient, *grpc.ClientConn, *TokenSource, error) {
	if addr == "" {
		return nil, nil, nil, errors.New("control plane address is required")
	}
	if token == "" {
		return nil, nil, nil, errors.New("agent token is required")
	}

	transport := insecure.NewCredentials()
	if !allowInsecure {
		transport = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
		if caFile != "" {
			trust, cerr := newCATrust(caFile)
			if cerr != nil {
				return nil, nil, nil, cerr
			}
			transport = &reloadingTLSCreds{trust: trust}
		}
	}

	tokens := NewTokenSource(token)
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(transport),
		grpc.WithPerRPCCredentials(tokenAuth{source: tokens, secure: !allowInsecure}),
		grpc.WithKeepaliveParams(tuning.keepalive),
		grpc.WithChainUnaryInterceptor(callDeadline(tuning)),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dialing control plane at %q: %w", addr, err)
	}
	return agentv1.NewAgentServiceClient(conn), conn, tokens, nil
}

// grpcLogSink adapts the StreamLogs bidirectional stream to the LogSink
// interface. The task's stdout and stderr are copied on separate goroutines but
// share this one stream, so Send is serialized with a mutex: gRPC client streams
// do not permit concurrent SendMsg, and racing sends corrupt the stream (see
// #36).
type grpcLogSink struct {
	mu     sync.Mutex
	stream grpc.BidiStreamingClient[agentv1.LogLine, agentv1.LogAck]
}

// OpenLogSink starts the StreamLogs RPC and returns a sink that forwards lines
// to it. It is the agent's first RPC, so it uses WaitForReady: with the lazy
// connection of grpc.NewClient the channel may not be established yet, and
// without this the stream would fail fast on a cold connection (the "opening log
// stream" EOF in #36) rather than waiting for the control plane to be reachable.
func OpenLogSink(ctx context.Context, client agentv1.AgentServiceClient) (LogSink, error) {
	stream, err := client.StreamLogs(ctx, grpc.WaitForReady(true))
	if err != nil {
		return nil, fmt.Errorf("opening log stream: %w", err)
	}
	return &grpcLogSink{stream: stream}, nil
}

// Send forwards a single log line to the control plane, serialized so concurrent
// stdout/stderr writers never call the underlying stream's Send at once. Task
// output is arbitrary bytes but LogLine's message is a proto string: invalid
// UTF-8 fails the marshal, and gRPC then tears down the whole stream, losing
// this line, every later one and any still queued. Invalid sequences are
// replaced with U+FFFD instead.
func (s *grpcLogSink) Send(line *agentv1.LogLine) error {
	if !utf8.ValidString(line.Message) {
		line.Message = strings.ToValidUTF8(line.Message, "\uFFFD")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(line)
}

// logDrainTimeout bounds how long Close waits for the server to acknowledge the
// streamed lines. It must be generous enough for normal delivery but finite: a
// task must never hang because log shipping stalls.
const logDrainTimeout = 5 * time.Second

// Close signals the control plane that no more log lines will be sent, then
// waits (briefly) for the server to consume them. The drain is essential:
// stream.Send only QUEUES a line into the gRPC transport, and CloseSend merely
// half-closes; without waiting for the RPC to complete, the agent returns and
// the deferred conn.Close() tears the connection down before the queued lines
// are flushed — so a short task's logs never arrive and the attempt's log file
// stays empty. Draining (Recv to EOF) blocks until the server has consumed every
// line, guaranteeing delivery.
//
// The wait is bounded by logDrainTimeout: log delivery is best-effort and must
// never hang the task. If the control plane stalls, Close returns and the caller
// closes the connection — losing at most the tail of the logs, never the task.
func (s *grpcLogSink) Close() error {
	s.mu.Lock()
	err := s.stream.CloseSend()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, rerr := s.stream.Recv(); rerr != nil {
				return // EOF (clean) or any error: the drain is over
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(logDrainTimeout):
		// The server did not finish in time; do not hang the task on it.
	}
	return nil
}
