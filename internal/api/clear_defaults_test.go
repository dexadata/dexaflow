package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// clearDefaultsServer builds a control plane holding one failed and one
// successful task instance in the same run, which is the shape that tells the
// two Airflow defaults apart.
func clearDefaultsServer(t *testing.T) (*gin.Engine, *fakeTaskRepo) {
	t.Helper()
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{
		{TaskID: "extract", RunID: "r1", State: domain.TaskStateFailed},
		{TaskID: "load", RunID: "r1", State: domain.TaskStateSuccess},
	}}
	srv := NewServer(Dependencies{
		Logger: discardLogger(), Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter: auth.NewRateLimiter(100, time.Minute), CORSOrigins: []string{"*"}, TokenTTLSecs: 3600,
		Tasks: tasks, DagRuns: &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1"}}},
	})
	return srv, tasks
}

// TestClearTaskInstancesUnflaggedIsAPreviewOfFailedOnly is #1137.
//
// Airflow 3.2.1 declares, in ClearTaskInstancesBody:
//
//	dry_run: bool = True
//	only_failed: bool = True
//
// leoflow defaulted both the other way: a missing dry_run EXECUTED, and a
// missing only_failed took every named task instance, successful ones included.
// So the same unflagged request that previews on Airflow destroyed state here,
// including the state of tasks that had succeeded. Anything written against the
// Airflow API, which leoflow declares as its compatibility target, sends exactly
// that request.
//
// The embedded UI is unaffected either way: it sends both fields explicitly on
// all three of its clear dialogs. This is about API clients.
func TestClearTaskInstancesUnflaggedIsAPreviewOfFailedOnly(t *testing.T) {
	srv, tasks := clearDefaultsServer(t)

	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", `{"dag_run_id":"r1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unflagged clear = %d (%s)", rec.Code, rec.Body.String())
	}

	// The destructive half. Nothing may be cleared by a request that named no
	// flags, because Airflow would have previewed it.
	if tasks.clearCalled {
		t.Error("an unflagged clear EXECUTED; Airflow defaults dry_run=true, so this request destroys state a caller expected only to preview")
	}

	// The scoping half is measured through the affected set, not through the
	// repository: a preview never reaches ClearTaskInstances, so the fake's
	// onlyFailed flag records nothing here. With only_failed defaulting to false
	// this set contained the successful task too, which is what the report was
	// about.
	var got struct {
		TaskInstances []map[string]any `json:"task_instances"`
		TotalEntries  int              `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TotalEntries != 1 || got.TaskInstances[0]["task_id"] != "extract" {
		t.Errorf("affected set = %d %v, want only the failed task", got.TotalEntries, got.TaskInstances)
	}
}

// TestClearTaskInstancesExplicitFlagsStillWin guards the other direction: the
// new defaults must not make the explicit values unreachable. Without this,
// defaulting dry_run to true could be "fixed" by ignoring the field.
func TestClearTaskInstancesExplicitFlagsStillWin(t *testing.T) {
	t.Run("dry_run=false executes", func(t *testing.T) {
		srv, tasks := clearDefaultsServer(t)
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", `{"dag_run_id":"r1","dry_run":false}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("clear = %d (%s)", rec.Code, rec.Body.String())
		}
		if !tasks.clearCalled {
			t.Error("dry_run=false did not execute the clear")
		}
	})

	// Scoping on the path where the repository actually sees it: still no
	// only_failed in the body, but executing rather than previewing.
	t.Run("an executed clear with no only_failed is still scoped to failures", func(t *testing.T) {
		srv, tasks := clearDefaultsServer(t)
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", `{"dag_run_id":"r1","dry_run":false}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("clear = %d (%s)", rec.Code, rec.Body.String())
		}
		if !tasks.clearCalled {
			t.Fatal("premise failed: the clear did not execute, so onlyFailed was never observed")
		}
		if !tasks.gotOnlyFailed {
			t.Error("an executed clear with no only_failed was not scoped to failures; Airflow defaults only_failed=true, so this wipes tasks that succeeded")
		}
	})

	// dry_run=false is load-bearing, not decoration. The fake records onlyFailed
	// only when the repository is reached, and its zero value is false, so the
	// same assertion on a preview passes whether or not the handler ever reads
	// only_failed: a handler that hardcoded onlyFailed=true would still be green.
	t.Run("only_failed=false widens to every named task", func(t *testing.T) {
		srv, tasks := clearDefaultsServer(t)
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", `{"dag_run_id":"r1","only_failed":false,"dry_run":false}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("clear = %d (%s)", rec.Code, rec.Body.String())
		}
		if !tasks.clearCalled {
			t.Fatal("premise failed: the clear did not execute, so onlyFailed was never observed")
		}
		if tasks.gotOnlyFailed {
			t.Error("only_failed=false was ignored; an explicit widening must still be possible")
		}
	})

	// The preview must widen with it. The affected set is what a caller reads
	// before confirming, so a preview that stays scoped to failures while the
	// execute path widens would understate what the confirm is about to do.
	t.Run("only_failed=false widens the preview too", func(t *testing.T) {
		srv, _ := clearDefaultsServer(t)
		rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", `{"dag_run_id":"r1","only_failed":false}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview = %d (%s)", rec.Code, rec.Body.String())
		}
		var got struct {
			TaskInstances []map[string]any `json:"task_instances"`
			TotalEntries  int              `json:"total_entries"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.TotalEntries != 2 {
			t.Errorf("affected set = %d %v, want both the failed and the successful task", got.TotalEntries, got.TaskInstances)
		}
	})
}

// TestClearTaskInstancesRejectsOnlyRunning closes a hazard this change sharpens.
//
// only_running is declared in the request struct and in the spec and is read by
// no code, so it has always been accepted and discarded. That was noise while
// only_failed defaulted to false. It stops being noise now.
//
// In Airflow the model validator rejects only_failed and only_running both true,
// and only_failed defaults true, so the ONLY way to express "clear the running
// ones" is `{"only_failed": false, "only_running": true}`. leoflow reads that
// exact body as onlyFailed=false with only_running discarded, which means "clear
// every task instance named by the request, successes included". The Airflow
// idiom for the narrowest possible clear would become leoflow's widest one.
//
// Refusing is honest and cheap. Implementing it is a feature: it needs a
// running-only reset and pod termination, because Airflow sets RESTARTING and
// kills the running task.
func TestClearTaskInstancesRejectsOnlyRunning(t *testing.T) {
	srv, tasks := clearDefaultsServer(t)

	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"r1","only_failed":false,"only_running":true,"dry_run":false}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("only_running=true = %d, want 400; silently ignoring it turns Airflow's narrowest clear into the widest one (body: %s)", rec.Code, rec.Body.String())
	}
	if tasks.clearCalled {
		t.Error("the clear executed anyway; a refused request must not touch state")
	}
	if !strings.Contains(rec.Body.String(), "only_running") {
		t.Errorf("the refusal does not name only_running, so a caller cannot tell which field it was: %s", rec.Body.String())
	}
}
