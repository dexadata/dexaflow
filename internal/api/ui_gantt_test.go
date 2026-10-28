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
)

// attemptsTaskRepo keeps earlier attempts apart from the current instances,
// as the store's task instance history does.
type attemptsTaskRepo struct {
	fakeTaskRepo
	history []domain.TaskInstance
	failErr error
}

func (f *attemptsTaskRepo) ListTaskInstances(context.Context, string, string, string, int, int) ([]domain.TaskInstance, int, error) {
	if f.failErr != nil {
		return nil, 0, f.failErr
	}
	return f.tis, len(f.tis), nil
}

func (f *attemptsTaskRepo) ListTaskInstanceAttempts(_ context.Context, _, _, _, taskID string) ([]domain.TaskInstance, error) {
	var out []domain.TaskInstance
	for _, ti := range append(append([]domain.TaskInstance{}, f.history...), f.tis...) {
		if ti.TaskID == taskID {
			out = append(out, ti)
		}
	}
	return out, nil
}

func ganttServer(tasks TaskInstanceRepository) *gin.Engine {
	return newTestServer(Dependencies{
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
		},
	})
}

// The Gantt view reads /ui/gantt/{dag_id}/{run_id}: one row per attempt of
// every task of the run. Before it existed the catch-all {} crashed the view
// (task_instances.some on undefined).
func TestGanttServesEveryAttempt(t *testing.T) {
	t0 := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	at := func(s int) *time.Time { v := t0.Add(time.Duration(s) * time.Second); return &v }
	tasks := &attemptsTaskRepo{
		fakeTaskRepo: fakeTaskRepo{tis: []domain.TaskInstance{
			{DagID: "etl", RunID: "r1", TaskID: "extract", TryNumber: 2, State: domain.TaskStateSuccess,
				ScheduledAt: at(10), QueuedAt: at(11), StartedAt: at(12), EndedAt: at(20)},
			{DagID: "etl", RunID: "r1", TaskID: "load", TryNumber: 0, State: domain.TaskStateNone},
		}},
		history: []domain.TaskInstance{
			{DagID: "etl", RunID: "r1", TaskID: "extract", TryNumber: 1, State: domain.TaskStateFailed,
				StartedAt: at(0), EndedAt: at(5)},
		},
	}
	srv := ganttServer(tasks)
	rec := authGet(srv, http.MethodGet, "/ui/gantt/etl/r1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("gantt = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		DagID         string           `json:"dag_id"`
		RunID         string           `json:"run_id"`
		TaskInstances []map[string]any `json:"task_instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DagID != "etl" || got.RunID != "r1" {
		t.Errorf("ids = %q/%q", got.DagID, got.RunID)
	}
	if len(got.TaskInstances) != 3 {
		t.Fatalf("rows = %d, want 3 (two extract attempts, one load): %s", len(got.TaskInstances), rec.Body.String())
	}
	for _, row := range got.TaskInstances {
		for _, f := range []string{"task_id", "task_display_name", "try_number", "state", "scheduled_dttm",
			"queued_dttm", "start_date", "end_date", "is_group", "is_mapped"} {
			if _, ok := row[f]; !ok {
				t.Errorf("row %v missing %q", row["task_id"], f)
			}
		}
	}
	first, second := got.TaskInstances[0], got.TaskInstances[1]
	if first["task_id"] != "extract" || first["try_number"] != float64(1) || first["state"] != "failed" {
		t.Errorf("first row = %v, want extract try 1 failed", first)
	}
	if second["try_number"] != float64(2) || second["queued_dttm"] != "2026-05-22T10:00:11Z" {
		t.Errorf("second row = %v, want extract try 2 queued at 10:00:11", second)
	}
	if load := got.TaskInstances[2]; load["state"] != nil || load["start_date"] != nil {
		t.Errorf("load row = %v, want a null state and start", load)
	}
}

func TestGanttStoreFailureIsAnError(t *testing.T) {
	srv := ganttServer(&attemptsTaskRepo{failErr: context.DeadlineExceeded})
	if rec := authGet(srv, http.MethodGet, "/ui/gantt/etl/r1", ""); rec.Code < 500 {
		t.Errorf("gantt on a failing store = %d, want 5xx", rec.Code)
	}
}
