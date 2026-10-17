package agent

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
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

// resumeServer ends the first log stream after one line, the way a control
// plane shutting down does, and answers every later stream with later.
type resumeServer struct {
	agentv1.UnimplementedAgentServiceServer
	later func(grpc.BidiStreamingServer[agentv1.LogLine, agentv1.LogAck]) error

	mu      sync.Mutex
	streams int
	resumes int
	lines   [][]string // lines received, per stream
}

func (s *resumeServer) StreamLogs(stream grpc.BidiStreamingServer[agentv1.LogLine, agentv1.LogAck]) error {
	md, _ := metadata.FromIncomingContext(stream.Context())
	s.mu.Lock()
	n := s.streams
	s.streams++
	s.lines = append(s.lines, nil)
	if len(md.Get(LogResumeMetadataKey)) > 0 {
		s.resumes++
	}
	s.mu.Unlock()
	if n > 0 {
		return s.later(stream)
	}
	if _, err := stream.Recv(); err != nil {
		return err
	}
	s.record(n, "first")
	return status.Error(codes.Unavailable, "control plane shutting down; log stream closed")
}

func (s *resumeServer) record(stream int, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines[stream] = append(s.lines[stream], line)
}

func (s *resumeServer) snapshot() (streams, resumes int, lines [][]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streams, s.resumes, append([][]string(nil), s.lines...)
}

// openTestSink serves srv over an in-memory connection and opens the agent's
// log sink on it, with instant re-open backoff and the given resume wait.
func openTestSink(t *testing.T, srv *resumeServer, ackWait time.Duration) *grpcLogSink {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gsrv := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(gsrv, srv)
	go func() { _ = gsrv.Serve(lis) }()
	t.Cleanup(gsrv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	sink, err := OpenLogSink(ctx, agentv1.NewAgentServiceClient(conn))
	if err != nil {
		t.Fatal(err)
	}
	s := sink.(*grpcLogSink)
	s.ackWait = ackWait
	s.after = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	return s
}

// sendUntilGone sends lines until Send reports that the sink stopped
// re-opening its stream.
func sendUntilGone(t *testing.T, s *grpcLogSink) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := s.Send(&agentv1.LogLine{Message: "later"})
		if errors.Is(err, errLogStreamGone) {
			return
		}
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the sink never stopped re-opening its stream")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLogSinkDoesNotWriteToAnUnconfirmedResume: a control plane older than the
// resume opens the re-opened stream with an empty writer, so writing to it
// would overwrite the attempt's stored log. It never confirms the resume; the
// sink must not send a line on that stream, and must stop re-opening.
func TestLogSinkDoesNotWriteToAnUnconfirmedResume(t *testing.T) {
	srv := &resumeServer{}
	srv.later = func(stream grpc.BidiStreamingServer[agentv1.LogLine, agentv1.LogAck]) error {
		for {
			if _, err := stream.Recv(); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			srv.record(1, "later")
		}
	}
	s := openTestSink(t, srv, 100*time.Millisecond)
	if err := s.Send(&agentv1.LogLine{Message: "first"}); err != nil {
		t.Fatal(err)
	}
	sendUntilGone(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	streams, resumes, lines := srv.snapshot()
	if streams != 2 || resumes != 1 {
		t.Fatalf("streams=%d resumes=%d, want one resumed re-open", streams, resumes)
	}
	if len(lines[1]) != 0 {
		t.Fatalf("the sink wrote %d lines to a resume the control plane never confirmed", len(lines[1]))
	}
}

// TestLogSinkStopsOnPermanentRefusal: a resume refused for good (a sink that
// cannot resume answers Unimplemented) ends the re-open after one try.
func TestLogSinkStopsOnPermanentRefusal(t *testing.T) {
	srv := &resumeServer{later: func(grpc.BidiStreamingServer[agentv1.LogLine, agentv1.LogAck]) error {
		return status.Error(codes.Unimplemented, "log stream resume is not supported by the configured log sink")
	}}
	s := openTestSink(t, srv, 5*time.Second)
	if err := s.Send(&agentv1.LogLine{Message: "first"}); err != nil {
		t.Fatal(err)
	}
	sendUntilGone(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if streams, resumes, _ := srv.snapshot(); streams != 2 || resumes != 1 {
		t.Fatalf("streams=%d resumes=%d, want one re-open and no retry of a permanent refusal", streams, resumes)
	}
}

// TestLogSinkRetriesTransientRefusal: a resume refused with Unavailable (the
// replica it reached is shutting down too) is retried, and the lines kept
// while the stream was down go out, in order, on the stream that is confirmed.
func TestLogSinkRetriesTransientRefusal(t *testing.T) {
	srv := &resumeServer{}
	srv.later = func(stream grpc.BidiStreamingServer[agentv1.LogLine, agentv1.LogAck]) error {
		srv.mu.Lock()
		n := srv.streams - 1
		srv.mu.Unlock()
		if n < 3 {
			return status.Error(codes.Unavailable, "control plane shutting down; log stream not accepted")
		}
		if err := stream.SendHeader(metadata.Pairs(LogResumedHeaderKey, "1")); err != nil {
			return err
		}
		for {
			line, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			srv.record(n, line.GetMessage())
		}
	}
	s := openTestSink(t, srv, 5*time.Second)
	if err := s.Send(&agentv1.LogLine{Message: "first"}); err != nil {
		t.Fatal(err)
	}
	// Keep printing until the confirmed stream is up: the lines printed while
	// the stream was down wait for it.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if streams, _, _ := srv.snapshot(); streams == 4 {
			break
		}
		if err := s.Send(&agentv1.LogLine{Message: "probe"}); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("the sink never reached the confirmed stream")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, msg := range []string{"one", "two", "three"} {
		if err := s.Send(&agentv1.LogLine{Message: msg}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	streams, resumes, lines := srv.snapshot()
	if streams != 4 || resumes != 3 {
		t.Fatalf("streams=%d resumes=%d, want two refused re-opens and a confirmed one", streams, resumes)
	}
	got := lines[3]
	if len(got) < 3 || got[len(got)-3] != "one" || got[len(got)-2] != "two" || got[len(got)-1] != "three" {
		t.Fatalf("confirmed stream received %q, want the kept lines ending one, two, three", got)
	}
}
