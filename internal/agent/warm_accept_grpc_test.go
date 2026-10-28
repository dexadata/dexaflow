package agent

import (
	"context"
	"net"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// stepDownAgent ends every assignment stream not-leader, after announcing the
// registration when announce is set (a leader that later stepped down) or
// straight away when it is not (a follower refusing the stream).
type stepDownAgent struct {
	agentv1.UnimplementedAgentServiceServer
	announce bool
}

func (a stepDownAgent) AwaitAssignment(stream grpc.BidiStreamingServer[agentv1.WorkerMessage, agentv1.WorkAssignment]) error {
	if a.announce {
		if _, err := stream.Recv(); err != nil {
			return err
		}
		if err := stream.SendHeader(metadata.Pairs(warmRegisteredHeader, "true")); err != nil {
			return err
		}
	}
	return status.Error(codes.FailedPrecondition, "not the scheduler leader; reconnect to reach the leader")
}

// TestAcceptedByHeaderOverGRPC checks the acceptance signal over a real gRPC
// transport: after the stream has ended not-leader, Header returns at once and
// carries the registration only when the server sent it.
func TestAcceptedByHeaderOverGRPC(t *testing.T) {
	for _, tc := range []struct {
		name     string
		announce bool
	}{{"leader stepped down", true}, {"follower refused", false}} {
		t.Run(tc.name, func(t *testing.T) {
			lis := bufconn.Listen(1 << 20)
			srv := grpc.NewServer()
			agentv1.RegisterAgentServiceServer(srv, stepDownAgent{announce: tc.announce})
			go func() { _ = srv.Serve(lis) }()
			defer srv.Stop()
			conn, err := grpc.NewClient("passthrough:///bufnet",
				grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
				grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer func() { _ = conn.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := agentv1.NewAgentServiceClient(conn).AwaitAssignment(ctx)
			if err != nil {
				t.Fatalf("open stream: %v", err)
			}
			_ = stream.Send(&agentv1.WorkerMessage{Msg: &agentv1.WorkerMessage_Register{
				Register: &agentv1.WorkerRegister{DagVersionId: "dv"}}})
			if _, rerr := stream.Recv(); status.Code(rerr) != codes.FailedPrecondition {
				t.Fatalf("recv: %v, want FailedPrecondition", rerr)
			}
			if got := acceptedByHeader(stream); got != tc.announce {
				t.Fatalf("acceptedByHeader = %v, want %v", got, tc.announce)
			}
		})
	}
}
