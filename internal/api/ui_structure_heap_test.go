package api

import (
	"container/heap"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// referenceTopoSort is the original Kahn's algorithm that re-sorts the ready
// list after every pop. It is the oracle the heap-based topoSortTasks must match
// task for task.
func referenceTopoSort(tasks []domain.TaskSpec) []domain.TaskSpec {
	byID := make(map[string]domain.TaskSpec, len(tasks))
	indeg := make(map[string]int, len(tasks))
	children := make(map[string][]string, len(tasks))
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		byID[t.TaskID] = t
		indeg[t.TaskID] = 0
		ids = append(ids, t.TaskID)
	}
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if _, ok := byID[dep]; !ok {
				continue
			}
			indeg[t.TaskID]++
			children[dep] = append(children[dep], t.TaskID)
		}
	}
	var ready []string
	for _, id := range ids {
		if indeg[id] == 0 {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	out := make([]domain.TaskSpec, 0, len(tasks))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		out = append(out, byID[id])
		for _, ch := range children[id] {
			indeg[ch]--
			if indeg[ch] == 0 {
				ready = append(ready, ch)
			}
		}
		sort.Strings(ready)
	}
	if len(out) < len(tasks) {
		seen := make(map[string]bool, len(out))
		for _, t := range out {
			seen[t.TaskID] = true
		}
		for _, id := range ids {
			if !seen[id] {
				out = append(out, byID[id])
			}
		}
	}
	return out
}

// randomTasks builds n tasks with random upstreams, some unknown ids, some
// repeated dependencies and, when cyclic, a few back edges.
func randomTasks(r *rand.Rand, n int, cyclic bool) []domain.TaskSpec {
	tasks := make([]domain.TaskSpec, n)
	for i := range tasks {
		tasks[i].TaskID = fmt.Sprintf("t%03d", r.IntN(n*3))
		for range r.IntN(4) {
			switch {
			case i > 0:
				tasks[i].DependsOn = append(tasks[i].DependsOn, tasks[r.IntN(i)].TaskID)
			default:
				tasks[i].DependsOn = append(tasks[i].DependsOn, "missing")
			}
		}
	}
	if cyclic && n > 2 {
		tasks[0].DependsOn = append(tasks[0].DependsOn, tasks[n-1].TaskID)
	}
	return tasks
}

// TestTopoSortMatchesReferenceOrder pins that the heap-based sort emits exactly
// the order of the original algorithm, including duplicate ids, unknown
// dependencies and the cyclic remainder.
func TestTopoSortMatchesReferenceOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for i := range 300 {
		tasks := randomTasks(r, 1+r.IntN(40), i%5 == 0)
		got, want := topoSortTasks(tasks), referenceTopoSort(tasks)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("case %d: order differs\n got  %v\n want %v", i, taskIDs(got), taskIDs(want))
		}
	}
}

func taskIDs(tasks []domain.TaskSpec) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.TaskID
	}
	return out
}

// TestReadyHeapPopsInIDOrder pins the ready set's contract: whatever the push
// order, pops come out in ascending task id order.
func TestReadyHeapPopsInIDOrder(t *testing.T) {
	h := &readyHeap{}
	for _, id := range []string{"m", "b", "z", "a", "b"} {
		heap.Push(h, id)
	}
	var got []string
	for h.Len() > 0 {
		got = append(got, heap.Pop(h).(string))
	}
	if want := []string{"a", "b", "b", "m", "z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pop order = %v, want %v", got, want)
	}
}
