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
	"github.com/dexadata/dexaflow/internal/xcom"
)

type fakeXComReader struct {
	entry   xcom.Entry
	err     error
	entries []domain.XComEntryMeta
}

func (f *fakeXComReader) GetXCom(context.Context, string, string, string, string, string) (xcom.Entry, error) {
	return f.entry, f.err
}

func (f *fakeXComReader) ListXComEntries(context.Context, string, string, string, string) ([]domain.XComEntryMeta, error) {
	return f.entries, f.err
}

func xcomServer(reader XComReader) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Tasks:         &fakeTaskRepo{},
		Xcoms:         reader,
	})
}

func TestXComReadReturnsEntry(t *testing.T) {
	reader := &fakeXComReader{entry: xcom.Entry{Value: []byte(`{"rows":100}`), ContentType: "application/json", CreatedAt: time.Now()}}
	rec := authGet(xcomServer(reader), http.MethodGet, "/api/v2/xcoms/etl/run-1/extract/return_value", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("xcom read = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var dto xComEntryDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if string(dto.Value) != `{"rows":100}` || dto.Key != "return_value" || dto.TaskID != "extract" || dto.MapIndex != -1 {
		t.Errorf("unexpected dto: %+v", dto)
	}
}

func TestTaskInstanceLinksServesExtraLinksXCom(t *testing.T) {
	// The /links endpoint serves the operator deep-link buttons from the
	// "_extra_links" XCom the agent stored (#375), not a hardcoded {}.
	reader := &fakeXComReader{entry: xcom.Entry{
		Value:       []byte(`{"BigQuery Job Detail":"https://console.cloud.google.com/bigquery?x=1"}`),
		ContentType: "application/json", CreatedAt: time.Now(),
	}}
	rec := authGet(xcomServer(reader), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/query/links", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("links = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		ExtraLinks map[string]string `json:"extra_links"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ExtraLinks["BigQuery Job Detail"] != "https://console.cloud.google.com/bigquery?x=1" {
		t.Errorf("extra_links = %v, want the BigQuery deep-link", resp.ExtraLinks)
	}
}

func TestXComReadNotFound(t *testing.T) {
	reader := &fakeXComReader{err: domain.ErrNotFound}
	rec := authGet(xcomServer(reader), http.MethodGet, "/api/v2/xcoms/etl/run-1/extract/missing", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing xcom = %d, want 404", rec.Code)
	}
}

func TestXComEntriesList(t *testing.T) {
	reader := &fakeXComReader{entries: []domain.XComEntryMeta{
		{Key: "return_value", Timestamp: time.Now().UTC(), MapIndex: -1},
		{Key: "summary", Timestamp: time.Now().UTC(), MapIndex: -1},
	}}
	rec := authGet(xcomServer(reader), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/xcomEntries", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("xcomEntries = %d (%s)", rec.Code, rec.Body.String())
	}
	var col xcomEntryCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if col.TotalEntries != 2 || len(col.XComEntries) != 2 {
		t.Fatalf("want 2 entries, got %+v", col)
	}
	e := col.XComEntries[0]
	if e.Key != "return_value" || e.TaskID != "extract" || e.DagID != "etl" || e.RunID != "run-1" || e.MapIndex != -1 {
		t.Errorf("entry metadata wrong: %+v", e)
	}
	if e.DagDisplayName != "etl" || e.TaskDisplayName != "extract" {
		t.Errorf("display names wrong: %+v", e)
	}
}

func TestXComEntryValueByKey(t *testing.T) {
	reader := &fakeXComReader{entry: xcom.Entry{Value: []byte(`42`), CreatedAt: time.Now().UTC()}}
	rec := authGet(xcomServer(reader), http.MethodGet,
		"/api/v2/dags/etl/dagRuns/run-1/taskInstances/extract/xcomEntries/return_value", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("xcomEntries/{key} = %d (%s)", rec.Code, rec.Body.String())
	}
	var dto xComEntryDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if string(dto.Value) != `42` || dto.Key != "return_value" || dto.TaskID != "extract" {
		t.Errorf("unexpected value dto: %+v", dto)
	}
}
