package agent

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// streamSeqFake hands out one scripted assignment stream per AwaitAssignment
// call, in order.
type streamSeqFake struct {
	*fakeClient
	streams []*fakeAssignmentStream
	calls   int
}

func (c *streamSeqFake) AwaitAssignment(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[agentv1.WorkerMessage, agentv1.WorkAssignment], error) {
	s := c.streams[c.calls]
	c.calls++
	return s, nil
}

// TestWarmWorkerReconnectsWhenAnOpenStreamEndsNotLeader: the control plane ends
// an established stream with FailedPrecondition when its replica steps down
// (#1602). The worker, idle on that stream, re-dials toward the new leader and
// registers there instead of exiting or waiting out its idle TTL.
func TestWarmWorkerReconnectsWhenAnOpenStreamEndsNotLeader(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "scratch")
	oldLeader := &fakeAssignmentStream{
		ctx:     context.Background(),
		recvErr: status.Error(codes.FailedPrecondition, "not the scheduler leader; reconnect to reach the leader"),
	}
	newLeader := &fakeAssignmentStream{ctx: context.Background()} // EOF: clean exit once registered
	client := &streamSeqFake{fakeClient: &fakeClient{}, streams: []*fakeAssignmentStream{oldLeader, newLeader}}
	redials := 0
	w := &WarmRunner{
		StreamClient: client, WorkClient: client, AttemptTokens: NewTokenSource("bootstrap"),
		Cmd: &fakeCmd{}, Hostname: "w", Version: "t", ScratchDir: scratch,
		ReconnectBackoff: time.Microsecond, ReconnectMaxBackoff: time.Millisecond,
		Redial: func() (agentv1.AgentServiceClient, *TokenSource, io.Closer, error) {
			redials++
			return client, nil, noopCloser{}, nil
		},
	}
	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("WarmRunner.Run: %v, want nil after reconnecting to the new leader", err)
	}
	if client.calls != 2 || redials != 1 {
		t.Fatalf("streams opened = %d, redials = %d; want 2 and 1", client.calls, redials)
	}
	if reg := newLeader.sent; len(reg) == 0 || reg[0].GetRegister().GetDagVersionId() != "dagver-1" {
		t.Fatalf("the worker did not register on the new leader's stream: %+v", reg)
	}
}
