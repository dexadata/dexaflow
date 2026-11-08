package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestDagViewsReportTheDagsMaxActiveTasks (#2427): the details endpoint and
// the UI DAG list report the max_active_tasks the DAG declares, and null when
// it declares none (no cap), instead of a constant 16 that nothing enforced.
// concurrency, Airflow's deprecated alias on the details view, follows it.
func TestDagViewsReportTheDagsMaxActiveTasks(t *testing.T) {
	dags := []domain.DAG{
		{DagID: "capped", IsActive: true, MaxActiveTasks: 3},
		{DagID: "open", IsActive: true},
	}
	srv := uiDagsServer(dags, &fakeLatestRuns{})
	field := func(path, key string, from func([]byte) map[string]json.RawMessage) string {
		t.Helper()
		rec := authGet(srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d (%s)", path, rec.Code, rec.Body.String())
		}
		return string(from(rec.Body.Bytes())[key])
	}
	details := func(body []byte) map[string]json.RawMessage {
		var d map[string]json.RawMessage
		if err := json.Unmarshal(body, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	for _, tc := range []struct{ dag, want string }{{"capped", "3"}, {"open", "null"}} {
		for _, key := range []string{"max_active_tasks", "concurrency"} {
			if got := field("/api/v2/dags/"+tc.dag+"/details", key, details); got != tc.want {
				t.Errorf("details of %s: %s = %s, want %s", tc.dag, key, got, tc.want)
			}
		}
	}
	var list struct {
		Dags []map[string]json.RawMessage `json:"dags"`
	}
	rec := authGet(srv, http.MethodGet, "/ui/dags", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Dags) != 2 {
		t.Fatalf("/ui/dags = %d (%s)", rec.Code, rec.Body.String())
	}
	got := map[string]string{}
	for _, d := range list.Dags {
		var id string
		_ = json.Unmarshal(d["dag_id"], &id)
		got[id] = string(d["max_active_tasks"])
	}
	if got["capped"] != "3" || got["open"] != "null" {
		t.Errorf("/ui/dags max_active_tasks = %v, want capped 3 and open null", got)
	}
}
