package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// fanOutRun builds a run with one succeeded root and n children that all depend
// on it, every child in childState. It is the shape that stresses the tick most:
// a wide dbt or mapped fan-out where every task becomes eligible at once.
func fanOutRun(id string, n int, childState domain.TaskState) RunState {
	tasks := make([]domain.TaskSpec, 0, n+1)
	states := make(map[string]domain.TaskState, n+1)
	tasks = append(tasks, domain.TaskSpec{TaskID: "root", Type: "python"})
	states["root"] = domain.TaskStateSuccess
	for i := 0; i < n; i++ {
		tid := fmt.Sprintf("t%05d", i)
		tasks = append(tasks, domain.TaskSpec{TaskID: tid, Type: "python", DependsOn: []string{"root"}})
		states[tid] = childState
	}
	return RunState{RunID: id, DagID: "d", State: domain.DagRunStateRunning, Tasks: tasks, States: states, Now: time.Now()}
}

// BenchmarkPlanRunFanOut measures planning one run whose children are all ready
// to be scheduled.
func BenchmarkPlanRunFanOut(b *testing.B) {
	for _, n := range []int{100, 1000, 5000} {
		run := fanOutRun("r", n, domain.TaskStateNone)
		b.Run(fmt.Sprintf("tasks=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = PlanRun(run)
			}
		})
	}
}

// BenchmarkStepSteadyState measures a tick over many active runs with nothing to
// do (every task running): the floor cost the scheduler pays every interval.
// The store is in memory, so database time is excluded.
func BenchmarkStepSteadyState(b *testing.B) {
	for _, c := range []struct{ runs, tasks int }{{100, 20}, {1000, 20}, {200, 500}} {
		runs := make([]RunState, c.runs)
		for i := range runs {
			runs[i] = fanOutRun(fmt.Sprintf("r%d", i), c.tasks, domain.TaskStateRunning)
		}
		b.Run(fmt.Sprintf("runs=%d/tasks=%d", c.runs, c.tasks), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				s := newScheduler(newFakeStore(runs...))
				_ = s.Step(context.Background())
			}
		})
	}
}

// BenchmarkStepQueueFanOut measures one tick that promotes and dispatches every
// task of a wide fan-out.
func BenchmarkStepQueueFanOut(b *testing.B) {
	for _, n := range []int{1000, 5000} {
		run := fanOutRun("r", n, domain.TaskStateScheduled)
		b.Run(fmt.Sprintf("tasks=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				s := newScheduler(newFakeStore(run))
				s.SetDispatcher(&fakeDispatcher{})
				_ = s.Step(context.Background())
			}
		})
	}
}
