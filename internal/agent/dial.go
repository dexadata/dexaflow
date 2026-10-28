package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
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
//
// The channel reconnects with reconnectBackoff, so once the control plane is
// back it is redialed within about one heartbeat interval, however long the
// outage lasted (#899).
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

// dialTuning holds the channel's liveness and reconnect settings, so tests can
// change them.
type dialTuning struct {
	keepalive        keepalive.ClientParameters
	callTimeout      time.Duration
	heartbeatTimeout time.Duration
	reconnect        backoff.Config
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
		reconnect:        reconnectBackoff(),
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

// reconnectBackoff is the reconnect backoff of the agent's channel: grpc-go's
// default ramp (1s, x1.6, 20% jitter) with the cap lowered from 120s to
// reportRetryMaxDelay. The cap matters because a fail-fast RPC (ReportState,
// Heartbeat) on a channel in TRANSIENT_FAILURE returns Unavailable without
// starting a connect: only the channel's own schedule redials. With the
// default cap, after a long outage the channel could stay down for up to two
// minutes after the control plane returned while the report loop retried
// every 15s in vain, and the heartbeats carried by the same channel lapsed
// with it.
func reconnectBackoff() backoff.Config {
	return backoff.Config{
		BaseDelay:  time.Second,
		Multiplier: 1.6,
		Jitter:     0.2,
		MaxDelay:   reportRetryMaxDelay,
	}
}

// minConnectTimeout is the least time one connection attempt gets, grpc-go's
// default.
const minConnectTimeout = 20 * time.Second

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
		// MinConnectTimeout restates grpc-go's default: a zero value here would
		// bound each connection attempt by the backoff delay instead (1s at
		// first), too short for a TLS handshake to a loaded server.
		grpc.WithConnectParams(grpc.ConnectParams{Backoff: tuning.reconnect, MinConnectTimeout: minConnectTimeout}),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dialing control plane at %q: %w", addr, err)
	}
	return agentv1.NewAgentServiceClient(conn), conn, tokens, nil
}
