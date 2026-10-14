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

// A clear with run_on_latest_version re-binds the run to the DAG's current
// version. When that version's tasks differ from the run's task instances,
// the re-bind would leave a task without a task instance and an orphan task
// instance behind (#1702), so the repository refuses it with a conflict. The
// handlers ask before they preview or clear anything, so a dry run reports
// the same refusal the clear would meet, and a clear over several runs
// clears none of them when one is refused.

const driftDetail = `version 2 of dag "etl" adds tasks [report]: clear without run_on_latest_version, or trigger a new run`

func driftErr() error { return domain.Safef(domain.ErrConflict, "%s", driftDetail) }

func assertDriftConflict(t *testing.T, code int, body []byte) {
	t.Helper()
	if code != http.StatusConflict {
		t.Fatalf("status = %d (%s), want 409", code, body)
	}
	var p Problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, body)
	}
	if p.Detail != driftDetail {
		t.Errorf("detail = %q, want %q", p.Detail, driftDetail)
	}
}

func TestClearTaskInstancesRefusesAVersionWithOtherTasks(t *testing.T) {
	for _, dry := range []string{"true", "false"} {
		t.Run("dry_run="+dry, func(t *testing.T) {
			srv, tasks := clearSrv(t)
			tasks.checkErr = driftErr()
			rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
				`{"dag_run_id":"r1","only_failed":false,"run_on_latest_version":true,"dry_run":`+dry+`}`)
			assertDriftConflict(t, rec.Code, rec.Body.Bytes())
			if tasks.clearCalled {
				t.Error("a refused clear reached the repository")
			}
			if strings.Join(tasks.checkedRuns, ",") != "r1" {
				t.Errorf("checked runs = %v, want [r1]", tasks.checkedRuns)
			}
		})
	}
}

func TestClearDagRunRefusesAVersionWithOtherTasks(t *testing.T) {
	for _, dry := range []string{"true", "false"} {
		t.Run("dry_run="+dry, func(t *testing.T) {
			srv, tasks := clearDefaultsServer(t)
			tasks.checkErr = driftErr()
			rec := authGet(srv, http.MethodPost, runClearPath, `{"run_on_latest_version":true,"dry_run":`+dry+`}`)
			assertDriftConflict(t, rec.Code, rec.Body.Bytes())
			if tasks.clearCalled {
				t.Error("a refused clear reached the repository")
			}
		})
	}
}

// The task set is only compared when the clear re-binds: without
// run_on_latest_version, or without reset_dag_runs, the run keeps its version
// and the check is not asked.
func TestClearWithoutRebindSkipsTheVersionCheck(t *testing.T) {
	bodies := []string{
		`{"dag_run_id":"r1","run_on_latest_version":false,"dry_run":false}`,
		`{"dag_run_id":"r1","dry_run":false}`,
		`{"dag_run_id":"r1","reset_dag_runs":false,"run_on_latest_version":true,"dry_run":false}`,
	}
	for _, body := range bodies {
		srv, tasks := clearSrv(t)
		tasks.checkErr = driftErr()
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", body)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d (%s), want 200", body, rec.Code, rec.Body.String())
		}
		if len(tasks.checkedRuns) != 0 {
			t.Errorf("%s: checked %v, want no check", body, tasks.checkedRuns)
		}
	}
	srv, tasks := clearDefaultsServer(t)
	tasks.checkErr = driftErr()
	if rec := authGet(srv, http.MethodPost, runClearPath, `{"dry_run":false}`); rec.Code != http.StatusOK {
		t.Errorf("run clear without run_on_latest_version = %d (%s), want 200", rec.Code, rec.Body.String())
	}
}

// include_future fans the clear across runs; every run is checked before any
// is cleared, so a refusal on a later run leaves the first one untouched.
func TestClearAcrossRunsChecksEveryRunFirst(t *testing.T) {
	now := time.Now().UTC()
	runs := &fakeRunRepo{runs: []domain.DagRun{
		{DagID: "etl", RunID: "cur", LogicalDate: now},
		{DagID: "etl", RunID: "future", LogicalDate: now.Add(time.Hour)},
	}}
	tasks := &fakeTaskRepo{checkErr: driftErr(), checkErrRun: "future"}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		Tasks: tasks, DagRuns: runs,
	})
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"cur","include_future":true,"run_on_latest_version":true,"dry_run":false}`)
	assertDriftConflict(t, rec.Code, rec.Body.Bytes())
	if tasks.clearCalled {
		t.Error("a run was cleared before every run was checked")
	}
	if strings.Join(tasks.checkedRuns, ",") != "cur,future" {
		t.Errorf("checked runs = %v, want [cur future]", tasks.checkedRuns)
	}
}
