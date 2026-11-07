package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// BenchmarkActiveRunProjection measures projectRunState, the per-run work
// ActiveRuns does on every scheduler tick once the rows are loaded: copying the
// cached spec's tasks, building the task-instance maps, and the retry-delay map.
// It calls the production function, so a change to the projection moves it
// (#1617). The database read is BenchmarkActiveRunsIntegration.
func BenchmarkActiveRunProjection(b *testing.B) {
	for _, n := range []int{20, 500} {
		tasks := make([]domain.TaskSpec, n)
		tis := make([]planTaskInstance, n)
		now := time.Now()
		delay := 30
		for i := range n {
			id := fmt.Sprintf("task_%05d", i)
			tasks[i] = domain.TaskSpec{TaskID: id, Type: "python", RetryDelaySeconds: &delay}
			tis[i] = planTaskInstance{TaskID: id, State: "running", TryNumber: 1, MaxTries: 2,
				EndedAt: pgtype.Timestamptz{Time: now, Valid: i%3 == 0}}
		}
		spec := domain.DAGSpec{DagID: "bench", Tasks: tasks}
		graph := scheduler.NewTaskGraph(tasks)
		row := queries.ListActiveDagRunsRow{DagRun: queries.DagRun{RunID: "manual__bench", State: "running"}}
		b.Run(fmt.Sprintf("tasks=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = projectRunState(row, spec, graph, tis, now)
			}
		})
	}
}

// The benchmark must exercise the same projection ActiveRuns returns.
func TestProjectRunStateCarriesTheTaskInstances(t *testing.T) {
	delay := 30
	tasks := []domain.TaskSpec{{TaskID: "a", RetryDelaySeconds: &delay}, {TaskID: "b"}}
	tis := []planTaskInstance{{TaskID: "a", State: "running", TryNumber: 1, MaxTries: 2}}
	rs := projectRunState(queries.ListActiveDagRunsRow{DagRun: queries.DagRun{RunID: "r", State: "running"}},
		domain.DAGSpec{DagID: "d", Tasks: tasks}, scheduler.NewTaskGraph(tasks), tis, time.Unix(0, 0))
	if rs.DagID != "d" || rs.DisplayRunID != "r" || len(rs.Tasks) != 2 {
		t.Fatalf("run state = %+v", rs)
	}
	if rs.States["a"] != domain.TaskState("running") || rs.RetryDelaySeconds["a"] != 30 {
		t.Fatalf("states %v, retry delays %v", rs.States, rs.RetryDelaySeconds)
	}
	if &rs.Tasks[0] == &tasks[0] {
		t.Fatal("the projection shares the cached spec's task slice")
	}
}
