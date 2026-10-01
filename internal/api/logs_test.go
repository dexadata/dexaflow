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

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeLogReader struct {
	body    string
	err     error
	tailed  []string
	tailErr error
}

func (f *fakeLogReader) ReadLogs(context.Context, string, string, string, string, int) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func (f *fakeLogReader) Tail(context.Context, string, string, string, string, int) (lines <-chan string, cancel func(), err error) {
	if f.tailErr != nil {
		return nil, nil, f.tailErr
	}
	ch := make(chan string, len(f.tailed))
	for _, l := range f.tailed {
		ch <- l
	}
	close(ch)
	return ch, func() {}, nil
}

func logsServer(reader LogReader) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Tasks:         &fakeTaskRepo{},
		Logs:          reader,
	})
}

func TestLogsReadStreamsFile(t *testing.T) {
	rec := authGet(logsServer(&fakeLogReader{body: "line one\nline two\n"}), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("logs read = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "line one\nline two\n" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestLogsStructuredJSON(t *testing.T) {
	body := "DAG bundles loaded\nERROR something broke\nWARN heads up\n"
	srv := logsServer(&fakeLogReader{body: body})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", http.NoBody)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("structured logs = %d (%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q, want json", ct)
	}
	var got struct {
		Content []struct {
			Event   string   `json:"event"`
			Level   string   `json:"level"`
			Sources []string `json:"sources"`
		} `json:"content"`
		ContinuationToken *string `json:"continuation_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Leading collapsible source group (the drill-down fold) + endgroup.
	if len(got.Content) < 5 {
		t.Fatalf("want group + endgroup + 3 lines, got %d items: %+v", len(got.Content), got.Content)
	}
	if !strings.HasPrefix(got.Content[0].Event, "::group::") || len(got.Content[0].Sources) == 0 {
		t.Errorf("first item must be a source group, got %+v", got.Content[0])
	}
	if got.Content[1].Event != "::endgroup::" {
		t.Errorf("second item must close the group, got %q", got.Content[1].Event)
	}
	// Per-line level drives the UI coloring.
	levels := map[string]string{}
	for _, e := range got.Content[2:] {
		levels[e.Event] = e.Level
	}
	if levels["ERROR something broke"] != "error" {
		t.Errorf("error line level = %q", levels["ERROR something broke"])
	}
	if levels["WARN heads up"] != "warning" {
		t.Errorf("warn line level = %q", levels["WARN heads up"])
	}
	if levels["DAG bundles loaded"] != "info" {
		t.Errorf("plain line level = %q, want info", levels["DAG bundles loaded"])
	}
	if got.ContinuationToken != nil {
		t.Errorf("continuation_token should be null, got %v", *got.ContinuationToken)
	}
}

func TestLogsStructuredCarriesTimestamp(t *testing.T) {
	// Stored logs are JSONL with a timestamp; the structured view must surface it
	// as `timestamp`, otherwise Airflow's log viewer renders an empty panel even
	// though the response is 200 (the per-line timestamp is required to render).
	body := `{"ts":"2026-05-23T18:29:18.005688Z","level":"info","stream":"stdout","msg":"extract: starting"}` + "\n"
	srv := logsServer(&fakeLogReader{body: body})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", http.NoBody)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("structured logs = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Content []struct {
			Event     string `json:"event"`
			Level     string `json:"level"`
			Timestamp string `json:"timestamp"`
			Logger    string `json:"logger"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The actual log line (after group/endgroup) must carry its timestamp.
	var line *struct {
		Event     string `json:"event"`
		Level     string `json:"level"`
		Timestamp string `json:"timestamp"`
		Logger    string `json:"logger"`
	}
	for i := range got.Content {
		if got.Content[i].Event == "extract: starting" {
			line = &got.Content[i]
		}
	}
	if line == nil {
		t.Fatalf("log line not found in content: %+v", got.Content)
		return // unreachable; makes the non-nil invariant explicit to staticcheck (SA5011)
	}
	if line.Timestamp == "" {
		t.Errorf("log line missing timestamp (Airflow viewer needs it to render): %+v", *line)
	}
	if line.Logger == "" {
		t.Errorf("log line missing logger: %+v", *line)
	}
}

func TestLogsNdjsonContract(t *testing.T) {
	// The Airflow 3.2 SPA log viewer requests Accept: application/x-ndjson and
	// parses one JSON object per line (NOT a single {content:[...]} object). If we
	// serve plain text or the wrong content-type, the viewer renders an empty
	// panel even on a 200. This pins the contract.
	body := `{"ts":"2026-05-23T18:29:18.005688Z","level":"info","stream":"stdout","msg":"extract: starting"}` + "\n"
	srv := logsServer(&fakeLogReader{body: body})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", http.NoBody)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Accept", "application/x-ndjson")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ndjson logs = %d (%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/x-ndjson") {
		t.Errorf("content-type = %q, want application/x-ndjson", ct)
	}
	// Each non-empty line must be a standalone JSON object; the source group is
	// first, then the log line carrying event+timestamp+logger.
	lines := []string{}
	for _, ln := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
		if ln != "" {
			lines = append(lines, ln)
		}
	}
	if len(lines) < 3 {
		t.Fatalf("want group + endgroup + >=1 line, got %d: %q", len(lines), rec.Body.String())
	}
	var foundLine bool
	for _, ln := range lines {
		var item map[string]any
		if err := json.Unmarshal([]byte(ln), &item); err != nil {
			t.Fatalf("line is not valid JSON: %q (%v)", ln, err)
		}
		if item["event"] == "extract: starting" {
			foundLine = true
			if item["timestamp"] == nil || item["timestamp"] == "" {
				t.Errorf("log line missing timestamp: %v", item)
			}
			if item["logger"] == nil || item["logger"] == "" {
				t.Errorf("log line missing logger: %v", item)
			}
		}
	}
	if !foundLine {
		t.Errorf("log line 'extract: starting' not emitted as an ndjson object: %q", rec.Body.String())
	}
}

func TestLogsReadMissingIsGraceful(t *testing.T) {
	// Missing logs (e.g. an attempt that never ran, or aged-out logs) render as
	// a graceful "no logs" 200, not a 404 the UI shows as a broken page (#52).
	rec := authGet(logsServer(&fakeLogReader{err: domain.ErrNotFound}), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("missing logs = %d, want graceful 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "No logs available") {
		t.Errorf("expected a 'no logs' message, got %q", rec.Body.String())
	}
}

func TestLogsReadMissingStructuredIsGraceful(t *testing.T) {
	srv := logsServer(&fakeLogReader{err: domain.ErrNotFound})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", http.NoBody)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("missing structured logs = %d, want 200", rec.Code)
	}
	var got struct {
		Content []struct {
			Event string `json:"event"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Content) == 0 || !strings.Contains(got.Content[0].Event, "No logs available") {
		t.Errorf("structured missing logs should carry a 'no logs' event, got %+v", got.Content)
	}
}

func TestLogsReadFollowStreamsTailedLines(t *testing.T) {
	reader := &fakeLogReader{body: "stored\n", tailed: []string{"live one", "live two"}}
	rec := authGet(logsServer(reader), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1?follow=true", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("follow read = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "stored\nlive one\nlive two\n" {
		t.Errorf("body = %q, want stored + live lines", rec.Body.String())
	}
}

func TestLogsReadBadTryNumber(t *testing.T) {
	rec := authGet(logsServer(&fakeLogReader{}), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/abc", "")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("non-integer try_number = %d, want 400", rec.Code)
	}
}

// logsServerWithTasks builds a logs-serving engine over a task repository, so the
// no-logs path can consult the attempt's recorded failure reason.
func logsServerWithTasks(reader LogReader, tasks TaskInstanceRepository) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Tasks:         tasks,
		Logs:          reader,
	})
}

// TestNoLogsSurfacesTheFailureReason is the operator-facing half of the fix: an
// attempt whose agent died before it streamed anything used to render a bare
// "No logs available for this attempt." — a dead end that sent operators to
// `kubectl logs`. When the control plane recorded WHY, the log view says so.
func TestNoLogsSurfacesTheFailureReason(t *testing.T) {
	const reason = "the control plane rejected this pod's projected ServiceAccount token."
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{{
		TaskID: "extract", TryNumber: 1, State: domain.TaskStateFailed, FailureReason: reason,
	}}}
	rec := authGet(logsServerWithTasks(&fakeLogReader{err: domain.ErrNotFound}, tasks), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("no-logs = %d, want graceful 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), reason) {
		t.Errorf("no-logs body must carry the recorded cause, got %q", rec.Body.String())
	}
}

// TestNoLogsStructuredSurfacesTheFailureReason covers the format the SPA actually
// requests: the reason must arrive as an error-level event, not only plain text.
func TestNoLogsStructuredSurfacesTheFailureReason(t *testing.T) {
	const reason = "the task container was OOMKilled (exit 137); raise the task's memory limit."
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{{
		TaskID: "extract", TryNumber: 2, State: domain.TaskStateFailed, FailureReason: reason,
	}}}
	srv := logsServerWithTasks(&fakeLogReader{err: domain.ErrNotFound}, tasks)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/2", http.NoBody)
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var got struct {
		Content []struct {
			Event string `json:"event"`
			Level string `json:"level"`
		} `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range got.Content {
		if strings.Contains(e.Event, reason) {
			found = true
			if e.Level != "error" {
				t.Errorf("the failure reason event level = %q, want %q", e.Level, "error")
			}
		}
	}
	if !found {
		t.Errorf("structured no-logs response must carry the cause, got %+v", got.Content)
	}
}

// TestNoLogsForOtherAttemptDoesNotBorrowAReason keeps the reason pinned to its
// own attempt: try 1's empty log view must not display try 2's failure.
func TestNoLogsForOtherAttemptDoesNotBorrowAReason(t *testing.T) {
	const reason = "the task container was OOMKilled (exit 137)."
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{{
		TaskID: "extract", TryNumber: 2, State: domain.TaskStateFailed, FailureReason: reason,
	}}}
	rec := authGet(logsServerWithTasks(&fakeLogReader{err: domain.ErrNotFound}, tasks), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", "")
	if strings.Contains(rec.Body.String(), reason) {
		t.Errorf("attempt 1 borrowed attempt 2's failure reason: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "No logs available") {
		t.Errorf("expected the plain no-logs message, got %q", rec.Body.String())
	}
}

// TestNoLogsWithoutAReasonKeepsTheOriginalMessage: when nothing was observed, the
// view stays exactly as it was rather than inventing a cause.
func TestNoLogsWithoutAReasonKeepsTheOriginalMessage(t *testing.T) {
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{{
		TaskID: "extract", TryNumber: 1, State: domain.TaskStateSuccess,
	}}}
	rec := authGet(logsServerWithTasks(&fakeLogReader{err: domain.ErrNotFound}, tasks), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/logs/1", "")
	if !strings.Contains(rec.Body.String(), "No logs available") {
		t.Errorf("expected the plain no-logs message, got %q", rec.Body.String())
	}
}
