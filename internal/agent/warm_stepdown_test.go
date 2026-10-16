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
	"google.golang.org/grpc/metadata"
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

var errNotLeader = status.Error(codes.FailedPrecondition, "not the scheduler leader; reconnect to reach the leader")

// runWithStreams runs a warm worker over the scripted streams with a
// reconnect budget of 2.
func runWithStreams(t *testing.T, client *streamSeqFake) error {
	t.Helper()
	w := &WarmRunner{
		StreamClient: client, WorkClient: client, AttemptTokens: NewTokenSource("bootstrap"),
		Cmd: &fakeCmd{}, Hostname: "w", Version: "t", ScratchDir: filepath.Join(t.TempDir(), "scratch"),
		ReconnectBackoff: time.Microsecond, ReconnectMaxBackoff: time.Millisecond, MaxReconnects: 2,
		Redial: func() (agentv1.AgentServiceClient, *TokenSource, io.Closer, error) {
			return client, nil, noopCloser{}, nil
		},
	}
	return w.Run(context.Background(), "dagver-1")
}

// TestWarmWorkerReconnectBudgetResetsOnceALeaderAccepts: the not-leader
// reconnect budget bounds consecutive rejections of a worker that cannot find a
// leader. Since a step-down sends every idle worker on (#1602), a stream a
// leader accepted (it announced the registration in its response header) must
// reset it; otherwise a long-lived worker exits after MaxReconnects leader
// changes over its whole life.
func TestWarmWorkerReconnectBudgetResetsOnceALeaderAccepts(t *testing.T) {
	accepted := func() *fakeAssignmentStream {
		return &fakeAssignmentStream{ctx: context.Background(),
			header: metadata.Pairs(warmRegisteredHeader, "true"), recvErr: errNotLeader}
	}
	client := &streamSeqFake{fakeClient: &fakeClient{},
		streams: []*fakeAssignmentStream{accepted(), accepted(), accepted(), {ctx: context.Background()}}}
	if err := runWithStreams(t, client); err != nil {
		t.Fatalf("WarmRunner.Run: %v, want nil (three step-downs, each after a leader accepted the worker)", err)
	}
	if client.calls != 4 {
		t.Fatalf("streams opened = %d, want 4", client.calls)
	}
}

// TestWarmWorkerReconnectBudgetResetsAfterServingAnAssignment: a control plane
// that does not announce the registration still proves acceptance by sending an
// assignment on the stream.
func TestWarmWorkerReconnectBudgetResetsAfterServingAnAssignment(t *testing.T) {
	served := func() *fakeAssignmentStream {
		return &fakeAssignmentStream{ctx: context.Background(), recvErr: errNotLeader,
			assignments: []*agentv1.WorkAssignment{
				{AssignmentId: "asg", AttemptToken: "tok", DagRunId: "run", TaskId: "t", TryNumber: 1},
			}}
	}
	spec := &agentv1.TaskSpec{Operator: "bash", Entrypoint: "true", RunId: "run", TaskId: "t", TryNumber: 1}
	client := &streamSeqFake{fakeClient: &fakeClient{spec: spec},
		streams: []*fakeAssignmentStream{served(), served(), served(), {ctx: context.Background()}}}
	if err := runWithStreams(t, client); err != nil {
		t.Fatalf("WarmRunner.Run: %v, want nil (each stream served an assignment before the step-down)", err)
	}
}

// TestWarmWorkerReconnectBudgetStillBoundsUnacceptedStreams: streams that end
// not-leader with no sign a leader accepted them keep counting.
func TestWarmWorkerReconnectBudgetStillBoundsUnacceptedStreams(t *testing.T) {
	refused := func() *fakeAssignmentStream {
		return &fakeAssignmentStream{ctx: context.Background(), recvErr: errNotLeader}
	}
	client := &streamSeqFake{fakeClient: &fakeClient{},
		streams: []*fakeAssignmentStream{refused(), refused(), refused(), {ctx: context.Background()}}}
	if err := runWithStreams(t, client); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("WarmRunner.Run: %v, want the not-leader give-up after 2 reconnects", err)
	}
}
