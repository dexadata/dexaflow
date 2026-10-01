package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeStatsReader struct {
	dag  domain.DagStats
	hist domain.HistoricalMetrics
}

func (f fakeStatsReader) DagStats(_ context.Context, _ string) (domain.DagStats, error) {
	return f.dag, nil
}

func (f fakeStatsReader) HistoricalMetrics(_ context.Context, _ string, _, _ time.Time) (domain.HistoricalMetrics, error) {
	return f.hist, nil
}

func newDashboardEngine(reader DashboardStatsReader) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerUIDashboard(r, reader)
	return r
}

func TestDagStatsHandler(t *testing.T) {
	reader := fakeStatsReader{dag: domain.DagStats{Active: 5, Failed: 2, Running: 1, Queued: 3}}
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ui/dashboard/dag_stats", http.NoBody)
	newDashboardEngine(reader).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var got map[string]int
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for k, want := range map[string]int{
		"active_dag_count": 5, "failed_dag_count": 2,
		"running_dag_count": 1, "queued_dag_count": 3,
	} {
		if got[k] != want {
			t.Errorf("%s = %d, want %d", k, got[k], want)
		}
	}
}

func TestHistoricalMetricsHandler(t *testing.T) {
	reader := fakeStatsReader{hist: domain.HistoricalMetrics{
		RunStates: map[string]int{"success": 4, "failed": 1},
		TIStates:  map[string]int{"success": 7, "failed": 2, "up_for_retry": 1},
	}}
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/ui/dashboard/historical_metrics_data?start_date=2026-05-01T00:00:00Z", http.NoBody)
	newDashboardEngine(reader).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		RunStates map[string]int `json:"dag_run_states"`
		TIStates  map[string]int `json:"task_instance_states"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// run states: every required member present, populated where known.
	for _, k := range []string{"queued", "running", "success", "failed"} {
		if _, ok := got.RunStates[k]; !ok {
			t.Errorf("dag_run_states missing %q", k)
		}
	}
	if got.RunStates["success"] != 4 || got.RunStates["failed"] != 1 {
		t.Errorf("run states = %v", got.RunStates)
	}
	// TI states: all 13 Airflow members present; none→no_status mapping holds.
	for _, k := range []string{"no_status", "removed", "scheduled", "queued", "running",
		"success", "restarting", "failed", "up_for_retry", "up_for_reschedule",
		"upstream_failed", "skipped", "deferred"} {
		if _, ok := got.TIStates[k]; !ok {
			t.Errorf("task_instance_states missing %q", k)
		}
	}
	if got.TIStates["success"] != 7 || got.TIStates["up_for_retry"] != 1 {
		t.Errorf("ti states = %v", got.TIStates)
	}
}

func TestHistoricalMetricsRequiresStartDate(t *testing.T) {
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/ui/dashboard/historical_metrics_data", http.NoBody)
	newDashboardEngine(fakeStatsReader{}).ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing start_date: status = %d, want 400", w.Code)
	}
}
