package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// runsServer serves one DAG ("etl") whose dagRuns endpoint honors the ?state
// filter and returns runs with fixed start dates so age filtering is testable.
func runsServer(t *testing.T, now time.Time) *httptest.Server {
	t.Helper()
	old := now.Add(-3 * time.Hour)
	recent := now.Add(-1 * time.Minute)
	all := []apiclient.DAGRun{
		{DagRunId: strptr("run-old"), DagId: strptr("etl"), State: runState(apiclient.DAGRunStateRunning), StartDate: &old},
		{DagRunId: strptr("run-new"), DagId: strptr("etl"), State: runState(apiclient.DAGRunStateRunning), StartDate: &recent},
		{DagRunId: strptr("run-done"), DagId: strptr("etl"), State: runState(apiclient.DAGRunStateSuccess), StartDate: &recent},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/dags", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags" {
			http.NotFound(w, r)
			return
		}
		dags := []apiclient.DAG{{DagId: strptr("etl")}}
		total := 1
		writeJSON(t, w, apiclient.DAGCollection{Dags: &dags, TotalEntries: &total})
	})
	mux.HandleFunc("/api/v2/dags/etl/dagRuns", func(w http.ResponseWriter, r *http.Request) {
		state := r.URL.Query().Get("state")
		runs := make([]apiclient.DAGRun, 0, len(all))
		for _, run := range all {
			if state == "" || string(*run.State) == state {
				runs = append(runs, run)
			}
		}
		total := len(runs)
		writeJSON(t, w, apiclient.DAGRunCollection{DagRuns: &runs, TotalEntries: &total})
	})
	return httptest.NewServer(mux)
}

func runState(s apiclient.DAGRunState) *apiclient.DAGRunState { return &s }

func TestCollectRunsFiltersByState(t *testing.T) {
	now := time.Now()
	srv := runsServer(t, now)
	defer srv.Close()

	runs, err := collectRuns(context.Background(), newTestClient(t, srv.URL), "", "running")
	if err != nil {
		t.Fatalf("collectRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("running runs = %d, want 2", len(runs))
	}
	for _, r := range runs {
		if r.state != "running" {
			t.Errorf("run %q state = %q, want running", r.runID, r.state)
		}
	}
}

func TestFilterRunsByAge(t *testing.T) {
	now := time.Now()
	old := now.Add(-3 * time.Hour)
	recent := now.Add(-1 * time.Minute)
	runs := []adminRun{
		{dagID: "etl", runID: "old", state: "running", start: &old},
		{dagID: "etl", runID: "new", state: "running", start: &recent},
	}
	got := filterRunsByAge(runs, 2*time.Hour, now)
	if len(got) != 1 || got[0].runID != "old" {
		t.Fatalf("older-than 2h = %+v, want just the old run", got)
	}
	// A zero threshold keeps everything, including runs with no start time.
	if len(filterRunsByAge(runs, 0, now)) != 2 {
		t.Errorf("zero threshold dropped runs")
	}
}

func TestAdminRunsListStateAndAge(t *testing.T) {
	now := time.Now()
	srv := runsServer(t, now)
	defer srv.Close()

	out, _, err := run(t, "admin", "runs", "list", "--state", "running", "--older-than", "2h",
		"--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("admin runs list: %v", err)
	}
	if !strings.Contains(out, "run-old") {
		t.Errorf("output = %q, want it to include run-old", out)
	}
	if strings.Contains(out, "run-new") || strings.Contains(out, "run-done") {
		t.Errorf("output = %q, should have filtered out run-new and run-done", out)
	}
}
