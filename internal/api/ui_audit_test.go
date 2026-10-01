package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeAuditReader struct {
	entries []domain.AuditLogEntry
	total   int
	gotDag  string
}

func (f *fakeAuditReader) ListAuditLogs(_ context.Context, _, dagID string, _, _ int) ([]domain.AuditLogEntry, int, error) {
	f.gotDag = dagID
	return f.entries, f.total, nil
}

func auditServer(reader AuditLogReader) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		AuditLog:      reader,
	})
}

func TestEventLogsHandler(t *testing.T) {
	reader := &fakeAuditReader{
		total: 1,
		entries: []domain.AuditLogEntry{{
			ID: 7, When: time.Now().UTC(), Action: "dag.version.register",
			ResourceType: "dag", ResourceID: "etl", Owner: "admin@x", Extra: "{}",
		}},
	}
	rec := authGet(auditServer(reader), http.MethodGet, "/api/v2/eventLogs?dag_id=etl", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if reader.gotDag != "etl" {
		t.Errorf("dag_id filter not forwarded: got %q", reader.gotDag)
	}
	var got struct {
		EventLogs []struct {
			EventLogID int64   `json:"event_log_id"`
			Event      string  `json:"event"`
			DagID      *string `json:"dag_id"`
			Owner      *string `json:"owner"`
		} `json:"event_logs"`
		TotalEntries int `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TotalEntries != 1 || len(got.EventLogs) != 1 {
		t.Fatalf("want 1 entry, got total=%d len=%d", got.TotalEntries, len(got.EventLogs))
	}
	e := got.EventLogs[0]
	if e.EventLogID != 7 || e.Event != "dag.version.register" {
		t.Errorf("entry mismatch: %+v", e)
	}
	if e.DagID == nil || *e.DagID != "etl" {
		t.Errorf("dag_id = %v, want etl", e.DagID)
	}
	if e.Owner == nil || *e.Owner != "admin@x" {
		t.Errorf("owner = %v", e.Owner)
	}
}

func TestEventLogsNonDagResourceHasNilDagID(t *testing.T) {
	reader := &fakeAuditReader{
		total:   1,
		entries: []domain.AuditLogEntry{{ID: 1, Action: "auth.login", ResourceType: "user", ResourceID: "u1"}},
	}
	rec := authGet(auditServer(reader), http.MethodGet, "/api/v2/eventLogs", "")
	var got struct {
		EventLogs []map[string]any `json:"event_logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.EventLogs[0]["dag_id"] != nil {
		t.Errorf("non-dag resource should have null dag_id, got %v", got.EventLogs[0]["dag_id"])
	}
}

func TestEventLogsPromotesRunIDFromMetadata(t *testing.T) {
	// A trigger event carries run_id in its metadata; the DTO must surface it as
	// run_id and not leak it (or an empty {}) into the extra column.
	reader := &fakeAuditReader{
		total: 2,
		entries: []domain.AuditLogEntry{
			{ID: 1, Action: "taskinstance.mark.success", ResourceType: "dag", ResourceID: "etl", Extra: `{"run_id":"r1","task_id":"extract","try_number":2}`},
			{ID: 2, Action: "dag.version.register", ResourceType: "dag", ResourceID: "etl", Extra: "{}"},
		},
	}
	rec := authGet(auditServer(reader), http.MethodGet, "/api/v2/eventLogs", "")
	var got struct {
		EventLogs []map[string]any `json:"event_logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.EventLogs[0]["run_id"] != "r1" {
		t.Errorf("run_id = %v, want r1", got.EventLogs[0]["run_id"])
	}
	if got.EventLogs[0]["extra"] != nil {
		t.Errorf("extra should be null once run_id is promoted, got %v", got.EventLogs[0]["extra"])
	}
	// Task-level fields are promoted out of metadata too.
	if got.EventLogs[0]["task_id"] != "extract" {
		t.Errorf("task_id = %v, want extract", got.EventLogs[0]["task_id"])
	}
	if got.EventLogs[0]["try_number"] != float64(2) {
		t.Errorf("try_number = %v, want 2", got.EventLogs[0]["try_number"])
	}
	if got.EventLogs[1]["extra"] != nil {
		t.Errorf("empty {} metadata should render as null extra, got %v", got.EventLogs[1]["extra"])
	}
}

func TestEventLogsEmptyStubWithoutReader(t *testing.T) {
	rec := authGet(auditServer(nil), http.MethodGet, "/api/v2/eventLogs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got struct {
		EventLogs    []any `json:"event_logs"`
		TotalEntries int   `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.TotalEntries != 0 || len(got.EventLogs) != 0 {
		t.Errorf("nil reader should yield empty collection, got %+v", got)
	}
}
