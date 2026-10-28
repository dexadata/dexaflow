package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// LogResumeMetadataKey is the request metadata key with which the agent asks
// the control plane to CONTINUE an attempt's stored log rather than start it:
// it is set on every StreamLogs stream the agent re-opens after the previous one
// ended mid-task (#912). The control plane opens such a stream with the sink's
// Resume, which keeps what is already stored, and confirms it by sending the
// LogResumedHeaderKey response header. agentrpc mirrors both keys.
const LogResumeMetadataKey = "dexaflow-log-resume"

// LogResumedHeaderKey is the response header with which the control plane
// confirms that it accepted a resumed log stream (see LogResumeMetadataKey).
const LogResumedHeaderKey = "dexaflow-log-resumed"

// logDrainTimeout bounds how long Close waits for the server to acknowledge the
// streamed lines, and how long a re-open waits for the ended stream's status.
// It must be generous enough for normal delivery but finite: a task must never
// hang because log shipping stalls.
const logDrainTimeout = 5 * time.Second

// logReopenGrace bounds how long Close waits for a re-open still running when
// the task ends. It is short: a control plane that is still away when the task
// ends is usually away for longer, and the wait is added to the task's end.
const logReopenGrace = 2 * time.Second

// logResumeAckTimeout bounds how long a re-opened stream waits for the control
// plane to confirm the resume. A control plane older than the resume never
// sends the confirmation (its stream sends no header until it ends), and
// writing to it would overwrite the attempt's stored log, so past this wait the
// sink stops re-opening. It is generous because a resume reads the stored log
// back before it answers.
const logResumeAckTimeout = 30 * time.Second

// maxPendingLogBytes bounds the task output the sink keeps while its stream is
// down, waiting for the re-open. Lines past it are dropped and counted.
const maxPendingLogBytes = 4 << 20

// errLogStreamGone is what Send returns once the sink stopped re-opening its
// stream: the remaining lines of the attempt are not shipped.
var errLogStreamGone = errors.New("log stream closed and not re-opened")

// logStream is the client side of one StreamLogs stream.
type logStream = grpc.BidiStreamingClient[agentv1.LogLine, agentv1.LogAck]

// grpcLogSink adapts the StreamLogs bidirectional stream to the LogSink
// interface. The task's stdout and stderr are copied on separate goroutines but
// share this one stream, so Send is serialized with a mutex: gRPC client streams
// do not permit concurrent SendMsg, and racing sends corrupt the stream (see
// #36).
//
// When the stream ends while the task runs (the control plane shut down, or
// the connection broke), the sink re-opens it in the background as a resumed
// stream (LogResumeMetadataKey), so the lines the task prints afterwards are
// still shipped and appended to what was stored, not written over it (#912).
// Until the new stream is up, lines wait in a buffer bounded by
// maxPendingLogBytes; Send never blocks the task's output on the re-open. Lines
// already handed to the transport when the stream ended are lost. The re-open
// retries with the report's backoff (reportBackoff, jittered) while the error
// is transient (retryableTerminalReportErr), until the context ends or Close
// gives up on it; a refusal that is not transient, or a control plane that does
// not confirm the resume, ends it, and Send then returns errLogStreamGone.
type grpcLogSink struct {
	ctx     context.Context
	client  agentv1.AgentServiceClient
	after   func(time.Duration) <-chan time.Time // backoff waits; tests replace it
	ackWait time.Duration                        // logResumeAckTimeout; tests shorten it

	mu           sync.Mutex
	stream       logStream          // nil while the stream is down
	cancelStream context.CancelFunc // ends a re-opened stream; nil for the first
	pending      []*agentv1.LogLine
	pendingBytes int
	dropped      int
	gone         bool               // re-opening stopped; lines are dropped
	reopenDone   chan struct{}      // closed when the running re-open ends; nil when none runs
	cancelReopen context.CancelFunc // stops the running re-open
}

// OpenLogSink starts the StreamLogs RPC and returns a sink that forwards lines
// to it. It is the agent's first RPC, so it uses WaitForReady: with the lazy
// connection of grpc.NewClient the channel may not be established yet, and
// without this the stream would fail fast on a cold connection (the "opening log
// stream" EOF in #36) rather than waiting for the control plane to be reachable.
// ctx also bounds the streams the sink re-opens.
func OpenLogSink(ctx context.Context, client agentv1.AgentServiceClient) (LogSink, error) {
	stream, err := client.StreamLogs(ctx, grpc.WaitForReady(true))
	if err != nil {
		return nil, fmt.Errorf("opening log stream: %w", err)
	}
	return &grpcLogSink{ctx: ctx, client: client, after: time.After, ackWait: logResumeAckTimeout, stream: stream}, nil
}

