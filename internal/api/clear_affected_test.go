package api

import (
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// A clear resets only tasks the version the re-run executes declares (ADR
// 0086). The dry run and the audit list what the clear resets, so a task the
// version dropped (removed, or about to be marked removed by the move) is
// neither previewed nor audited as cleared.

// droppedTaskServer serves run r1 with a and b both failed, and a current
// version that declares only a.
func droppedTaskServer(t *testing.T, tis []domain.TaskInstance, kept []domain.ClearVersionKept) (*gin.Engine, *fakeTaskRepo, *fakeAuditWriter) {
	t.Helper()
	tasks := &fakeTaskRepo{tis: tis, kept: kept}
	audit := &fakeAuditWriter{}
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
			DagRuns: &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1"}}},
			Specs:   &fakeSpecReader{spec: domain.DAGSpec{DagID: "etl", Tasks: []domain.TaskSpec{{TaskID: "a"}}}},
			Audit:   audit,
		},
	})
	return srv, tasks, audit
}

var bothFailed = []domain.TaskInstance{
	{TaskID: "a", RunID: "r1", State: domain.TaskStateFailed},
	{TaskID: "b", RunID: "r1", State: domain.TaskStateFailed},
}

func auditedTasks(w *fakeAuditWriter) string {
	ids := make([]string, 0, len(w.calls))
	for _, c := range w.calls {
		if c.action == "taskinstance.clear" {
			ids = append(ids, c.taskID)
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// A run that moves to the current version: b is dropped, so only a is
// previewed and audited, on both clear routes.
func TestClearOfAMovingRunListsOnlyTheDeclaredTasks(t *testing.T) {
	routes := map[string]struct{ path, body string }{
		"clearTaskInstances": {"/api/v2/dags/etl/clearTaskInstances", `{"dag_run_id":"r1","only_failed":true,"run_on_latest_version":true,"dry_run":%s}`},
		"run clear":          {runClearPath, `{"only_failed":true,"run_on_latest_version":true,"dry_run":%s}`},
	}
	for name, r := range routes {
		for _, dry := range []string{"true", "false"} {
			t.Run(name+" dry_run="+dry, func(t *testing.T) {
				srv, _, audit := droppedTaskServer(t, bothFailed, nil)
				rec := authGet(srv, http.MethodPost, r.path, strings.Replace(r.body, "%s", dry, 1))
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
				}
				if dry == "true" {
					if got := affectedIDs(t, rec.Body.Bytes()); got != "a" {
						t.Errorf("previewed %q, want a: b is dropped by the move", got)
					}
					return
				}
				if got := auditedTasks(audit); got != "a" {
					t.Errorf("audited %q, want a: b is marked removed, not cleared", got)
				}
			})
		}
	}
}

// A run that keeps its version is cleared on that version: its tasks are
// listed whatever the current version declares.
func TestClearOfAKeptRunListsItsOwnTasks(t *testing.T) {
	srv, _, _ := droppedTaskServer(t, bothFailed, []domain.ClearVersionKept{{RunID: "r1", Reason: keptReason}})
	rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"r1","only_failed":true,"run_on_latest_version":true,"dry_run":true}`)
	if got := affectedIDs(t, rec.Body.Bytes()); got != "a,b" {
		t.Errorf("previewed %q, want a,b", got)
	}
}

// A task instance a past move left removed is never reset by a clear that
// keeps the run's version, so it is neither previewed nor audited.
func TestClearDoesNotListARemovedTask(t *testing.T) {
	tis := []domain.TaskInstance{
		{TaskID: "a", RunID: "r1", State: domain.TaskStateSuccess},
		{TaskID: "b", RunID: "r1", State: domain.TaskStateRemoved},
	}
	for _, dry := range []string{"true", "false"} {
		t.Run("dry_run="+dry, func(t *testing.T) {
			srv, _, audit := droppedTaskServer(t, tis, nil)
			rec := authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
				`{"dag_run_id":"r1","task_ids":["a","b"],"only_failed":false,"dry_run":`+dry+`}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
			}
			if got := affectedIDs(t, rec.Body.Bytes()); got != "a" {
				t.Errorf("answered %q, want a", got)
			}
			if dry == "false" {
				if got := auditedTasks(audit); got != "a" {
					t.Errorf("audited %q, want a", got)
				}
			}
		})
	}
}
