package agent_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/dexadata/dexaflow/internal/agent"
	"github.com/dexadata/dexaflow/internal/logs"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// logServer is a minimal StreamLogs server that persists received lines to a real
// DiskSink, mirroring the control plane's write path (recv until EOF, then flush
// on Close).
type logServer struct {
	agentv1.UnimplementedAgentServiceServer
	sink *logs.DiskSink
	ref  logs.Ref
}

func (s *logServer) StreamLogs(stream grpc.BidiStreamingServer[agentv1.LogLine, agentv1.LogAck]) error {
	w, err := s.sink.Open(s.ref)
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }() // flushes the buffered lines to disk
	for {
		line, rerr := stream.Recv()
		if errors.Is(rerr, io.EOF) {
			return nil
		}
		if rerr != nil {
			return rerr
		}
		_ = w.WriteEvent(logs.Event{Time: line.GetTime().AsTime(), Stream: line.GetStream(), Message: line.GetMessage()})
	}
}

// TestLogSinkDeliversBeforeClose is the regression guard for the empty-task-log
// bug: the agent's log sink must guarantee that every line it sent is actually
// persisted by the time Close() returns. The agent closes its gRPC connection
// immediately after Close(); if Close only half-closed the stream (CloseSend)
// without draining the server's response, a short task's queued lines were torn
// down with the connection and never reached disk — a 0-byte log file. Close must
// block until the server has consumed and flushed every line.
func TestLogSinkDeliversBeforeClose(t *testing.T) {
	dir := t.TempDir()
	ref := logs.Ref{TenantID: "tn", DagID: "leoflow", RunID: "run1", TaskID: "hello", TryNumber: 1}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, &logServer{sink: logs.NewDiskSink(dir), ref: ref})
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	client := agentv1.NewAgentServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sink, err := agent.OpenLogSink(ctx, client)
	if err != nil {
		t.Fatalf("OpenLogSink: %v", err)
	}
	if serr := sink.Send(&agentv1.LogLine{Time: timestamppb.Now(), Stream: "stdout", Message: "HELLO_FROM_TASK", LineNumber: 1}); serr != nil {
		t.Fatalf("Send: %v", serr)
	}
	// Close must block until the line is delivered + flushed server-side.
	if cerr := sink.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	// The agent tears the connection down right after Close — exactly the moment
	// that used to lose the line.
	_ = conn.Close()

	data, err := os.ReadFile(filepath.Join(dir, "tn", "leoflow", "run1", "hello", "1.log"))
	if err != nil {
		t.Fatalf("reading the persisted log: %v", err)
	}
	if !strings.Contains(string(data), "HELLO_FROM_TASK") {
		t.Fatalf("log line was not persisted before Close returned; file=%q", string(data))
	}
}

// TestLogSinkSurvivesInvalidUTF8 guards the log stream against task output that
// is not valid UTF-8 (Latin-1 text, binary data, a truncated multi-byte
// sequence). LogLine's message is a proto string, so sending such a line as is
// fails the marshal and gRPC ends the stream: that line, every later one and the
// ones still queued were lost. The sink must replace the invalid bytes and keep
// the stream alive.
func TestLogSinkSurvivesInvalidUTF8(t *testing.T) {
	dir := t.TempDir()
	ref := logs.Ref{TenantID: "tn", DagID: "leoflow", RunID: "run1", TaskID: "latin1", TryNumber: 1}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(srv, &logServer{sink: logs.NewDiskSink(dir), ref: ref})
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
	sink, err := agent.OpenLogSink(ctx, agentv1.NewAgentServiceClient(conn))
	if err != nil {
		t.Fatalf("OpenLogSink: %v", err)
	}
	for _, msg := range []string{"before", "bad \xff\xfe bytes", "after"} {
		if sendErr := sink.Send(&agentv1.LogLine{Time: timestamppb.Now(), Stream: "stdout", Message: msg}); sendErr != nil {
			t.Fatalf("Send(%q): %v", msg, sendErr)
		}
	}
	if err = sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "tn", "leoflow", "run1", "latin1", "1.log"))
	if err != nil {
		t.Fatalf("read stored log: %v", err)
	}
	got := string(b)
	for _, want := range []string{"before", "bad \uFFFD bytes", "after"} {
		if !strings.Contains(got, want) {
			t.Errorf("stored log lacks %q:\n%s", want, got)
		}
	}
}