// Send forwards a single log line to the control plane, serialized so concurrent
// stdout/stderr writers never call the underlying stream's Send at once. Task
// output is arbitrary bytes but LogLine's message is a proto string: invalid
// UTF-8 fails the marshal, and gRPC then tears down the whole stream, losing
// this line, every later one and any still queued. Invalid sequences are
// replaced with U+FFFD instead.
//
// When the stream has ended, the line waits for the re-open (see grpcLogSink)
// and Send returns nil; once the sink stopped re-opening it returns
// errLogStreamGone.
func (s *grpcLogSink) Send(line *agentv1.LogLine) error {
	if !utf8.ValidString(line.Message) {
		line.Message = strings.ToValidUTF8(line.Message, "\uFFFD")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream != nil {
		err := s.stream.Send(line)
		if err == nil {
			return nil
		}
		s.streamEndedLocked()
		if !errors.Is(err, io.EOF) {
			// The client itself failed this line (it could not be sent at all):
			// queueing it would fail the re-opened stream the same way.
			return err
		}
	}
	if s.gone {
		return errLogStreamGone
	}
	s.queueLocked(line)
	return nil
}

// queueLocked keeps a line for the re-opened stream, or drops it once the
// buffer is full. Caller holds mu.
func (s *grpcLogSink) queueLocked(line *agentv1.LogLine) {
	if s.pendingBytes+len(line.Message) > maxPendingLogBytes {
		if s.dropped == 0 {
			slog.Warn("log stream is down and its buffer is full; dropping task log lines until it is re-opened",
				"max_bytes", maxPendingLogBytes)
		}
		s.dropped++
		return
	}
	s.pending = append(s.pending, line)
	s.pendingBytes += len(line.Message)
}

// streamEndedLocked takes the ended stream out of service and starts the
// re-open, unless one is already running. Caller holds mu.
func (s *grpcLogSink) streamEndedLocked() {
	ended, cancel := s.stream, s.cancelStream
	s.stream, s.cancelStream = nil, nil
	if s.gone || s.reopenDone != nil {
		return
	}
	ctx, stop := context.WithCancel(s.ctx)
	done := make(chan struct{})
	s.reopenDone, s.cancelReopen = done, stop
	go func() {
		defer close(done)
		defer stop()
		s.reopen(ctx, ended, cancel)
	}()
}

// reopen waits for the ended stream's status, then opens resumed streams until
// one is confirmed and the waiting lines are on it, or until it gives up.
func (s *grpcLogSink) reopen(ctx context.Context, ended logStream, cancelEnded context.CancelFunc) {
	for attempt := 1; ; attempt++ {
		// The control plane sends a stream's end after its writer's last flush,
		// so once the ended stream's status is read, the resumed writer starts
		// from everything the ended one stored.
		endErr := drainStream(ended, logDrainTimeout)
		if cancelEnded != nil {
			cancelEnded()
		}
		if attempt == 1 {
			slog.Warn("task log stream ended mid-task; re-opening it", "error", endErr)
		}
		stream, cancel, err := s.openResumed(ctx)
		switch {
		case err == nil && s.install(stream, cancel):
			slog.Info("task log stream re-opened", "attempt", attempt)
			return
		case err == nil:
			// The confirmed stream ended while the waiting lines were sent: drain
			// it on the next attempt, after the backoff.
			ended, cancelEnded = stream, cancel
		case ctx.Err() != nil || !retryableTerminalReportErr(err):
			s.giveUp(err)
			return
		default:
			ended, cancelEnded = nil, nil
		}
		select {
		case <-ctx.Done():
			s.giveUp(ctx.Err())
			return
		case <-s.after(jitterDelay(reportBackoff(attempt))):
		}
	}
}

// openResumed opens a StreamLogs stream that asks the control plane to resume
// the attempt's log, and returns it once the control plane confirmed the
// resume. A refusal comes back as the stream's status; no confirmation within
// ackWait is a control plane that cannot resume, reported as errResumeUnsupported.
func (s *grpcLogSink) openResumed(ctx context.Context) (logStream, context.CancelFunc, error) {
	// The stream outlives the re-open (whose ctx ends when it returns), so it
	// hangs off the sink's context, and ctx only ends it until it is confirmed.
	sctx, cancel := context.WithCancel(metadata.AppendToOutgoingContext(s.ctx, LogResumeMetadataKey, "1"))
	detach := context.AfterFunc(ctx, cancel)
	stream, err := s.client.StreamLogs(sctx, grpc.WaitForReady(true)) //nolint:contextcheck // the stream intentionally outlives ctx, see above
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("re-opening log stream: %w", err)
	}
	type header struct {
		md  metadata.MD
		err error
	}
	got := make(chan header, 1)
	go func() {
		md, herr := stream.Header()
		got <- header{md, herr}
	}()
	select {
	case h := <-got:
		if h.err == nil && len(h.md.Get(LogResumedHeaderKey)) > 0 && detach() {
			return stream, cancel, nil
		}
		// No confirmation: the stream ended without headers, and its status
		// says why.
		_, rerr := stream.Recv()
		cancel()
		if h.err != nil {
			rerr = h.err
		}
		if rerr == nil || errors.Is(rerr, io.EOF) {
			rerr = errResumeUnsupported
		}
		return nil, nil, fmt.Errorf("re-opening log stream: %w", rerr)
	case <-time.After(s.ackWait):
		cancel()
		return nil, nil, errResumeUnsupported
	case <-ctx.Done():
		cancel()
		return nil, nil, ctx.Err()
	}
}

