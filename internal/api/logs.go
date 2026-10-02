package api

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// LogReader streams a task attempt's stored logs and, for running tasks, tails
// new lines live.
type LogReader interface {
	ReadLogs(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (io.ReadCloser, error)
	Tail(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (<-chan string, func(), error)
}

// serveLogs streams the stored logs for a task attempt and, when follow=true,
// tails live lines. The caller has already parsed the try number (the route is a
// catch-all shared with the single task-instance endpoint).
//
// tasks is consulted only on the no-logs path, to explain an empty log view; it
// may be nil, which keeps that view exactly as it was.
func serveLogs(c *gin.Context, reader LogReader, tasks TaskInstanceRepository, try int) {
	if reader == nil {
		AbortProblem(c, http.StatusNotFound, "not found", "logs are not available")
		return
	}
	rc, err := reader.ReadLogs(c.Request.Context(), tenantOf(c),
		c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"), try)
	if errors.Is(err, ErrNotFound) {
		// No stored logs for this attempt (e.g. it never ran, or the logs aged
		// out / predate a backend change). Serve a graceful "no logs" rather than
		// a 404 the UI renders as a broken page.
		serveNoLogs(c, tasks, try)
		return
	}
	if err != nil {
		handleRepoError(c, err)
		return
	}
	defer func() {
		if cerr := rc.Close(); cerr != nil {
			slog.Warn("closing log stream", "error", cerr)
		}
	}()
	stored := &lastLineTracker{r: rc}
	switch negotiateLogFormat(c) {
	case logFormatNDJSON:
		serveNdjsonLogs(c, stored, try)
		if c.Query("follow") == "true" {
			tailNdjson(c, reader, try, stored.served())
		}
		return
	case logFormatJSON:
		serveStructuredLogs(c, rc, try)
		return
	case logFormatPlain:
		// fall through to the plain-text stream below.
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Status(http.StatusOK)
	// Logs are stored as JSONL; the plain-text view emits just each message
	// (DecodeLine tolerates legacy plain lines too).
	scanner := bufio.NewScanner(stored)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if _, werr := c.Writer.WriteString(logs.DecodeLine(scanner.Text()).Message + "\n"); werr != nil {
			slog.Warn("streaming logs to client", "error", werr)
			break
		}
	}
	if c.Query("follow") == "true" {
		tailLogs(c, reader, try, stored.served())
	}
}

// lastLineTracker passes a stored log through while remembering its last line,
// so a follower knows how far the stored read got.
type lastLineTracker struct {
	r    io.Reader
	last []byte // the last complete line seen
	tail []byte // bytes after the last newline seen
}

func (t *lastLineTracker) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	chunk := p[:n]
	i := bytes.LastIndexByte(chunk, '\n')
	if i < 0 {
		t.tail = append(t.tail, chunk...)
		return n, err
	}
	if j := bytes.LastIndexByte(chunk[:i], '\n'); j >= 0 {
		t.last = append(t.last[:0], chunk[j+1:i]...)
	} else {
		t.last = append(append(t.last[:0], t.tail...), chunk[:i]...)
	}
	t.tail = append(t.tail[:0], chunk[i+1:]...)
	return n, err
}

// storedTail is the last line a stored read served: its raw encoding and its
// timestamp. The zero value means nothing was served.
type storedTail struct {
	line string
	at   time.Time // zero when the line carries no timestamp (a legacy plain line)
}

// served reports the last stored line read.
func (t *lastLineTracker) served() storedTail {
	line := t.last
	if len(t.tail) > 0 {
		line = t.tail
	}
	if len(line) == 0 {
		return storedTail{}
	}
	return storedTail{line: string(line), at: logs.DecodeLine(string(line)).Time}
}

// replaySkipper drops the leading live lines a stored read already served. With
// logs.tail.publish set to on_demand the publisher replays the lines it held
// until it noticed this follower (see agentrpc.tailGate), and the oldest of
// those can already be in the store. Only lines flagged as a replay
// (logs.MarkReplay) are ever skipped, so the default publish mode, which never
// replays, shows every live line as before. Within the leading replayed run, the
// exact last stored line ends the skip: the replay is in the order the server
// received the lines, which is the order it stored them, so nothing after that
// line was served. Before it, a line stamped no later than the stored tail is
// taken as served. The first line that is not skipped ends the skip for good.
type replaySkipper struct {
	served storedTail
	done   bool
}

// next decodes a live line and reports whether to skip it.
func (s *replaySkipper) next(line string) (logs.Event, bool) {
	raw, replay := logs.SplitReplay(line)
	ev := logs.DecodeLine(raw)
	if s.done || !replay || s.served.line == "" {
		s.done = true
		return ev, false
	}
	if raw == s.served.line {
		s.done = true
		return ev, true
	}
	if !s.served.at.IsZero() && !ev.Time.IsZero() && !ev.Time.After(s.served.at) {
		return ev, true
	}
	s.done = true
	return ev, false
}

// serveNoLogs renders an empty-but-valid log response (200) when no logs exist
// for an attempt, so the UI shows "no logs" instead of erroring.
//
// An empty log view is the operator's dead end for the worst failure class: an
// agent that died before it registered streams nothing, so "No logs available"
// is literally true and completely unhelpful. Where the control plane observed a
// cause, it is appended here as an error-level event — the log view is the one
// place in the stock Airflow UI an operator is already looking when an attempt
// failed with nothing to show.
func serveNoLogs(c *gin.Context, tasks TaskInstanceRepository, try int) {
	const msg = "No logs available for this attempt."
	events := []structuredLogEvent{{Event: msg, Level: "info"}}
	if reason := attemptFailureReason(c, tasks, try); reason != "" {
		events = append(events, structuredLogEvent{Event: reason, Level: "error"})
	}
	switch negotiateLogFormat(c) {
	case logFormatNDJSON:
		c.Header("Content-Type", "application/x-ndjson")
		c.Status(http.StatusOK)
		writeNdjson(c, events)
		return
	case logFormatJSON:
		c.JSON(http.StatusOK, structuredLogResponse{Content: events, ContinuationToken: nil})
		return
	case logFormatPlain:
		// fall through to the plain-text message below.
	}
	c.Header("Content-Type", "text/plain; charset=utf-8")
	var body strings.Builder
	for _, e := range events {
		body.WriteString(e.Event)
		body.WriteByte('\n')
	}
	c.String(http.StatusOK, body.String())
}

// attemptFailureReason returns the recorded cause for exactly this attempt, or ""
// when there is none, the repository is absent, or the lookup fails. It is purely
// explanatory, so every uncertain path degrades to the plain no-logs view.
//
// It matches on try_number rather than taking the task's current reason: a task
// that failed on try 2 must not make try 1's empty log view claim a cause that
// belongs to a different attempt.
func attemptFailureReason(c *gin.Context, tasks TaskInstanceRepository, try int) string {
	if tasks == nil {
		return ""
	}
	attempts, err := tasks.ListTaskInstanceAttempts(c.Request.Context(), tenantOf(c),
		c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"))
	if err != nil {
		return ""
	}
	for _, a := range attempts {
		if a.TryNumber == try && a.FailureReason != "" {
			return taskoutcome.TruncateReason(a.FailureReason, maxFailureReasonLen)
		}
	}
	return ""
}

// tailLogs streams live log lines to the client until the task stops producing
// them or the client disconnects. It is best-effort: if tailing is unavailable
// the already-sent stored logs stand on their own. served is the last stored
// line already sent (see replaySkipper).
func tailLogs(c *gin.Context, reader LogReader, try int, served storedTail) {
	ctx := c.Request.Context()
	lines, cancel, err := reader.Tail(ctx, tenantOf(c),
		c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"), try)
	if err != nil {
		return
	}
	defer cancel()
	flusher, canFlush := c.Writer.(http.Flusher)
	skipper := &replaySkipper{served: served}
	for {
		select {
		case <-ctx.Done():
			return
		case line, open := <-lines:
			if !open {
				return
			}
			// The channel now carries the full event JSON; the plain stream shows
			// just the message (DecodeLine tolerates legacy raw lines too).
			ev, skip := skipper.next(line)
			if skip {
				continue
			}
			if _, werr := c.Writer.WriteString(ev.Message + "\n"); werr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
	}
}
