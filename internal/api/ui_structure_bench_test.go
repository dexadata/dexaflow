package api

import (
	"fmt"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// BenchmarkTopoSortFanOut measures the grid and graph task ordering for one root
// with n children, the shape of a wide dbt project.
func BenchmarkTopoSortFanOut(b *testing.B) {
	for _, n := range []int{1000, 5000} {
		tasks := make([]domain.TaskSpec, 0, n+1)
		tasks = append(tasks, domain.TaskSpec{TaskID: "root"})
		for i := range n {
			tasks = append(tasks, domain.TaskSpec{TaskID: fmt.Sprintf("t%05d", i), DependsOn: []string{"root"}})
		}
		b.Run(fmt.Sprintf("tasks=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = topoSortTasks(tasks)
			}
		})
	}
}
