package agentrpc

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/agent"
	"github.com/dexadata/dexaflow/internal/logs"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
)

// captureSink is an agentrpc.LogSink that records every line written through it.
type captureSink struct {
	mu     sync.Mutex
	lines  []string
	closed bool
}

func (c *captureSink) Open(logs.Ref) (logs.LogWriter, error) { return c, nil }

func (c *captureSink) WriteEvent(ev logs.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, ev.Message)
	return nil
}

func (c *captureSink) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *captureSink) snapshot() ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.lines...), c.closed
}

// TestStreamLogsOverRealTransport drives StreamLogs over a real gRPC connection
// using the production agent client (agent.Dial + agent.OpenLogSink), the way a
// task pod does. The in-process fake-stream tests bypass the transport and auth
// handshake, so they cannot catch a regression where the stream EOFs on open
// (see #36). Lines sent by the agent must land in the control plane's sink.
func TestStreamLogsOverRealTransport(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{Operator: "python"}}
	srv, authn := newServer(store)
	sink := &captureSink{}
	srv.SetLogSink(sink)

	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gsrv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(gsrv, srv)
	go func() { _ = gsrv.Serve(lis) }()
	defer gsrv.Stop()

	token, err := authn.IssueAgentToken(testIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	client, conn, _, err := agent.Dial(lis.Addr().String(), token, true, "")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientSink, err := agent.OpenLogSink(ctx, client)
	if err != nil {
		t.Fatalf("OpenLogSink (the #36 failure point): %v", err)
	}

	// A real task writes stdout and stderr from separate goroutines, both onto
	// this single stream — exactly the concurrency that broke #36. Reproduce it:
	// without serialization, concurrent Send corrupts the stream (EOF / -race).
	const perStream = 25
	var wg sync.WaitGroup
	for _, streamName := range []string{"stdout", "stderr"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			for i := 0; i < perStream; i++ {
				if serr := clientSink.Send(&agentv1.LogLine{Message: name, Stream: name, LineNumber: int64(i + 1)}); serr != nil {
					t.Errorf("Send(%s): %v", name, serr)
					return
				}
			}
		}(streamName)
	}
	wg.Wait()
	if cerr := clientSink.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	// The server drains asynchronously; wait briefly for the sink to fill.
	want := perStream * 2
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got, closed := sink.snapshot(); len(got) == want && closed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, closed := sink.snapshot()
	if len(got) != want {
		t.Fatalf("sink received %d lines, want %d (concurrent Send corrupted the stream — #36)", len(got), want)
	}
	if !closed {
		t.Error("sink writer was not closed (logs not flushed)")
	}
}
