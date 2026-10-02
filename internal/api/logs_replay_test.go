package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/logs"
)

// replayFixture is a stored log of two lines and a live tail that starts with a
// replay of the second one (the publisher replays the lines it held while it
// had not yet noticed the subscriber, and some may already be stored), then a
// new line, then a line stamped earlier than the stored tail but received later.
func replayFixture() *fakeLogReader {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ev := func(sec int, msg string) string {
		return logs.EncodeLine(logs.Event{Time: t0.Add(time.Duration(sec) * time.Second), Level: "info", Stream: "stdout", Message: msg})
	}
	return &fakeLogReader{
		body:   ev(1, "one") + "\n" + ev(2, "two") + "\n",
		tailed: []string{ev(2, "two"), ev(3, "three"), ev(1, "late")},
	}
}

// TestFollowSkipsReplayedStoredLines: the live tail drops its leading lines that
// the stored read already served, and nothing after the first new line.
func TestFollowSkipsReplayedStoredLines(t *testing.T) {
	rec := authGet(logsServer(replayFixture()), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1?follow=true", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("follow = %d (%s)", rec.Code, rec.Body.String())
	}
	if got, want := rec.Body.String(), "one\ntwo\nthree\nlate\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestFollowNdjsonSkipsReplayedStoredLines is the same guarantee for the SPA's
// NDJSON follower.
func TestFollowNdjsonSkipsReplayedStoredLines(t *testing.T) {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1?follow=true", http.NoBody)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Accept", "application/x-ndjson")
	rec := httptest.NewRecorder()
	logsServer(replayFixture()).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("follow = %d (%s)", rec.Code, rec.Body.String())
	}
	var events []string
	for _, l := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
		var e structuredLogEvent
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("decoding %q: %v", l, err)
		}
		if !strings.HasPrefix(e.Event, "::") {
			events = append(events, e.Event)
		}
	}
	if got, want := strings.Join(events, ","), "one,two,three,late"; got != want {
		t.Errorf("events = %s, want %s", got, want)
	}
}
