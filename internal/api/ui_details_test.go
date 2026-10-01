package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func TestTimetableDescription(t *testing.T) {
	cases := map[string]string{
		"@daily":      "Daily",
		"@hourly":     "Hourly",
		"@weekly":     "Weekly",
		"*/5 * * * *": "Every 5 minutes",
		"0 */2 * * *": "Every 2 hours",
		"30 6 * * *":  "At 06:30, every day",
		"0 0 1 * *":   "0 0 1 * *", // unrecognized -> raw fallback
		"weird-cron":  "weird-cron",
	}
	for in, want := range cases {
		got := timetableDescription(&in)
		if got == nil || *got != want {
			t.Errorf("timetableDescription(%q) = %v, want %q", in, deref(got), want)
		}
	}
	if timetableDescription(nil) != nil {
		t.Errorf("nil schedule should yield null description")
	}
	empty := ""
	if timetableDescription(&empty) != nil {
		t.Errorf("empty schedule should yield null description")
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestDagDetailsFullShape(t *testing.T) {
	sched := "0 5 * * *"
	srv := uiDagsServer([]domain.DAG{{
		DagID: "etl", Owner: "data", Tags: []string{"x"}, Schedule: &sched,
		IsActive: true, Catchup: true, MaxActiveRuns: 8, Description: "the etl",
	}}, &fakeLatestRuns{})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/details", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("details = %d (%s)", rec.Code, rec.Body.String())
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	required := []string{
		"dag_id", "dag_display_name", "is_paused", "is_stale", "last_parsed_time",
		"last_parse_duration", "last_expired", "bundle_name", "bundle_version",
		"relative_fileloc", "fileloc", "description", "timetable_summary",
		"timetable_description", "timetable_partitioned", "tags", "max_active_tasks",
		"max_active_runs", "max_consecutive_failed_dag_runs", "has_task_concurrency_limits",
		"has_import_errors", "next_dagrun_logical_date", "next_dagrun_data_interval_start",
		"next_dagrun_data_interval_end", "next_dagrun_run_after", "allowed_run_types",
		"owners", "catchup", "dag_run_timeout", "asset_expression", "doc_md", "start_date",
		"end_date", "is_paused_upon_creation", "params", "render_template_as_native_obj",
		"template_search_path", "timezone", "last_parsed", "default_args", "file_token",
		"concurrency", "latest_dag_version",
	}
	for _, f := range required {
		if _, ok := d[f]; !ok {
			t.Errorf("details missing required field %q", f)
		}
	}
	var desc string
	_ = json.Unmarshal(d["timetable_description"], &desc)
	if desc != "At 05:00, every day" {
		t.Errorf("timetable_description = %q", desc)
	}
	// A DAG with no declared params (and no Specs reader) must emit params: {}
	// exactly as before — back-compatible with the pre-params wiring.
	if got := string(d["params"]); got != "{}" {
		t.Errorf("param-free DAG params = %s, want {}", got)
	}
}

// airflowParam mirrors the Airflow 3.2.1 serialized param entry the trigger
// dialog's flexible form consumes, used to assert the mapper's exact output.
type airflowParam struct {
	Value       json.RawMessage `json:"value"`
	Schema      json.RawMessage `json:"schema"`
	Description *string         `json:"description"`
}

// paramsFixtureSpec is a DAGSpec declaring one bare-default, one typed-enum
// (with default), and one required (no-default) param — the three shapes the
// mapper must reshape into Airflow's {value, schema, description} form.
func paramsFixtureSpec() domain.DAGSpec {
	return domain.DAGSpec{
		DagID: "etl",
		Params: map[string]domain.ParamSpec{
			"limit":  {Default: json.RawMessage("5")},
			"region": {Default: json.RawMessage(`"us"`), Schema: json.RawMessage(`{"type":"string","enum":["us","eu"]}`)},
			"token":  {Schema: json.RawMessage(`{"type":"string"}`)},
		},
	}
}

func TestParamsToAirflowDict(t *testing.T) {
	raw := paramsToAirflowDict(paramsFixtureSpec().Params)
	var got map[string]airflowParam
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("mapper output is not valid JSON: %v (%s)", err, raw)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 params, got %d (%s)", len(got), raw)
	}

	// bare default -> value carried, schema defaults to {}, description null.
	if v := string(got["limit"].Value); v != "5" {
		t.Errorf("limit.value = %s, want 5", v)
	}
	if s := string(got["limit"].Schema); s != "{}" {
		t.Errorf("limit.schema = %s, want {}", s)
	}
	if got["limit"].Description != nil {
		t.Errorf("limit.description = %v, want null", *got["limit"].Description)
	}

	// typed enum -> default renamed to value, schema carried verbatim.
	if v := string(got["region"].Value); v != `"us"` {
		t.Errorf("region.value = %s, want \"us\"", v)
	}
	if s := string(got["region"].Schema); s != `{"type":"string","enum":["us","eu"]}` {
		t.Errorf("region.schema = %s", s)
	}

	// required (no default) -> value:null so the field still renders and the
	// form's required-detector flags it; schema carried verbatim.
	if v := string(got["token"].Value); v != "null" {
		t.Errorf("token.value = %s, want null", v)
	}
	if s := string(got["token"].Schema); s != `{"type":"string"}` {
		t.Errorf("token.schema = %s", s)
	}
}

func TestParamsToAirflowDictEmpty(t *testing.T) {
	if got := string(paramsToAirflowDict(nil)); got != "{}" {
		t.Errorf("nil params -> %s, want {}", got)
	}
	if got := string(paramsToAirflowDict(map[string]domain.ParamSpec{})); got != "{}" {
		t.Errorf("empty params -> %s, want {}", got)
	}
}

// TestDagDetailsParamsShape is the durable contract guard: it drives the real
// details handler with a DagSpecReader and asserts the exact params JSON the
// endpoint emits for a bare-default + typed-enum + required param.
func TestDagDetailsParamsShape(t *testing.T) {
	srv := uiDagsServerWithSpecs(
		[]domain.DAG{{DagID: "etl"}},
		&fakeLatestRuns{},
		&fakeSpecReader{spec: paramsFixtureSpec()},
	)
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/details", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("details = %d (%s)", rec.Code, rec.Body.String())
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	var params map[string]airflowParam
	if err := json.Unmarshal(d["params"], &params); err != nil {
		t.Fatalf("params is not the Airflow param-dict shape: %v (%s)", err, d["params"])
	}
	if string(params["limit"].Value) != "5" || string(params["limit"].Schema) != "{}" {
		t.Errorf("limit = %+v", params["limit"])
	}
	if string(params["region"].Value) != `"us"` || string(params["region"].Schema) != `{"type":"string","enum":["us","eu"]}` {
		t.Errorf("region = %+v", params["region"])
	}
	if string(params["token"].Value) != "null" || string(params["token"].Schema) != `{"type":"string"}` {
		t.Errorf("token = %+v", params["token"])
	}
}

// TestDagDetailsParamsReadErrorFallsBack proves the details endpoint never
// breaks on a spec-read error: it degrades to params: {} (JSON-only dialog).
func TestDagDetailsParamsReadErrorFallsBack(t *testing.T) {
	srv := uiDagsServerWithSpecs(
		[]domain.DAG{{DagID: "etl"}},
		&fakeLatestRuns{},
		&fakeSpecReader{err: errors.New("boom")},
	)
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/details", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("details = %d (%s)", rec.Code, rec.Body.String())
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if got := string(d["params"]); got != "{}" {
		t.Errorf("spec-read error should fall back to {}, got %s", got)
	}
}
