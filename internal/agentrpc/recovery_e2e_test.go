package agentrpc_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// poisonAgent is a stub AgentService: one RPC panics (a malformed/poison request
// hitting a handler bug), a unary RPC succeeds (to prove the server survives),
// and a stream RPC panics (to exercise the stream interceptor).
type poisonAgent struct {
	agentv1.UnimplementedAgentServiceServer
}

func (poisonAgent) Heartbeat(context.Context, *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error) {
	panic("poison: malformed heartbeat blew up the handler")
}

func (poisonAgent) Register(context.Context, *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error) {
	return &agentv1.RegisterResponse{}, nil
}

func (poisonAgent) StreamLogs(grpc.BidiStreamingServer[agentv1.LogLine, agentv1.LogAck]) error {
	panic("poison: stream handler blew up")
}

// TestRecoveryInterceptorsEndToEnd drives a real gRPC client against a real
// server wired with the recovery interceptors exactly as main.go wires them. A
// poison RPC that panics must return Internal to the client without crashing the
// server — a subsequent healthy RPC still succeeds.
func TestRecoveryInterceptorsEndToEnd(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(agentrpc.RecoveryUnaryInterceptor(logger)),
		grpc.ChainStreamInterceptor(agentrpc.RecoveryStreamInterceptor(logger)),
	)
	agentv1.RegisterAgentServiceServer(srv, poisonAgent{})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	client := agentv1.NewAgentServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1) The poison unary RPC: the handler panics; the client must see Internal,
	//    not a connection reset / crash.
	if _, hbErr := client.Heartbeat(ctx, &agentv1.HeartbeatRequest{}); status.Code(hbErr) != codes.Internal {
		t.Fatalf("poison Heartbeat: got code %v (%v), want Internal", status.Code(hbErr), hbErr)
	}

	// 2) The server survived: a healthy RPC on the same connection still works.
	if _, regErr := client.Register(ctx, &agentv1.RegisterRequest{}); regErr != nil {
		t.Fatalf("server should keep serving after a recovered panic; Register failed: %v", regErr)
	}

	// 3) A poison streaming RPC is likewise recovered to Internal.
	stream, err := client.StreamLogs(ctx)
	if err != nil {
		t.Fatalf("opening StreamLogs: %v", err)
	}
	_, rerr := stream.Recv()
	if status.Code(rerr) != codes.Internal {
		t.Fatalf("poison StreamLogs: got code %v (%v), want Internal", status.Code(rerr), rerr)
	}

	// 4) And still serving after the stream panic too.
	if _, regErr := client.Register(ctx, &agentv1.RegisterRequest{}); regErr != nil {
		t.Fatalf("server should keep serving after a recovered stream panic; Register failed: %v", regErr)
	}
}
