package agentrpc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/agent"
	"github.com/dexadata/dexaflow/internal/logs"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// bucket is an in-memory logs.ObjectStore shared by the control-plane replicas
// of a test, standing in for the S3 or GCS bucket they share.
type bucket struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (b *bucket) Put(_ context.Context, key string, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objs[key] = body
	return nil
}

func (b *bucket) Get(_ context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	body, ok := b.objs[key]
	if !ok {
		return nil, logs.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), body...))), nil
}

// countingSink is the object sink with a count of the lines its writers
// received, so a test can wait for the control plane to have read a line.
type countingSink struct {
	*logs.ObjectSink
	lines atomic.Int32
}

func (c *countingSink) Open(ref logs.Ref) (logs.LogWriter, error) {
	w, err := c.ObjectSink.Open(ref)
	return &countingWriter{LogWriter: w, n: &c.lines}, err
}

func (c *countingSink) Resume(ref logs.Ref) (logs.LogWriter, error) {
	w, err := c.ObjectSink.Resume(ref)
	return &countingWriter{LogWriter: w, n: &c.lines}, err
}

type countingWriter struct {
	logs.LogWriter
	n *atomic.Int32
}

func (w *countingWriter) WriteEvent(ev logs.Event) error {
	w.n.Add(1)
	return w.LogWriter.WriteEvent(ev)
}

// storedMessages reads the attempt's stored log back through the sink.
func storedMessages(t *testing.T, sink *logs.ObjectSink, ref logs.Ref) []string {
	t.Helper()
	rc, err := sink.Read(t.Context(), ref)
	if err != nil {
		return nil
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading the stored log: %v", err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if l != "" {
			out = append(out, logs.DecodeLine(l).Message)
		}
	}
	return out
}

// serveReplica starts a control-plane replica (a Server sharing sink) on addr
// and returns its gRPC server and the cancel that begins its shutdown.
func serveReplica(t *testing.T, addr string, sink LogSink) (*grpc.Server, context.CancelFunc) {
	t.Helper()
	srv, _ := newServer(&fakeStore{spec: TaskSpec{Operator: "python"}})
	srv.SetLogSink(sink)
	shutdown, begin := context.WithCancel(context.Background())
	srv.SetShutdown(shutdown)
	lis := listenAgain(t, addr)
	gsrv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(gsrv, srv)
	go func() { _ = gsrv.Serve(lis) }()
	return gsrv, begin
}

// listenAgain listens on addr, a port this test chose and released. While no
// replica listens there, the agent keeps dialing it, and a dial from an
// ephemeral port equal to addr's connects to itself and holds the port until
// the client drops it; another package's test may also be handed the port for
// a moment. So an address already in use is retried for a few seconds.
func listenAgain(t *testing.T, addr string) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	deadline := time.Now().Add(5 * time.Second)
	for {
		lis, err := lc.Listen(context.Background(), "tcp", addr)
		if err == nil {
			return lis
		}
		if !errors.Is(err, syscall.EADDRINUSE) || time.Now().After(deadline) {
			t.Fatalf("listen %s: %v", addr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestStreamLogsResumesAfterControlPlaneRestart is the end-to-end case of #912
// over the production pieces: the agent's log sink (agent.Dial,
// agent.OpenLogSink), StreamLogs, and the object sink in its default single
// layout. The control plane shuts down mid-task (closing and flushing the
// stream), a replacement comes up on the same address, and the task keeps
// printing. Every line printed after the restart must reach the stored log,
// and the lines stored before it must still be there, in order.
func TestStreamLogsResumesAfterControlPlaneRestart(t *testing.T) {
	if agent.LogResumeMetadataKey != logResumeMetadataKey || agent.LogResumedHeaderKey != logResumedHeaderKey {
		t.Fatalf("resume keys drifted: agent %q/%q, agentrpc %q/%q", agent.LogResumeMetadataKey, agent.LogResumedHeaderKey, logResumeMetadataKey, logResumedHeaderKey)
	}
	objects := logs.NewObjectSink(context.Background(), &bucket{objs: map[string][]byte{}}, "", nil)
	sink := &countingSink{ObjectSink: objects}
	id := testIdentity()
	ref := logs.Ref{TenantID: id.TenantID, DagID: id.DagID, RunID: id.RunID, TaskID: id.TaskID, TryNumber: id.TryNumber}

	var lc net.ListenConfig
	probe, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	_ = probe.Close()
	first, beginShutdown := serveReplica(t, addr, sink)

	_, authn := newServer(&fakeStore{})
	token, err := authn.IssueAgentToken(id, time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	client, conn, _, err := agent.Dial(addr, token, true, "")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logSink, err := agent.OpenLogSink(ctx, client)
	if err != nil {
		t.Fatalf("OpenLogSink: %v", err)
	}
	send := func(msg string) {
		t.Helper()
		if serr := logSink.Send(&agentv1.LogLine{Message: msg, Stream: "stdout"}); serr != nil {
			t.Fatalf("Send(%q): %v", msg, serr)
		}
	}
	send("before 1")
	send("before 2")

	// The control plane shuts down once it has read both lines: the stream is
	// closed and flushed.
	waitFor(t, 5*time.Second, "the control plane to read the first lines", func() bool { return sink.lines.Load() == 2 })
	beginShutdown()
	waitFor(t, 5*time.Second, "the closed stream's flush", func() bool {
		return len(storedMessages(t, objects, ref)) == 2
	})
	first.Stop()

	// The task keeps printing while the control plane is down, then a
	// replacement comes up on the same address. The lines printed in the gap
	// may be lost in the transport when the end of the stream has not reached
	// the agent yet; every line printed after the agent saw it must arrive.
	n := 0
	for ; n < 3; n++ {
		send("gap")
		time.Sleep(50 * time.Millisecond)
	}
	second, _ := serveReplica(t, addr, sink)
	defer second.Stop()
	send("after restart")
	if cerr := logSink.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	got := storedMessages(t, objects, ref)
	if len(got) < 3 || got[0] != "before 1" || got[1] != "before 2" || got[len(got)-1] != "after restart" {
		t.Fatalf("stored log = %q, want the pre-restart prefix kept and the post-restart line appended", got)
	}
	for _, m := range got[2 : len(got)-1] {
		if m != "gap" {
			t.Fatalf("stored log = %q, want only gap lines between the prefix and the last line", got)
		}
	}
}

// TestStreamLogsResumeRefusedBySinkWithoutResume: a resume asked of a sink that
// cannot continue a stored log is refused, never served with Open (which would
// overwrite what the attempt stored).
func TestStreamLogsResumeRefusedBySinkWithoutResume(t *testing.T) {
	srv, authn := newServer(&fakeStore{spec: TaskSpec{Operator: "python"}})
	sink := &captureSink{}
	srv.SetLogSink(sink)
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gsrv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(gsrv, srv)
	go func() { _ = gsrv.Serve(lis) }()
	defer gsrv.Stop()

	token, err := authn.IssueAgentToken(testIdentity(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token, logResumeMetadataKey, "1")
	stream, err := agentv1.NewAgentServiceClient(conn).StreamLogs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, rerr := stream.Recv()
	if status.Code(rerr) != codes.Unimplemented {
		t.Fatalf("resume on a sink without Resume ended with %v, want Unimplemented", rerr)
	}
	if lines, _ := sink.snapshot(); len(lines) != 0 {
		t.Fatalf("the sink was written: %q", lines)
	}
}

// waitFor polls cond until it holds or the timeout passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
