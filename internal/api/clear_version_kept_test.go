package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// A clear with run_on_latest_version moves a run to the DAG's current version
// only when the run has finished and no task of it is queued or running (ADR
// 0086, as in Airflow 3.2). Any other run keeps its version and the clear
// applies on it, so both clear routes, dry run included, name such runs and
// the reason in run_on_latest_version_not_applied instead of dropping the
// option silently.

const keptReason = "the run is running: only a finished run moves to the current version"

type notApplied struct {
	NotApplied []struct {
		DagRunID string `json:"dag_run_id"`
		Reason   string `json:"reason"`
	} `json:"run_on_latest_version_not_applied"`
}

func decodeNotApplied(t *testing.T, body []byte) notApplied {
	t.Helper()
	var got notApplied
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return got
}

func assertNotApplied(t *testing.T, code int, body []byte, runID string) {
	t.Helper()
	if code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", code, body)
	}
	got := decodeNotApplied(t, body)
	if len(got.NotApplied) != 1 || got.NotApplied[0].DagRunID != runID || got.NotApplied[0].Reason != keptReason {
		t.Errorf("run_on_latest_version_not_applied = %+v, want %s with %q (%s)", got.NotApplied, runID, keptReason, body)
	}
}

func TestClearTaskInstancesReportsARunThatKeepsItsVersion(t *testing.T) {
	for _, dry := range []bool{true, false} {
		t.Run(map[bool]string{true: "dry_run", false: "clear"}[dry], func(t *testing.T) {
			srv, tasks := clearSrv(t)
			tasks.kept = []domain.ClearVersionKept{{RunID: "r1", Reason: keptReason}}
			body := `{"dag_run_id":"r1","only_failed":false,"run_on_latest_version":true,"dry_run":false}`
			if dry {
				body = strings.Replace(body, `"dry_run":false`, `"dry_run":true`, 1)
			}
			rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", body)
			assertNotApplied(t, rec.Code, rec.Body.Bytes(), "r1")
			if tasks.clearCalled == dry {
				t.Errorf("clear called = %v on dry_run=%v", tasks.clearCalled, dry)
			}
			if !dry && !tasks.lastClearOpts.RunOnLatestVersion {
				t.Error("the clear lost run_on_latest_version: the repository decides again on the locked run")
			}
		})
	}
}

func TestClearDagRunReportsARunThatKeepsItsVersion(t *testing.T) {
	for _, dry := range []string{"true", "false"} {
		t.Run("dry_run="+dry, func(t *testing.T) {
			srv, tasks := clearDefaultsServer(t)
			tasks.kept = []domain.ClearVersionKept{{RunID: "r1", Reason: keptReason}}
			rec := authGet(srv, http.MethodPost, runClearPath, `{"run_on_latest_version":true,"dry_run":`+dry+`}`)
			assertNotApplied(t, rec.Code, rec.Body.Bytes(), "r1")
			if dry == "false" {
				var run struct {
					DagRunID string `json:"dag_run_id"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil || run.DagRunID != "r1" {
					t.Errorf("the clear's body is no longer the run: %s", rec.Body.String())
				}
			}
		})
	}
}

// A run that moves, or a clear that does not ask for the current version,
// leaves the response as Airflow's: no run_on_latest_version_not_applied.
func TestClearWithNothingKeptAddsNoField(t *testing.T) {
	bodies := []string{
		`{"dag_run_id":"r1","run_on_latest_version":true,"dry_run":false}`,
		`{"dag_run_id":"r1","run_on_latest_version":true}`,
		`{"dag_run_id":"r1","run_on_latest_version":false,"dry_run":false}`,
	}
	for _, body := range bodies {
		srv, _ := clearSrv(t)
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d (%s)", body, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "run_on_latest_version_not_applied") {
			t.Errorf("%s: body carries the field with nothing kept: %s", body, rec.Body.String())
		}
	}
}

// Without run_on_latest_version, or without reset_dag_runs, the run keeps
// its version as asked and the repository is not asked.
func TestClearWithoutRebindSkipsTheVersionCheck(t *testing.T) {
	bodies := []string{
		`{"dag_run_id":"r1","run_on_latest_version":false,"dry_run":false}`,
		`{"dag_run_id":"r1","dry_run":false}`,
		`{"dag_run_id":"r1","reset_dag_runs":false,"run_on_latest_version":true,"dry_run":false}`,
	}
	for _, body := range bodies {
		srv, tasks := clearSrv(t)
		tasks.kept = []domain.ClearVersionKept{{RunID: "r1", Reason: keptReason}}
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", body)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d (%s), want 200", body, rec.Code, rec.Body.String())
		}
		if len(tasks.checkedRuns) != 0 {
			t.Errorf("%s: checked %v, want no check", body, tasks.checkedRuns)
		}
	}
	srv, tasks := clearDefaultsServer(t)
	tasks.kept = []domain.ClearVersionKept{{RunID: "r1", Reason: keptReason}}
	if rec := authGet(srv, http.MethodPost, runClearPath, `{"dry_run":false}`); rec.Code != http.StatusOK ||
		strings.Contains(rec.Body.String(), "run_on_latest_version_not_applied") {
		t.Errorf("run clear without run_on_latest_version = %d (%s), want 200 without the field", rec.Code, rec.Body.String())
	}
}

// An unknown run among the targets is a 404 before anything is cleared.
func TestClearOfAnUnknownRunIsNotFound(t *testing.T) {
	srv, tasks := clearSrv(t)
	tasks.checkErr = domain.ErrNotFound
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"r1","run_on_latest_version":true,"dry_run":false}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	if tasks.clearCalled {
		t.Error("a clear of an unknown run reached the repository")
	}
}

// include_future fans the clear across runs: every run is looked up in one
// call before any is cleared, and only the runs that keep their version are
// named.
func TestClearAcrossRunsReportsOnlyTheRunsThatKeepTheirVersion(t *testing.T) {
	now := time.Now().UTC()
	runs := &fakeRunRepo{runs: []domain.DagRun{
		{DagID: "etl", RunID: "cur", LogicalDate: now},
		{DagID: "etl", RunID: "future", LogicalDate: now.Add(time.Hour)},
	}}
	tasks := &fakeTaskRepo{kept: []domain.ClearVersionKept{{RunID: "future", Reason: keptReason}}}
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
			TokenTTLSecs:  3600,
		},
		Stores: StoreDeps{
			Tasks:   tasks,
			DagRuns: runs,
		},
	})
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"cur","include_future":true,"run_on_latest_version":true,"dry_run":false}`)
	assertNotApplied(t, rec.Code, rec.Body.Bytes(), "future")
	if strings.Join(tasks.checkedRuns, ",") != "cur,future" || tasks.checkCalls != 1 {
		t.Errorf("checked runs = %v in %d calls, want [cur future] in one", tasks.checkedRuns, tasks.checkCalls)
	}
}
