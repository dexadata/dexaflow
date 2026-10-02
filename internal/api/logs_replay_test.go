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
		tailed: []string{logs.MarkReplay(ev(2, "two")), ev(3, "three"), ev(1, "late")},
	}
}

// TestFollowKeepsUnreplayedEarlyLines: a live line that is not a replay was
// never served from the store, so it is shown even when it is stamped before
// the stored tail (stdout and stderr are stamped on separate goroutines, and
// the reaper's marker carries the server's clock). This keeps the default
// publish mode, which never replays, exactly as before.
func TestFollowKeepsUnreplayedEarlyLines(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ev := func(sec int, msg string) string {
		return logs.EncodeLine(logs.Event{Time: t0.Add(time.Duration(sec) * time.Second), Level: "info", Stream: "stdout", Message: msg})
	}
	reader := &fakeLogReader{
		body:   ev(1, "one") + "\n" + ev(5, "marker") + "\n",
		tailed: []string{ev(2, "early"), ev(6, "later")},
	}
	rec := authGet(logsServer(reader), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1?follow=true", "")
	if got, want := rec.Body.String(), "one\nmarker\nearly\nlater\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

// TestFollowStopsSkippingAtTheStoredTail: the replay is in the order the
// server received the lines, which is the order it stored them, so once the
// replay reaches the exact last stored line nothing after it was served, even
// a line stamped earlier.
func TestFollowStopsSkippingAtTheStoredTail(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ev := func(sec int, msg string) string {
		return logs.EncodeLine(logs.Event{Time: t0.Add(time.Duration(sec) * time.Second), Level: "info", Stream: "stdout", Message: msg})
	}
	reader := &fakeLogReader{
		body: ev(1, "one") + "\n" + ev(2, "two") + "\n",
		tailed: []string{
			logs.MarkReplay(ev(1, "one")), logs.MarkReplay(ev(2, "two")),
			logs.MarkReplay(ev(1, "stderr")), ev(3, "three"),
		},
	}
	rec := authGet(logsServer(reader), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1?follow=true", "")
	if got, want := rec.Body.String(), "one\ntwo\nstderr\nthree\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
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
