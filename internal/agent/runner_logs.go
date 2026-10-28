package agent

import (
	"bytes"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// maxLogLineBytes bounds one log line and so the partial line a logWriter
// buffers. The control plane accepts gRPC messages up to 4 MiB, and a LogLine
// over that limit used to end the whole log stream; the bound leaves room for
// the message's other fields, so every line that was deliverable before is
// still sent whole. A longer line is sent in pieces of at most this size.
// var (not const) so tests can lower it.
var maxLogLineBytes = 4<<20 - 4<<10

// logWriter splits written bytes into newline-delimited log lines and forwards
// each one to the sink, tagging it with its stream name and level. A line longer
// than maxLogLineBytes is split, so the buffer of a stream that never writes a
// newline stays bounded.
type logWriter struct {
	sink   LogSink
	stream string
	level  agentv1.LogLevel
	buf    []byte
	line   int64
	splits int64 // extra lines produced by splitting over-long lines
}

// Write buffers p and emits every complete line it contains, then any piece of
// the pending partial line that reached the bound.
func (w *logWriter) Write(p []byte) (int, error) {
	n := len(p)
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			break
		}
		w.buf = append(w.buf, p[:i]...)
		w.emitLine(w.buf)
		w.buf = w.buf[:0]
		p = p[i+1:]
	}
	w.buf = append(w.buf, p...)
	for len(w.buf) > maxLogLineBytes {
		cut := splitPoint(w.buf)
		w.emitPiece(w.buf[:cut])
		w.buf = append(w.buf[:0], w.buf[cut:]...)
	}
	return n, nil
}

// emitLine emits one complete line, in pieces when it exceeds the bound.
func (w *logWriter) emitLine(b []byte) {
	for len(b) > maxLogLineBytes {
		cut := splitPoint(b)
		w.emitPiece(b[:cut])
		b = b[cut:]
	}
	w.emit(b)
}

// emitPiece emits the head of an over-long line and counts the split. The first
// split of a stream is logged; the total is logged on flush.
func (w *logWriter) emitPiece(b []byte) {
	if w.splits == 0 {
		slog.Warn("task log line exceeds the line bound; sending it in pieces",
			"stream", w.stream, "max_bytes", maxLogLineBytes)
	}
	w.splits++
	w.emit(b)
}

// splitPoint returns where to cut a line longer than the bound: at the bound,
// moved back to the start of a rune so both pieces stay valid UTF-8 (LogLine's
// message is a proto string). Bytes that are not UTF-8 are cut at the bound.
func splitPoint(b []byte) int {
	for cut := maxLogLineBytes; cut > maxLogLineBytes-utf8.UTFMax && cut > 0; cut-- {
		if utf8.RuneStart(b[cut]) {
			return cut
		}
	}
	return maxLogLineBytes
}

// flush emits any buffered line that lacked a trailing newline.
func (w *logWriter) flush() {
	if len(w.buf) > 0 {
		w.emit(w.buf)
		w.buf = nil
	}
	if w.splits > 0 {
		slog.Warn("task log lines were split at the line bound",
			"stream", w.stream, "extra_lines", w.splits, "max_bytes", maxLogLineBytes)
	}
}

// emitTaskStarted writes a synthetic start-of-task event to the log sink, so the
// UI's Logs panel always shows at least one line, even for a task that calls no
// print(). Best-effort; a sink error is logged but not propagated (the task
// itself is what matters).
func emitTaskStarted(sink LogSink) {
	if err := sink.Send(&agentv1.LogLine{
		Time:    timestamppb.Now(),
		Level:   agentv1.LogLevel_LOG_LEVEL_INFO,
		Message: "▸ task started",
		Stream:  "agent",
	}); err != nil {
		slog.Warn("emitting task-started log", "error", err)
	}
}

