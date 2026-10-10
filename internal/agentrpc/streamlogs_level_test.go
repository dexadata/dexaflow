package agentrpc

import (
	"io"
	"testing"

	"github.com/dexadata/dexaflow/internal/logs"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

type capLogWriter struct{ events []logs.Event }

func (c *capLogWriter) WriteEvent(ev logs.Event) error { c.events = append(c.events, ev); return nil }
func (c *capLogWriter) Close() error                   { return nil }

// TestWriteLinesRefinesLevelButKeepsStream asserts the log pipeline corrects the
// level from the line's content (an error on stdout, an info on stderr) while
// leaving the stream — part of the stored contract — exactly as the agent sent
// it. A line with no severity token keeps its stream-derived level.
func TestWriteLinesRefinesLevelButKeepsStream(t *testing.T) {
	lines := []*agentv1.LogLine{
		{Message: "ERROR: connection refused", Stream: "stdout", Level: agentv1.LogLevel_LOG_LEVEL_INFO},
		{Message: "INFO: run started", Stream: "stderr", Level: agentv1.LogLevel_LOG_LEVEL_ERROR},
		{Message: "processed 42 rows", Stream: "stdout", Level: agentv1.LogLevel_LOG_LEVEL_INFO},
	}
	i := 0
	recv := func() (*agentv1.LogLine, error) {
		if i >= len(lines) {
			return nil, io.EOF
		}
		l := lines[i]
		i++
		return l, nil
	}

	w := &capLogWriter{}
	if err := writeLines(nil, w, recv, func(string) {}, nil); err != nil {
		t.Fatalf("writeLines: %v", err)
	}
	if len(w.events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(w.events))
	}

	wantLevel := []string{"error", "info", "info"}
	wantStream := []string{"stdout", "stderr", "stdout"}
	for idx, ev := range w.events {
		if ev.Level != wantLevel[idx] {
			t.Errorf("line %d level = %q, want %q", idx, ev.Level, wantLevel[idx])
		}
		if ev.Stream != wantStream[idx] {
			t.Errorf("line %d stream = %q, want %q (contract: stream is preserved)", idx, ev.Stream, wantStream[idx])
		}
	}
}
