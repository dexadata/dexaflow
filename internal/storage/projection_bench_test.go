package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// BenchmarkActiveRunProjection measures the per-run work ActiveRuns does on every
// scheduler tick once the rows are loaded: copying the cached spec's tasks,
// building the task-instance maps, and the retry-delay map.
func BenchmarkActiveRunProjection(b *testing.B) {
	for _, n := range []int{20, 500} {
		tasks := make([]domain.TaskSpec, n)
		tis := make([]queries.TaskInstance, n)
		now := time.Now()
		delay := 30
		for i := range n {
			id := fmt.Sprintf("task_%05d", i)
			tasks[i] = domain.TaskSpec{TaskID: id, Type: "python", RetryDelaySeconds: &delay}
			tis[i] = queries.TaskInstance{TaskID: id, State: "running", TryNumber: 1, MaxTries: 2,
				EndedAt: pgtype.Timestamptz{Time: now, Valid: i%3 == 0}}
		}
		b.Run(fmt.Sprintf("tasks=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				copied := make([]domain.TaskSpec, len(tasks))
				copy(copied, tasks)
				_ = taskInstanceMaps(tis)
				retryDelay := make(map[string]int, len(copied))
				for _, t := range copied {
					if t.RetryDelaySeconds != nil {
						retryDelay[t.TaskID] = *t.RetryDelaySeconds
					}
				}
			}
		})
	}
}
