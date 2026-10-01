package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/logs"
)

// structuredLogEvent is one item in Airflow 3.2.1's structured log content: a
// fold marker (::group::/::endgroup:: with sources) or a log line. Real log
// lines carry a timestamp and logger; the UI's log viewer needs the timestamp
// to render the row (without it the panel stays empty even on a 200 response).
type structuredLogEvent struct {
	Event string `json:"event"`
	// Timestamp is always present to match real Airflow (which emits it as
	// `null` on the ::group::/::endgroup:: markers and as an RFC3339 string on
	// log lines); a pointer serializes nil as JSON null rather than being omitted.
	Timestamp *string  `json:"timestamp"`
	Level     string   `json:"level,omitempty"`
	Logger    string   `json:"logger,omitempty"`
	Sources   []string `json:"sources,omitempty"`
}

// structuredLogResponse is the Accept: application/json log payload the UI's
// drill-down viewer renders (collapsible groups + per-level coloring).
type structuredLogResponse struct {
	Content           []structuredLogEvent `json:"content"`
	ContinuationToken *string              `json:"continuation_token"`
}

// logFormat is the negotiated structured-log encoding for a request.
type logFormat int

const (
	logFormatPlain  logFormat = iota // text/plain stream (default)
	logFormatJSON                    // single {content:[...]} object
	logFormatNDJSON                  // one JSON event per line (the SPA viewer)
)

// negotiateLogFormat picks the log encoding from the Accept header. The Airflow
// 3.2 SPA log viewer requests application/x-ndjson and parses one JSON object per
// line; a few callers request application/json for the single-object form.
// Anything else gets the plain-text stream.
func negotiateLogFormat(c *gin.Context) logFormat {
	accept := c.GetHeader("Accept")
	switch {
	case strings.Contains(accept, "application/x-ndjson"), strings.Contains(accept, "application/ndjson"):
		return logFormatNDJSON
	case strings.Contains(accept, "application/json"):
		return logFormatJSON
	default:
		return logFormatPlain
	}
}

// structuredLogContent reads the stored log lines into Airflow's structured
// content: a leading collapsible source group, then one event per line carrying
// the timestamp, level, and logger the viewer renders.
func structuredLogContent(c *gin.Context, rc io.Reader, try int) []structuredLogEvent {
	source := fmt.Sprintf("dag_id=%s/run_id=%s/task_id=%s/attempt=%d.log",
		c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"), try)
	content := []structuredLogEvent{
		{Event: "::group::Log message source details", Sources: []string{source}},
		{Event: "::endgroup::"},
	}
	scanner := bufio.NewScanner(rc)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		// Logs are stored as JSONL with the real level the producer tagged;
		// DecodeLine infers a level for legacy plain lines.
		content = append(content, toStructuredEvent(logs.DecodeLine(scanner.Text())))
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("reading logs for structured response", "error", err)
	}
	return content
}

// toStructuredEvent maps a stored/tailed log event onto one Airflow structured
// content item, carrying the timestamp and per-level/stream metadata the viewer
// renders. Shared by the stored read and the live NDJSON tail so both color
// identically.
func toStructuredEvent(ev logs.Event) structuredLogEvent {
	item := structuredLogEvent{Event: ev.Message, Level: ev.Level}
	if !ev.Time.IsZero() {
		ts := ev.Time.UTC().Format("2006-01-02T15:04:05.000000Z07:00")
		item.Timestamp = &ts
	}
	if ev.Stream != "" {
		// Airflow labels print output "task.stdout"/"task.stderr"; mirror that
		// so the viewer's logger column matches.
		item.Logger = "task." + ev.Stream
	}
	return item
}

// tailNdjson streams live log lines as NDJSON structured events, so a follower
// (Accept: application/x-ndjson with follow=true) colors live lines exactly like
// the stored drill-down. Best-effort: it ends when the task stops producing
// lines or the client disconnects.
func tailNdjson(c *gin.Context, reader LogReader, try int) {
	ctx := c.Request.Context()
	lines, cancel, err := reader.Tail(ctx, tenantOf(c),
		c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"), try)
	if err != nil {
		return
	}
	defer cancel()
	flusher, canFlush := c.Writer.(http.Flusher)
	enc := json.NewEncoder(c.Writer)
	for {
		select {
		case <-ctx.Done():
			return
		case line, open := <-lines:
			if !open {
				return
			}
			if encErr := enc.Encode(toStructuredEvent(logs.DecodeLine(line))); encErr != nil {
				return
			}
			if canFlush {
				flusher.Flush()
			}
		}
	}
}

// serveStructuredLogs emits the single-object {content:[...]} form (Accept:
// application/json). continuation_token is null (logs are served whole).
func serveStructuredLogs(c *gin.Context, rc io.Reader, try int) {
	c.JSON(http.StatusOK, structuredLogResponse{Content: structuredLogContent(c, rc, try), ContinuationToken: nil})
}

// serveNdjsonLogs streams one JSON event per line (Accept: application/x-ndjson),
// the format the Airflow 3.2 SPA log viewer parses for its colored drill-down.
func serveNdjsonLogs(c *gin.Context, rc io.Reader, try int) {
	c.Header("Content-Type", "application/x-ndjson")
	c.Status(http.StatusOK)
	writeNdjson(c, structuredLogContent(c, rc, try))
}

// writeNdjson encodes each event as its own JSON line.
func writeNdjson(c *gin.Context, events []structuredLogEvent) {
	enc := json.NewEncoder(c.Writer)
	for i := range events {
		if err := enc.Encode(events[i]); err != nil {
			slog.Warn("encoding ndjson log line", "error", err)
			return
		}
	}
}
