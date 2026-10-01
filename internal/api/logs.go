package api

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

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
	switch negotiateLogFormat(c) {
	case logFormatNDJSON:
		serveNdjsonLogs(c, rc, try)
		if c.Query("follow") == "true" {
			tailNdjson(c, reader, try)
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
	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if _, werr := c.Writer.WriteString(logs.DecodeLine(scanner.Text()).Message + "\n"); werr != nil {
			slog.Warn("streaming logs to client", "error", werr)
			break
		}
	}
	if c.Query("follow") == "true" {
		tailLogs(c, reader, try)
	}
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
// the already-sent stored logs stand on their own.
func tailLogs(c *gin.Context, reader LogReader, try int) {
	ctx := c.Request.Context()
	lines, cancel, err := reader.Tail(ctx, tenantOf(c),
		c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"), try)
	if err != nil {
		return
	}
	defer cancel()
	flusher, canFlush := c.Writer.(http.Flusher)
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
			if _, werr := c.Writer.WriteString(logs.DecodeLine(line).Message + "\n"); werr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
	}
}