// emitTaskBoot writes a lifecycle line summarizing what's about to run: the
// interpreter argv and the names of injected AIRFLOW_CONN_*, AIRFLOW_VAR_*, and
// LEOFLOW_XCOM_* env vars. Without this, a task with no print() shows only the
// 2-line framing, leaving the user wondering whether their conns/vars were
// injected or their kwargs resolved. Best-effort: a sink error is logged.
func emitTaskBoot(sink LogSink, argv, env []string) {
	if len(argv) == 0 {
		return
	}
	var conns, vars, xcoms []string
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "AIRFLOW_CONN_"):
			if eq := strings.IndexByte(kv, '='); eq > 0 {
				conns = append(conns, kv[:eq])
			}
		case strings.HasPrefix(kv, "AIRFLOW_VAR_"):
			if eq := strings.IndexByte(kv, '='); eq > 0 {
				vars = append(vars, kv[:eq])
			}
		case strings.HasPrefix(kv, "LEOFLOW_XCOM_"):
			if eq := strings.IndexByte(kv, '='); eq > 0 {
				xcoms = append(xcoms, kv[:eq])
			}
		}
	}
	sort.Strings(conns)
	sort.Strings(vars)
	sort.Strings(xcoms)
	msg := fmt.Sprintf("running: %s", strings.Join(argv, " "))
	if err := sink.Send(&agentv1.LogLine{
		Time: timestamppb.Now(), Level: agentv1.LogLevel_LOG_LEVEL_INFO,
		Message: msg, Stream: "agent",
	}); err != nil {
		slog.Warn("emitting task-boot log", "error", err)
	}
	if len(conns)+len(vars)+len(xcoms) == 0 {
		return
	}
	parts := make([]string, 0, 3)
	if len(conns) > 0 {
		parts = append(parts, fmt.Sprintf("conns: %s", strings.Join(conns, ", ")))
	}
	if len(vars) > 0 {
		parts = append(parts, fmt.Sprintf("vars: %s", strings.Join(vars, ", ")))
	}
	if len(xcoms) > 0 {
		parts = append(parts, fmt.Sprintf("xcom inputs: %s", strings.Join(xcoms, ", ")))
	}
	if err := sink.Send(&agentv1.LogLine{
		Time: timestamppb.Now(), Level: agentv1.LogLevel_LOG_LEVEL_INFO,
		Message: "injected " + strings.Join(parts, " | "), Stream: "agent",
	}); err != nil {
		slog.Warn("emitting task-boot env log", "error", err)
	}
}

// emitTaskEnded writes a synthetic end-of-task event with the run duration and
// either a success marker or the exit code + cause. Pairs with emitTaskStarted to
// guarantee the Logs panel is never empty for a completed task (#119).
func emitTaskEnded(sink LogSink, exitCode int, cause error, duration time.Duration) {
	d := duration.Round(time.Millisecond)
	var msg string
	level := agentv1.LogLevel_LOG_LEVEL_INFO
	if cause == nil && exitCode == 0 {
		msg = fmt.Sprintf("✓ task succeeded in %s", d)
	} else {
		level = agentv1.LogLevel_LOG_LEVEL_ERROR
		if cause != nil {
			msg = fmt.Sprintf("✗ task failed (exit %d) in %s: %s", exitCode, d, cause.Error())
		} else {
			msg = fmt.Sprintf("✗ task failed (exit %d) in %s", exitCode, d)
		}
	}
	if err := sink.Send(&agentv1.LogLine{
		Time:    timestamppb.Now(),
		Level:   level,
		Message: msg,
		Stream:  "agent",
	}); err != nil {
		slog.Warn("emitting task-ended log", "error", err)
	}
}

func (w *logWriter) emit(b []byte) {
	w.line++
	if err := w.sink.Send(&agentv1.LogLine{
		Time:       timestamppb.Now(),
		Level:      w.level,
		Message:    string(b),
		Stream:     w.stream,
		LineNumber: w.line,
	}); err != nil {
		slog.Warn("streaming log line", "stream", w.stream, "error", err)
	}
}
