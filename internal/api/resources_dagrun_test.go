package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestTriggerDagRunPersistsConf pins that a conf object in the trigger request
// is carried onto the created run and reflected back in the response, rather
// than dropped. This is the server half of the CLI --conf plumbing: the
// stored conf is what the agent later exposes to tasks as params.
func TestTriggerDagRunPersistsConf(t *testing.T) {
	srv := authedServer()
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns",
		`{"conf":{"date":"2026-01-01","limit":10}}`)
	if rec.Code >= http.StatusMultipleChoices {
		t.Fatalf("trigger with conf = %d, want 2xx; body=%q", rec.Code, rec.Body.String())
	}
	var got struct {
		Conf map[string]any `json:"conf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parsing response %q: %v", rec.Body.String(), err)
	}
	if got.Conf["date"] != "2026-01-01" || got.Conf["limit"] != float64(10) {
		t.Errorf("conf = %v, want the posted conf carried onto the run", got.Conf)
	}
}

// TestTriggerDagRunDefaultsConfToEmptyObject pins that a trigger with no conf
// still reports conf as {}, preserving the prior contract.
func TestTriggerDagRunDefaultsConfToEmptyObject(t *testing.T) {
	srv := authedServer()
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{}`)
	if rec.Code >= http.StatusMultipleChoices {
		t.Fatalf("trigger without conf = %d, want 2xx; body=%q", rec.Code, rec.Body.String())
	}
	var got struct {
		Conf json.RawMessage `json:"conf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parsing response %q: %v", rec.Body.String(), err)
	}
	if string(got.Conf) != "{}" {
		t.Errorf("conf = %s, want {} when no conf is supplied", got.Conf)
	}
}

// TestTriggerDagRunNormalizesNullConf pins that an explicit JSON null conf is
// treated as absent and persisted as the empty object, not the literal null —
// the run's conf is contractually never null.
func TestTriggerDagRunNormalizesNullConf(t *testing.T) {
	srv := authedServer()
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/dagRuns", `{"conf":null}`)
	if rec.Code >= http.StatusMultipleChoices {
		t.Fatalf("trigger with null conf = %d, want 2xx; body=%q", rec.Code, rec.Body.String())
	}
	var got struct {
		Conf json.RawMessage `json:"conf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("parsing response %q: %v", rec.Body.String(), err)
	}
	if string(got.Conf) != "{}" {
		t.Errorf("conf = %s, want {} when conf is JSON null", got.Conf)
	}
}

// TestTriggerDagRunRejectsNonObjectConf pins that a conf that is valid JSON but
// not an object (an array or scalar) is a 400: conf must map to task params.
func TestTriggerDagRunRejectsNonObjectConf(t *testing.T) {
	for _, body := range []string{`{"conf":[1,2]}`, `{"conf":5}`, `{"conf":"x"}`} {
		rec := authGet(authedServer(), http.MethodPost, "/api/v2/dags/etl/dagRuns", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", body, rec.Code)
		}
	}
}

func TestGetDagRunHandler(t *testing.T) {
	srv := authedServer() // has run "r1"
	if r := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns/r1", ""); r.Code != http.StatusOK {
		t.Errorf("existing run = %d, want 200", r.Code)
	}
	if r := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns/missing", ""); r.Code != http.StatusNotFound {
		t.Errorf("missing run = %d, want 404", r.Code)
	}
}

func TestToDagRunDTODuration(t *testing.T) {
	start := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	end := start.Add(90 * time.Second)

	// Not started yet: no duration.
	if dto := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1"}); dto.Duration != nil {
		t.Errorf("an unstarted run should have nil duration, got %v", *dto.Duration)
	}
	// Started and ended: exact elapsed seconds.
	if dto := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1", StartedAt: &start, EndedAt: &end}); dto.Duration == nil || *dto.Duration != 90 {
		t.Errorf("finished run duration = %v, want 90", dto.Duration)
	}
	// Started but not ended: a positive (now-based) duration, not nil.
	if dto := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1", StartedAt: &start}); dto.Duration == nil || *dto.Duration <= 0 {
		t.Errorf("running run should have a positive duration, got %v", dto.Duration)
	}
	// The data interval is never null (a zero-width window at the logical date).
	dto := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1"})
	if dto.DataIntervalStart == nil || dto.DataIntervalEnd == nil {
		t.Error("data interval must never be null")
	}
	if string(dto.Conf) != "{}" {
		t.Errorf("conf should default to {}, got %s", dto.Conf)
	}
}

// dag_run_id is taken verbatim from the request body and becomes a path segment
// in the log sink, so a caller who can trigger a run could otherwise steer the
// control plane's writes outside the log root. The sink rejects it too; this
// keeps the run from being created at all, so the failure is a 400 the caller
// can read rather than a run whose logs silently never land.
func TestTriggerDagRunRejectsUnsafeRunID(t *testing.T) {
	for _, body := range []string{
		`{"dag_run_id":"../../../../tmp/pwned"}`,
		`{"dag_run_id":"a/b"}`,
		`{"dag_run_id":"/etc/cron.d/x"}`,
		`{"dag_run_id":".."}`,
		`{"dag_run_id":"back\\slash"}`,
	} {
		rec := authGet(authedServer(), http.MethodPost, "/api/v2/dags/etl/dagRuns", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST %s = %d, want 400", body, rec.Code)
		}
	}
}

// The guard must not reject the identifiers Airflow itself generates.
func TestTriggerDagRunAcceptsAirflowStyleRunID(t *testing.T) {
	rec := authGet(authedServer(), http.MethodPost, "/api/v2/dags/etl/dagRuns",
		`{"dag_run_id":"manual__2026-07-30T12:00:00+00:00"}`)
	if rec.Code == http.StatusBadRequest {
		t.Errorf("rejected a legitimate Airflow run id: %s", rec.Body.String())
	}
}
