package agent

import (
	"context"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/metadata"
)

// heartbeatStream is a concurrency-safe AwaitAssignment client double for the
// stream heartbeat: Recv blocks until the worker has sent `want` messages (or a
// deadline passes), then ends the stream with io.EOF.
type heartbeatStream struct {
	ctx  context.Context
	want int

	mu      sync.Mutex
	sent    []*agentv1.WorkerMessage
	reached chan struct{}
	once    sync.Once
}

func (s *heartbeatStream) Send(m *agentv1.WorkerMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	if len(s.sent) >= s.want {
		s.once.Do(func() { close(s.reached) })
	}
	return nil
}

func (s *heartbeatStream) Recv() (*agentv1.WorkAssignment, error) {
	select {
	case <-s.reached:
	case <-time.After(5 * time.Second):
	}
	return nil, io.EOF
}

func (s *heartbeatStream) messages() []*agentv1.WorkerMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*agentv1.WorkerMessage(nil), s.sent...)
}

func (s *heartbeatStream) Header() (metadata.MD, error) { return metadata.MD{}, nil }
func (s *heartbeatStream) Trailer() metadata.MD         { return nil }
func (s *heartbeatStream) CloseSend() error             { return nil }
func (s *heartbeatStream) Context() context.Context     { return s.ctx }
func (s *heartbeatStream) SendMsg(any) error            { return nil }
func (s *heartbeatStream) RecvMsg(any) error            { return nil }

// An idle warm worker re-sends its WorkerRegister on the open stream at
// StreamHeartbeat, so the control plane can tell a live worker from a stale
// stream and refuse a duplicate registration of a live identity.
func TestWarmWorkerSendsStreamHeartbeat(t *testing.T) {
	stream := &heartbeatStream{ctx: context.Background(), want: 3, reached: make(chan struct{})}
	client := &warmFake{fakeClient: &fakeClient{}, streamOverride: stream}
	w := &WarmRunner{
		StreamClient:    client,
		WorkClient:      client,
		AttemptTokens:   NewTokenSource("bootstrap"),
		Cmd:             &scratchProbeCmd{scratchDir: filepath.Join(t.TempDir(), "scratch")},
		PodName:         "warm-pod-1",
		ScratchDir:      filepath.Join(t.TempDir(), "scratch"),
		StreamHeartbeat: 5 * time.Millisecond,
	}
	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("WarmRunner.Run: %v", err)
	}
	msgs := stream.messages()
	if len(msgs) < 3 {
		t.Fatalf("worker sent %d messages, want the register plus at least two heartbeats", len(msgs))
	}
	for i, m := range msgs {
		reg := m.GetRegister()
		if reg == nil {
			t.Fatalf("message %d = %+v, want a WorkerRegister heartbeat", i, m)
		}
		if reg.GetDagVersionId() != "dagver-1" || reg.GetPodName() != "warm-pod-1" {
			t.Fatalf("heartbeat %d = %+v, want dagver-1 / warm-pod-1", i, reg)
		}
	}
	// Run has returned, so the heartbeat goroutine has stopped: nothing more is sent.
	n := len(stream.messages())
	time.Sleep(20 * time.Millisecond)
	if got := len(stream.messages()); got != n {
		t.Fatalf("heartbeat kept sending after Run returned: %d -> %d messages", n, got)
	}
}

// A negative StreamHeartbeat disables the heartbeat: only the initial register
// is sent.
func TestWarmWorkerStreamHeartbeatDisabled(t *testing.T) {
	stream := &heartbeatStream{ctx: context.Background(), want: 2, reached: make(chan struct{})}
	client := &warmFake{fakeClient: &fakeClient{}, streamOverride: stream}
	w := &WarmRunner{
		StreamClient:    client,
		WorkClient:      client,
		AttemptTokens:   NewTokenSource("bootstrap"),
		Cmd:             &scratchProbeCmd{scratchDir: filepath.Join(t.TempDir(), "scratch")},
		ScratchDir:      filepath.Join(t.TempDir(), "scratch"),
		StreamHeartbeat: -1,
		IdleTTL:         50 * time.Millisecond,
	}
	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("WarmRunner.Run: %v", err)
	}
	if got := len(stream.messages()); got != 1 {
		t.Fatalf("worker sent %d messages with the heartbeat disabled, want 1", got)
	}
}

// The task never learns where the agent's projected token lives: the token path
// and transport marker are agent-only variables under both prefixes.
func TestTaskEnvOmitsAgentTokenLocation(t *testing.T) {
	for _, name := range []string{
		"LEOFLOW_AGENT_TOKEN_PATH", "DEXAFLOW_AGENT_TOKEN_PATH",
		"LEOFLOW_AGENT_TOKEN_TRANSPORT", "DEXAFLOW_AGENT_TOKEN_TRANSPORT",
		"LEOFLOW_AGENT_TOKEN", "DEXAFLOW_AGENT_TOKEN",
	} {
		if !isAgentOnlyEnv(name) {
			t.Errorf("%s must be stripped from the task env", name)
		}
	}
	got := stripAgentOnly([]string{"PATH=/usr/bin", "LEOFLOW_AGENT_TOKEN_PATH=/var/run/leoflow/token"})
	if len(got) != 1 || got[0] != "PATH=/usr/bin" {
		t.Fatalf("stripAgentOnly kept the token path: %v", got)
	}
}
