package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/logs"
)

// fakeTailReader serves no stored logs and tails a fixed set of live lines.
type fakeTailReader struct{ ch chan string }

func (f *fakeTailReader) ReadLogs(context.Context, string, string, string, string, int) (io.ReadCloser, error) {
	return nil, ErrNotFound
}

func (f *fakeTailReader) Tail(context.Context, string, string, string, string, int) (lines <-chan string, cancel func(), err error) {
	return f.ch, func() {}, nil
}

// TestTailNdjsonEmitsStructuredEvents asserts live-tailed lines are emitted as
// NDJSON structured events carrying the level and stream-derived logger — so the
// live view colors lines like the stored drill-down.
func TestTailNdjsonEmitsStructuredEvents(t *testing.T) {
	ch := make(chan string, 2)
	ch <- logs.EncodeLine(logs.Event{Level: "error", Stream: "stdout", Message: "boom", Time: time.Now().UTC()})
	close(ch) // the loop returns once the channel drains

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/logs?follow=true", http.NoBody)

	tailNdjson(c, &fakeTailReader{ch: ch}, 1, time.Time{})

	body := rec.Body.String()
	if !strings.Contains(body, `"event":"boom"`) {
		t.Errorf("tailed line should carry the message, got %q", body)
	}
	if !strings.Contains(body, `"level":"error"`) {
		t.Errorf("tailed line should carry the level, got %q", body)
	}
	if !strings.Contains(body, `"logger":"task.stdout"`) {
		t.Errorf("tailed line should carry the stream-derived logger, got %q", body)
	}
}

// TestStructuredEventTimestampParity asserts strict parity with real Airflow:
// the timestamp field is always present — JSON null on group markers, an RFC3339
// string on log lines.
func TestStructuredEventTimestampParity(t *testing.T) {
	marker, err := json.Marshal(structuredLogEvent{Event: "::endgroup::"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(marker), `"timestamp":null`) {
		t.Errorf("group marker must emit timestamp:null, got %s", marker)
	}

	line := toStructuredEvent(logs.Event{Message: "hi", Level: "info", Stream: "stdout", Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)})
	encoded, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"timestamp":"2026-01-02T03:04:05`) {
		t.Errorf("log line must emit an RFC3339 timestamp, got %s", encoded)
	}
}