// errResumeUnsupported reports a control plane that did not confirm a resumed
// log stream.
var errResumeUnsupported = errors.New("the control plane did not confirm the resumed log stream")

// install sends the waiting lines on a confirmed stream and puts it in
// service. It reports false when the stream failed during that, leaving the
// lines not yet sent waiting for the next attempt.
func (s *grpcLogSink) install(stream logStream, cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.pending) > 0 {
		line := s.pending[0]
		if err := stream.Send(line); err != nil {
			if !errors.Is(err, io.EOF) {
				s.dropPendingHeadLocked()
			}
			return false
		}
		s.dropPendingHeadLocked()
	}
	if s.dropped > 0 {
		slog.Warn("task log lines were dropped while the log stream was down", "lines", s.dropped)
		s.dropped = 0
	}
	s.pending = nil
	s.stream, s.cancelStream = stream, cancel
	s.reopenDone, s.cancelReopen = nil, nil
	return true
}

// dropPendingHeadLocked removes the first waiting line. Caller holds mu.
func (s *grpcLogSink) dropPendingHeadLocked() {
	s.pendingBytes -= len(s.pending[0].Message)
	s.pending[0] = nil
	s.pending = s.pending[1:]
}

// giveUp stops re-opening: the waiting lines and every later one are dropped.
func (s *grpcLogSink) giveUp(cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	slog.Warn("task log stream could not be re-opened; the rest of this attempt's logs will not be shipped",
		"lines_lost", len(s.pending)+s.dropped, "error", cause)
	s.gone = true
	s.pending, s.pendingBytes, s.dropped = nil, 0, 0
	s.reopenDone, s.cancelReopen = nil, nil
}

// drainStream reads a stream to its end, for at most timeout, and returns the
// error that ended it (io.EOF for a clean end). A nil stream returns nil.
func drainStream(stream logStream, timeout time.Duration) error {
	if stream == nil {
		return nil
	}
	ended := make(chan error, 1)
	go func() {
		for {
			if _, err := stream.Recv(); err != nil {
				ended <- err // EOF (clean) or any error: the drain is over
				return
			}
		}
	}()
	select {
	case err := <-ended:
		return err
	case <-time.After(timeout):
		return nil // the server did not finish in time; do not hang on it
	}
}

// Close signals the control plane that no more log lines will be sent, then
// waits (briefly) for the server to consume them. The drain is essential:
// stream.Send only QUEUES a line into the gRPC transport, and CloseSend merely
// half-closes; without waiting for the RPC to complete, the agent returns and
// the deferred conn.Close() tears the connection down before the queued lines
// are flushed, so a short task's logs never arrive and the attempt's log file
// stays empty. Draining (Recv to EOF) blocks until the server has consumed every
// line, guaranteeing delivery.
//
// A re-open still running gets up to logReopenGrace to put the waiting lines
// on a new stream before it is stopped. Each wait is bounded: log delivery is
// best-effort and must never hang the task. If the control plane stalls, Close
// returns and the caller closes the connection, losing at most the tail of the
// logs, never the task.
func (s *grpcLogSink) Close() error {
	s.mu.Lock()
	done, stop := s.reopenDone, s.cancelReopen
	s.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(logReopenGrace):
			stop()
			<-done
		}
	}

	s.mu.Lock()
	stream, cancel := s.stream, s.cancelStream
	if stream == nil {
		lost := len(s.pending) + s.dropped
		s.pending, s.pendingBytes, s.dropped = nil, 0, 0
		s.mu.Unlock()
		if lost > 0 {
			slog.Warn("task log stream was not re-opened before the task ended; its last lines were not shipped", "lines_lost", lost)
		}
		return nil
	}
	err := stream.CloseSend()
	s.mu.Unlock()
	if cancel != nil {
		defer cancel()
	}
	if err != nil {
		return err
	}
	_ = drainStream(stream, logDrainTimeout) //nolint:errcheck // the stream's end is the signal, not a failure
	return nil
}
