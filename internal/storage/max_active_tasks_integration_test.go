//go:build integration

package storage_test

import (
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
)

// TestMaxActiveTasksHoldsPerRunIntegration (#2427): a DAG registered with
// max_active_tasks=2 and three independent tasks, with two runs, admits two
// tasks in each run and holds the third, over the real store and tick. The
// cap is per DAG run, as in Airflow 3, so the runs do not share it. The DAG
// views read the declared value back from the current version.
func TestMaxActiveTasksHoldsPerRunIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)
	dagID := fmt.Sprintf("max_tasks_%d", time.Now().UnixNano())
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1", MaxActiveTasks: 2,
		Tasks: []domain.TaskSpec{
			{TaskID: "a", Type: domain.TaskTypePython, Entrypoint: "dag:a"},
			{TaskID: "b", Type: domain.TaskTypePython, Entrypoint: "dag:b"},
			{TaskID: "c", Type: domain.TaskTypePython, Entrypoint: "dag:c"},
		},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil {
		t.Fatalf("register: %v", err)
	}
	setPaused(t, ctx, repo, dagID, false)
	createRun(t, ctx, repo, dagID, "r1", domain.DagRunStateQueued)
	createRun(t, ctx, repo, dagID, "r2", domain.DagRunStateQueued)

	sched := scheduler.NewScheduler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond)
	sched.SetDispatcher(noopDispatcher{})
	sched.SetLeading(true)
	for i := 0; i < 5; i++ {
		if err = sched.Step(ctx); err != nil {
			t.Fatalf("step: %v", err)
		}
	}
	for _, runID := range []string{"r1", "r2"} {
		active, held := 0, 0
		for _, st := range taskStates(t, ctx, repo, dagID, runID) {
			switch st {
			case domain.TaskStateQueued, domain.TaskStateRunning:
				active++
			case domain.TaskStateScheduled:
				held++
			default:
			}
		}
		if active != 2 || held != 1 {
			t.Errorf("run %s with max_active_tasks=2: %d active and %d held, want 2 and 1", runID, active, held)
		}
	}

	d, err := repo.GetDag(ctx, "default", dagID)
	if err != nil {
		t.Fatal(err)
	}
	if d.MaxActiveTasks != 2 {
		t.Errorf("GetDag max_active_tasks = %d, want 2 from the current version", d.MaxActiveTasks)
	}
	dags, _, err := repo.ListDagsWithFilter(ctx, "default", domain.DagFilter{DagIDPattern: dagID, Limit: 10})
	if err != nil || len(dags) != 1 || dags[0].MaxActiveTasks != 2 {
		t.Errorf("ListDagsWithFilter = %+v, %v; want the DAG with max_active_tasks 2", dags, err)
	}
}
